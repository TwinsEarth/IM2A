#!/usr/bin/env node
/**
 * Restore a DSH session (and its sub-agent descendants) into another workspace.
 *
 *   node tools/session/restore-session.mjs --from-dsh session-<id> --workspace <YOUR_PROJECT_ROOT>
 *   node tools/session/restore-session.mjs --snapshot sessions\current --workspace <YOUR_PROJECT_ROOT>
 *
 * Options:
 *   --from-dsh <id>     read the session from <DSH_HOME>/sessions (live store)
 *   --snapshot <dir>    read session logs from a snapshot directory (project copy)
 *   --workspace <path>  target workspace; must exist
 *   --title <text>      workspace title when the registry creates a new record
 *   --keep-ids          keep the original session ids (refused when they exist)
 *   --no-attach         write the logs but do not touch workspace.json
 *   --force-running     allow the workspace.json edit while DSH is running
 *   --dry-run           report without writing anything
 *
 * Imported sessions always get fresh ids by default, so the original session is
 * never duplicated or moved; the new copy lives under the target workspace.
 */
import { existsSync, readdirSync, statSync } from 'node:fs'
import { basename, join } from 'node:path'
import { zstdCompressSync } from 'node:zlib'
import { newChildId, newSessionId, rewriteSession } from './format.mjs'
import { collectLineage, readStoredLog, resolveDshHome, storedSessionIds, writeStoredLog } from './store.mjs'
import { attachSessionsToWorkspace, canonicalPath, runningDshProcesses } from './workspace.mjs'

function parseArgs(argv) {
  const args = { attach: true, keepIds: false, dryRun: false, forceRunning: false }
  for (let index = 0; index < argv.length; index++) {
    const token = argv[index]
    const next = () => {
      index += 1
      if (index >= argv.length) throw new Error(`${token} needs a value`)
      return argv[index]
    }
    switch (token) {
      case '--from-dsh': args.fromDsh = next(); break
      case '--snapshot': args.snapshot = next(); break
      case '--workspace': args.workspace = next(); break
      case '--title': args.title = next(); break
      case '--keep-ids': args.keepIds = true; break
      case '--no-attach': args.attach = false; break
      case '--force-running': args.forceRunning = true; break
      case '--attach-only': args.attachOnly = true; break
      case '--session-id': args.sessionId = next(); break
      case '--dry-run': args.dryRun = true; break
      case '--help': args.help = true; break
      default: throw new Error(`unknown option: ${token}`)
    }
  }
  return args
}

function requireZstd() {
  if (typeof zstdCompressSync !== 'function') {
    throw new Error(`this Node has no zstd support (${process.version}); use Node >= 22.15 (24+ recommended)`)
  }
}

function findSnapshotLogs(dir) {
  const files = []
  const walk = (current) => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const full = join(current, entry.name)
      if (entry.isDirectory()) walk(full)
      else if (entry.isFile() && /^session\.v4\.jsonl\.zstd$/.test(entry.name)) files.push(full)
    }
  }
  if (!existsSync(dir)) throw new Error(`snapshot directory does not exist: ${dir}`)
  if (statSync(dir).isFile()) files.push(dir)
  else walk(dir)
  return files
}

function loadSessions(args) {
  if (args.fromDsh) {
    const lineage = collectLineage(args.fromDsh)
    return lineage.map((entry) => {
      const { header, events } = readStoredLog(entry.file)
      return { header, events, source: entry.file }
    })
  }
  if (args.snapshot) {
    return findSnapshotLogs(args.snapshot).map((file) => {
      const { header, events } = readStoredLog(file)
      return { header, events, source: file }
    })
  }
  throw new Error('one of --from-dsh or --snapshot is required')
}

function main() {
  requireZstd()
  const args = parseArgs(process.argv.slice(2))
  if (args.help || (!args.fromDsh && !args.snapshot)) {
    console.log('usage: node tools/session/restore-session.mjs --from-dsh <id> | --snapshot <dir> --workspace <path> [options]')
    return
  }
  if (!args.workspace) throw new Error('--workspace is required')

  const dshHome = resolveDshHome()
  const workspace = canonicalPath(args.workspace)
  if (!existsSync(workspace)) throw new Error(`workspace directory does not exist: ${workspace} (create it first)`)

  // Re-attach a previously imported copy after DSH has been restarted.
  if (args.attachOnly) {
    if (!args.sessionId) throw new Error('--attach-only needs --session-id <id>')
    const result = attachSessionsToWorkspace({
      workspacePath: workspace,
      title: args.title || basename(workspace),
      sessionIds: [args.sessionId],
      dshHome,
      dryRun: args.dryRun,
    })
    console.log(JSON.stringify({ ok: true, workspace, attach: { ...result, skipped: false } }, null, 2))
    return
  }

  const sessions = loadSessions(args)
  const roots = sessions.filter((entry) => !entry.header.parentSession)
  if (roots.length !== 1) throw new Error(`expected exactly one root session, found ${roots.length}`)
  const root = roots[0]
  const children = sessions.filter((entry) => entry !== root)

  const existingIds = storedSessionIds(dshHome)
  const idMap = new Map()
  for (const entry of sessions) {
    if (args.keepIds) {
      if (existingIds.has(entry.header.id)) throw new Error(`session id already exists: ${entry.header.id} (drop --keep-ids to import a copy)`)
      idMap.set(entry.header.id, entry.header.id)
    } else {
      idMap.set(entry.header.id, entry === root ? newSessionId() : newChildId())
    }
  }

  const written = []
  for (const entry of [root, ...children]) {
    const oldId = entry.header.id
    const newId = idMap.get(oldId)
    const parent = entry.header.parentSession ? (idMap.get(entry.header.parentSession) ?? entry.header.parentSession) : undefined
    const rewritten = rewriteSession({
      header: entry.header,
      events: entry.events,
      newId,
      newCwd: workspace,
      newParentSession: parent,
      idMap,
    })
    let file = null
    if (!args.dryRun) {
      file = writeStoredLog({ dshHome, id: newId, cwd: workspace, header: rewritten.header, events: rewritten.events })
    }
    written.push({
      oldId,
      newId,
      role: entry === root ? 'root' : 'subagent',
      parentSession: rewritten.header.parentSession ?? null,
      events: rewritten.events.length,
      file,
      source: entry.source,
    })
  }

  const rootNewId = idMap.get(root.header.id)
  let attach = null
  if (args.attach) {
    if (runningDshProcesses() && !args.forceRunning && !args.dryRun) {
      attach = {
        skipped: true,
        reason: 'DeepSeek Harness is running; close it and rerun with --no-attach omitted, or pass --force-running',
      }
    } else {
      attach = attachSessionsToWorkspace({
        workspacePath: workspace,
        title: args.title || basename(workspace),
        sessionIds: [rootNewId],
        dshHome,
        dryRun: args.dryRun,
      })
      attach.skipped = false
    }
  }

  console.log(JSON.stringify({ ok: true, workspace, dryRun: args.dryRun, dshHome, written, attach }, null, 2))
}

main()
