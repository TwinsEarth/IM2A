// 模块职责：不可变账本（DEPOSIT / CHARGE / REFUND / SETTLE 条目只追加、exactly-once 入账）/ 契约来源：contracts/INTERFACES.md §8（§37 v0.0.1 追加 DEPOSIT 充值流水，向后兼容：既有三类型语义不变）/ 关键不变量：idempotencyKey 全局唯一、重复投递返回首次结果且 duplicate:true、条目永不修改、金额必须是正整数分。
import { randomUUID } from 'node:crypto';

export const ENTRY_TYPE = Object.freeze({
  CHARGE: 'CHARGE',
  REFUND: 'REFUND',
  SETTLE: 'SETTLE',
  // v0.0.1 §37 加法：钱包充值流水（只追加的入金事件；balanceOf 的 CHARGE/REFUND 口径不变，钱包自行用条目推导可用余额）
  DEPOSIT: 'DEPOSIT',
});

const ENTRY_TYPES = new Set(Object.values(ENTRY_TYPE));

export class LedgerError extends Error {
  constructor(message, code = 'LEDGER_ERROR') {
    super(message);
    this.name = 'LedgerError';
    this.code = code;
  }
}

function describe(value) {
  if (typeof value === 'string') return JSON.stringify(value);
  if (typeof value === 'number') return String(value);
  return `${typeof value}(${String(value)})`;
}

function toIso(clock) {
  const now = clock();
  if (!(now instanceof Date) || Number.isNaN(now.getTime())) {
    throw new TypeError('clock 必须返回合法的 Date 实例');
  }
  return now.toISOString();
}

/**
 * 金额校验的唯一入口：只接受 > 0 的安全整数分。
 * 0 / 负数 / 小数 / 字符串 / NaN / Infinity / 超出安全整数范围 一律 INVALID_AMOUNT。
 */
export function assertPositiveCents(amountCents) {
  if (typeof amountCents !== 'number' || !Number.isSafeInteger(amountCents) || amountCents <= 0) {
    throw new LedgerError(`amountCents 必须是正整数分（cents），收到 ${describe(amountCents)}`, 'INVALID_AMOUNT');
  }
  return amountCents;
}

export class Ledger {
  #clock;
  /** @type {ReadonlyArray<object>} 只追加的条目序列 */
  #entries = [];
  /** @type {Map<string, object>} idempotencyKey -> entry（分布式部署时该键必须是 DB 唯一索引） */
  #byKey = new Map();

  constructor({ clock = () => new Date() } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    this.#clock = clock;
  }

  /**
   * 入账。查重 + 追加必须同步完成：Node 单线程内即为原子操作。
   * 分布式部署时，本方法的「查重 + 插入」必须由数据库唯一索引 + 单事务保证（见 §8 并发说明）。
   */
  post({ idempotencyKey, type, userId, amountCents, ref = null, meta = {} } = {}) {
    // 1) 参数校验先于查重：非法载荷永远不会被登记（避免坏键占位）
    if (typeof idempotencyKey !== 'string' || idempotencyKey.length === 0) {
      throw new LedgerError('idempotencyKey 必须是全局唯一的非空字符串', 'KEY_REQUIRED');
    }
    if (!ENTRY_TYPES.has(type)) {
      throw new LedgerError(
        `type 必须是 ${Object.values(ENTRY_TYPE).join(' / ')} 之一，收到 ${describe(type)}`,
        'INVALID_TYPE'
      );
    }
    assertPositiveCents(amountCents);
    if (typeof userId !== 'string' || userId.length === 0) {
      throw new TypeError('userId 必须是非空字符串');
    }
    if (ref !== null && typeof ref !== 'string') {
      throw new TypeError('ref 必须是字符串或 null');
    }
    if (meta === null || typeof meta !== 'object' || Array.isArray(meta)) {
      throw new TypeError('meta 必须是普通对象');
    }

    // 2) exactly-once：同一幂等键永远返回首次结果，绝不二次入账
    const existing = this.#byKey.get(idempotencyKey);
    if (existing !== undefined) {
      return Object.freeze({ entry: existing, duplicate: true });
    }

    const entry = Object.freeze({
      id: `led-${randomUUID()}`,
      idempotencyKey,
      type,
      userId,
      amountCents,
      ref,
      meta: Object.freeze({ ...meta }),
      at: toIso(this.#clock),
    });
    this.#entries.push(entry);
    this.#byKey.set(idempotencyKey, entry);
    return Object.freeze({ entry, duplicate: false });
  }

  /** 条目列表（新数组副本；元素本身冻结，外部改动副本不会影响账本）。 */
  entries({ userId = null } = {}) {
    if (userId === null) return [...this.#entries];
    if (typeof userId !== 'string' || userId.length === 0) {
      throw new TypeError('userId 必须是字符串或 null');
    }
    return this.#entries.filter((entry) => entry.userId === userId);
  }

  /**
   * 余额口径（§8 原文）：CHARGE 累加，REFUND 扣减。
   * SETTLE 是「结算给接收方」的对账事件，**不改变** 本方法口径的余额，
   * 否则发送方/接收方对账会重复计算（接收方结算收入应查 stats().settled 或 entries({userId})）。
   */
  balanceOf(userId) {
    if (typeof userId !== 'string' || userId.length === 0) {
      throw new TypeError('userId 必须是非空字符串');
    }
    let cents = 0;
    for (const entry of this.#entries) {
      if (entry.userId !== userId) continue;
      if (entry.type === ENTRY_TYPE.CHARGE) cents += entry.amountCents;
      else if (entry.type === ENTRY_TYPE.REFUND) cents -= entry.amountCents;
      // SETTLE：不计入余额（见上方口径说明）
    }
    return cents;
  }

  /** 查询幂等键对应的条目；未命中返回 undefined。 */
  findByIdempotencyKey(key) {
    if (typeof key !== 'string' || key.length === 0) return undefined;
    return this.#byKey.get(key);
  }

  /** 按类型统计条目**条数**（金额字段一律以 Cents 结尾，故此处为计数而非金额合计）。 */
  stats() {
    const counters = { count: 0, charged: 0, refunded: 0, settled: 0 };
    for (const entry of this.#entries) {
      counters.count += 1;
      if (entry.type === ENTRY_TYPE.CHARGE) counters.charged += 1;
      else if (entry.type === ENTRY_TYPE.REFUND) counters.refunded += 1;
      else if (entry.type === ENTRY_TYPE.SETTLE) counters.settled += 1;
    }
    return Object.freeze(counters);
  }

  /** 可序列化全量快照（供持久化/对账/测试）；返回冻结对象，entries 为只读副本。 */
  snapshot() {
    return Object.freeze({
      version: 1,
      count: this.#entries.length,
      entries: Object.freeze([...this.#entries]),
      at: toIso(this.#clock),
    });
  }
}
