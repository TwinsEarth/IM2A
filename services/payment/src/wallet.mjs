// 模块职责：钱包（余额/充值/扣费/退款）——账本只记不可变流水，钱包回答"这个人现在能花多少、能不能退"；禁止"先扣后退"白嫖 / 契约来源：contracts/INTERFACES.md §37（复用 §8 ledger：每笔变动写 DEPOSIT/CHARGE/REFUND）/ 关键不变量：①余额真相在账本，钱包只做投影（available = DEPOSIT - CHARGE + REFUND）；②充值/扣费幂等（同键重放返回首次结果、只入账一次）；③余额不允许为负（INSUFFICIENT_FUNDS）；④退款仅限"对方尚未回复"（按注入 messageState 判定，已回复 → REFUND_WINDOW_CLOSED）；⑤日发送上限按自然日累计（DAILY_CAP_EXCEEDED）；⑥金额一律正整数分。
import { ENTRY_TYPE } from './ledger.mjs';

export const WALLET_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  INVALID_AMOUNT: 'INVALID_AMOUNT',
  INSUFFICIENT_FUNDS: 'INSUFFICIENT_FUNDS',
  REFUND_WINDOW_CLOSED: 'REFUND_WINDOW_CLOSED',
  DAILY_CAP_EXCEEDED: 'DAILY_CAP_EXCEEDED',
  SUSPENDED: 'SUSPENDED',
});

const CODE = WALLET_CODE;
/** 未回复态：只有这三种消息状态允许退款（§9 状态机：REPLIED/SETTLED 之后争议走人工） */
const REFUNDABLE_MESSAGE_STATES = new Set(['AWAITING_PAYMENT', 'PAID', 'VIEWED']);

const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

function assertAmountCents(amountCents) {
  if (typeof amountCents !== 'number' || !Number.isSafeInteger(amountCents) || amountCents <= 0) {
    throw new WalletError(
      `amountCents 必须是正整数分（cents），收到 ${String(amountCents)}`,
      CODE.INVALID_AMOUNT
    );
  }
}

export class WalletError extends Error {
  constructor(message, code = CODE.INVALID_AMOUNT) {
    super(message);
    this.name = 'WalletError';
    this.code = code;
  }
}

export class Wallet {
  #clock;
  #ledger;
  #dailySendCapCents;
  #messageState;
  #accountState;
  /** @type {Map<string, object>} 仅 ledger === null（演示/单测）时的内存投影 */
  #memory = new Map();
  /** @type {Map<string, number>} 内存模式充值幂等键 -> 入账金额 */
  #depositKeys = new Map();

  constructor({
    clock = () => new Date(),
    ledger = null,
    dailySendCapCents = 50000,
    messageState = () => null,
    accountState = () => 'ACTIVE',
  } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (ledger !== null) {
      if (
        typeof ledger !== 'object' ||
        typeof ledger.post !== 'function' ||
        typeof ledger.entries !== 'function' ||
        typeof ledger.findByIdempotencyKey !== 'function'
      ) {
        throw new TypeError('ledger 必须是 §8 Ledger 实例或 null');
      }
    }
    if (!Number.isInteger(dailySendCapCents) || dailySendCapCents < 0) {
      throw new RangeError('dailySendCapCents 必须是 >= 0 的整数（分）');
    }
    if (typeof messageState !== 'function') throw new TypeError('messageState 必须是函数（messageId -> 状态）');
    if (typeof accountState !== 'function') throw new TypeError('accountState 必须是函数（userId -> 状态）');
    this.#clock = clock;
    this.#ledger = ledger;
    this.#dailySendCapCents = dailySendCapCents;
    this.#messageState = messageState;
    this.#accountState = accountState;
  }

  /**
   * 充值（幂等键）。同键重放只入账一次并返回 duplicate:true。
   * @returns 冻结 { credited, duplicate, amountCents, availableCents, channel }
   */
  topUp({ userId, amountCents, idempotencyKey, channel = 'manual' } = {}) {
    this.#assertUserId(userId);
    if (!isNonEmptyString(idempotencyKey)) throw new TypeError('idempotencyKey 必须是非空字符串');
    if (typeof channel !== 'string') throw new TypeError('channel 必须是字符串');
    this.#assertActive(userId);
    assertAmountCents(amountCents);

    if (this.#ledger === null) {
      const state = this.#memoryOf(userId);
      if (this.#depositKeys.has(idempotencyKey)) {
        return this.#topUpResult(userId, this.#depositKeys.get(idempotencyKey), true, channel);
      }
      state.depositedCents += amountCents;
      this.#depositKeys.set(idempotencyKey, amountCents);
      return this.#topUpResult(userId, amountCents, false, channel);
    }

    const existing = this.#ledger.findByIdempotencyKey(idempotencyKey);
    if (existing) return this.#topUpResult(userId, existing.amountCents, true, channel);
    this.#ledger.post({
      idempotencyKey,
      type: ENTRY_TYPE.DEPOSIT,
      userId,
      amountCents,
      ref: channel,
      meta: { channel, source: 'wallet' },
    });
    return this.#topUpResult(userId, amountCents, false, channel);
  }

  /** 余额投影：frozenCents 为预留字段（当前恒 0，字段必须存在以便前端/对账消费）。 */
  balance({ userId } = {}) {
    this.#assertUserId(userId);
    return Object.freeze({ availableCents: this.#availableCents(userId), frozenCents: 0 });
  }

  /**
   * 为一条付费消息扣费（幂等键 pay:<messageId>）。
   * 顺序：幂等 → 日上限 → 余额（不允许负余额）→ 写账本 CHARGE。
   * @returns 冻结 { charged, duplicate, amountCents, availableCents }
   */
  chargeForMessage({ userId, messageId, amountCents } = {}) {
    this.#assertUserId(userId);
    this.#assertMessageId(messageId);
    this.#assertActive(userId);
    assertAmountCents(amountCents);
    const idempotencyKey = `pay:${messageId}`;

    if (this.#ledger === null) {
      const state = this.#memoryOf(userId);
      if (state.charges.has(messageId)) {
        return this.#chargeResult(userId, state.charges.get(messageId).amountCents, true);
      }
      const today = this.#today();
      const daily = state.daily.get(today) ?? 0;
      if (daily + amountCents > this.#dailySendCapCents) {
        throw new WalletError(
          `本日发送金额已达上限 dailySendCapCents=${this.#dailySendCapCents}（已用 ${daily}）`,
          CODE.DAILY_CAP_EXCEEDED
        );
      }
      if (this.#availableCents(userId) < amountCents) {
        throw new WalletError(
          `余额不足：available=${this.#availableCents(userId)} < amount=${amountCents}`,
          CODE.INSUFFICIENT_FUNDS
        );
      }
      state.chargedCents += amountCents;
      state.daily.set(today, daily + amountCents);
      state.charges.set(messageId, { amountCents, at: this.#nowIso() });
      return this.#chargeResult(userId, amountCents, false);
    }

    // 幂等：同 messageId 只扣一次（账本 pay:<messageId> 是唯一幂等键，跨模块共享账本时同样生效）
    const existing = this.#ledger.findByIdempotencyKey(idempotencyKey);
    if (existing) {
      if (existing.userId !== userId) {
        throw new WalletError(`消息 ${messageId} 的扣费属于其它用户，拒绝重复扣费`, CODE.NOT_FOUND);
      }
      return this.#chargeResult(userId, existing.amountCents, true);
    }
    // 日上限（按自然日累计；退款不回滚当日累计，避免"退旧扣新"绕开额度）
    const daily = this.#dailyChargedCents(userId);
    if (daily + amountCents > this.#dailySendCapCents) {
      throw new WalletError(
        `本日发送金额已达上限 dailySendCapCents=${this.#dailySendCapCents}（已用 ${daily}）`,
        CODE.DAILY_CAP_EXCEEDED
      );
    }
    // 余额不足 → 绝不产生负余额（不写账本）
    const available = this.#availableCents(userId);
    if (available < amountCents) {
      throw new WalletError(
        `余额不足：available=${available} < amount=${amountCents}`,
        CODE.INSUFFICIENT_FUNDS
      );
    }
    this.#ledger.post({
      idempotencyKey,
      type: ENTRY_TYPE.CHARGE,
      userId,
      amountCents,
      ref: messageId,
      meta: { messageId, source: 'wallet' },
    });
    return this.#chargeResult(userId, amountCents, false);
  }

  /**
   * 退款（幂等键 refund:<messageId>）。**仅当对方尚未回复**（messageState ∈ AWAITING_PAYMENT|PAID|VIEWED）
   * 才可退；REPLIED/SETTLED 等 → REFUND_WINDOW_CLOSED（防"先扣后退"白嫖）。
   * 重复退款幂等：已退过直接返回 duplicate，不重复回补。
   * @returns 冻结 { refunded, duplicate, amountCents, availableCents, reason }
   */
  refund({ userId, messageId, reason } = {}) {
    this.#assertUserId(userId);
    this.#assertMessageId(messageId);
    if (!isNonEmptyString(reason)) throw new TypeError('reason 必填且必须是非空字符串（退款必须留原因）');
    this.#assertActive(userId);
    const idempotencyKey = `refund:${messageId}`;

    if (this.#ledger === null) {
      const state = this.#memoryOf(userId);
      if (state.refunds.has(messageId)) {
        return this.#refundResult(userId, state.refunds.get(messageId).amountCents, true, reason);
      }
      const charge = state.charges.get(messageId);
      if (!charge) throw new WalletError(`没有找到该消息的扣费记录: ${messageId}`, CODE.NOT_FOUND);
      this.#assertRefundable(messageId);
      state.refundedCents += charge.amountCents;
      state.refunds.set(messageId, { amountCents: charge.amountCents, at: this.#nowIso() });
      return this.#refundResult(userId, charge.amountCents, false, reason);
    }

    // 幂等：已退过直接返回首次结果（即使消息状态随后变化也不再判窗口）
    const existingRefund = this.#ledger.findByIdempotencyKey(idempotencyKey);
    if (existingRefund) {
      if (existingRefund.userId !== userId) {
        throw new WalletError(`消息 ${messageId} 的退款属于其它用户`, CODE.NOT_FOUND);
      }
      return this.#refundResult(userId, existingRefund.amountCents, true, reason);
    }
    // 找到原始扣费（优先 pay:<messageId>，因此也支持 §9 直接写账本的扣费记录）
    const chargeEntry = this.#ledger.findByIdempotencyKey(`pay:${messageId}`);
    if (!chargeEntry || chargeEntry.type !== ENTRY_TYPE.CHARGE || chargeEntry.userId !== userId) {
      throw new WalletError(`没有找到该消息对应用户的扣费记录: ${messageId}`, CODE.NOT_FOUND);
    }
    this.#assertRefundable(messageId);
    this.#ledger.post({
      idempotencyKey,
      type: ENTRY_TYPE.REFUND,
      userId,
      amountCents: chargeEntry.amountCents,
      ref: messageId,
      meta: { messageId, reason, source: 'wallet' },
    });
    return this.#refundResult(userId, chargeEntry.amountCents, false, reason);
  }

  /** 钱包汇总（只读投影）：余额 + 累计充值/扣费/退款 + 当日已用额度。 */
  summary({ userId } = {}) {
    this.#assertUserId(userId);
    const totals = this.#totals(userId);
    return Object.freeze({
      userId,
      availableCents: totals.depositedCents - totals.chargedCents + totals.refundedCents,
      frozenCents: 0,
      depositedCents: totals.depositedCents,
      chargedCents: totals.chargedCents,
      refundedCents: totals.refundedCents,
      dailyChargedCents: this.#dailyChargedCents(userId),
      dailySendCapCents: this.#dailySendCapCents,
    });
  }

  // ── 内部 ────────────────────────────────────────────────────────────────

  #assertUserId(userId) {
    if (!isNonEmptyString(userId)) throw new TypeError('userId 必须是非空字符串');
  }

  #assertMessageId(messageId) {
    if (!isNonEmptyString(messageId)) throw new TypeError('messageId 必须是非空字符串');
  }

  #assertActive(userId) {
    if (this.#accountState(userId) === 'SUSPENDED') {
      throw new WalletError(`账号已暂停，资金操作被阻断: ${userId}`, CODE.SUSPENDED);
    }
  }

  #assertRefundable(messageId) {
    const state = this.#messageState(messageId);
    if (!REFUNDABLE_MESSAGE_STATES.has(state)) {
      throw new WalletError(
        `消息 ${messageId} 已进入 ${String(state)}（非未回复态），退款窗口已关闭，争议走人工`,
        CODE.REFUND_WINDOW_CLOSED
      );
    }
  }

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

  #today() {
    return this.#now().toISOString().slice(0, 10);
  }

  #memoryOf(userId) {
    let state = this.#memory.get(userId);
    if (!state) {
      state = {
        depositedCents: 0,
        chargedCents: 0,
        refundedCents: 0,
        daily: new Map(),
        charges: new Map(),
        refunds: new Map(),
      };
      this.#memory.set(userId, state);
    }
    return state;
  }

  /** 金额真相：ledger 有则从不可变流水投影；否则用内存投影（仅演示/单测）。 */
  #totals(userId) {
    if (this.#ledger === null) {
      const state = this.#memoryOf(userId);
      return {
        depositedCents: state.depositedCents,
        chargedCents: state.chargedCents,
        refundedCents: state.refundedCents,
      };
    }
    let depositedCents = 0;
    let chargedCents = 0;
    let refundedCents = 0;
    for (const entry of this.#ledger.entries({ userId })) {
      if (entry.type === ENTRY_TYPE.DEPOSIT) depositedCents += entry.amountCents;
      else if (entry.type === ENTRY_TYPE.CHARGE) chargedCents += entry.amountCents;
      else if (entry.type === ENTRY_TYPE.REFUND) refundedCents += entry.amountCents;
      // SETTLE：结算事件，不属于用户钱包余额口径
    }
    return { depositedCents, chargedCents, refundedCents };
  }

  #availableCents(userId) {
    const totals = this.#totals(userId);
    return totals.depositedCents - totals.chargedCents + totals.refundedCents;
  }

  #dailyChargedCents(userId) {
    const today = this.#today();
    if (this.#ledger === null) return this.#memoryOf(userId).daily.get(today) ?? 0;
    let cents = 0;
    for (const entry of this.#ledger.entries({ userId })) {
      if (entry.type !== ENTRY_TYPE.CHARGE) continue;
      if (typeof entry.at === 'string' && entry.at.slice(0, 10) === today) cents += entry.amountCents;
    }
    return cents;
  }

  #topUpResult(userId, amountCents, duplicate, channel) {
    return Object.freeze({
      credited: !duplicate,
      duplicate,
      amountCents,
      availableCents: this.#availableCents(userId),
      channel,
    });
  }

  #chargeResult(userId, amountCents, duplicate) {
    return Object.freeze({
      charged: !duplicate,
      duplicate,
      amountCents,
      availableCents: this.#availableCents(userId),
    });
  }

  #refundResult(userId, amountCents, duplicate, reason) {
    return Object.freeze({
      refunded: !duplicate,
      duplicate,
      amountCents,
      availableCents: this.#availableCents(userId),
      reason,
    });
  }
}
