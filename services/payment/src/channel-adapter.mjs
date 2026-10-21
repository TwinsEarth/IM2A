// 模块职责：支付渠道抽象与回调验签（渠道意图 / 原始 body HMAC 验签 / 幂等入账），用 mock 渠道把真实支付语义做完整，接微信/支付宝只换 provider / 契约来源：contracts/INTERFACES.md §49（复用 §8 ledger 的 DEPOSIT/REFUND）/ 关键不变量：①回调必须先验签（x-signature: sha256=<hex>，HMAC-SHA256 over 原始 body 字节），失败一律 SIGNATURE_INVALID 且**不产生任何账本流水**；②x-timestamp 超窗 → TIMESTAMP_EXPIRED；③同一 providerTxnId 重复回调幂等（只 DEPOSIT 一次）；④金额必须与订单一致（AMOUNT_MISMATCH 不改状态不入账）；⑤只有 PAID 可退且重复退款幂等；⑥未启用渠道 → CHANNEL_UNSUPPORTED，绝不静默降级到 mock。
import { createHmac, randomUUID, timingSafeEqual } from 'node:crypto';
import { ENTRY_TYPE, assertPositiveCents } from './ledger.mjs';

export const CHANNEL_KIND = Object.freeze({ MOCK: 'mock', WECHAT: 'wechat', ALIPAY: 'alipay' });

export const PAYMENT_STATE = Object.freeze({
  INTENT: 'INTENT',
  PENDING: 'PENDING',
  PAID: 'PAID',
  FAILED: 'FAILED',
  REFUNDED: 'REFUNDED',
});

export const PAYMENT_CODE = Object.freeze({
  CHANNEL_UNSUPPORTED: 'CHANNEL_UNSUPPORTED',
  ORDER_EXISTS: 'ORDER_EXISTS',
  ORDER_NOT_FOUND: 'ORDER_NOT_FOUND',
  SIGNATURE_INVALID: 'SIGNATURE_INVALID',
  AMOUNT_MISMATCH: 'AMOUNT_MISMATCH',
  ILLEGAL_STATE: 'ILLEGAL_STATE',
  CALLBACK_REPLAYED: 'CALLBACK_REPLAYED',
  TIMESTAMP_EXPIRED: 'TIMESTAMP_EXPIRED',
});

const CODE = PAYMENT_CODE;
const CHANNEL_VALUES = new Set(Object.values(CHANNEL_KIND));
const STATE_VALUES = new Set(Object.values(PAYMENT_STATE));
const CALLBACK_STATUSES = new Set(['SUCCESS', 'PENDING', 'FAILED']);
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isPlainObject = (value) => typeof value === 'object' && value !== null && !Array.isArray(value);

/** headers 大小写不敏感取值（HTTP header 名不区分大小写） */
function headerOf(headers, name) {
  if (!isPlainObject(headers)) return undefined;
  const target = name.toLowerCase();
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === target) return value;
  }
  return undefined;
}

export class PaymentChannelError extends Error {
  constructor(message, code = CODE.ILLEGAL_STATE) {
    super(message);
    this.name = 'PaymentChannelError';
    this.code = code;
  }
}

export class PaymentIntentService {
  #clock;
  #ledger;
  #channels;
  #secret;
  #timestampWindowMs;
  /** @type {Map<string, object>} orderId -> 内部订单记录（对外只暴露冻结视图） */
  #orders = new Map();
  /** @type {Map<string, string>} providerTxnId -> orderId（跨订单防重放） */
  #txnToOrder = new Map();
  /** @type {string[]} 创建顺序 */
  #order = [];

  constructor({ clock = () => new Date(), ledger, channels = [CHANNEL_KIND.MOCK], secret = null, timestampWindowMs = 5 * 60e3 } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (
      !ledger ||
      typeof ledger !== 'object' ||
      typeof ledger.post !== 'function' ||
      typeof ledger.entries !== 'function' ||
      typeof ledger.findByIdempotencyKey !== 'function'
    ) {
      throw new TypeError('ledger 必填（§8 Ledger 实例：支付入账的唯一真相）');
    }
    if (!Array.isArray(channels) || channels.some((channel) => !CHANNEL_VALUES.has(channel))) {
      throw new TypeError(`channels 必须是 ${Object.values(CHANNEL_KIND).join(' / ')} 的数组`);
    }
    if (secret !== null && !isNonEmptyString(secret)) {
      throw new TypeError('secret 必须是非空字符串或 null（null 时回调一律验签失败，fail-closed）');
    }
    if (!Number.isInteger(timestampWindowMs) || timestampWindowMs <= 0) {
      throw new RangeError('timestampWindowMs 必须是正整数（毫秒）');
    }
    this.#clock = clock;
    this.#ledger = ledger;
    this.#channels = new Set(channels);
    this.#secret = secret;
    this.#timestampWindowMs = timestampWindowMs;
  }

  /**
   * 创建支付意图（渠道订单）。同 orderId 重复创建 → ORDER_EXISTS。
   * @returns 冻结 order
   */
  createIntent({ userId, amountCents, channel = CHANNEL_KIND.MOCK, orderId = null, description = null } = {}) {
    if (!isNonEmptyString(userId)) throw new TypeError('userId 必须是非空字符串');
    assertPositiveCents(amountCents); // 非法金额 → LedgerError(INVALID_AMOUNT)，与账本口径一致
    if (!this.#channels.has(channel)) {
      throw new PaymentChannelError(
        `渠道未启用: ${String(channel)}（已启用 ${[...this.#channels].join(', ') || '无'}；不允许静默降级到 mock）`,
        CODE.CHANNEL_UNSUPPORTED
      );
    }
    if (orderId !== null && !isNonEmptyString(orderId)) throw new TypeError('orderId 必须是字符串或 null');
    if (description !== null && typeof description !== 'string') throw new TypeError('description 必须是字符串或 null');
    const resolvedOrderId = orderId ?? `order-${randomUUID()}`;
    if (this.#orders.has(resolvedOrderId)) {
      throw new PaymentChannelError(`订单已存在: ${resolvedOrderId}`, CODE.ORDER_EXISTS);
    }
    const now = this.#now();
    const record = {
      orderId: resolvedOrderId,
      userId,
      amountCents,
      channel,
      state: PAYMENT_STATE.INTENT,
      payUrl: `mock://pay/${resolvedOrderId}`, // mock 渠道占位；接真实渠道时由 provider 返回
      description,
      createdAt: now.toISOString(),
      paidAt: null,
      providerTxnId: null,
      refundedAt: null,
      depositEntryId: null,
      refundEntryId: null,
    };
    this.#orders.set(record.orderId, record);
    this.#order.push(record.orderId);
    return this.#view(record);
  }

  /**
   * 处理渠道回调。顺序：渠道支持 → 验签（原始 body HMAC）→ 时间窗 → 解析 body/查订单 →
   * 金额一致 → 幂等入账（ledger.DEPOSIT，幂等键 deposit:<orderId>）。
   * @returns 冻结 { ok, order, ledgerEntry?, duplicate? }
   */
  handleCallback({ channel, headers, body } = {}) {
    if (!this.#channels.has(channel)) {
      throw new PaymentChannelError(
        `回调渠道未启用: ${String(channel)}（不允许静默降级到 mock）`,
        CODE.CHANNEL_UNSUPPORTED
      );
    }
    const raw = this.#verifySignature({ headers, body });
    this.#assertFresh(headers);

    let payload;
    try {
      payload = JSON.parse(typeof body === 'string' ? body : raw.toString('utf8'));
    } catch {
      throw new PaymentChannelError('回调 body 不是合法 JSON', CODE.ILLEGAL_STATE);
    }
    if (!isPlainObject(payload)) throw new PaymentChannelError('回调 body 必须是 JSON 对象', CODE.ILLEGAL_STATE);
    if (!isNonEmptyString(payload.orderId)) throw new PaymentChannelError('回调缺少 orderId', CODE.ORDER_NOT_FOUND);

    const record = this.#orders.get(payload.orderId);
    if (!record) throw new PaymentChannelError(`订单不存在: ${payload.orderId}`, CODE.ORDER_NOT_FOUND);
    // 金额必须与订单一致（先于任何状态变更/入账）
    if (!Number.isSafeInteger(payload.amountCents) || payload.amountCents !== record.amountCents) {
      throw new PaymentChannelError(
        `回调金额与订单不一致: order=${record.amountCents} callback=${String(payload.amountCents)}`,
        CODE.AMOUNT_MISMATCH
      );
    }
    const status = payload.status ?? 'SUCCESS';
    if (!CALLBACK_STATUSES.has(status)) {
      throw new PaymentChannelError(`未知回调状态: ${String(status)}`, CODE.ILLEGAL_STATE);
    }

    if (status === 'PENDING') {
      if (record.state === PAYMENT_STATE.INTENT || record.state === PAYMENT_STATE.PENDING) {
        record.state = PAYMENT_STATE.PENDING;
      }
      return Object.freeze({ ok: true, order: this.#view(record), duplicate: false });
    }
    if (status === 'FAILED') {
      if (record.state === PAYMENT_STATE.INTENT || record.state === PAYMENT_STATE.PENDING) {
        record.state = PAYMENT_STATE.FAILED;
      }
      return Object.freeze({ ok: true, order: this.#view(record), duplicate: false });
    }

    // SUCCESS 路径
    if (!isNonEmptyString(payload.providerTxnId)) {
      throw new PaymentChannelError('成功回调缺少 providerTxnId（幂等依据）', CODE.ILLEGAL_STATE);
    }
    const txnOwner = this.#txnToOrder.get(payload.providerTxnId);
    if (txnOwner !== undefined && txnOwner !== record.orderId) {
      throw new PaymentChannelError(
        `providerTxnId 已被其它订单使用: ${payload.providerTxnId} → ${txnOwner}`,
        CODE.CALLBACK_REPLAYED
      );
    }
    if (record.state === PAYMENT_STATE.REFUNDED) {
      throw new PaymentChannelError(`订单已退款，不能再支付: ${record.orderId}`, CODE.ILLEGAL_STATE);
    }
    if (record.state === PAYMENT_STATE.PAID) {
      if (record.providerTxnId !== payload.providerTxnId) {
        throw new PaymentChannelError(
          `订单已支付但 providerTxnId 不同（疑似重复回调/串单）: ${payload.providerTxnId}`,
          CODE.CALLBACK_REPLAYED
        );
      }
      const entry = this.#ledger.findByIdempotencyKey(`deposit:${record.orderId}`) ?? null;
      return Object.freeze({ ok: true, order: this.#view(record), ledgerEntry: entry, duplicate: true });
    }

    // 幂等入账：deposit:<orderId> 是唯一幂等键（崩溃恢复时 duplicate:true，不会二次入账）
    const { entry, duplicate } = this.#ledger.post({
      idempotencyKey: `deposit:${record.orderId}`,
      type: ENTRY_TYPE.DEPOSIT,
      userId: record.userId,
      amountCents: record.amountCents,
      ref: record.orderId,
      meta: { orderId: record.orderId, channel: record.channel, providerTxnId: payload.providerTxnId },
    });
    record.state = PAYMENT_STATE.PAID;
    record.paidAt = this.#nowIso();
    record.providerTxnId = payload.providerTxnId;
    record.depositEntryId = entry.id;
    this.#txnToOrder.set(payload.providerTxnId, record.orderId);
    return Object.freeze({ ok: true, order: this.#view(record), ledgerEntry: entry, duplicate });
  }

  /**
   * 退款：仅 PAID 可退；重复退款幂等（ledger.REFUND 幂等键 refund:<orderId>）。
   * @returns 冻结 { ok, order, ledgerEntry, duplicate }
   */
  refund({ orderId, reason = null } = {}) {
    const record = this.#require(orderId);
    if (reason !== null && typeof reason !== 'string') throw new TypeError('reason 必须是字符串或 null');
    if (record.state === PAYMENT_STATE.REFUNDED) {
      const entry = this.#ledger.findByIdempotencyKey(`refund:${record.orderId}`) ?? null;
      return Object.freeze({ ok: true, order: this.#view(record), ledgerEntry: entry, duplicate: true });
    }
    if (record.state !== PAYMENT_STATE.PAID) {
      throw new PaymentChannelError(`订单状态 ${record.state} 不允许退款（仅 PAID）`, CODE.ILLEGAL_STATE);
    }
    const { entry, duplicate } = this.#ledger.post({
      idempotencyKey: `refund:${record.orderId}`,
      type: ENTRY_TYPE.REFUND,
      userId: record.userId,
      amountCents: record.amountCents,
      ref: record.orderId,
      meta: { orderId: record.orderId, channel: record.channel, reason },
    });
    record.state = PAYMENT_STATE.REFUNDED;
    record.refundedAt = this.#nowIso();
    record.refundEntryId = entry.id;
    return Object.freeze({ ok: true, order: this.#view(record), ledgerEntry: entry, duplicate });
  }

  /** v0.1.2：导出可持久化状态（订单 + providerTxnId 防重放索引） */
  exportState() {
    return {
      orders: this.#order.map((orderId) => ({ ...this.#orders.get(orderId) })),
      txnToOrder: [...this.#txnToOrder.entries()]
    };
  }

  /** v0.1.2：从持久化状态恢复（恢复后幂等索引仍生效，重放回调不会二次入账） */
  importState(state = {}) {
    if (state === null || typeof state !== 'object' || !Array.isArray(state.orders)) {
      throw new TypeError('importState 需要 { orders: [] }');
    }
    this.#orders = new Map(state.orders.map((record) => [record.orderId, record]));
    this.#order = state.orders.map((record) => record.orderId);
    this.#txnToOrder = new Map(Array.isArray(state.txnToOrder) ? state.txnToOrder : []);
    return { orders: this.#orders.size };
  }

  get(orderId) {
    return this.#view(this.#require(orderId));
  }

  /** 列出订单（可按 state / userId 过滤），按创建顺序；返回冻结数组。 */
  list({ state = null, userId = null } = {}) {
    if (state !== null && !STATE_VALUES.has(state)) throw new TypeError(`未知支付状态: ${String(state)}`);
    if (userId !== null && !isNonEmptyString(userId)) throw new TypeError('userId 必须是字符串或 null');
    const result = [];
    for (const orderId of this.#order) {
      const record = this.#orders.get(orderId);
      if (state !== null && record.state !== state) continue;
      if (userId !== null && record.userId !== userId) continue;
      result.push(this.#view(record));
    }
    return Object.freeze(result);
  }

  stats() {
    const counters = {
      total: 0,
      intent: 0,
      pending: 0,
      paid: 0,
      failed: 0,
      refunded: 0,
      paidAmountCents: 0,
      refundedAmountCents: 0,
    };
    for (const record of this.#orders.values()) {
      counters.total += 1;
      if (record.state === PAYMENT_STATE.INTENT) counters.intent += 1;
      else if (record.state === PAYMENT_STATE.PENDING) counters.pending += 1;
      else if (record.state === PAYMENT_STATE.PAID) {
        counters.paid += 1;
        counters.paidAmountCents += record.amountCents;
      } else if (record.state === PAYMENT_STATE.FAILED) counters.failed += 1;
      else if (record.state === PAYMENT_STATE.REFUNDED) {
        counters.refunded += 1;
        counters.refundedAmountCents += record.amountCents;
      }
    }
    return Object.freeze(counters);
  }

  // ── 内部 ────────────────────────────────────────────────────────────────

  #now() {
    const sampled = this.#clock();
    if (!(sampled instanceof Date) || Number.isNaN(sampled.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return sampled;
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #require(orderId) {
    if (!isNonEmptyString(orderId)) throw new TypeError('orderId 必须是非空字符串');
    const record = this.#orders.get(orderId);
    if (!record) throw new PaymentChannelError(`订单不存在: ${orderId}`, CODE.ORDER_NOT_FOUND);
    return record;
  }

  /** HMAC-SHA256 over 原始 body 字节；未配置 secret 时 fail-closed（一律 SIGNATURE_INVALID）。 */
  #verifySignature({ headers, body }) {
    if (!isNonEmptyString(this.#secret)) {
      throw new PaymentChannelError('未配置 secret，拒绝任何回调（fail-closed）', CODE.SIGNATURE_INVALID);
    }
    let raw;
    if (typeof body === 'string') raw = Buffer.from(body, 'utf8');
    else if (Buffer.isBuffer(body)) raw = body;
    else throw new PaymentChannelError('回调 body 必须是字符串或 Buffer（按原始字节验签）', CODE.SIGNATURE_INVALID);

    const header = headerOf(headers, 'x-signature');
    if (typeof header !== 'string') {
      throw new PaymentChannelError('缺少 x-signature 头', CODE.SIGNATURE_INVALID);
    }
    const matched = /^sha256=([0-9a-fA-F]{64})$/.exec(header.trim());
    if (!matched) {
      throw new PaymentChannelError('x-signature 格式必须是 sha256=<64 位 hex>', CODE.SIGNATURE_INVALID);
    }
    const expected = createHmac('sha256', this.#secret).update(raw).digest();
    const provided = Buffer.from(matched[1], 'hex');
    if (provided.length !== expected.length || !timingSafeEqual(provided, expected)) {
      throw new PaymentChannelError('回调签名校验失败', CODE.SIGNATURE_INVALID);
    }
    return raw;
  }

  /** 时间戳窗口：支持毫秒或秒（< 1e12 视为秒），超窗视为过期（防重放）。 */
  #assertFresh(headers) {
    const header = headerOf(headers, 'x-timestamp');
    const value = typeof header === 'number' ? header : Number(header);
    if (!Number.isFinite(value)) {
      throw new PaymentChannelError('缺少或非法的 x-timestamp 头', CODE.TIMESTAMP_EXPIRED);
    }
    const timestampMs = value < 1e12 ? value * 1000 : value;
    const nowMs = this.#now().getTime();
    if (Math.abs(nowMs - timestampMs) > this.#timestampWindowMs) {
      throw new PaymentChannelError(
        `回调时间戳超出窗口（${Math.abs(nowMs - timestampMs)}ms > ${this.#timestampWindowMs}ms）`,
        CODE.TIMESTAMP_EXPIRED
      );
    }
  }

  #view(record) {
    return Object.freeze({
      orderId: record.orderId,
      userId: record.userId,
      amountCents: record.amountCents,
      channel: record.channel,
      state: record.state,
      payUrl: record.payUrl,
      createdAt: record.createdAt,
      paidAt: record.paidAt,
      providerTxnId: record.providerTxnId,
      refundedAt: record.refundedAt,
    });
  }
}
