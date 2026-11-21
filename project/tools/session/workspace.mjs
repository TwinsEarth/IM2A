/**
 * Workspace registry helpers (`<DSH_HOME>/storages/workspace.json`, unit v2).
 *
 * Membership is authoritative: a session is listed under a workspace only when
 * `workspace.sessionIds` contains it and the stored header's `cwd` resolves to
 * `workspace.path`. Editing this file while DSH is running is unsafe — the Host
 * keeps the registry in memory and rewrites it on change — so the CLIs warn and
 * require `--force-running` for an in-place edit.
 */
import { spawnSync } from 'node:child_process'
import { copyFileSync, existsSync, readFileSync, realpathSync, renameSync, writeFileSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { randomUUID } from 'node:crypto'
import { resolveDshHome } from './store.mjs'

export function workspaceStatePath(dshHome = resolveDshHome()) {
  return join(dshHome, 'storages', 'workspace.json')
}

/** Canonical absolute path; DSH compares `fs.realpath(header.cwd)` with workspace.path. */
export function canonicalPath(target) {
  const absolute = resolve(target)
  try {
    return realpathSync.native(absolute)
  } catch {
    return absolute
  }
}

export function readWorkspaceState(dshHome = resolveDshHome()) {
  const file = workspaceStatePath(dshHome)
  if (!existsSync(file)) throw new Error(`workspace registry not found: ${file}`)
  return { file, state: JSON.parse(readFileSync(file, 'utf8')) }
}

export function writeWorkspaceState({ file, state }) {
  const backup = `${file}.bak-preimport-${new Date().toISOString().replace(/[-:.TZ]/g, '').slice(0, 14)}`
  copyFileSync(file, backup)
  const temporary = `${file}.tmp-${process.pid}-${Date.now()}`
  writeFileSync(temporary, `${JSON.stringify(state, null, 2)}\n`, 'utf8')
  renameSync(temporary, file)
  return backup
}

/** DSH desktop processes that may hold the registry in memory. */
export function runningDshProcesses() {
  const finder = process.platform === 'win32'
    ? ['tasklist', ['/FI', 'IMAGENAME eq DeepSeek Harness.exe', '/FO', 'CSV', '/NH']]
    : ['ps', ['-A', '-o', 'comm=']]
  const result = spawnSync(finder[0], finder[1], { encoding: 'utf8', timeout: 5000 })
  const text = result.stdout ?? ''
  if (process.platform === 'win32') {
    return /DeepSeek Harness\.exe/i.test(text)
  }
  return /DeepSeek Harness/i.test(text)
}

function workspaceByPath(state, canonical) {
  for (const [id, record] of Object.entries(state.tables?.workspaces ?? {})) {
    if (record.path === canonical) return { id, record }
    try {
      if (canonicalPath(record.path) === canonical) return { id, record }
    } catch {
      // an unreadable path simply cannot match
    }
  }
  return null
}

function removeFromOtherWorkspaces(state, sessionIds, keepWorkspaceId) {
  for (const [id, record] of Object.entries(state.tables?.workspaces ?? {})) {
    if (id === keepWorkspaceId) continue
    const before = record.sessionIds?.length ?? 0
    record.sessionIds = (record.sessionIds ?? []).filter((entry) => !sessionIds.has(entry))
    if ((record.sessionIds.length ?? 0) !== before) record.updatedAt = new Date().toISOString()
  }
}

/**
 * Insert `sessionIds` at the head of the workspace record, creating the record
 * when the directory is new. Returns a summary for the CLI.
 */
export function attachSessionsToWorkspace({
  workspacePath,
  title,
  sessionIds,
  dshHome = resolveDshHome(),
  dryRun = false,
}) {
  const canonical = canonicalPath(workspacePath)
  const { file, state } = readWorkspaceState(dshHome)
  if (!existsSync(canonical)) throw new Error(`workspace directory does not exist: ${canonical}`)

  let found = workspaceByPath(state, canonical)
  let created = false
  if (!found) {
    const id = randomUUID()
    const now = new Date().toISOString()
    state.tables ??= {}
    state.tables.workspaces ??= {}
    state.tables.workspaces[id] = {
      path: canonical,
      title: title || basename(canonical) || canonical,
      sessionIds: [],
      createdAt: now,
      updatedAt: now,
    }
    state.global.workspaceIds = [...(state.global.workspaceIds ?? []), id]
    found = { id, record: state.tables.workspaces[id] }
    created = true
  }

  const wanted = sessionIds.filter(Boolean)
  const existing = new Set(found.record.sessionIds ?? [])
  const added = wanted.filter((id) => !existing.has(id))
  // Keep the most recent import first, preserving the previous order.
  found.record.sessionIds = [...added, ...(found.record.sessionIds ?? [])]
  found.record.updatedAt = new Date().toISOString()
  removeFromOtherWorkspaces(state, new Set(wanted), found.id)

  let backup = null
  if (!dryRun) backup = writeWorkspaceState({ file, state })
  return {
    workspaceId: found.id,
    workspacePath: canonical,
    created,
    added,
    total: found.record.sessionIds.length,
    file,
    backup,
    dryRun,
  }
}
