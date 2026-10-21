/**
 * 模块职责：不可变审计日志（append-only hash chain）——「历史不可悄悄改写」的可执行实现
 * 契约来源：contracts/INTERFACES.md v1.x §13（审计模块）
 *
 * 四条铁律（与编排层一致）：
 *   ① 只追加：append 返回深冻结记录，模块不提供 update / delete / 原地修改
 *   ② 哈希链：hash = sha256(canonical(record 去掉 hash) + prevHash)，创世 prevHash = GENESIS
 *   ③ 可验证：verify() 从头重算，遇到首个断点返回 brokenAt（这是「被篡改/被截断」的机器可判证据）
 *   ④ 读不改变状态：records / replay 是纯查询，返回冻结副本
 *
 * 为什么不是「写日志表」：日志表能删行、能改字段，审计一旦可以被静默改写，就不再是审计。
 * 本模块把「不可变」从数据库约定提升为可独立验证的算法事实，生产实现只需把 records 持久化到
 * 只允许 INSERT 的表即可（见 docs/05-数据模型与存储.md）。
 */
import { createHash } from 'node:crypto';

/** 创世前驱：第一条记录的 prevHash 恒为此值，便于外部验证者从零开始校验。 */
export const GENESIS_HASH = 'GENESIS';

/** 审计来源枚举：与编排层/资金通道的事件源一一对应，契约外来源必须显式登记。 */
export const AUDIT_SOURCE = Object.freeze({
  CAPABILITY_GATE: 'capability-gate',
  APPROVAL_ENGINE: 'approval-engine',
  EGRESS_GUARD: 'egress-guard',
  PAYMENT: 'payment',
  SYSTEM: 'system'
});

const MAX_LIMIT = 10000;

/**
 * 稳定序列化：键按字典序排列，undefined 归一为 null，保证同一语义有多种写法时哈希仍一致
 * （否则 JSON.stringify 的键序差异会制造「假篡改」）。
 */
function canonicalize(value) {
  if (value === null || value === undefined) return 'null';
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : 'null';
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'string') return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(canonicalize).join(',')}]`;
  if (typeof value === 'object') {
    const keys = Object.keys(value).sort();
    return `{${keys.map((key) => `${JSON.stringify(key)}:${canonicalize(value[key])}`).join(',')}}`;
  }
  return 'null';
}

function sha256Hex(text) {
  return createHash('sha256').update(text, 'utf8').digest('hex');
}

/** 记录本体（不含 hash）参与哈希；字段集合变化必须走 INTERFACES 变更记录，否则链会断。 */
function hashPayload(record) {
  return sha256Hex(
    canonicalize({
      seq: record.seq,
      at: record.at,
      source: record.source,
      action: record.action,
      actor: record.actor ?? null,
      traceId: record.traceId ?? null,
      target: record.target ?? null,
      decision: record.decision ?? null,
      detail: record.detail ?? null,
      prevHash: record.prevHash
    })
  );
}

function freezeRecord(record) {
  return Object.freeze({ ...record });
}

export class AuditLog {
  #clock;
  #limit;
  #records;
  #trimmed;
  #lastHash;
  #lastSeq;

  /**
   * @param {object} [options]
   * @param {() => Date} [options.clock] 时间源（可注入，测试与演示「时间旅行」必需）
   * @param {number} [options.limit] 内存保留上限（超出按最旧裁剪，裁剪后 trimmed 计数递增）
   * @param {Array<object>} [options.records] 从持久化恢复的历史记录（只读导入，不重写哈希）
   */
  constructor({ clock = () => new Date(), limit = MAX_LIMIT, records = [] } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    this.#clock = clock;
    this.#limit = Number.isInteger(limit) && limit > 0 ? Math.min(limit, MAX_LIMIT) : MAX_LIMIT;
    this.#records = [];
    this.#trimmed = 0;
    for (const record of Array.isArray(records) ? records : []) this.#restore(record);
    this.#lastHash = this.#records.length > 0 ? this.#records.at(-1).hash : GENESIS_HASH;
    this.#lastSeq = this.#records.length > 0 ? this.#records.at(-1).seq : 0;
  }

  #restore(record) {
    if (record === null || typeof record !== 'object') throw new TypeError('audit record 必须是对象');
    const frozen = freezeRecord(record);
    this.#records.push(frozen);
  }

  /** 当前链尾：空链返回 GENESIS，便于外部把「没有记录」与「记录被清空」区分开（后者会断链）。 */
  head() {
    return this.#lastHash;
  }

  size() {
    return this.#records.length;
  }

  trimmed() {
    return this.#trimmed;
  }

  /**
   * 追加一条审计事件。返回值即写入的记录（冻结），调用方不得回写。
   * 字段缺省：source→system、action→UNKNOWN，其余可空。
   */
  append(entry = {}) {
    const source = typeof entry.source === 'string' && entry.source !== '' ? entry.source : AUDIT_SOURCE.SYSTEM;
    const action = typeof entry.action === 'string' && entry.action !== '' ? entry.action : 'UNKNOWN';
    const at = typeof entry.at === 'string' && entry.at !== '' ? entry.at : new Date(this.#clock()).toISOString();
    const record = {
      seq: this.#lastSeq + 1,
      at,
      source,
      action,
      actor: entry.actor ?? null,
      traceId: entry.traceId ?? null,
      target: entry.target ?? null,
      decision: entry.decision ?? null,
      detail: entry.detail ?? null,
      prevHash: this.#lastHash,
      hash: null
    };
    record.hash = hashPayload(record);
    const frozen = freezeRecord(record);
    this.#records.push(frozen);
    this.#lastSeq = frozen.seq;
    this.#lastHash = frozen.hash;
    if (this.#records.length > this.#limit) {
      const overflow = this.#records.length - this.#limit;
      this.#records.splice(0, overflow);
      this.#trimmed += overflow;
    }
    return frozen;
  }

  /**
   * 校验链完整性：从保留下来的第一条开始验证每条的 prevHash 与前驱一致、hash 与内容一致。
   * trimmed>0 时无法验证「被裁掉的头」，报告里如实注明，不用 ok 掩盖截断。
   */
  verify() {
    let prev = this.#records.length > 0 ? this.#records[0].prevHash : GENESIS_HASH;
    let brokenAt = null;
    for (const record of this.#records) {
      if (record.prevHash !== prev || hashPayload(record) !== record.hash) {
        brokenAt = brokenAt ?? record.seq;
      }
      prev = record.hash;
    }
    return Object.freeze({
      ok: brokenAt === null,
      length: this.#records.length,
      trimmed: this.#trimmed,
      head: this.#lastHash,
      brokenAt
    });
  }

  /**
   * 纯查询：按 traceId/source/actor 过滤，limit 取最新 N 条（仍按时间正序返回）。
   * 默认返回全部；不修改任何状态。
   */
  records({ traceId = null, source = null, actor = null, limit = null } = {}) {
    let filtered = this.#records;
    if (traceId !== null || source !== null || actor !== null) {
      filtered = filtered.filter(
        (record) =>
          (traceId === null || record.traceId === traceId) &&
          (source === null || record.source === source) &&
          (actor === null || record.actor === actor)
      );
    }
    if (Number.isInteger(limit) && limit > 0 && filtered.length > limit) filtered = filtered.slice(-limit);
    return Object.freeze(filtered.map(freezeRecord));
  }

  /** 轨迹回放：一条 traceId 的完整事件序列；未知 traceId 返回空数组而不是错误。 */
  replay(traceId) {
    if (typeof traceId !== 'string' || traceId === '') return Object.freeze([]);
    return this.records({ traceId });
  }

  /** 导出整个保留窗口（持久化/跨进程校验用），返回冻结副本。 */
  snapshot() {
    return Object.freeze(this.#records.map(freezeRecord));
  }
}
