// 模块职责：陌生人付费消息 9 态状态机 + 两档计费（SEND_PAY / VIEW_REPLY_PAY）/ 契约来源：contracts/INTERFACES.md §9 / 关键不变量：先 Agent 预审后付费（未 screen 不得 pay）、BLOCK 与 REFUSED 永不可复活为可付、同一 messageId 只扣一次（账本幂等键 pay:<messageId>）、仅 REPLIED 之前可退、终态不可再流转。
import { randomUUID } from 'node:crypto';
import { ENTRY_TYPE, assertPositiveCents } from './ledger.mjs';

export const MSG_STATE = Object.freeze({
  INTENT: 'INTENT',
  AWAITING_PAYMENT: 'AWAITING_PAYMENT',
  REFUSED: 'REFUSED',
  PAID: 'PAID',
  VIEWED: 'VIEWED',
  REPLIED: 'REPLIED',
  SETTLED: 'SETTLED',
  REFUNDED: 'REFUNDED',
  EXPIRED: 'EXPIRED',
});

export const BILLING = Object.freeze({ SEND_PAY: 1, VIEW_REPLY_PAY: 2 });

const BILLING_MODES = new Set(Object.values(BILLING));
const VERDICTS = new Set(['PASS', 'BLOCK']);
const CHARGED_STATES = new Set([MSG_STATE.PAID, MSG_STATE.VIEWED, MSG_STATE.REPLIED]);
const TERMINAL_STATES = new Set([
  MSG_STATE.REFUSED,
  MSG_STATE.REFUNDED,
  MSG_STATE.EXPIRED,
  MSG_STATE.SETTLED,
]);

const PAY_KEY = (messageId) => `pay:${messageId}`;
const SETTLE_KEY = (messageId) => `settle:${messageId}`;
const REFUND_KEY = (messageId) => `refund:${messageId}`;

export class PaidMessageError extends Error {
  constructor(message, code = 'PAID_MESSAGE_ERROR') {
    super(message);
    this.name = 'PaidMessageError';
    this.code = code;
  }
}

function isNonEmptyString(value) {
  return typeof value === 'string' && value.length > 0;
}

function isPlainObject(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function toIso(clock) {
  const now = clock();
  if (!(now instanceof Date) || Number.isNaN(now.getTime())) {
    throw new TypeError('clock 必须返回合法的 Date 实例');
  }
  return now.toISOString();
}

export class PaidMessageService {
  #clock;
  /** @type {Map<string, object>} messageId -> frozen message（只读，状态迁移用新对象覆盖） */
  #messages = new Map();
  /** @type {Map<string, object>} submitIntent 幂等键 -> message */
  #intentKeys = new Map();

  constructor({ ledger, clock = () => new Date(), dailySendCap = 3, approveCapCents = 50000 } = {}) {
    if (!ledger || typeof ledger.post !== 'function' || typeof ledger.findByIdempotencyKey !== 'function') {
      throw new TypeError('ledger 必填（需实现 post / findByIdempotencyKey）');
    }
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!Number.isInteger(dailySendCap) || dailySendCap < 0) {
      throw new RangeError('dailySendCap 必须是 >= 0 的整数');
    }
    if (!Number.isSafeInteger(approveCapCents) || approveCapCents <= 0) {
      throw new RangeError('approveCapCents 必须是正整数分');
    }
    this.ledger = ledger;
    this.dailySendCap = dailySendCap;
    this.approveCapCents = approveCapCents;
    this.#clock = clock;
  }

  /**
   * 提交发送意图（不扣费）。幂等键相同的重试返回首次创建的消息，
   * 且**先于**每日额度校验 —— 网络重试不能被误判为超限。
   */
  submitIntent({
    senderUserId,
    senderAgentId = null,
    receiverUserId,
    topic = '',
    summary = '',
    billingMode,
    amountCents,
    idempotencyKey,
  } = {}) {
    if (isNonEmptyString(idempotencyKey) && this.#intentKeys.has(idempotencyKey)) {
      return this.#intentKeys.get(idempotencyKey);
    }
    if (!isNonEmptyString(senderUserId)) throw new TypeError('senderUserId 必须是非空字符串');
    if (!isNonEmptyString(receiverUserId)) throw new TypeError('receiverUserId 必须是非空字符串');
    if (senderAgentId !== null && typeof senderAgentId !== 'string') {
      throw new TypeError('senderAgentId 必须是字符串或 null');
    }
    if (typeof topic !== 'string') throw new TypeError('topic 必须是字符串');
    if (typeof summary !== 'string') throw new TypeError('summary 必须是字符串');
    if (!isNonEmptyString(idempotencyKey)) {
      throw new TypeError('idempotencyKey 必须是非空字符串（发送意图恰好一次的幂等键）');
    }
    if (!BILLING_MODES.has(billingMode)) {
      throw new TypeError('billingMode 必须是 BILLING.SEND_PAY(1) 或 BILLING.VIEW_REPLY_PAY(2)');
    }
    // 金额校验：非法金额（0 / 负数 / 小数 / 字符串）一律 INVALID_AMOUNT（与 §0 / §8 同一语义）
    assertPositiveCents(amountCents);
    if (amountCents > this.approveCapCents) {
      throw new PaidMessageError(
        `amountCents=${amountCents} 超过单笔审批上限 approveCapCents=${this.approveCapCents}，需人工审批，本服务不自动放行`,
        'NOT_PAYABLE'
      );
    }
    if (senderUserId === receiverUserId) {
      throw new PaidMessageError('不能给自己发送陌生人付费消息（senderUserId === receiverUserId）', 'SELF_MESSAGE');
    }
    this.#assertDailyCap(senderUserId);

    const at = toIso(this.#clock);
    const message = Object.freeze({
      id: `msg-${randomUUID()}`,
      senderUserId,
      senderAgentId,
      receiverUserId,
      topic,
      summary,
      billingMode,
      amountCents,
      state: MSG_STATE.INTENT,
      idempotencyKey,
      screening: null,
      payerUserId: null,
      chargeEntryId: null,
      settleEntryId: null,
      refundEntryId: null,
      replyText: null,
      viewerUserId: null,
      refuseReason: null,
      createdAt: at,
      updatedAt: at,
      paidAt: null,
      viewedAt: null,
      repliedAt: null,
      settledAt: null,
      refundedAt: null,
      refusedAt: null,
      expiredAt: null,
      history: Object.freeze([
        Object.freeze({ at, action: 'SUBMIT_INTENT', from: null, to: MSG_STATE.INTENT }),
      ]),
    });
    this.#messages.set(message.id, message);
    this.#intentKeys.set(idempotencyKey, message);
    return message;
  }

  /**
   * 目标用户（其 Agent）预审结果落库。**先预审后付费**的唯一入口：
   * - INTENT + PASS  -> AWAITING_PAYMENT
   * - INTENT + BLOCK -> REFUSED（终态；之后再 pay 抛 NOT_PAYABLE）
   * - AWAITING_PAYMENT + BLOCK -> REFUSED（异步驳回/撤销放行）
   * - AWAITING_PAYMENT + PASS  -> 幂等空操作（状态不变）
   */
  screen({ messageId, verdict } = {}) {
    const message = this.#require(messageId);
    if (!VERDICTS.has(verdict)) {
      throw new TypeError("verdict 必须是 'PASS' 或 'BLOCK'");
    }
    const at = toIso(this.#clock);
    const screening = Object.freeze({ verdict, at });
    if (message.state === MSG_STATE.INTENT) {
      if (verdict === 'BLOCK') {
        return this.#transition(message, MSG_STATE.REFUSED, 'SCREEN_BLOCK', {
          screening,
          refusedAt: at,
        });
      }
      return this.#transition(message, MSG_STATE.AWAITING_PAYMENT, 'SCREEN_PASS', { screening });
    }
    if (message.state === MSG_STATE.AWAITING_PAYMENT) {
      if (verdict === 'BLOCK') {
        return this.#transition(message, MSG_STATE.REFUSED, 'SCREEN_BLOCK', {
          screening,
          refusedAt: at,
        });
      }
      return message; // 重复 PASS 幂等
    }
    throw new PaidMessageError(
      `消息状态 ${message.state} 不允许 screen（仅 INTENT | AWAITING_PAYMENT）`,
      'ILLEGAL_STATE'
    );
  }

  /**
   * 支付。付费方恒为发送方（发送方付费买触达），payerUserId !== senderUserId 直接 PAYER_MISMATCH。
   * 幂等双保险：① 消息状态已 PAID 家族 -> charged:false；② 账本幂等键 pay:<messageId> 兜底
   * （覆盖「状态已更新但账本条目已存在 / 恰好一次重放」的窗口）。
   */
  pay({ messageId, payerUserId } = {}) {
    const message = this.#require(messageId);
    if (!isNonEmptyString(payerUserId)) {
      throw new TypeError('payerUserId 必须是非空字符串');
    }
    if (payerUserId !== message.senderUserId) {
      throw new PaidMessageError(
        `payerUserId=${payerUserId} 必须是消息发送方 senderUserId=${message.senderUserId}（付费消息由发送方付费）`,
        'PAYER_MISMATCH'
      );
    }
    switch (message.state) {
      case MSG_STATE.INTENT:
        // 铁律 1：先 Agent 预审，后付费。未 screen 一律不得扣费。
        throw new PaidMessageError('消息尚未通过目标用户 Agent 预审（screen），不得付费', 'NEEDS_SCREENING');
      case MSG_STATE.REFUSED:
        // 铁律 1：BLOCK / 拒绝之后永不可复活为可付，且绝不静默返回。
        throw new PaidMessageError('消息已 REFUSED（预审 BLOCK 或目标用户拒绝），不可支付', 'NOT_PAYABLE');
      case MSG_STATE.PAID:
      case MSG_STATE.VIEWED:
      case MSG_STATE.REPLIED:
        return Object.freeze({
          message,
          charged: false,
          entry: this.ledger.findByIdempotencyKey(PAY_KEY(message.id)) ?? null,
        });
      case MSG_STATE.AWAITING_PAYMENT:
        break;
      case MSG_STATE.SETTLED:
      case MSG_STATE.REFUNDED:
      case MSG_STATE.EXPIRED:
        throw new PaidMessageError(`消息状态 ${message.state} 为终态，不可再支付`, 'ILLEGAL_STATE');
      default:
        throw new PaidMessageError(`未知状态 ${message.state}，不可支付`, 'ILLEGAL_STATE');
    }

    const { entry, duplicate } = this.ledger.post({
      idempotencyKey: PAY_KEY(message.id),
      type: ENTRY_TYPE.CHARGE,
      userId: message.senderUserId,
      amountCents: message.amountCents,
      ref: message.id,
      meta: {
        messageId: message.id,
        billingMode: message.billingMode,
        receiverUserId: message.receiverUserId,
      },
    });
    const paid = this.#transition(message, MSG_STATE.PAID, 'PAY', {
      paidAt: toIso(this.#clock),
      payerUserId,
      chargeEntryId: entry.id,
    });
    return Object.freeze({ message: paid, charged: !duplicate, entry });
  }

  /**
   * 目标用户（或其代理）主动拒绝：INTENT | AWAITING_PAYMENT -> REFUSED。
   * 不产生任何账本条目（未扣费，也不会有 REFUND）。
   */
  refuse({ messageId, reason = null } = {}) {
    const message = this.#require(messageId);
    if (reason !== null && typeof reason !== 'string') {
      throw new TypeError('reason 必须是字符串或 null');
    }
    if (message.state !== MSG_STATE.INTENT && message.state !== MSG_STATE.AWAITING_PAYMENT) {
      throw new PaidMessageError(
        `消息状态 ${message.state} 不允许 refuse（仅 INTENT | AWAITING_PAYMENT）`,
        'ILLEGAL_STATE'
      );
    }
    const at = toIso(this.#clock);
    return this.#transition(message, MSG_STATE.REFUSED, 'REFUSE', {
      refusedAt: at,
      refuseReason: reason,
    });
  }

  /** 接收方查看已付费消息：仅 PAID -> VIEWED。 */
  view({ messageId, viewerUserId = null } = {}) {
    const message = this.#require(messageId);
    if (viewerUserId !== null && typeof viewerUserId !== 'string') {
      throw new TypeError('viewerUserId 必须是字符串或 null');
    }
    if (message.state !== MSG_STATE.PAID) {
      throw new PaidMessageError(`消息状态 ${message.state} 不允许 view（仅 PAID）`, 'ILLEGAL_STATE');
    }
    return this.#transition(message, MSG_STATE.VIEWED, 'VIEW', {
      viewedAt: toIso(this.#clock),
      viewerUserId,
    });
  }

  /** 回复已查看的消息：仅 VIEWED -> REPLIED。 */
  reply({ messageId, replyText = null } = {}) {
    const message = this.#require(messageId);
    if (replyText !== null && typeof replyText !== 'string') {
      throw new TypeError('replyText 必须是字符串或 null');
    }
    if (message.state !== MSG_STATE.VIEWED) {
      throw new PaidMessageError(`消息状态 ${message.state} 不允许 reply（仅 VIEWED）`, 'ILLEGAL_STATE');
    }
    return this.#transition(message, MSG_STATE.REPLIED, 'REPLY', {
      repliedAt: toIso(this.#clock),
      replyText,
    });
  }

  /**
   * 结算：仅 REPLIED -> SETTLED。账本记一条 SETTLE
   * （userId = 接收方，amountCents = 消息金额，ref = messageId）；SETTLE 不改变 balanceOf 口径。
   */
  settle({ messageId } = {}) {
    const message = this.#require(messageId);
    if (message.state !== MSG_STATE.REPLIED) {
      throw new PaidMessageError(`消息状态 ${message.state} 不允许 settle（仅 REPLIED）`, 'ILLEGAL_STATE');
    }
    const { entry } = this.ledger.post({
      idempotencyKey: SETTLE_KEY(message.id),
      type: ENTRY_TYPE.SETTLE,
      userId: message.receiverUserId,
      amountCents: message.amountCents,
      ref: message.id,
      meta: {
        messageId: message.id,
        senderUserId: message.senderUserId,
        chargeEntryId: message.chargeEntryId,
      },
    });
    return this.#transition(message, MSG_STATE.SETTLED, 'SETTLE', {
      settledAt: toIso(this.#clock),
      settleEntryId: entry.id,
    });
  }

  /**
   * 退款：仅 PAID | VIEWED -> REFUNDED。REPLIED 之后不可退（争议走人工），
   * 终态（REFUSED / REFUNDED / EXPIRED / SETTLED）不可再流转。
   */
  refund({ messageId, reason = null } = {}) {
    const message = this.#require(messageId);
    if (reason !== null && typeof reason !== 'string') {
      throw new TypeError('reason 必须是字符串或 null');
    }
    if (message.state === MSG_STATE.REPLIED) {
      throw new PaidMessageError('消息已 REPLIED，不可退款（争议走人工审批）', 'ILLEGAL_STATE');
    }
    if (TERMINAL_STATES.has(message.state)) {
      throw new PaidMessageError(`消息状态 ${message.state} 为终态，不可退款`, 'ILLEGAL_STATE');
    }
    if (message.state !== MSG_STATE.PAID && message.state !== MSG_STATE.VIEWED) {
      // INTENT / AWAITING_PAYMENT：尚未扣费，没有可退的钱
      throw new PaidMessageError(`消息状态 ${message.state} 尚未扣费，不可退款`, 'ILLEGAL_STATE');
    }
    const { entry } = this.ledger.post({
      idempotencyKey: REFUND_KEY(message.id),
      type: ENTRY_TYPE.REFUND,
      userId: message.payerUserId ?? message.senderUserId,
      amountCents: message.amountCents,
      ref: message.id,
      meta: { messageId: message.id, reason, chargeEntryId: message.chargeEntryId },
    });
    return this.#transition(message, MSG_STATE.REFUNDED, 'REFUND', {
      refundedAt: toIso(this.#clock),
      refundEntryId: entry.id,
      refundReason: reason,
    });
  }

  /**
   * 过期：仅 AWAITING_PAYMENT -> EXPIRED（超时未付，由调度方调用）。
   * 契约 §9 未列出独立方法名，此为实现层补充入口（不改动契约签名/状态机）。
   */
  expire({ messageId } = {}) {
    const message = this.#require(messageId);
    if (message.state !== MSG_STATE.AWAITING_PAYMENT) {
      throw new PaidMessageError(`消息状态 ${message.state} 不允许 expire（仅 AWAITING_PAYMENT）`, 'ILLEGAL_STATE');
    }
    return this.#transition(message, MSG_STATE.EXPIRED, 'EXPIRE', { expiredAt: toIso(this.#clock) });
  }

  /** 读取消息；不存在抛 NOT_FOUND。 */
  get(messageId) {
    return this.#require(messageId);
  }

  /** 列表（新数组；元素为冻结消息）。receiverUserId / state 来自契约，senderUserId 为可选补充过滤。 */
  list({ receiverUserId = null, state = null, senderUserId = null } = {}) {
    const result = [];
    for (const message of this.#messages.values()) {
      if (receiverUserId !== null && message.receiverUserId !== receiverUserId) continue;
      if (senderUserId !== null && message.senderUserId !== senderUserId) continue;
      if (state !== null && message.state !== state) continue;
      result.push(message);
    }
    return result;
  }

  #require(messageId) {
    const message = this.#messages.get(messageId);
    if (!message) {
      throw new PaidMessageError(`消息不存在: ${String(messageId)}`, 'NOT_FOUND');
    }
    return message;
  }

  #nowIso() {
    return toIso(this.#clock);
  }

  /**
   * 每日发送上限：按 UTC 自然日统计该发送方当日创建、且未被预审 BLOCK 的消息数。
   * 被 Agent 拦截（REFUSED）的消息不消耗用户额度，避免恶意接收方刷掉发送方配额。
   */
  #assertDailyCap(senderUserId) {
    const today = this.#nowIso().slice(0, 10);
    let used = 0;
    for (const message of this.#messages.values()) {
      if (message.senderUserId !== senderUserId) continue;
      if (message.createdAt.slice(0, 10) !== today) continue;
      if (message.state === MSG_STATE.REFUSED) continue;
      used += 1;
    }
    if (used >= this.dailySendCap) {
      throw new PaidMessageError(
        `已达到每日发送上限 dailySendCap=${this.dailySendCap}（当日已用 ${used} 条）`,
        'DAILY_CAP_EXCEEDED'
      );
    }
  }

  /** 状态迁移唯一入口：返回**新**冻结对象，历史只追加，绝不原地修改。 */
  #transition(message, to, action, patch = {}) {
    const at = this.#nowIso();
    const previous = message.state;
    const next = Object.freeze({
      ...message,
      ...patch,
      state: to,
      updatedAt: at,
      history: Object.freeze([
        ...message.history,
        Object.freeze({ at, action, from: previous, to }),
      ]),
    });
    this.#messages.set(next.id, next);
    return next;
  }
}
