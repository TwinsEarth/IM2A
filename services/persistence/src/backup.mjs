// 模块职责：数据备份与恢复演练——内测跑在 jsonfile 上，没有备份就没有"可运营"；恢复必须可演练、可验签，且**不得隐式改线上状态** / 契约来源：contracts/INTERFACES.md §44（纯 node:fs + node:crypto，零依赖）/ 关键不变量：①备份文件必须带 sha256（覆盖 data/createdAt/label），restore 前必须校验，损坏/篡改 → HASH_MISMATCH；②restore 只返回数据、不自动应用（由调用方决定切换时机）；③备份不存在 → NOT_FOUND；④轮转保留最近 keep 份且不删最新；⑤边界：本模块只管文件级（jsonfile/快照）备份，PostgreSQL 备份走数据库自身机制。
import { createHash } from 'node:crypto';
import {
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  unlinkSync,
  writeFileSync,
} from 'node:fs';
import { basename, isAbsolute, join, relative, resolve } from 'node:path';

export const BACKUP_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  HASH_MISMATCH: 'HASH_MISMATCH',
  IO_FAILED: 'IO_FAILED',
  INVALID: 'INVALID',
});

const CODE = BACKUP_CODE;
const FILE_PREFIX = 'im2agent-';
const FILE_SUFFIX = '.bak.json';
const FILE_PATTERN = /^im2agent-.*\.bak\.json$/;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isPlainObject = (value) => typeof value === 'object' && value !== null && !Array.isArray(value);
const sha256Hex = (text) => createHash('sha256').update(text, 'utf8').digest('hex');

/** 文件名安全化：Windows 不允许 ':'，统一把时间戳里的非安全字符替换掉 */
function safeStamp(iso) {
  return iso.replace(/[:]/g, '-').replace(/[^0-9A-Za-z._-]/g, '_');
}

export class BackupError extends Error {
  constructor(message, code = CODE.INVALID) {
    super(message);
    this.name = 'BackupError';
    this.code = code;
  }
}

/** 备份内容哈希的固定口径：sha256(JSON.stringify({ data, createdAt, label }))，不含 sha256 自身 */
function payloadHash({ data, createdAt, label }) {
  return sha256Hex(JSON.stringify({ data, createdAt, label }));
}

export class BackupService {
  #store;
  #dir;
  #keep;
  #clock;

  constructor({ store, dir, keep = 5, clock = () => new Date() } = {}) {
    if (!store || typeof store !== 'object' || typeof store.read !== 'function') {
      throw new TypeError('store 必填且需实现 read(): snapshot');
    }
    if (!isNonEmptyString(dir)) throw new TypeError('dir 必须是备份目录的路径字符串');
    if (!Number.isInteger(keep) || keep <= 0) throw new RangeError('keep 必须是正整数（至少保留最新一份）');
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    this.#store = store;
    this.#dir = resolve(dir);
    this.#keep = keep;
    this.#clock = clock;
    mkdirSync(this.#dir, { recursive: true });
  }

  /**
   * 从 store 读快照并落盘为 `<dir>/im2agent-<安全时间戳>.bak.json`。
   * 文件内容 = { meta:{ sha256, createdAt, label }, data }；sha256 覆盖 { data, createdAt, label }。
   * @returns 冻结 { file, sha256, bytes, createdAt, label }
   */
  create({ label = null } = {}) {
    if (label !== null && typeof label !== 'string') throw new TypeError('label 必须是字符串或 null');
    let snapshot;
    try {
      snapshot = this.#store.read();
    } catch {
      throw new BackupError('读取快照失败（store.read 抛错），未写任何备份', CODE.IO_FAILED);
    }
    let data;
    try {
      data = JSON.parse(JSON.stringify(snapshot)); // 规范化：保证落盘内容与后续哈希/恢复完全一致
    } catch {
      throw new BackupError('快照不可 JSON 序列化，拒绝生成不可恢复的备份', CODE.INVALID);
    }
    if (data === undefined) throw new BackupError('快照为 undefined，拒绝生成空备份', CODE.INVALID);

    const createdAt = this.#nowIso();
    const sha256 = payloadHash({ data, createdAt, label });
    const content = JSON.stringify({ meta: { sha256, createdAt, label }, data });
    const file = join(this.#dir, this.#nextFileName(createdAt));
    const tempFile = `${file}.tmp`;
    try {
      writeFileSync(tempFile, content, 'utf8');
      renameSync(tempFile, file); // 先写临时文件再原子改名，避免留下半截备份
    } catch {
      try {
        unlinkSync(tempFile);
      } catch {
        // 清理临时文件失败不掩盖原始错误
      }
      throw new BackupError('写备份文件失败', CODE.IO_FAILED);
    }
    return Object.freeze({ file, sha256, bytes: Buffer.byteLength(content, 'utf8'), createdAt, label });
  }

  /**
   * 列出备份元数据（按创建时间倒序；同刻按文件名序号倒序）。
   * 坏文件不丢弃、不假装不存在：追加 `{ file, name, skipped:true, reason }` 条目。
   */
  list() {
    let dirents;
    try {
      dirents = readdirSync(this.#dir, { withFileTypes: true });
    } catch {
      throw new BackupError('读取备份目录失败', CODE.IO_FAILED);
    }
    const valid = [];
    const skipped = [];
    for (const dirent of dirents) {
      if (!dirent.isFile() || !FILE_PATTERN.test(dirent.name)) continue;
      const file = join(this.#dir, dirent.name);
      const loaded = this.#load(file);
      if (!loaded.ok) {
        skipped.push(Object.freeze({ file, name: dirent.name, skipped: true, reason: loaded.code }));
        continue;
      }
      valid.push({
        file,
        name: dirent.name,
        sha256: loaded.parsed.meta.sha256,
        bytes: Buffer.byteLength(loaded.raw, 'utf8'),
        createdAt: loaded.parsed.meta.createdAt,
        label: loaded.parsed.meta.label ?? null,
        skipped: false,
        seq: this.#seqOf(dirent.name),
      });
    }
    valid.sort((a, b) => {
      if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? 1 : -1;
      if (a.seq !== b.seq) return b.seq - a.seq;
      return a.name < b.name ? 1 : -1;
    });
    skipped.sort((a, b) => (a.name < b.name ? 1 : -1));
    return Object.freeze([
      ...valid.map(({ seq, ...rest }) => Object.freeze(rest)),
      ...skipped,
    ]);
  }

  /** 重算内容 sha256 并与文件内 meta.sha256 比对；只返回判定，绝不抛异常。 */
  verify({ file } = {}) {
    try {
      const filePath = this.#resolveFile(file);
      if (filePath === null) {
        return Object.freeze({
          ok: false,
          code: isNonEmptyString(file) ? CODE.NOT_FOUND : CODE.INVALID,
        });
      }
      const loaded = this.#load(filePath);
      if (!loaded.ok) return Object.freeze({ ok: false, code: loaded.code });
      const expected = payloadHash({
        data: loaded.parsed.data,
        createdAt: loaded.parsed.meta.createdAt,
        label: loaded.parsed.meta.label,
      });
      if (loaded.parsed.meta.sha256 !== expected) {
        return Object.freeze({ ok: false, code: CODE.HASH_MISMATCH });
      }
      return Object.freeze({ ok: true, code: null });
    } catch {
      return Object.freeze({ ok: false, code: CODE.IO_FAILED });
    }
  }

  /**
   * 恢复演练：先 verify，成功后返回 { data, meta }。
   * ⚠️ **不写回 store、不自动应用**——是否切换线上状态由调用方决定（避免"恢复动作"隐式改数据）。
   */
  restore({ file } = {}) {
    const verdict = this.verify({ file });
    if (!verdict.ok) {
      throw new BackupError(`恢复前校验失败（${verdict.code}），已拒绝恢复`, verdict.code);
    }
    const filePath = this.#resolveFile(file);
    const loaded = this.#load(filePath);
    if (!loaded.ok) throw new BackupError(`备份读取失败（${loaded.code}）`, loaded.code);
    return Object.freeze({
      data: loaded.parsed.data,
      meta: Object.freeze({ ...loaded.parsed.meta }),
    });
  }

  /** 轮转：只保留最近 keep 份合法备份（最新一份永不删除），返回 { removed: file[] }。 */
  rotate() {
    const backups = this.list().filter((entry) => entry.skipped === false);
    if (backups.length <= this.#keep) return Object.freeze({ removed: Object.freeze([]) });
    const removed = [];
    try {
      for (const entry of backups.slice(this.#keep)) {
        unlinkSync(entry.file);
        removed.push(entry.file);
      }
    } catch {
      throw new BackupError('轮转删除旧备份失败（保留最新备份不变）', CODE.IO_FAILED);
    }
    return Object.freeze({ removed: Object.freeze(removed) });
  }

  // ── 内部 ────────────────────────────────────────────────────────────────

  #nowIso() {
    const sampled = this.#clock();
    if (!(sampled instanceof Date) || Number.isNaN(sampled.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return sampled.toISOString();
  }

  #nextFileName(createdAt) {
    const stamp = safeStamp(createdAt);
    let name = `${FILE_PREFIX}${stamp}${FILE_SUFFIX}`;
    let suffix = 1;
    while (existsSync(join(this.#dir, name))) {
      suffix += 1;
      name = `${FILE_PREFIX}${stamp}-${suffix}${FILE_SUFFIX}`;
    }
    return name;
  }

  /** 同刻备份的稳定排序依据：文件名里的冲突序号（无序号视为 1） */
  #seqOf(name) {
    const match = /-(\d+)$/.exec(name.slice(0, -FILE_SUFFIX.length));
    return match ? Number(match[1]) : 1;
  }

  #resolveFile(file) {
    if (!isNonEmptyString(file)) return null;
    const candidate = isAbsolute(file) ? resolve(file) : resolve(this.#dir, file);
    const rel = relative(this.#dir, candidate);
    if (rel === '' || rel.startsWith('..') || isAbsolute(rel)) return null; // 只允许备份目录内的文件
    return candidate;
  }

  #load(filePath) {
    let raw;
    try {
      raw = readFileSync(filePath, 'utf8');
    } catch (error) {
      if (error && error.code === 'ENOENT') return { ok: false, code: CODE.NOT_FOUND };
      return { ok: false, code: CODE.IO_FAILED };
    }
    let parsed;
    try {
      parsed = JSON.parse(raw);
    } catch {
      return { ok: false, code: CODE.INVALID };
    }
    if (!isPlainObject(parsed) || !isPlainObject(parsed.meta)) return { ok: false, code: CODE.INVALID };
    if (!isNonEmptyString(parsed.meta.sha256)) return { ok: false, code: CODE.INVALID };
    if (!Object.hasOwn(parsed, 'data')) return { ok: false, code: CODE.INVALID };
    if (!isNonEmptyString(parsed.meta.createdAt) || !Number.isFinite(Date.parse(parsed.meta.createdAt))) {
      return { ok: false, code: CODE.INVALID };
    }
    if (parsed.meta.label !== null && parsed.meta.label !== undefined && typeof parsed.meta.label !== 'string') {
      return { ok: false, code: CODE.INVALID };
    }
    if (parsed.meta.label === undefined) parsed.meta.label = null;
    return { ok: true, raw, parsed };
  }
}
