#!/usr/bin/env node
/**
 * Import an official DSH session export ZIP into a workspace.
 *
 *   node tools/session/import-session.mjs --zip dsh-session-<id>.zip --workspace <YOUR_PROJECT_ROOT>
 *
 * The official export (DSH 0.2.2-rc.5, `/api/session.export`) is a plain ZIP:
 *
 *   session.v4.jsonl                 root session, plaintext JSONL
 *   subagents/<childId>/session.v4.jsonl
 *   media/<attachmentId>.<ext>       images, attachmentId = sha256:<hex>
 *   files/<ab>/<digest>/<name>       regular attachments
 *   attachments-missing.txt          optional
 *
 * DSH ships no import code; this tool rebuilds the stored zstd log format, maps
 * the session ids to fresh ones (unless --keep-ids), and optionally attaches the
 * root session to the workspace registry.
 *
 * Options: --workspace <path> --title <text> --keep-ids --no-attach
 *          --force-running --dry-run
 */
import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { zstdCompressSync } from 'node:zlib'
import { decodeTextLog, newChildId, newSessionId, rewriteSession } from './format.mjs'
import { readZip } from './zip.mjs'
import { resolveDshHome, storedSessionIds, writeStoredLog } from './store.mjs'
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
      case '--zip': args.zip = next(); break
      case '--workspace': args.workspace = next(); break
      case '--title': args.title = next(); break
      case '--keep-ids': args.keepIds = true; break
      case '--no-attach': args.attach = false; break
      case '--force-running': args.forceRunning = true; break
      case '--dry-run': args.dryRun = true; break
      case '--help': args.help = true; break
      default: throw new Error(`unknown option: ${token}`)
    }
  }
  return args
}

const SESSION_ENTRY = /^(?:subagents\/[^/]+\/)?session\.v4\.jsonl$/

function atomicWrite(file, data) {
  mkdirSync(dirname(file), { recursive: true })
  const temporary = `${file}.tmp-${process.pid}-${Date.now()}`
  writeFileSync(temporary, data)
  renameSync(temporary, file)
}

function main() {
  if (typeof zstdCompressSync !== 'function') {
    throw new Error(`this Node has no zstd support (${process.version}); use Node >= 22.15 (24+ recommended)`)
  }
  const args = parseArgs(process.argv.slice(2))
  if (args.help) {
    console.log('usage: node tools/session/import-session.mjs --zip <file> --workspace <path> [options]')
    return
  }
  if (!args.zip || !args.workspace) throw new Error('--zip and --workspace are required')
  if (!existsSync(args.zip)) throw new Error(`zip not found: ${args.zip}`)

  const dshHome = resolveDshHome()
  const workspace = canonicalPath(args.workspace)
  if (!existsSync(workspace)) throw new Error(`workspace directory does not exist: ${workspace}`)

  const { entries, skipped: skippedEntries } = readZip(readFileSync(args.zip))
  const sessionEntries = entries.filter((entry) => SESSION_ENTRY.test(entry.name))
  if (sessionEntries.length === 0) throw new Error('zip has no session.v4.jsonl entry (is this an official DSH session export?)')

  const parsed = sessionEntries.map((entry) => {
    const decoded = decodeTextLog(entry.data.toString('utf8'))
    return { name: entry.name, header: decoded.header, events: decoded.events }
  })
  const roots = parsed.filter((entry) => !entry.header.parentSession)
  if (roots.length !== 1) throw new Error(`expected exactly one root session in the zip, found ${roots.length}`)
  const root = roots[0]

  const existing = storedSessionIds(dshHome)
  const idMap = new Map()
  for (const entry of parsed) {
    if (args.keepIds) {
      if (existing.has(entry.header.id)) throw new Error(`session id already exists: ${entry.header.id} (drop --keep-ids)`)
      idMap.set(entry.header.id, entry.header.id)
    } else {
      idMap.set(entry.header.id, entry === root ? newSessionId() : newChildId())
    }
  }

  const written = []
  for (const entry of [root, ...parsed.filter((item) => item !== root)]) {
    const newId = idMap.get(entry.header.id)
    const parent = entry.header.parentSession ? (idMap.get(entry.header.parentSession) ?? entry.header.parentSession) : undefined
    const rewritten = rewriteSession({ header: entry.header, events: entry.events, newId, newCwd: workspace, newParentSession: parent, idMap })
    let file = null
    if (!args.dryRun) {
      file = writeStoredLog({ dshHome, id: newId, cwd: workspace, header: rewritten.header, events: rewritten.events })
    }
    written.push({ zipEntry: entry.name, oldId: entry.header.id, newId, events: rewritten.events.length, file })
  }

  // Attachments: images go to objects/<ab>/<hex>, regular files keep their digest path.
  const attachments = { media: 0, files: 0, skipped: [] }
  const attachmentsRoot = join(dshHome, 'attachments', 'v1')
  for (const entry of entries) {
    if (entry.name.startsWith('media/')) {
      const match = /^media\/(?:sha256:)?([a-f0-9]{64})\.(png|jpe?g|webp|gif)$/i.exec(entry.name)
      if (!match) {
        attachments.skipped.push({ name: entry.name, reason: 'unrecognised media name' })
        continue
      }
      const digest = match[1].toLowerCase()
      if (!args.dryRun) atomicWrite(join(attachmentsRoot, 'objects', digest.slice(0, 2), digest), entry.data)
      attachments.media += 1
    } else if (entry.name.startsWith('files/')) {
      const match = /^files\/([a-f0-9]{2})\/([a-f0-9]{64})\/(.+)$/i.exec(entry.name)
      if (!match) {
        attachments.skipped.push({ name: entry.name, reason: 'unrecognised file name' })
        continue
      }
      const target = join(attachmentsRoot, 'files', match[1].toLowerCase(), match[2].toLowerCase(), basename(match[3]))
      if (!args.dryRun) atomicWrite(target, entry.data)
      attachments.files += 1
    }
  }

  const rootNewId = idMap.get(root.header.id)
  let attach = null
  if (args.attach) {
    if (runningDshProcesses() && !args.forceRunning && !args.dryRun) {
      attach = { skipped: true, reason: 'DeepSeek Harness is running; close it and rerun, or pass --force-running' }
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

  console.log(
    JSON.stringify(
      {
        ok: true,
        zip: args.zip,
        workspace,
        dryRun: args.dryRun,
        dshHome,
        written,
        attachments,
        attach,
        skippedZipEntries: skippedEntries.slice(0, 20),
      },
      null,
      2,
    ),
  )
}

main()
