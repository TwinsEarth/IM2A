// 模块职责：陌生 Agent 通信的「人类审批挂起」——Agent 来信在需要人决策的节点挂起，只有属主放行后才可投递进会话。
// 契约来源：contracts/INTERFACES.md §32。
// 关键不变量：①默认陌生来信直接 SUSPENDED，挂起中的来意不得进入会话（deliverable 只有 APPROVED 才 ok）；
//             ②只有 principalUserId 能 decide（非属主按 NOT_FOUND 处理，防枚举且不得让别人批）；③终态不可再批；
//             ④过期不可被补批（decide/deliverable 会惰性转 EXPIRED）；⑤每次状态迁移写事件并可接签名审计。
import { randomUUID } from 'node:crypto';

export const CORRESPONDENCE_STATE = Object.freeze({
  OPEN: 'OPEN',
  SUSPENDED: 'SUSPENDED',
  APPROVED: 'APPROVED',
  REFUSED: 'REFUSED',
  EXPIRED: 'EXPIRED'
});

export const CORRESPONDENCE_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  ILLEGAL_STATE: 'ILLEGAL_STATE',
  APPROVAL_REQUIRED: 'APPROVAL_REQUIRED',
  ALREADY_DECIDED: 'ALREADY_DECIDED',
  EXPIRED: 'EXPIRED'
});

const CODE = CORRESPONDENCE_CODE;
const TERMINAL_STATES = new Set([
  CORRESPONDENCE_STATE.APPROVED,
  CORRESPONDENCE_STATE.REFUSED,
  CORRESPONDENCE_STATE.EXPIRED
]);
const DEFAULT_TTL_MS = 24 * 3600e3;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

function resolveNowMs(nowMs, clock) {
  if (nowMs === null || nowMs === undefined) return clock.getTime();
  if (nowMs instanceof Date) return nowMs.getTime();
  if (typeof nowMs === 'number') return nowMs;
  const parsed = Date.parse(nowMs);
  return Number.isFinite(parsed) ? parsed : Number.NaN;
}

export class CorrespondenceError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'CorrespondenceError';
    this.code = code;
  }
}

export class AgentCorrespondence {
  #clock;
  #ttlMs;
  #onAudit;
  /** @type {Map<string, object>} correspondenceId -> frozen conversation */
  #conversations;
  /** @type {ReadonlyArray<object>} 只追加事件（供签名审计消费） */
  #events;

  constructor({ clock = () => new Date(), ttlMs = DEFAULT_TTL_MS, onAudit = () => {} } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    if (!Number.isFinite(ttlMs) || ttlMs <= 0) throw new TypeError('ttlMs 必须是正毫秒数');
    this.#ttlMs = Math.trunc(ttlMs);
    this.#onAudit = typeof onAudit === 'function' ? onAudit : () => {};
    this.#conversations = new Map();
    this.#events = [];
  }

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
      target,
      decision,
      detail: Object.freeze({ ...detail })
    });
    this.#events.push(event);
    this.#onAudit(event);
    return event;
  }

  #require(correspondenceId) {
    const conversation = this.#conversations.get(correspondenceId);
    if (!conversation) {
      throw new CorrespondenceError(`来信不存在: ${String(correspondenceId)}`, CODE.NOT_FOUND);
    }
    return conversation;
  }

  #transition(conversation, nextState, { actor, action, comment = null, at }) {
    const atIso = at ?? this.#nowIso();
    const next = Object.freeze({
      ...conversation,
      state: nextState,
      updatedAt: atIso,
      decidedAt: TERMINAL_STATES.has(nextState) && nextState !== CORRESPONDENCE_STATE.EXPIRED ? atIso : conversation.decidedAt,
      decidedBy:
        TERMINAL_STATES.has(nextState) && nextState !== CORRESPONDENCE_STATE.EXPIRED
          ? actor
          : conversation.decidedBy,
      comment: nextState === CORRESPONDENCE_STATE.EXPIRED ? conversation.comment : comment
    });
    this.#conversations.set(next.id, next);
    this.#emit({
      at: atIso,
      actor,
      action,
      target: next.id,
      decision: nextState,
      detail: { fromAgentId: next.fromAgentId, toAgentId: next.toAgentId, comment }
    });
    return next;
  }

  /** 惰性过期：只有 SUSPENDED 会因超过 expiresAt 自动转 EXPIRED（并留事件） */
  #refresh(conversation) {
    if (conversation.state !== CORRESPONDENCE_STATE.SUSPENDED) return conversation;
    if (Date.parse(conversation.expiresAt) > this.#now().getTime()) return conversation;
    return this.#transition(conversation, CORRESPONDENCE_STATE.EXPIRED, {
      actor: 'system',
      action: 'EXPIRE'
    });
  }

  // ── 公开 API（契约 §32）────────────────────────────────────────────────────

  /**
   * 打开一条 Agent 来信。默认 requiresHumanApproval=true => 直接挂起等待人类裁决。
   * @returns {Readonly<object>} 冻结 conversation
   */
  open({
    correspondenceId = null,
    fromAgentId,
    toAgentId,
    principalUserId,
    topic,
    payload = {},
    requiresHumanApproval = true
  } = {}) {
    for (const [field, value] of Object.entries({ fromAgentId, toAgentId, principalUserId, topic })) {
      if (!isNonEmptyString(value)) throw new TypeError(`open: ${field} 必填`);
    }
    const id = correspondenceId ?? `corr-${randomUUID()}`;
    if (!isNonEmptyString(id)) throw new TypeError('open: correspondenceId 必须是非空字符串');
    if (this.#conversations.has(id)) {
      throw new CorrespondenceError(`来信 id 已存在: ${id}`, CODE.ILLEGAL_STATE);
    }

    const createdAt = this.#nowIso();
    const state =
      requiresHumanApproval === false ? CORRESPONDENCE_STATE.APPROVED : CORRESPONDENCE_STATE.SUSPENDED;
    const conversation = Object.freeze({
      id,
      fromAgentId,
      toAgentId,
      principalUserId,
      topic,
      payload: Object.freeze({ ...payload }),
      requiresHumanApproval: requiresHumanApproval !== false,
      state,
      createdAt,
      updatedAt: createdAt,
      expiresAt: new Date(Date.parse(createdAt) + this.#ttlMs).toISOString(),
      decidedAt: state === CORRESPONDENCE_STATE.APPROVED ? createdAt : null,
      decidedBy: state === CORRESPONDENCE_STATE.APPROVED ? 'system:auto-approved' : null,
      comment: null
    });
    this.#conversations.set(id, conversation);
    this.#emit({
      at: createdAt,
      actor: fromAgentId,
      action: 'OPEN',
      target: id,
      decision: state,
      detail: {
        fromAgentId,
        toAgentId,
        principalUserId,
        topic,
        requiresHumanApproval: conversation.requiresHumanApproval
      }
    });
    return conversation;
  }

  /** 只有 principalUserId 能裁决；终态不可再批；过期不可补批 */
  decide({ correspondenceId, actorUserId, action, comment = null } = {}) {
    let conversation = this.#require(correspondenceId);
    if (!isNonEmptyString(actorUserId) || actorUserId !== conversation.principalUserId) {
      // 契约允许 APPROVAL_REQUIRED 或 NOT_FOUND；这里用 NOT_FOUND 防枚举，且绝不让他人代批
      throw new CorrespondenceError(
        `只有属主 principalUserId 能裁决: 他人无权（NOT_FOUND）`,
        CODE.NOT_FOUND
      );
    }
    conversation = this.#refresh(conversation);
    if (conversation.state === CORRESPONDENCE_STATE.EXPIRED) {
      throw new CorrespondenceError('来信已过期，不可补批', CODE.EXPIRED);
    }
    if (TERMINAL_STATES.has(conversation.state)) {
      throw new CorrespondenceError(`来信已裁决（${conversation.state}），不可重复决策`, CODE.ALREADY_DECIDED);
    }
    if (action !== 'APPROVE' && action !== 'REFUSE') {
      throw new CorrespondenceError(`不支持的裁决动作: ${String(action)}`, CODE.ILLEGAL_STATE);
    }
    return this.#transition(conversation, action === 'APPROVE' ? CORRESPONDENCE_STATE.APPROVED : CORRESPONDENCE_STATE.REFUSED, {
      actor: actorUserId,
      action,
      comment
    });
  }

  /** 显式过期：SUSPENDED 超过 expiresAt => EXPIRED；其它状态幂等返回 */
  expire({ correspondenceId, nowMs = null } = {}) {
    let conversation = this.#require(correspondenceId);
    conversation = this.#refresh(conversation);
    if (conversation.state !== CORRESPONDENCE_STATE.SUSPENDED) return conversation;
    const now = resolveNowMs(nowMs, this.#now());
    if (Number.isFinite(now) && now >= Date.parse(conversation.expiresAt)) {
      conversation = this.#transition(conversation, CORRESPONDENCE_STATE.EXPIRED, {
        actor: 'system',
        action: 'EXPIRE',
        at: new Date(now).toISOString()
      });
    }
    return conversation;
  }

  /**
   * 是否可投递进人类会话：只有 APPROVED 才 ok:true（挂起中的来意没有任何办法进入会话）。
   * @returns {Readonly<{ok:boolean, code:string|null}>}
   */
  deliverable({ correspondenceId } = {}) {
    const conversation = this.#conversations.get(correspondenceId);
    if (!conversation) return Object.freeze({ ok: false, code: CODE.NOT_FOUND });
    const refreshed = this.#refresh(conversation);
    switch (refreshed.state) {
      case CORRESPONDENCE_STATE.APPROVED:
        return Object.freeze({ ok: true, code: null });
      case CORRESPONDENCE_STATE.SUSPENDED:
        return Object.freeze({ ok: false, code: CODE.APPROVAL_REQUIRED });
      case CORRESPONDENCE_STATE.EXPIRED:
        return Object.freeze({ ok: false, code: CODE.EXPIRED });
      case CORRESPONDENCE_STATE.REFUSED:
        return Object.freeze({ ok: false, code: CODE.ALREADY_DECIDED });
      default:
        return Object.freeze({ ok: false, code: CODE.ILLEGAL_STATE });
    }
  }

  get(correspondenceId) {
    return this.#refresh(this.#require(correspondenceId));
  }

  /** @returns {ReadonlyArray<object>} 按 createdAt 再 id 升序；过滤前先惰性过期 */
  list({ state = null } = {}) {
    const all = [...this.#conversations.values()].map((conversation) => this.#refresh(conversation));
    const filtered = state === null ? all : all.filter((conversation) => conversation.state === state);
    filtered.sort((a, b) => {
      if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1;
      return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
    });
    return Object.freeze(filtered);
  }

  /** 事件回放：只追加；correspondenceId 为 null 时返回全局事件 */
  events(correspondenceId = null) {
    if (correspondenceId === null || correspondenceId === undefined) {
      return Object.freeze([...this.#events]);
    }
    return Object.freeze(this.#events.filter((event) => event.target === correspondenceId));
  }
}
