// 模块职责：退款争议与时间线举证——§37 钱包只自动退"未回复"；已回复/已结算的争议必须走人工，且退款只能由"已批准争议"触发，经账本 `refund-dispute:<disputeId>` 幂等记账（唯一金额真相仍是 §8，禁止绕过窗口直接改余额）/ 契约来源：contracts/INTERFACES.md §39（复用 §8 ledger.post/entries）/ 关键不变量：①仅 REPLIED/SETTLED 可开争议（否则 NOT_ELIGIBLE，引导走 §37 自助退款）；②同一消息只能有一个未决争议（DUPLICATE_OPEN）；③APPROVE 金额取 `pay:<messageId>` 扣费额，账本记 `refund-dispute:<disputeId>`；④REJECT 不动余额；⑤每个状态迁移写审计，timeline 可还原完整时间线。
import { randomUUID } from 'node:crypto';
import { ENTRY_TYPE } from './ledger.mjs';

export const DISPUTE_STATE = Object.freeze({
  OPEN: 'OPEN',
  APPROVED: 'APPROVED',
  REJECTED: 'REJECTED',
});

export const DISPUTE_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  ILLEGAL_STATE: 'ILLEGAL_STATE',
  NOT_ELIGIBLE: 'NOT_ELIGIBLE',
  FORBIDDEN: 'FORBIDDEN',
  DUPLICATE_OPEN: 'DUPLICATE_OPEN',
});

const CODE = DISPUTE_CODE;
const STATE_VALUES = new Set(Object.values(DISPUTE_STATE));
/** §9 消息状态中允许开争议的"已回复/已结算"态（其余引导走 §37 自助退款） */
const ELIGIBLE_MESSAGE_STATES = new Set(['REPLIED', 'SETTLED']);
const DECISIONS = new Set(['APPROVE', 'REJECT']);
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isPlainObject = (value) => typeof value === 'object' && value !== null && !Array.isArray(value);

/** 证据原样保留（不合并/不去重，只做数组+一层对象冻结拷贝） */
function freezeEvidence(evidence) {
  return Object.freeze(
    evidence.map((item) => (isPlainObject(item) ? Object.freeze({ ...item }) : item))
  );
}

export class DisputeError extends Error {
  constructor(message, code = CODE.ILLEGAL_STATE) {
    super(message);
    this.name = 'DisputeError';
    this.code = code;
  }
}

export class DisputeService {
  #clock;
  #ledger;
  #messageState;
  #onAudit;
  /** @type {Map<string, object>} disputeId -> 当前冻结争议记录 */
  #disputes = new Map();
  /** @type {string[]} 创建顺序 */
  #order = [];

  constructor({ clock = () => new Date(), ledger, messageState = () => null, onAudit = () => {} } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (
      !ledger ||
      typeof ledger !== 'object' ||
      typeof ledger.post !== 'function' ||
      typeof ledger.entries !== 'function' ||
      typeof ledger.findByIdempotencyKey !== 'function'
    ) {
      throw new TypeError('ledger 必填（§8 Ledger 实例：唯一金额真相）');
    }
    if (typeof messageState !== 'function') throw new TypeError('messageState 必须是函数（messageId -> §9 状态）');
    if (typeof onAudit !== 'function') throw new TypeError('onAudit 必须是函数');
    this.#clock = clock;
    this.#ledger = ledger;
    this.#messageState = messageState;
    this.#onAudit = onAudit;
  }

  /**
   * 开争议：仅当消息已 REPLIED/SETTLED（未回复请走 §37 自助退款 → NOT_ELIGIBLE）。
   * 同一消息只允许一个未决争议（DUPLICATE_OPEN）。
   * @returns 冻结 dispute
   */
  open({ messageId, userId, reason, evidence = [] } = {}) {
    if (!isNonEmptyString(messageId)) throw new TypeError('messageId 必须是非空字符串');
    if (!isNonEmptyString(userId)) throw new TypeError('userId 必须是非空字符串');
    if (!isNonEmptyString(reason)) throw new TypeError('reason 必填且必须是非空字符串（争议必须留理由）');
    if (!Array.isArray(evidence)) throw new TypeError('evidence 必须是数组');

    const state = this.#messageState(messageId);
    if (!ELIGIBLE_MESSAGE_STATES.has(state)) {
      throw new DisputeError(
        `消息 ${messageId} 当前状态 ${String(state)} 不满足争议条件：未回复请走 §37 自助退款，已回复/已结算才走人工争议`,
        CODE.NOT_ELIGIBLE
      );
    }
    const sameMessage = [...this.#disputes.values()].filter((dispute) => dispute.messageId === messageId);
    if (sameMessage.some((dispute) => dispute.state === DISPUTE_STATE.OPEN)) {
      throw new DisputeError(`消息 ${messageId} 已有未决争议`, CODE.DUPLICATE_OPEN);
    }
    if (sameMessage.some((dispute) => dispute.state === DISPUTE_STATE.APPROVED)) {
      throw new DisputeError(`消息 ${messageId} 的争议已批准并退款，不可重复开`, CODE.NOT_ELIGIBLE);
    }
    const chargeEntry = this.#ledger.findByIdempotencyKey(`pay:${messageId}`);
    if (!chargeEntry || chargeEntry.type !== ENTRY_TYPE.CHARGE) {
      throw new DisputeError(`消息 ${messageId} 没有扣费流水（pay:<messageId>），无可退金额`, CODE.NOT_ELIGIBLE);
    }

    const at = this.#nowIso();
    const record = Object.freeze({
      id: `disp-${randomUUID()}`,
      messageId,
      userId,
      state: DISPUTE_STATE.OPEN,
      reason,
      evidence: freezeEvidence(evidence),
      createdAt: at,
      resolvedAt: null,
      decision: null,
      actorUserId: null,
      note: null,
      refundEntryId: null,
      refundAmountCents: null,
    });
    this.#disputes.set(record.id, record);
    this.#order.push(record.id);
    this.#audit({
      action: 'DISPUTE_OPEN',
      actor: userId,
      target: record.id,
      decision: DISPUTE_STATE.OPEN,
      detail: { messageId, evidenceCount: evidence.length },
    });
    return this.#view(record);
  }

  /**
   * 人工处理争议。APPROVE → 账本记 `refund-dispute:<disputeId>` REFUND（金额取 pay:<messageId> 扣费额），
   * 已存在同一键的退款（崩溃恢复）则复用不重复记账；REJECT → 不动账本。
   * 重复 resolve（已非 OPEN）→ ILLEGAL_STATE；非属主 → FORBIDDEN；actorUserId 缺失 → TypeError。
   * @returns 冻结 dispute
   */
  resolve({ disputeId, actorUserId, decision, note = null } = {}) {
    const record = this.#require(disputeId);
    if (!isNonEmptyString(actorUserId)) {
      throw new TypeError('actorUserId 必填（人工处理必须留痕；缺失按调用方编程错误处理）');
    }
    if (!DECISIONS.has(decision)) {
      throw new TypeError("decision 必须是 'APPROVE' 或 'REJECT'");
    }
    if (note !== null && typeof note !== 'string') throw new TypeError('note 必须是字符串或 null');
    if (record.state !== DISPUTE_STATE.OPEN) {
      throw new DisputeError(`争议已处理（${record.state}），拒绝重复 resolve`, CODE.ILLEGAL_STATE);
    }
    // v0.0.2 没有管理员注册表：只有争议属主可确认结果（接审批引擎时替换为审批人身份校验）
    if (actorUserId !== record.userId) {
      throw new DisputeError(`非争议属主不可处理：${actorUserId}`, CODE.FORBIDDEN);
    }

    const at = this.#nowIso();
    if (decision === 'REJECT') {
      const next = this.#transition(record, {
        state: DISPUTE_STATE.REJECTED,
        resolvedAt: at,
        actorUserId,
        decision,
        note,
      });
      this.#audit({
        action: 'DISPUTE_REJECT',
        actor: actorUserId,
        target: record.id,
        decision: DISPUTE_STATE.REJECTED,
        detail: { messageId: record.messageId, note },
      });
      return this.#view(next);
    }

    // APPROVE：金额取该消息的原始扣费流水
    const chargeEntry = this.#ledger.findByIdempotencyKey(`pay:${record.messageId}`);
    if (!chargeEntry || chargeEntry.type !== ENTRY_TYPE.CHARGE) {
      throw new DisputeError(`找不到消息 ${record.messageId} 的扣费流水，无法确定退款金额`, CODE.NOT_FOUND);
    }
    const refundKey = `refund-dispute:${record.id}`;
    let refundEntry = this.#ledger.findByIdempotencyKey(refundKey);
    if (!refundEntry) {
      // 防重复退款：该消息已有其它 REFUND 流水（§37 自助退 / 其它争议）→ 拒绝，绝不二次退
      const otherRefund = this.#ledger
        .entries()
        .find((entry) => entry.type === ENTRY_TYPE.REFUND && entry.ref === record.messageId);
      if (otherRefund) {
        throw new DisputeError(
          `消息 ${record.messageId} 已存在退款流水（${otherRefund.idempotencyKey}），禁止重复退款`,
          CODE.ILLEGAL_STATE
        );
      }
      refundEntry = this.#ledger.post({
        idempotencyKey: refundKey,
        type: ENTRY_TYPE.REFUND,
        userId: record.userId,
        amountCents: chargeEntry.amountCents,
        ref: record.messageId,
        meta: { disputeId: record.id, messageId: record.messageId, note, source: 'dispute' },
      }).entry;
    }
    const next = this.#transition(record, {
      state: DISPUTE_STATE.APPROVED,
      resolvedAt: at,
      actorUserId,
      decision,
      note,
      refundEntryId: refundEntry.id,
      refundAmountCents: refundEntry.amountCents,
    });
    this.#audit({
      action: 'DISPUTE_APPROVE',
      actor: actorUserId,
      target: record.id,
      decision: DISPUTE_STATE.APPROVED,
      detail: { messageId: record.messageId, amountCents: refundEntry.amountCents, refundEntryId: refundEntry.id },
    });
    return this.#view(next);
  }

  /**
   * 时间线：按时间合并该消息的账本条目（CHARGE/REFUND/SETTLE/DEPOSIT，ref===messageId）
   * 与争议事件，返回 [{ at, type, ref, amountCents? }]（时间升序，同刻保持记账/创建顺序）。
   */
  timeline({ messageId } = {}) {
    if (!isNonEmptyString(messageId)) throw new TypeError('messageId 必须是非空字符串');
    const events = [];
    const ledgerEntries = this.#ledger.entries();
    ledgerEntries.forEach((entry, index) => {
      if (entry.ref !== messageId) return;
      events.push({ at: entry.at, type: entry.type, ref: entry.ref, amountCents: entry.amountCents, tie: index });
    });
    let disputeIndex = 0;
    for (const disputeId of this.#order) {
      const dispute = this.#disputes.get(disputeId);
      if (dispute.messageId !== messageId) continue;
      const baseTie = ledgerEntries.length + disputeIndex;
      disputeIndex += 1;
      events.push({ at: dispute.createdAt, type: 'DISPUTE_OPEN', ref: dispute.id, tie: baseTie });
      if (dispute.resolvedAt !== null) {
        events.push({
          at: dispute.resolvedAt,
          type: dispute.state === DISPUTE_STATE.APPROVED ? 'DISPUTE_APPROVED' : 'DISPUTE_REJECTED',
          ref: dispute.id,
          tie: baseTie + 0.5,
        });
      }
    }
    events.sort((a, b) => (a.at === b.at ? a.tie - b.tie : a.at < b.at ? -1 : 1));
    return Object.freeze(
      events.map(({ at, type, ref, amountCents }) =>
        amountCents === undefined
          ? Object.freeze({ at, type, ref })
          : Object.freeze({ at, type, ref, amountCents })
      )
    );
  }

  get(disputeId) {
    return this.#view(this.#require(disputeId));
  }

  /** 列出争议（按 state 过滤），按创建顺序；返回冻结数组。 */
  list({ state = null } = {}) {
    if (state !== null && !STATE_VALUES.has(state)) throw new TypeError(`未知争议状态: ${String(state)}`);
    const result = [];
    for (const disputeId of this.#order) {
      const dispute = this.#disputes.get(disputeId);
      if (state !== null && dispute.state !== state) continue;
      result.push(this.#view(dispute));
    }
    return Object.freeze(result);
  }

  stats() {
    const counters = { total: 0, open: 0, approved: 0, rejected: 0, refundedCents: 0 };
    for (const dispute of this.#disputes.values()) {
      counters.total += 1;
      if (dispute.state === DISPUTE_STATE.OPEN) counters.open += 1;
      else if (dispute.state === DISPUTE_STATE.APPROVED) {
        counters.approved += 1;
        counters.refundedCents += dispute.refundAmountCents ?? 0;
      } else if (dispute.state === DISPUTE_STATE.REJECTED) counters.rejected += 1;
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

  #require(disputeId) {
    if (!isNonEmptyString(disputeId)) throw new TypeError('disputeId 必须是非空字符串');
    const record = this.#disputes.get(disputeId);
    if (!record) throw new DisputeError(`争议不存在: ${disputeId}`, CODE.NOT_FOUND);
    return record;
  }

  #transition(record, patch) {
    const next = Object.freeze({ ...record, ...patch });
    this.#disputes.set(next.id, next);
    return next;
  }

  #view(record) {
    return Object.freeze({
      id: record.id,
      messageId: record.messageId,
      userId: record.userId,
      state: record.state,
      reason: record.reason,
      evidence: record.evidence,
      createdAt: record.createdAt,
      resolvedAt: record.resolvedAt,
      decision: record.decision,
      actorUserId: record.actorUserId,
      note: record.note,
      refundEntryId: record.refundEntryId,
      refundAmountCents: record.refundAmountCents,
    });
  }

  /** 审计事件形状与项目其它模块一致：{ at, actor, action, target, decision, detail } */
  #audit({ action, actor, target, decision, detail }) {
    const event = Object.freeze({
      at: this.#nowIso(),
      actor: actor ?? null,
      action,
      target: target ?? null,
      decision: decision ?? null,
      detail: Object.freeze({ ...detail }),
    });
    this.#onAudit(event);
    return event;
  }
}
