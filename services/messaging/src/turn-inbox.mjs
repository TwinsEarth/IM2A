// 模块职责：轮次内投递（mid-turn delivery）——Agent 正在执行一轮任务时，对方的消息/协商提案应在该轮次内即时到达，而不是等下一次用户提示 / 契约来源：contracts/INTERFACES.md §25 / 关键不变量：活动轮次内注入标记 MID_TURN 且立即可 drain；无活动轮次入队 QUEUED 等下一轮；轮次结束仍未消费 → 计入 dropped 并保留审计（绝不静默丢失）；容量超限 → CAPACITY_EXCEEDED。
import { randomUUID } from 'node:crypto';

export const TURN_STATE = Object.freeze({
  IDLE: 'IDLE',
  RUNNING: 'RUNNING',
  CLOSED: 'CLOSED',
});

export const INBOX_CODE = Object.freeze({
  TURN_NOT_FOUND: 'TURN_NOT_FOUND',
  TURN_CLOSED: 'TURN_CLOSED',
  AGENT_MISMATCH: 'AGENT_MISMATCH',
  CAPACITY_EXCEEDED: 'CAPACITY_EXCEEDED',
  NO_ACTIVE_TURN: 'NO_ACTIVE_TURN',
});

function isNonEmptyString(value) {
  return typeof value === 'string' && value.length > 0;
}

export class TurnInboxError extends Error {
  constructor(message, code = 'TURN_INBOX_ERROR') {
    super(message);
    this.name = 'TurnInboxError';
    this.code = code;
  }
}

export class TurnInbox {
  #clock;
  #capacity;
  /** @type {Map<string, {activeTurnId:string|null, pending:Array<object>, turns:string[], audit:Array<object>}>} */
  #agents = new Map();
  /** @type {Map<string, object>} turnId -> 轮次记录（内部可变；对外一律返回冻结视图） */
  #turns = new Map();

  constructor({ clock = () => new Date(), capacity = 50 } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!Number.isInteger(capacity) || capacity <= 0) {
      throw new RangeError('capacity 必须是正整数');
    }
    this.#clock = clock;
    this.#capacity = capacity;
  }

  /**
   * 开启一轮。若该 agent 已有活动轮次，旧轮次被自动关闭（未消费消息进 dropped + 审计，
   * 不静默丢失）；传入已存在的 turnId 时：同一 agent 的 RUNNING 轮次幂等返回，其余按错误码拒绝。
   * @returns {Readonly<{id, agentId, state:'RUNNING', startedAt}>}
   */
  beginTurn({ agentId, turnId = null } = {}) {
    this.#assertAgentId(agentId);
    if (turnId !== null && !isNonEmptyString(turnId)) throw new TypeError('turnId 必须是非空字符串或 null');
    const inbox = this.#ensureInbox(agentId);

    if (turnId !== null) {
      const existing = this.#turns.get(turnId);
      if (existing) {
        if (existing.agentId !== agentId) {
          throw new TurnInboxError(`轮次 ${turnId} 属于其它 agent: ${existing.agentId}`, INBOX_CODE.AGENT_MISMATCH);
        }
        if (existing.state === TURN_STATE.CLOSED) {
          throw new TurnInboxError(`轮次 ${turnId} 已关闭，不可复用`, INBOX_CODE.TURN_CLOSED);
        }
        return this.#turnView(existing); // 幂等：同一 turnId 重试返回同一活动轮次
      }
    }

    if (inbox.activeTurnId !== null) {
      // 上一轮未正常 endTurn：自动关闭并把未消费消息计入 dropped（审计可查）
      this.#closeTurn(this.#turns.get(inbox.activeTurnId), {
        outcome: 'superseded',
        reason: 'superseded_by_beginTurn',
      });
    }

    const record = {
      id: turnId ?? `turn-${randomUUID()}`,
      agentId,
      state: TURN_STATE.RUNNING,
      startedAt: this.#nowIso(),
      endedAt: null,
      outcome: null,
      injected: 0,
      consumed: 0,
      dropped: 0,
      droppedMessages: [],
    };
    this.#turns.set(record.id, record);
    inbox.turns.push(record.id);
    inbox.activeTurnId = record.id;
    this.#appendAudit(inbox, {
      action: 'BEGIN_TURN',
      turnId: record.id,
      decision: TURN_STATE.RUNNING,
      detail: {},
    });
    return this.#turnView(record);
  }

  /**
   * 注入消息。有活动轮次 → 立即进入该轮待消费队列（delivered='MID_TURN'）；
   * 无活动轮次 → QUEUED 等下一轮 drain。
   * @returns {Readonly<{delivered:'MID_TURN'|'QUEUED', pending:number}>}
   */
  inject({ agentId, message, turnId = null } = {}) {
    this.#assertAgentId(agentId);
    if (message === null || message === undefined) throw new TypeError('message 不可为空');
    if (turnId !== null && !isNonEmptyString(turnId)) throw new TypeError('turnId 必须是非空字符串或 null');
    const inbox = this.#ensureInbox(agentId);

    let targetTurn = null;
    if (turnId !== null) {
      targetTurn = this.#resolveRunningTurn(agentId, turnId);
    } else if (inbox.activeTurnId !== null) {
      targetTurn = this.#turns.get(inbox.activeTurnId);
    }
    if (inbox.pending.length >= this.#capacity) {
      throw new TurnInboxError(`收件箱已达容量上限 capacity=${this.#capacity}`, INBOX_CODE.CAPACITY_EXCEEDED);
    }

    const delivered = targetTurn === null ? 'QUEUED' : 'MID_TURN';
    inbox.pending.push({
      message,
      delivered,
      turnId: targetTurn === null ? null : targetTurn.id,
      at: this.#nowIso(),
    });
    if (targetTurn !== null) targetTurn.injected += 1;
    this.#appendAudit(inbox, {
      action: delivered === 'MID_TURN' ? 'INJECT_MID_TURN' : 'QUEUED',
      turnId: targetTurn === null ? null : targetTurn.id,
      decision: delivered,
      detail: { message },
    });
    return Object.freeze({ delivered, pending: inbox.pending.length });
  }

  /**
   * 消费当前活动轮次的全部待消费消息（QUEUED 与 MID_TURN 一并取出，按到达顺序）。
   * 无活动轮次 → NO_ACTIVE_TURN。
   * @returns {Readonly<{consumed:message[], pending:number}>}
   */
  drain({ agentId, turnId = null } = {}) {
    this.#assertAgentId(agentId);
    if (turnId !== null && !isNonEmptyString(turnId)) throw new TypeError('turnId 必须是非空字符串或 null');
    const inbox = this.#ensureInbox(agentId);
    const turn = turnId === null ? this.#activeTurn(agentId) : this.#resolveRunningTurn(agentId, turnId);

    const consumed = inbox.pending.map((entry) => entry.message);
    inbox.pending = [];
    turn.consumed += consumed.length;
    this.#appendAudit(inbox, {
      action: 'DRAIN',
      turnId: turn.id,
      decision: 'CONSUMED',
      detail: { count: consumed.length },
    });
    return Object.freeze({ consumed: Object.freeze(consumed), pending: 0 });
  }

  /**
   * 结束一轮：未消费消息全部计入 dropped 并逐条写审计（不静默丢失）。
   * @returns {Readonly<{injected:number, consumed:number, dropped:number, state:'CLOSED'}>}
   */
  endTurn({ agentId, turnId = null, outcome = null } = {}) {
    this.#assertAgentId(agentId);
    if (turnId !== null && !isNonEmptyString(turnId)) throw new TypeError('turnId 必须是非空字符串或 null');
    const turn = turnId === null ? this.#activeTurn(agentId) : this.#resolveRunningTurn(agentId, turnId);
    const closed = this.#closeTurn(turn, { outcome, reason: 'endTurn_unconsumed' });
    return Object.freeze({
      injected: closed.injected,
      consumed: closed.consumed,
      dropped: closed.dropped,
      state: TURN_STATE.CLOSED,
    });
  }

  /** 收件箱状态：{ state, pending, turns }；dropped 明细在 turns[*].droppedMessages 中可查。 */
  status({ agentId } = {}) {
    this.#assertAgentId(agentId);
    const inbox = this.#agents.get(agentId);
    if (!inbox) {
      return Object.freeze({ state: TURN_STATE.IDLE, pending: 0, turns: Object.freeze([]) });
    }
    const activeTurn = inbox.activeTurnId === null ? null : this.#turns.get(inbox.activeTurnId);
    const turns = inbox.turns.map((turnId) => this.#turnView(this.#turns.get(turnId)));
    return Object.freeze({
      state: activeTurn ? TURN_STATE.RUNNING : TURN_STATE.IDLE,
      pending: inbox.pending.length,
      turns: Object.freeze(turns),
    });
  }

  /** 审计事件（实现层补充查询：只追加，验证 dropped/注入/消费过程可回溯）。 */
  audit({ agentId } = {}) {
    this.#assertAgentId(agentId);
    const inbox = this.#agents.get(agentId);
    if (!inbox) return Object.freeze([]);
    return Object.freeze([...inbox.audit]);
  }

  // ── 内部 ────────────────────────────────────────────────────────────────

  #assertAgentId(agentId) {
    if (!isNonEmptyString(agentId)) throw new TypeError('agentId 必须是非空字符串');
  }

  #ensureInbox(agentId) {
    let inbox = this.#agents.get(agentId);
    if (!inbox) {
      inbox = { agentId, activeTurnId: null, pending: [], turns: [], audit: [] };
      this.#agents.set(agentId, inbox);
    }
    return inbox;
  }

  #nowIso() {
    const sampled = this.#clock();
    if (!(sampled instanceof Date) || Number.isNaN(sampled.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return sampled.toISOString();
  }

  #activeTurn(agentId) {
    const inbox = this.#agents.get(agentId);
    if (!inbox || inbox.activeTurnId === null) {
      throw new TurnInboxError(`agent 没有活动轮次: ${agentId}`, INBOX_CODE.NO_ACTIVE_TURN);
    }
    return this.#turns.get(inbox.activeTurnId);
  }

  #resolveRunningTurn(agentId, turnId) {
    const turn = this.#turns.get(turnId);
    if (!turn) throw new TurnInboxError(`轮次不存在: ${turnId}`, INBOX_CODE.TURN_NOT_FOUND);
    if (turn.agentId !== agentId) {
      throw new TurnInboxError(`轮次 ${turnId} 属于其它 agent: ${turn.agentId}`, INBOX_CODE.AGENT_MISMATCH);
    }
    if (turn.state !== TURN_STATE.RUNNING) {
      throw new TurnInboxError(`轮次 ${turnId} 已关闭`, INBOX_CODE.TURN_CLOSED);
    }
    return turn;
  }

  #closeTurn(turn, { outcome, reason }) {
    const inbox = this.#agents.get(turn.agentId);
    const at = this.#nowIso();
    const remaining = [...inbox.pending];
    inbox.pending = [];
    turn.state = TURN_STATE.CLOSED;
    turn.endedAt = at;
    turn.outcome = outcome ?? null;
    for (const entry of remaining) {
      turn.dropped += 1;
      turn.droppedMessages.push(entry.message);
      this.#appendAudit(inbox, {
        action: 'DROP',
        turnId: turn.id,
        decision: 'DROPPED',
        detail: { reason, delivered: entry.delivered, message: entry.message },
      });
    }
    inbox.activeTurnId = null;
    this.#appendAudit(inbox, {
      action: 'END_TURN',
      turnId: turn.id,
      decision: TURN_STATE.CLOSED,
      detail: { outcome: turn.outcome, injected: turn.injected, consumed: turn.consumed, dropped: turn.dropped },
    });
    return turn;
  }

  #appendAudit(inbox, { action, turnId, decision, detail }) {
    inbox.audit.push(
      Object.freeze({
        at: this.#nowIso(),
        actor: inbox.agentId,
        action,
        target: turnId,
        decision,
        detail: Object.freeze({ ...detail }),
      })
    );
  }

  #turnView(record) {
    return Object.freeze({
      id: record.id,
      agentId: record.agentId,
      state: record.state,
      startedAt: record.startedAt,
      endedAt: record.endedAt,
      outcome: record.outcome,
      injected: record.injected,
      consumed: record.consumed,
      dropped: record.dropped,
      droppedMessages: Object.freeze([...record.droppedMessages]),
    });
  }
}
