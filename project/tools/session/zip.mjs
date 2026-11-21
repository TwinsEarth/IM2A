/**
 * Minimal, dependency-free ZIP reader for official DSH session exports.
 *
 * Supports stored (method 0) and deflate (method 8) entries, which is what the
 * session-log exporter produces. No Zip64 and no encryption: those are rejected
 * with an actionable error instead of being half-read.
 */
import { inflateRawSync } from 'node:zlib'

const EOCD_SIGNATURE = 0x06054b50
const CENTRAL_SIGNATURE = 0x02014b50
const LOCAL_SIGNATURE = 0x04034b50

function findEndOfCentralDirectory(buffer) {
  const minimum = Math.max(0, buffer.length - 0xffff - 22)
  for (let offset = buffer.length - 22; offset >= minimum; offset--) {
    if (buffer.readUInt32LE(offset) === EOCD_SIGNATURE) return offset
  }
  throw new Error('not a zip archive: end-of-central-directory record not found')
}

/**
 * Read a zip archive into `{ name, data }` entries.
 * @param {Buffer} buffer
 * @returns {{ entries: Array<{ name: string, data: Buffer }>, skipped: Array<{ name: string, reason: string }> }}
 */
export function readZip(buffer) {
  const eocd = findEndOfCentralDirectory(buffer)
  const entriesCount = buffer.readUInt16LE(eocd + 10)
  const centralSize = buffer.readUInt32LE(eocd + 12)
  const centralOffset = buffer.readUInt32LE(eocd + 16)
  if (entriesCount === 0xffff || centralOffset === 0xffffffff || centralSize === 0xffffffff) {
    throw new Error('Zip64 archives are not supported by this tool')
  }
  if (centralOffset + centralSize > eocd) throw new Error('corrupt zip: central directory outside file bounds')

  const entries = []
  const skipped = []
  let cursor = centralOffset
  for (let index = 0; index < entriesCount; index++) {
    if (buffer.readUInt32LE(cursor) !== CENTRAL_SIGNATURE) throw new Error(`corrupt zip: bad central header #${index}`)
    const method = buffer.readUInt16LE(cursor + 10)
    const compressedSize = buffer.readUInt32LE(cursor + 20)
    const uncompressedSize = buffer.readUInt32LE(cursor + 24)
    const nameLength = buffer.readUInt16LE(cursor + 28)
    const extraLength = buffer.readUInt16LE(cursor + 30)
    const commentLength = buffer.readUInt16LE(cursor + 32)
    const localOffset = buffer.readUInt32LE(cursor + 42)
    const rawName = buffer.subarray(cursor + 46, cursor + 46 + nameLength).toString('utf8')
    // The ZIP spec uses forward slashes, but Windows tools (Compress-Archive,
    // right-click "Send to > Compressed folder") write backslashes; normalise.
    const name = rawName.replace(/\\/g, '/')
    cursor += 46 + nameLength + extraLength + commentLength

    if (name.endsWith('/')) continue
    if (buffer.readUInt32LE(localOffset) !== LOCAL_SIGNATURE) throw new Error(`corrupt zip: bad local header for ${name}`)
    const localNameLength = buffer.readUInt16LE(localOffset + 26)
    const localExtraLength = buffer.readUInt16LE(localOffset + 28)
    const dataStart = localOffset + 30 + localNameLength + localExtraLength
    const raw = buffer.subarray(dataStart, dataStart + compressedSize)
    if (dataStart + compressedSize > buffer.length) throw new Error(`corrupt zip: entry ${name} runs past the file`)

    try {
      let data
      if (method === 0) data = Buffer.from(raw)
      else if (method === 8) data = inflateRawSync(raw)
      else {
        skipped.push({ name, reason: `unsupported compression method ${method}` })
        continue
      }
      if (uncompressedSize !== 0 && data.length !== uncompressedSize) {
        throw new Error(`size mismatch (${data.length} != ${uncompressedSize})`)
      }
      entries.push({ name, data })
    } catch (error) {
      skipped.push({ name, reason: error.message })
    }
  }
  return { entries, skipped }
}
