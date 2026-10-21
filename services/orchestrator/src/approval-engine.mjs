// 模块职责：审批单生命周期（人机混合中“人”这一侧的唯一落点）：显式状态机 + 事件溯源 + 超时升级。
// 契约来源：contracts/INTERFACES.md §4。
// 关键不变量：①状态机显式且不可跳步（白名单外一律 ILLEGAL_TRANSITION）；②事件只追加、绝不改写；
//             ③escalateOverdue 幂等（重复调用不重复升级）；④ticket 一律冻结，状态变更返回新对象。
import { randomUUID } from 'node:crypto';

export const TICKET_STATE = Object.freeze({
  PENDING: 'PENDING',
  REVIEWING: 'REVIEWING',
  APPROVED: 'APPROVED',
  REJECTED: 'REJECTED',
  MODIFIED: 'MODIFIED',
  EXECUTING: 'EXECUTING',
  COMPLETED: 'COMPLETED',
  FAILED: 'FAILED',
  EXPIRED: 'EXPIRED'
});

export const TICKET_TYPE = Object.freeze({
  NEGOTIATION: 'NEGOTIATION',
  PAYMENT: 'PAYMENT',
  SKILL_CALL: 'SKILL_CALL',
  EXTERNAL_ACTION: 'EXTERNAL_ACTION'
});

const OPEN_STATES = Object.freeze([TICKET_STATE.PENDING, TICKET_STATE.REVIEWING]);

/** 合法迁移白名单（契约 §4，其它一律 ILLEGAL_TRANSITION） */
const LEGAL_TRANSITIONS = Object.freeze({
  [TICKET_STATE.PENDING]: Object.freeze([
    TICKET_STATE.REVIEWING,
    TICKET_STATE.APPROVED,
    TICKET_STATE.MODIFIED,
    TICKET_STATE.REJECTED,
    TICKET_STATE.EXPIRED
  ]),
  [TICKET_STATE.REVIEWING]: Object.freeze([
    TICKET_STATE.APPROVED,
    TICKET_STATE.MODIFIED,
    TICKET_STATE.REJECTED,
    TICKET_STATE.EXPIRED
  ]),
  [TICKET_STATE.APPROVED]: Object.freeze([TICKET_STATE.EXECUTING, TICKET_STATE.EXPIRED]),
  [TICKET_STATE.MODIFIED]: Object.freeze([TICKET_STATE.EXECUTING, TICKET_STATE.EXPIRED]),
  [TICKET_STATE.EXECUTING]: Object.freeze([TICKET_STATE.COMPLETED, TICKET_STATE.FAILED])
});

const PRIORITY_ORDER = Object.freeze({ urgent: 0, high: 1, normal: 2, low: 3 });
const DEFAULT_ESCALATION = Object.freeze({
  afterMs: Object.freeze([30 * 60e3, 2 * 3600e3]),
  channels: Object.freeze(['watch', 'sms'])
});
const DEFAULT_DEADLINE_MS = 30 * 60e3;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class ApprovalStateError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'ApprovalStateError';
    this.code = code;
  }
}

export class ApprovalEngine {
  #clock;
  #escalation;
  #onEvent;
  /** @type {Map<string, object>} ticketId -> 当前冻结 ticket */
  #tickets;
  /** @type {ReadonlyArray<object>} 全局只追加事件 */
  #events;
  /** @type {Map<string, number>} ticketId -> 已升级到的级别（幂等依据） */
  #escalated;

  constructor({
    clock = () => new Date(),
    escalation = DEFAULT_ESCALATION,
    onEvent = () => {}
  } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#onEvent = typeof onEvent === 'function' ? onEvent : () => {};
    this.#tickets = new Map();
    this.#events = [];
    this.#escalated = new Map();

    const afterMsSource = Array.isArray(escalation?.afterMs) ? escalation.afterMs : DEFAULT_ESCALATION.afterMs;
    const afterMs = afterMsSource
      .filter((ms) => Number.isFinite(ms) && ms >= 0)
      .map((ms) => Math.trunc(ms))
      .sort((a, b) => a - b);
    const channelSource =
      Array.isArray(escalation?.channels) && escalation.channels.length > 0
        ? escalation.channels
        : DEFAULT_ESCALATION.channels;
    this.#escalation = Object.freeze({
      afterMs: Object.freeze(afterMs),
      channels: Object.freeze(channelSource.map((channel) => String(channel)))
    });
  }

  // ── 内部工具 ────────────────────────────────────────────────────────────────

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #emit({ at, actor, action, target, decision, detail = {} }) {
    const event = Object.freeze({
      at,
      actor: actor ?? null,
      action,
      target: target ?? null,
      decision,
      detail: Object.freeze({ ...detail })
    });
    this.#events.push(event);
    this.#onEvent(event);
    return event;
  }

  #require(ticketId) {
    const ticket = this.#tickets.get(ticketId);
    if (!ticket) throw new ApprovalStateError(`审批单不存在: ${String(ticketId)}`, 'NOT_FOUND');
    return ticket;
  }

  #assertOwner(ticket, actorUserId, actionLabel) {
    if (!isNonEmptyString(actorUserId) || actorUserId !== ticket.userId) {
      throw new ApprovalStateError(`非审批单所有者无权${actionLabel}（FORBIDDEN）`, 'FORBIDDEN');
    }
  }

  #isOverdue(ticket) {
    const deadlineMs = Date.parse(ticket.deadline);
    return Number.isFinite(deadlineMs) && deadlineMs <= this.#now().getTime();
  }

  #transition(ticket, to, extra = {}) {
    const allowed = LEGAL_TRANSITIONS[ticket.state];
    if (!allowed || !allowed.includes(to)) {
      throw new ApprovalStateError(`非法状态迁移 ${ticket.state} -> ${to}`, 'ILLEGAL_TRANSITION');
    }
    const next = Object.freeze({ ...ticket, state: to, ...extra });
    this.#tickets.set(next.id, next);
    return next;
  }

  /** 惰性到期：仅对白名单允许 EXPIRED 的状态生效（PENDING/REVIEWING/APPROVED/MODIFIED） */
  #expire(ticket, at) {
    if (ticket.state === TICKET_STATE.EXPIRED) return ticket;
    const allowed = LEGAL_TRANSITIONS[ticket.state];
    if (!allowed?.includes(TICKET_STATE.EXPIRED)) return ticket;
    const next = Object.freeze({ ...ticket, state: TICKET_STATE.EXPIRED });
    this.#tickets.set(next.id, next);
    this.#emit({
      at,
      actor: 'system',
      action: 'EXPIRE',
      target: ticket.id,
      decision: TICKET_STATE.EXPIRED,
      detail: { deadline: ticket.deadline, stateFrom: ticket.state }
    });
    return next;
  }

  // ── 公开 API（契约 §4）────────────────────────────────────────────────────

  create({
    userId,
    sourceAgentId,
    ticketType,
    payload = {},
    deadlineMs = DEFAULT_DEADLINE_MS,
    priority = 'normal',
    traceId = null
  } = {}) {
    if (!Number.isFinite(deadlineMs) || deadlineMs < 0) {
      throw new TypeError('create: deadlineMs 必须是非负毫秒数');
    }
    const now = this.#now();
    const createdAt = now.toISOString();
    const deadline = new Date(now.getTime() + Math.trunc(deadlineMs)).toISOString();
    const ticket = Object.freeze({
      id: `tkt-${randomUUID()}`,
      userId: userId ?? null,
      sourceAgentId: sourceAgentId ?? null,
      ticketType: ticketType ?? null,
      state: TICKET_STATE.PENDING,
      payload: Object.freeze({ ...payload }),
      createdAt,
      deadline,
      priority: priority ?? 'normal',
      traceId: traceId ?? null,
      patch: null
    });
    this.#tickets.set(ticket.id, ticket);
    this.#emit({
      at: createdAt,
      actor: ticket.sourceAgentId,
      action: 'CREATE',
      target: ticket.id,
      decision: TICKET_STATE.PENDING,
      detail: {
        userId: ticket.userId,
        ticketType: ticket.ticketType,
        priority: ticket.priority,
        deadline,
        traceId: ticket.traceId
      }
    });
    return ticket;
  }

  get(ticketId) {
    return this.#require(ticketId);
  }

  /** 按 priority(urgent>high>normal>low) 再按 deadline 升序 */
  listPending(userId) {
    const pending = [...this.#tickets.values()]
      .filter((ticket) => ticket.userId === userId && OPEN_STATES.includes(ticket.state))
      .sort((a, b) => {
        const weightA = PRIORITY_ORDER[a.priority] ?? PRIORITY_ORDER.normal;
        const weightB = PRIORITY_ORDER[b.priority] ?? PRIORITY_ORDER.normal;
        if (weightA !== weightB) return weightA - weightB;
        if (a.deadline !== b.deadline) return a.deadline < b.deadline ? -1 : 1;
        return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
      });
    return Object.freeze(pending);
  }

  /** 人工决策：APPROVE | MODIFY | REJECT；非本人 => FORBIDDEN；过期 => EXPIRED_TICKET */
  decide({ ticketId, actorUserId, action, patch = null, comment = null } = {}) {
    const ticket = this.#require(ticketId);
    const at = this.#nowIso();
    this.#assertOwner(ticket, actorUserId, '决策');
    if (ticket.state === TICKET_STATE.EXPIRED || this.#isOverdue(ticket)) {
      this.#expire(ticket, at);
      throw new ApprovalStateError('审批单已过期，不可再决策', 'EXPIRED_TICKET');
    }

    let to;
    switch (action) {
      case 'APPROVE':
        to = TICKET_STATE.APPROVED;
        break;
      case 'MODIFY':
        to = TICKET_STATE.MODIFIED;
        break;
      case 'REJECT':
        to = TICKET_STATE.REJECTED;
        break;
      default:
        throw new ApprovalStateError(`不支持的决策动作: ${String(action)}`, 'ILLEGAL_TRANSITION');
    }

    const patchValue = patch === null || patch === undefined ? null : Object.freeze({ ...patch });
    const next = this.#transition(ticket, to, { patch: patchValue });
    this.#emit({
      at,
      actor: actorUserId,
      action: 'DECIDE',
      target: ticket.id,
      decision: to,
      detail: { action, comment, patch: patchValue, stateFrom: ticket.state }
    });
    return next;
  }

  /**
   * 附加方法（契约 §4 未列出，用于覆盖 9 态中的 REVIEWING）：
   * 仅 PENDING 可进入 REVIEWING（白名单已包含），其它状态 => ILLEGAL_TRANSITION。
   */
  beginReview({ ticketId, actorUserId } = {}) {
    const ticket = this.#require(ticketId);
    const at = this.#nowIso();
    this.#assertOwner(ticket, actorUserId, '开始审阅');
    if (ticket.state === TICKET_STATE.EXPIRED || this.#isOverdue(ticket)) {
      this.#expire(ticket, at);
      throw new ApprovalStateError('审批单已过期，不可开始审阅', 'EXPIRED_TICKET');
    }
    if (ticket.state !== TICKET_STATE.PENDING) {
      throw new ApprovalStateError(`只有 PENDING 可进入 REVIEWING，当前 ${ticket.state}`, 'ILLEGAL_TRANSITION');
    }
    const next = this.#transition(ticket, TICKET_STATE.REVIEWING);
    this.#emit({
      at,
      actor: actorUserId,
      action: 'BEGIN_REVIEW',
      target: ticket.id,
      decision: TICKET_STATE.REVIEWING,
      detail: {}
    });
    return next;
  }

  /** 仅 APPROVED|MODIFIED 可进入 EXECUTING */
  beginExecution({ ticketId } = {}) {
    const ticket = this.#require(ticketId);
    const next = this.#transition(ticket, TICKET_STATE.EXECUTING);
    this.#emit({
      at: this.#nowIso(),
      actor: 'system',
      action: 'BEGIN_EXECUTION',
      target: ticket.id,
      decision: TICKET_STATE.EXECUTING,
      detail: { stateFrom: ticket.state }
    });
    return next;
  }

  complete({ ticketId, result = null } = {}) {
    const ticket = this.#require(ticketId);
    const next = this.#transition(ticket, TICKET_STATE.COMPLETED);
    this.#emit({
      at: this.#nowIso(),
      actor: 'system',
      action: 'COMPLETE',
      target: ticket.id,
      decision: TICKET_STATE.COMPLETED,
      detail: { result }
    });
    return next;
  }

  fail({ ticketId, error } = {}) {
    const ticket = this.#require(ticketId);
    const next = this.#transition(ticket, TICKET_STATE.FAILED);
    this.#emit({
      at: this.#nowIso(),
      actor: 'system',
      action: 'FAIL',
      target: ticket.id,
      decision: TICKET_STATE.FAILED,
      detail: { error: error instanceof Error ? `${error.name}: ${error.message}` : error ?? null }
    });
    return next;
  }

  /**
   * 超时升级（幂等）：以 createdAt 起算，elapsed 跨过 afterMs 第 N 档即升级到 N 级。
   * 重复调用不会重复升级；返回本次新增的升级记录。
   */
  escalateOverdue(nowMs = null) {
    const now = nowMs === null || nowMs === undefined ? this.#now() : new Date(nowMs);
    const at = now.toISOString();
    const created = [];
    for (const ticket of [...this.#tickets.values()]) {
      if (!OPEN_STATES.includes(ticket.state)) continue;
      const createdMs = Date.parse(ticket.createdAt);
      const elapsed = now.getTime() - createdMs;
      if (!Number.isFinite(elapsed) || elapsed < 0) continue;
      const targetLevel = this.#escalation.afterMs.filter((threshold) => elapsed >= threshold).length;
      const already = this.#escalated.get(ticket.id) ?? 0;
      for (let level = already + 1; level <= targetLevel; level += 1) {
        const channel =
          this.#escalation.channels[Math.min(level - 1, this.#escalation.channels.length - 1)] ?? null;
        const entry = Object.freeze({ ticketId: ticket.id, level, channel, at });
        created.push(entry);
        this.#escalated.set(ticket.id, level);
        this.#emit({
          at,
          actor: 'system',
          action: 'ESCALATE',
          target: ticket.id,
          decision: 'ESCALATED',
          detail: { level, channel, escalationLevel: level }
        });
      }
    }
    return Object.freeze(created);
  }

  /** 事件溯源：只追加；ticketId 为 null 时返回全局事件 */
  events(ticketId = null) {
    if (ticketId === null || ticketId === undefined) return Object.freeze([...this.#events]);
    return Object.freeze(this.#events.filter((event) => event.target === ticketId));
  }
}
