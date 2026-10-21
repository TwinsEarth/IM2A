// 模块职责：付费消息审核链（机审 → 人审 → 申诉）——扣费/投递前必须先过审核；拿不准转人审，被拒可申诉，结论与规则/模型版本进审计。
// 契约来源：contracts/INTERFACES.md §40。（审核状态机与 §4 审批引擎分属两套，不复用 ticket 状态）
// 关键不变量：①机审不可用/抛错 → HUMAN_PENDING（fail-closed，绝不自动放行）；②只有 APPROVED/APPEAL_GRANTED 允许投递与扣费（canDeliver 是唯一判定）；
//             ③申诉只对发送者本人开放且有窗口；④申诉复核人不得与原审核人相同；⑤每次迁移都写审计（含 ruleVersion/modelVersion）。
import { randomUUID } from 'node:crypto';

export const MODERATION_STATE = Object.freeze({
  RECEIVED: 'RECEIVED',
  MACHINE_PASSED: 'MACHINE_PASSED',
  HUMAN_PENDING: 'HUMAN_PENDING',
  APPROVED: 'APPROVED',
  REJECTED: 'REJECTED',
  APPEALED: 'APPEALED',
  APPEAL_GRANTED: 'APPEAL_GRANTED',
  APPEAL_UPHELD: 'APPEAL_UPHELD'
});

export const VERDICT = Object.freeze({ PASS: 'PASS', FLAG: 'FLAG', BLOCK: 'BLOCK' });

export const MODERATION_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  ILLEGAL_STATE: 'ILLEGAL_STATE',
  ALREADY_DECIDED: 'ALREADY_DECIDED',
  FORBIDDEN: 'FORBIDDEN',
  SELF_REVIEW_FORBIDDEN: 'SELF_REVIEW_FORBIDDEN',
  MACHINE_UNAVAILABLE: 'MACHINE_UNAVAILABLE',
  APPEAL_WINDOW_CLOSED: 'APPEAL_WINDOW_CLOSED',
  CONTENT_REQUIRED: 'CONTENT_REQUIRED'
});

const CODE = MODERATION_CODE;
const CODE_VALUES = new Set(Object.values(CODE));
const STATE_VALUES = new Set(Object.values(MODERATION_STATE));
const VERDICT_VALUES = new Set(Object.values(VERDICT));
const DEFAULT_APPEAL_WINDOW_MS = 7 * 24 * 3600e3;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class ModerationError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'ModerationError';
    if (!CODE_VALUES.has(code)) throw new TypeError(`未知 MODERATION_CODE: ${String(code)}`);
    this.code = code;
  }
}

const freezeHits = (hits) =>
  Object.freeze(
    (Array.isArray(hits) ? hits : []).map((hit) =>
      hit && typeof hit === 'object' ? Object.freeze({ ...hit }) : Object.freeze({ value: hit })
    )
  );

export class ModerationService {
  #clock;
  #machine;
  #onAudit;
  #appealWindowMs;
  /** @type {Map<string, object>} caseId -> frozen case */
  #cases;
  /** @type {ReadonlyArray<object>} 只追加审核审计事件 */
  #events;

  constructor({ clock = () => new Date(), machine = null, onAudit = () => {}, appealWindowMs = DEFAULT_APPEAL_WINDOW_MS } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#machine = typeof machine === 'function' ? machine : null;
    this.#onAudit = typeof onAudit === 'function' ? onAudit : () => {};
    if (!Number.isFinite(appealWindowMs) || appealWindowMs < 0) throw new TypeError('appealWindowMs 必须是非负毫秒数');
    this.#appealWindowMs = Math.trunc(appealWindowMs);
    this.#cases = new Map();
    this.#events = [];
  }

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #require(caseId) {
    const moderationCase = this.#cases.get(caseId);
    if (!moderationCase) throw new ModerationError(`审核单不存在: ${String(caseId)}`, CODE.NOT_FOUND);
    return moderationCase;
  }

  #audit(moderationCase, { from, to, actor, decision }) {
    const event = Object.freeze({
      caseId: moderationCase.caseId,
      messageId: moderationCase.messageId,
      from,
      to,
      actor: actor ?? null,
      decision,
      ruleVersion: moderationCase.ruleVersion,
      modelVersion: moderationCase.modelVersion,
      at: this.#nowIso()
    });
    this.#events.push(event);
    this.#onAudit(event);
    return event;
  }

  #put(caseId, next) {
    const frozen = Object.freeze(next);
    this.#cases.set(caseId, frozen);
    return frozen;
  }

  /** 状态迁移（唯一写入口）：更新 case + 写审计，保证「审计事件条数 === 迁移次数」 */
  #transition(moderationCase, { to, actor, decision, patch = {} }) {
    const nowIso = this.#nowIso();
    const next = this.#put(moderationCase.caseId, {
      ...moderationCase,
      ...patch,
      state: to,
      updatedAt: nowIso
    });
    this.#audit(next, { from: moderationCase.state, to, actor, decision });
    return next;
  }

  // ── 公开 API（契约 §40）────────────────────────────────────────────────────

  /**
   * 提审：机审 PASS→APPROVED（经 MACHINE_PASSED）、FLAG→HUMAN_PENDING、BLOCK→REJECTED；
   * 机审不可用/抛错 → HUMAN_PENDING（fail-closed）。
   */
  submit({ messageId, senderUserId, content, channel = 'PAID_MESSAGE' } = {}) {
    if (!isNonEmptyString(messageId) || !isNonEmptyString(senderUserId)) {
      throw new TypeError('submit: messageId/senderUserId 必填');
    }
    if (typeof content !== 'string' || content.trim() === '') {
      throw new ModerationError('待审内容不能为空', CODE.CONTENT_REQUIRED);
    }
    const nowIso = this.#nowIso();
    const base = {
      id: `mod-${randomUUID()}`,
      caseId: null,
      messageId,
      senderUserId,
      content,
      channel,
      state: MODERATION_STATE.RECEIVED,
      verdict: null,
      ruleVersion: null,
      modelVersion: null,
      hits: Object.freeze([]),
      machineUnavailable: false,
      createdAt: nowIso,
      updatedAt: nowIso,
      reviewedBy: null,
      reviewDecision: null,
      reviewNote: null,
      reviewedAt: null,
      appealedAt: null,
      appealReason: null,
      appealDeadline: null,
      resolvedBy: null,
      resolutionDecision: null,
      resolvedAt: null
    };
    const created = Object.freeze({ ...base, caseId: base.id });
    this.#cases.set(created.caseId, created);

    let verdict = null;
    let ruleVersion = null;
    let modelVersion = null;
    let hits = Object.freeze([]);
    let machineUnavailable = false;
    let machineFailureCode = null;
    if (this.#machine === null) {
      machineUnavailable = true;
      machineFailureCode = CODE.MACHINE_UNAVAILABLE;
    } else {
      let outcome = null;
      try {
        outcome = this.#machine({ messageId, content, senderUserId });
      } catch {
        machineUnavailable = true;
        machineFailureCode = CODE.MACHINE_UNAVAILABLE;
      }
      if (outcome && typeof outcome.then === 'function') {
        // 机审必须是同步返回 verdict；Promise 视为"不可用"（不能同步等待，fail-closed）
        machineUnavailable = true;
        machineFailureCode = CODE.MACHINE_UNAVAILABLE;
      } else if (!machineUnavailable) {
        if (!outcome || typeof outcome !== 'object' || !VERDICT_VALUES.has(outcome.verdict)) {
          machineUnavailable = true;
          machineFailureCode = CODE.MACHINE_UNAVAILABLE;
        } else {
          verdict = outcome.verdict;
          ruleVersion = isNonEmptyString(outcome.ruleVersion) ? outcome.ruleVersion : null;
          modelVersion = isNonEmptyString(outcome.modelVersion) ? outcome.modelVersion : null;
          hits = freezeHits(outcome.hits);
        }
      }
    }

    const withMachine = this.#put(created.caseId, {
      ...created,
      verdict: machineUnavailable ? null : verdict,
      ruleVersion,
      modelVersion,
      hits,
      machineUnavailable
    });

    if (machineUnavailable) {
      // fail-closed：机审不可用时绝不自动放行，转人工
      return this.#transition(withMachine, {
        to: MODERATION_STATE.HUMAN_PENDING,
        actor: 'system',
        decision: machineFailureCode,
        patch: { machineUnavailable: true }
      });
    }

    if (verdict === VERDICT.PASS) {
      // 经 MACHINE_PASSED 再自动通过（自动通过也记录 rule/model 版本）
      const passed = this.#transition(withMachine, {
        to: MODERATION_STATE.MACHINE_PASSED,
        actor: 'machine',
        decision: 'MACHINE_PASS'
      });
      return this.#transition(passed, {
        to: MODERATION_STATE.APPROVED,
        actor: 'machine',
        decision: 'AUTO_APPROVE'
      });
    }
    if (verdict === VERDICT.BLOCK) {
      const now = this.#now();
      return this.#transition(withMachine, {
        to: MODERATION_STATE.REJECTED,
        actor: 'machine',
        decision: 'MACHINE_BLOCK',
        patch: { appealDeadline: new Date(now.getTime() + this.#appealWindowMs).toISOString() }
      });
    }
    // FLAG
    return this.#transition(withMachine, {
      to: MODERATION_STATE.HUMAN_PENDING,
      actor: 'machine',
      decision: 'MACHINE_FLAG'
    });
  }

  /** 人工复核：仅 HUMAN_PENDING；终态/已决定一律 ILLEGAL_STATE */
  review({ caseId, actorUserId, decision, note = null } = {}) {
    const moderationCase = this.#require(caseId);
    if (!isNonEmptyString(actorUserId)) throw new TypeError('review: actorUserId 必填');
    if (decision !== 'APPROVE' && decision !== 'REJECT') {
      throw new ModerationError(`不支持的人工复核动作: ${String(decision)}`, CODE.ILLEGAL_STATE);
    }
    if (moderationCase.state !== MODERATION_STATE.HUMAN_PENDING) {
      throw new ModerationError(
        `只有 HUMAN_PENDING 可人工复核，当前 ${moderationCase.state}`,
        CODE.ILLEGAL_STATE
      );
    }
    const nowIso = this.#nowIso();
    const patch = {
      reviewedBy: actorUserId,
      reviewDecision: decision,
      reviewNote: note ?? null,
      reviewedAt: nowIso
    };
    if (decision === 'REJECT') {
      patch.appealDeadline = new Date(this.#now().getTime() + this.#appealWindowMs).toISOString();
    }
    return this.#transition(moderationCase, {
      to: decision === 'APPROVE' ? MODERATION_STATE.APPROVED : MODERATION_STATE.REJECTED,
      actor: actorUserId,
      decision,
      patch
    });
  }

  /** 申诉：仅发送者本人、仅 REJECTED/APPEAL_UPHELD、且在窗口内 */
  appeal({ caseId, userId, reason } = {}) {
    const moderationCase = this.#require(caseId);
    if (!isNonEmptyString(userId) || userId !== moderationCase.senderUserId) {
      throw new ModerationError('只有发送者本人可以申诉（FORBIDDEN）', CODE.FORBIDDEN);
    }
    if (!isNonEmptyString(reason)) throw new TypeError('appeal: reason 必填');
    if (
      moderationCase.state !== MODERATION_STATE.REJECTED &&
      moderationCase.state !== MODERATION_STATE.APPEAL_UPHELD
    ) {
      if (moderationCase.state === MODERATION_STATE.APPEALED) {
        throw new ModerationError('申诉已在处理中', CODE.ALREADY_DECIDED);
      }
      throw new ModerationError(
        `当前状态不可申诉: ${moderationCase.state}`,
        CODE.ILLEGAL_STATE
      );
    }
    const deadline = moderationCase.appealDeadline ? Date.parse(moderationCase.appealDeadline) : Number.NaN;
    if (Number.isFinite(deadline) && this.#now().getTime() > deadline) {
      throw new ModerationError('申诉窗口已关闭', CODE.APPEAL_WINDOW_CLOSED);
    }
    return this.#transition(moderationCase, {
      to: MODERATION_STATE.APPEALED,
      actor: userId,
      decision: 'APPEAL',
      patch: {
        appealedAt: this.#nowIso(),
        appealReason: reason
      }
    });
  }

  /** 申诉复核：GRANT→APPEAL_GRANTED、UPHOLD→APPEAL_UPHELD；与原审核人相同 => SELF_REVIEW_FORBIDDEN */
  resolveAppeal({ caseId, actorUserId, decision, note = null } = {}) {
    const moderationCase = this.#require(caseId);
    if (!isNonEmptyString(actorUserId)) throw new TypeError('resolveAppeal: actorUserId 必填');
    if (decision !== 'GRANT' && decision !== 'UPHOLD') {
      throw new ModerationError(`不支持的申诉复核动作: ${String(decision)}`, CODE.ILLEGAL_STATE);
    }
    if (moderationCase.state !== MODERATION_STATE.APPEALED) {
      if (
        moderationCase.state === MODERATION_STATE.APPEAL_GRANTED ||
        moderationCase.state === MODERATION_STATE.APPEAL_UPHELD
      ) {
        throw new ModerationError('申诉已裁决，不可重复复核', CODE.ALREADY_DECIDED);
      }
      throw new ModerationError(
        `只有 APPEALED 可复核申诉，当前 ${moderationCase.state}`,
        CODE.ILLEGAL_STATE
      );
    }
    if (isNonEmptyString(moderationCase.reviewedBy) && actorUserId === moderationCase.reviewedBy) {
      throw new ModerationError('申诉复核人不得与原审核人相同（SELF_REVIEW_FORBIDDEN）', CODE.SELF_REVIEW_FORBIDDEN);
    }
    const now = this.#now();
    const patch = {
      resolvedBy: actorUserId,
      resolutionDecision: decision,
      resolvedAt: this.#nowIso()
    };
    if (decision === 'UPHOLD') {
      // 维持原判后重新开启申诉窗口（契约允许对 APPEAL_UPHELD 再次申诉）
      patch.appealDeadline = new Date(now.getTime() + this.#appealWindowMs).toISOString();
    }
    return this.#transition(moderationCase, {
      to: decision === 'GRANT' ? MODERATION_STATE.APPEAL_GRANTED : MODERATION_STATE.APPEAL_UPHELD,
      actor: actorUserId,
      decision,
      patch
    });
  }

  /** 唯一投递/扣费判定：只有 APPROVED / APPEAL_GRANTED 返回 ok:true */
  canDeliver({ caseId } = {}) {
    const moderationCase = this.#cases.get(caseId);
    if (!moderationCase) return Object.freeze({ ok: false, code: CODE.NOT_FOUND });
    if (
      moderationCase.state === MODERATION_STATE.APPROVED ||
      moderationCase.state === MODERATION_STATE.APPEAL_GRANTED
    ) {
      return Object.freeze({ ok: true, code: null });
    }
    const code =
      moderationCase.state === MODERATION_STATE.REJECTED ||
      moderationCase.state === MODERATION_STATE.APPEAL_UPHELD
        ? CODE.FORBIDDEN
        : CODE.ILLEGAL_STATE;
    return Object.freeze({ ok: false, code });
  }

  get(caseId) {
    return this.#require(caseId);
  }

  /** @returns {ReadonlyArray<object>} 按 createdAt 再 caseId 升序 */
  list({ state = null } = {}) {
    if (state !== null && !STATE_VALUES.has(state)) {
      throw new ModerationError(`非法审核状态过滤: ${String(state)}`, CODE.ILLEGAL_STATE);
    }
    const all = [...this.#cases.values()].filter((moderationCase) => state === null || moderationCase.state === state);
    all.sort((a, b) => {
      if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1;
      return a.caseId < b.caseId ? -1 : a.caseId > b.caseId ? 1 : 0;
    });
    return Object.freeze(all);
  }

  /** 审核飞轮指标 */
  stats() {
    const byState = {};
    for (const state of STATE_VALUES) byState[state] = 0;
    let machineUnavailable = 0;
    for (const moderationCase of this.#cases.values()) {
      byState[moderationCase.state] += 1;
      if (moderationCase.machineUnavailable) machineUnavailable += 1;
    }
    return Object.freeze({
      total: this.#cases.size,
      byState: Object.freeze(byState),
      machineUnavailable,
      humanPending: byState[MODERATION_STATE.HUMAN_PENDING],
      approved:
        byState[MODERATION_STATE.APPROVED] + byState[MODERATION_STATE.APPEAL_GRANTED],
      rejected: byState[MODERATION_STATE.REJECTED] + byState[MODERATION_STATE.APPEAL_UPHELD]
    });
  }

  /** 附加方法：只追加审计事件（可按 caseId 过滤），供签名审计消费 */
  events(caseId = null) {
    if (caseId === null || caseId === undefined) return Object.freeze([...this.#events]);
    return Object.freeze(this.#events.filter((event) => event.caseId === caseId));
  }
}
