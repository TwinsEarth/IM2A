#!/usr/bin/env node
/**
 * Snapshot a live DSH session (and its sub-agent descendants) into a portable
 * directory: raw `.zstd` logs plus the projection-cache file when present.
 *
 *   node tools/session/snapshot-session.mjs --from-dsh session-<id> --out sessions/current
 *
 * This is a byte-for-byte copy; it never rewrites cwd or ids. To restore the
 * snapshot into a workspace use `restore-session.mjs --snapshot <dir>`.
 */
import { copyFileSync, existsSync, mkdirSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import { resolveDshHome, collectLineage } from './store.mjs'

function parseArgs(argv) {
  const args = { out: 'sessions/current' }
  for (let index = 0; index < argv.length; index++) {
    const token = argv[index]
    const next = () => {
      index += 1
      if (index >= argv.length) throw new Error(`${token} needs a value`)
      return argv[index]
    }
    if (token === '--from-dsh') args.fromDsh = next()
    else if (token === '--out') args.out = next()
    else if (token === '--help') args.help = true
    else throw new Error(`unknown option: ${token}`)
  }
  return args
}

function main() {
  const args = parseArgs(process.argv.slice(2))
  if (args.help || !args.fromDsh) {
    console.log('usage: node tools/session/snapshot-session.mjs --from-dsh <sessionId> [--out sessions/current]')
    return
  }
  const dshHome = resolveDshHome()
  const outRoot = resolve(args.out)
  const lineage = collectLineage(args.fromDsh)
  const copied = []
  for (const entry of lineage) {
    const targetDir = join(outRoot, basename(entry.dir))
    mkdirSync(targetDir, { recursive: true })
    const target = join(targetDir, 'session.v4.jsonl.zstd')
    copyFileSync(entry.file, target)
    const cache = join(dshHome, 'storages', 'session_projcache', 'sessions', `${entry.id}.json`)
    let cacheTarget = null
    if (existsSync(cache)) {
      cacheTarget = join(outRoot, `${entry.id}.json`)
      copyFileSync(cache, cacheTarget)
    }
    copied.push({ id: entry.id, role: entry.header?.parentSession ? 'subagent' : 'root', source: entry.file, target, cache: cacheTarget })
  }
  console.log(JSON.stringify({ ok: true, out: outRoot, dshHome, copied }, null, 2))
}

main()
