/**
 * On-disk DSH session store helpers.
 *
 * Layout (confirmed against the installed DSH 0.2.2-rc.5 sources):
 *   <DSH_HOME>/sessions/<projectKey(cwd)>/<sessionId>/session.v4.jsonl.zstd
 *
 * The header of a stored log carries the only workspace binding (`cwd`), and
 * DSH rejects a log whose on-disk project directory does not match that cwd.
 * Every helper here therefore treats `cwd` and the directory name as one unit.
 */
import { existsSync, mkdirSync, readdirSync, readFileSync, renameSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { decodeStoredLog, encodeStoredLog, projectKey, readStoredHeader } from './format.mjs'

export function resolveDshHome(env = process.env) {
  return env.DSH_HOME?.trim() || join(env.USERPROFILE || env.HOME || '.', '.dsh')
}

export function sessionsRoot(dshHome = resolveDshHome()) {
  return join(dshHome, 'sessions')
}

/** All stored logs under the sessions root: `{ id, cwd, file, projectDir, dir }`. */
export function listStoredLogs(dshHome = resolveDshHome()) {
  const root = sessionsRoot(dshHome)
  if (!existsSync(root)) return []
  const logs = []
  for (const projectEntry of readdirSync(root, { withFileTypes: true })) {
    if (!projectEntry.isDirectory()) continue
    const projectDir = join(root, projectEntry.name)
    for (const sessionEntry of readdirSync(projectDir, { withFileTypes: true })) {
      if (!sessionEntry.isDirectory()) continue
      const dir = join(projectDir, sessionEntry.name)
      const file = join(dir, 'session.v4.jsonl.zstd')
      if (!existsSync(file)) continue
      let header = null
      try {
        header = readHeaderQuick(file)
      } catch {
        // A torn or unreadable log is reported by its own id when possible.
      }
      logs.push({ id: header?.id ?? sessionEntry.name, cwd: header?.cwd, file, projectDir, dir, header })
    }
  }
  return logs
}

/** Decode just the header frame; falls back to the whole file for huge headers. */
export function readHeaderQuick(file) {
  const size = statSync(file).size
  if (size <= 256 * 1024) return readStoredHeader(readFileSync(file))
  const buffer = readFileSync(file)
  return readStoredHeader(buffer)
}

export function readStoredLog(file) {
  return decodeStoredLog(readFileSync(file))
}

/** Every stored log id, used to keep imported ids globally unique. */
export function storedSessionIds(dshHome = resolveDshHome()) {
  return new Set(listStoredLogs(dshHome).map((entry) => entry.id))
}

export function findLogById(id, dshHome = resolveDshHome()) {
  return listStoredLogs(dshHome).find((entry) => entry.id === id || entry.dir.endsWith(`\\${id}`) || entry.dir.endsWith(`/${id}`))
}

/**
 * Expand a session and all of its descendants (sub-agent sessions reference
 * their parent through `header.parentSession`).
 */
export function collectLineage(rootId, dshHome = resolveDshHome()) {
  const logs = listStoredLogs(dshHome)
  const root = logs.find((entry) => entry.id === rootId)
  if (!root) throw new Error(`session ${rootId} not found under ${sessionsRoot(dshHome)}`)
  const selected = [root]
  const ids = new Set([rootId])
  let added = true
  while (added) {
    added = false
    for (const entry of logs) {
      if (ids.has(entry.id)) continue
      const parent = entry.header?.parentSession
      if (parent && ids.has(parent)) {
        selected.push(entry)
        ids.add(entry.id)
        added = true
      }
    }
  }
  return selected
}

/**
 * Write one session log atomically under the project directory derived from
 * `cwd`. Refuses to overwrite an existing different session.
 *
 * @returns {string} the written file path.
 */
export function writeStoredLog({ dshHome = resolveDshHome(), id, cwd, header, events }) {
  const dir = join(sessionsRoot(dshHome), projectKey(cwd), id)
  const file = join(dir, 'session.v4.jsonl.zstd')
  if (existsSync(file)) throw new Error(`refusing to overwrite existing session log: ${file}`)
  mkdirSync(dir, { recursive: true })
  const bytes = encodeStoredLog(header, events)
  const temporary = `${file}.tmp-${process.pid}-${Date.now()}`
  writeFileSync(temporary, bytes)
  renameSync(temporary, file)
  return file
}

/** Copy a session log byte-for-byte (used for backups only). */
export function copyStoredLog(sourceFile, targetFile) {
  const temporary = `${targetFile}.tmp-${process.pid}-${Date.now()}`
  mkdirSync(dirname(targetFile), { recursive: true })
  writeFileSync(temporary, readFileSync(sourceFile))
  renameSync(temporary, targetFile)
}
