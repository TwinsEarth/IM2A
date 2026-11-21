/**
 * DSH session log format helpers (v4).
 *
 * A stored log is a sequence of independent zstd frames; the first frame must
 * contain exactly the header line (`{"type":"session",...}\n`) and every later
 * frame must end on a JSONL line boundary. Frames carry a zstd checksum.
 *
 * The functions here are dependency-free on purpose: the project tools must run
 * on a fresh machine with a stock Node (>= 22.15 for zstd).
 */
import { randomUUID } from 'node:crypto'
import { constants as zlibConstants, zstdCompressSync, zstdDecompressSync } from 'node:zlib'

const MAGIC = Buffer.from([0x28, 0xb5, 0x2f, 0xfd])
/** ZSTD_c_checksumFlag is 200 in the public zstd API; Node exposes it when built with zstd. */
const CHECKSUM_FLAG = zlibConstants.ZSTD_c_checksumFlag ?? 200

/** Compress one block of text into a single checksummed zstd frame. */
export function zstdFrame(text) {
  return zstdCompressSync(Buffer.from(text, 'utf8'), { params: { [CHECKSUM_FLAG]: 1 } })
}

/** Split a concatenated zstd file into decoded chunks. */
function decodeFrames(buffer) {
  const offsets = []
  let cursor = 0
  while ((cursor = buffer.indexOf(MAGIC, cursor)) !== -1) {
    offsets.push(cursor)
    cursor += MAGIC.length
  }
  if (offsets.length === 0) throw new Error('not a zstd stream: magic 28 b5 2f fd not found')
  if (offsets[0] !== 0) throw new Error(`zstd stream has ${offsets[0]} leading byte(s)`)
  const chunks = []
  for (let index = 0; index < offsets.length; index++) {
    const end = index + 1 < offsets.length ? offsets[index + 1] : buffer.length
    try {
      chunks.push(zstdDecompressSync(buffer.subarray(offsets[index], end)))
    } catch (error) {
      throw new Error(`invalid zstd frame #${index} at byte ${offsets[index]}: ${error.message}`)
    }
  }
  return chunks
}

/** Decode the header of a stored log without decompressing the whole file. */
export function readStoredHeader(buffer) {
  const first = buffer.indexOf(MAGIC)
  if (first !== 0) throw new Error('not a zstd session log')
  // The header is exactly the first frame; decode it alone.
  const second = buffer.indexOf(MAGIC, MAGIC.length)
  const end = second === -1 ? buffer.length : second
  const text = zstdDecompressSync(buffer.subarray(0, end)).toString('utf8')
  const newline = text.indexOf('\n')
  return JSON.parse(text.slice(0, newline === -1 ? undefined : newline))
}

/** Decode a stored log into `{ header, headerLine, events }`. */
export function decodeStoredLog(buffer) {
  const text = Buffer.concat(decodeFrames(buffer)).toString('utf8')
  return decodeTextLog(text)
}

/** Decode a plaintext `session.vN.jsonl` export into `{ header, headerLine, events }`. */
export function decodeTextLog(text) {
  const lines = String(text).split('\n')
  while (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
  if (lines.length === 0) throw new Error('empty session log')
  const header = JSON.parse(lines[0])
  if (header.type !== 'session') throw new Error('first line is not a session header')
  return { header, headerLine: lines[0], events: lines.slice(1) }
}

/** Encode `{ header, events }` as the two-frame stored format. */
export function encodeStoredLog(header, events) {
  const headerLine = JSON.stringify(header) + '\n'
  const body = events.length > 0 ? events.map((line) => line + '\n').join('') : ''
  return Buffer.concat([zstdFrame(headerLine), zstdFrame(body)])
}

/**
 * Project directory name for a cwd (mirrors the persistence implementation):
 * runs of `/`, `\` and `:` collapse to one `-`, unsafe characters become
 * `~XXXX` (upper-case UTF-16 code unit), leading `-` is stripped, 251 chars max,
 * wrapped in `--`.
 */
export function projectKey(cwd) {
  if (cwd === undefined || cwd === null || cwd === '') return '_no-cwd'
  let text = String(cwd).replace(/[\\/:]+/g, '-')
  text = text.replace(/[^A-Za-z0-9._-]/g, (char) => `~${char.charCodeAt(0).toString(16).toUpperCase().padStart(4, '0')}`)
  text = text.replace(/^-+/, '')
  text = text.slice(0, 251)
  return `--${text}--`
}

/** Fresh id for an imported root session (`session-<uuid>`). */
export function newSessionId() {
  return `session-${randomUUID()}`
}

/** Fresh id for an imported sub-session (no prefix, matching DSH). */
export function newChildId() {
  return randomUUID()
}

/**
 * Rewrite a decoded session for import:
 *  - every occurrence of an old session id is replaced (ids are opaque UUIDs);
 *  - the header gets the new id/cwd/parentSession;
 *  - event lines keep their content otherwise.
 *
 * @returns {{ header: object, events: string[] }}
 */
export function rewriteSession({ header, events, newId, newCwd, newParentSession, idMap = new Map() }) {
  const replaceIds = (line) => {
    let out = line
    for (const [from, to] of idMap) {
      if (from && from !== to) out = out.split(from).join(to)
    }
    return out
  }
  const nextHeader = { ...header }
  if (newId) nextHeader.id = newId
  if (newCwd) nextHeader.cwd = newCwd
  if (newParentSession === null) delete nextHeader.parentSession
  else if (newParentSession) nextHeader.parentSession = newParentSession
  return { header: nextHeader, events: events.map(replaceIds) }
}
