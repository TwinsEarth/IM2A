// 模块职责：Agent↔Agent 结构化协商（8 态 Task 状态机），与人类 IM 通道物理隔离；终止时才产出 artifact。
// 契约来源：contracts/INTERFACES.md §5。
// 关键不变量：①协商中间过程不产生用户可见通知；②轮次/Token 硬上限，超限置 INPUT_REQUIRED + terminalReason，绝不抛业务异常；
//             ③turns/artifact 不可变，session 状态变更返回新对象。
import { randomUUID } from 'node:crypto';

export const TASK_STATE = Object.freeze({
  SUBMITTED: 'SUBMITTED',
  WORKING: 'WORKING',
  INPUT_REQUIRED: 'INPUT_REQUIRED',
  AUTH_REQUIRED: 'AUTH_REQUIRED',
  COMPLETED: 'COMPLETED',
  FAILED: 'FAILED',
  CANCELED: 'CANCELED',
  REJECTED: 'REJECTED'
});

export const INTENT = Object.freeze({
  PROPOSE: 'PROPOSE',
  COUNTER: 'COUNTER',
  ACCEPT: 'ACCEPT',
  REJECT: 'REJECT',
  REQUEST_INFO: 'REQUEST_INFO',
  REQUEST_AUTH: 'REQUEST_AUTH'
});

const INTENT_VALUES = new Set(Object.values(INTENT));
const TERMINAL_STATES = Object.freeze([
  TASK_STATE.COMPLETED,
  TASK_STATE.FAILED,
  TASK_STATE.CANCELED,
  TASK_STATE.REJECTED
]);
const isTerminal = (state) => TERMINAL_STATES.includes(state);
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class NegotiationError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'NegotiationError';
    this.code = code;
  }
}

export class NegotiationEngine {
  #clock;
  #maxRounds;
  #maxTokens;
  #maxSeconds; // 预留：超时由外层调度器（生产为 Temporal 式工作流）判定，本层不实现超时语义。
  /** @type {Map<string, object>} sessionId -> 当前冻结 session */
  #sessions;
  /** @type {string[]} 打开顺序，保证 list() 确定性 */
  #order;

  constructor({ clock = () => new Date(), maxRounds = 5, maxTokens = 4000, maxSeconds = 300 } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#maxRounds = Number.isInteger(maxRounds) && maxRounds >= 0 ? maxRounds : 5;
    this.#maxTokens = Number.isInteger(maxTokens) && maxTokens >= 0 ? maxTokens : 4000;
    this.#maxSeconds = Number.isFinite(maxSeconds) && maxSeconds > 0 ? maxSeconds : 300;
    this.#sessions = new Map();
    this.#order = [];
  }

  #nowIso() {
    const sampled = this.#clock();
    const now = sampled instanceof Date ? sampled : new Date(sampled);
    return now.toISOString();
  }

  #require(sessionId) {
    const session = this.#sessions.get(sessionId);
    if (!session) throw new NegotiationError(`协商会话不存在: ${String(sessionId)}`, 'NOT_FOUND');
    return session;
  }

  /** 参与方校验：只有 initiator/responder 两个 Agent 可以出招 */
  #assertParticipant(session, fromAgentId) {
    if (fromAgentId !== session.initiatorAgentId && fromAgentId !== session.responderAgentId) {
      throw new NegotiationError(`Agent 不是本会话参与方: ${String(fromAgentId)}`, 'WRONG_AGENT');
    }
  }

  #assertNotTerminal(session) {
    if (isTerminal(session.state)) {
      throw new NegotiationError(`终态会话不可再收 turn: ${session.state}`, 'ILLEGAL_STATE');
    }
  }

  /** 打开协商：state=SUBMITTED, round=0, tokens=0, turns=[] */
  open({
    initiatorAgentId,
    initiatorUserId,
    responderAgentId,
    responderUserId,
    topic,
    payload = {}
  } = {}) {
    if (
      !isNonEmptyString(initiatorAgentId) ||
      !isNonEmptyString(responderAgentId) ||
      !isNonEmptyString(initiatorUserId) ||
      !isNonEmptyString(responderUserId)
    ) {
      // 契约 §5 未定义 open 的参数校验错误码；此处为程序员错误，用 TypeError 而非自造业务码。
      throw new TypeError('open: initiator/responder 的 Agent 与 User 标识均为必填');
    }
    const at = this.#nowIso();
    const session = Object.freeze({
      id: `neg-${randomUUID()}`,
      topic: topic ?? '',
      state: TASK_STATE.SUBMITTED,
      round: 0,
      tokens: 0,
      initiatorAgentId,
      responderAgentId,
      initiatorUserId,
      responderUserId,
      // 契约 §5 的 `initiator*` / `responder*` 同时以嵌套对象暴露，便于阅读；以上扁平字段为集成方消费口径。
      initiator: Object.freeze({ agentId: initiatorAgentId, userId: initiatorUserId }),
      responder: Object.freeze({ agentId: responderAgentId, userId: responderUserId }),
      turns: Object.freeze([]),
      terminalReason: null,
      artifact: null,
      createdAt: at
    });
    this.#sessions.set(session.id, session);
    this.#order.push(session.id);
    return session;
  }

  /**
   * 出招。返回 { session, turn }；轮次/Token 超限不抛异常，而是置 INPUT_REQUIRED + terminalReason。
   */
  turn({ sessionId, fromAgentId, intent, payload = {}, tokens = 0 } = {}) {
    const session = this.#require(sessionId);
    this.#assertParticipant(session, fromAgentId);
    this.#assertNotTerminal(session);
    if (!INTENT_VALUES.has(intent)) {
      throw new NegotiationError(`非法协商意图: ${String(intent)}`, 'INTENT_INVALID');
    }

    const addTokens = Number.isFinite(tokens) && tokens > 0 ? Math.trunc(tokens) : 0;
    const at = this.#nowIso();
    const turnPayload = Object.freeze({ ...payload });

    let { state } = session;
    let round = session.round;
    let terminalReason = null;
    let artifact = session.artifact;

    switch (intent) {
      case INTENT.PROPOSE:
        if (state !== TASK_STATE.SUBMITTED) {
          throw new NegotiationError(`PROPOSE 仅允许在 SUBMITTED 状态，当前 ${state}`, 'ILLEGAL_STATE');
        }
        state = TASK_STATE.WORKING;
        break;
      case INTENT.COUNTER:
        if (state !== TASK_STATE.WORKING && state !== TASK_STATE.INPUT_REQUIRED) {
          throw new NegotiationError(`COUNTER 仅允许在 WORKING/INPUT_REQUIRED，当前 ${state}`, 'ILLEGAL_STATE');
        }
        round += 1;
        state = TASK_STATE.WORKING;
        break;
      case INTENT.ACCEPT:
        if (
          state !== TASK_STATE.WORKING &&
          state !== TASK_STATE.INPUT_REQUIRED &&
          state !== TASK_STATE.AUTH_REQUIRED
        ) {
          throw new NegotiationError(
            `ACCEPT 仅允许在 WORKING/INPUT_REQUIRED/AUTH_REQUIRED，当前 ${state}`,
            'ILLEGAL_STATE'
          );
        }
        state = TASK_STATE.COMPLETED;
        artifact = Object.freeze({
          type: 'AGREEMENT',
          summary: turnPayload.summary ?? session.topic,
          payload: turnPayload,
          at
        });
        break;
      case INTENT.REJECT:
        // 任意非终态均可拒绝
        state = TASK_STATE.REJECTED;
        break;
      case INTENT.REQUEST_INFO:
        state = TASK_STATE.INPUT_REQUIRED;
        break;
      case INTENT.REQUEST_AUTH:
        state = TASK_STATE.AUTH_REQUIRED;
        break;
      default:
        // INTENT_VALUES 已校验，不可达
        throw new NegotiationError(`非法协商意图: ${String(intent)}`, 'INTENT_INVALID');
    }

    const totalTokens = session.tokens + addTokens;

    // 硬上限：轮次优先于 Token；两者都只降级为 INPUT_REQUIRED，不抛异常。
    if (intent === INTENT.COUNTER && round > this.#maxRounds) {
      state = TASK_STATE.INPUT_REQUIRED;
      terminalReason = 'MAX_ROUNDS';
    } else if (!isTerminal(state) && totalTokens > this.#maxTokens) {
      state = TASK_STATE.INPUT_REQUIRED;
      terminalReason = 'TOKEN_BUDGET';
    }

    const turn = Object.freeze({
      id: `turn-${randomUUID()}`,
      sessionId: session.id,
      fromAgentId,
      intent,
      payload: turnPayload,
      tokens: addTokens,
      round,
      at
    });
    const next = Object.freeze({
      ...session,
      state,
      round,
      tokens: totalTokens,
      turns: Object.freeze([...session.turns, turn]),
      terminalReason,
      artifact
    });
    this.#sessions.set(session.id, next);
    return Object.freeze({ session: next, turn });
  }

  get(sessionId) {
    return this.#require(sessionId);
  }

  /** 列出会话（可按状态过滤），按打开顺序，确定性 */
  list({ state = null } = {}) {
    const sessions = this.#order
      .map((id) => this.#sessions.get(id))
      .filter((session) => session && (state === null || session.state === state));
    return Object.freeze(sessions);
  }

  /** @returns {object|null} 终止时产出的 AGREEMENT artifact */
  artifact(sessionId) {
    return this.#require(sessionId).artifact;
  }

  /**
   * 附加方法（契约 §5 未列出，用于覆盖 8 态中的 FAILED/CANCELED）：
   * 终态再调用 => ILLEGAL_STATE；terminalReason 取传入 reason，否则 'FAILED'/'CANCELED'。
   */
  fail({ sessionId, reason = null } = {}) {
    return this.#terminate({ sessionId, to: TASK_STATE.FAILED, reason, fallbackReason: 'FAILED' });
  }

  /** 附加方法：取消协商（终态再调用 => ILLEGAL_STATE） */
  cancel({ sessionId, reason = null } = {}) {
    return this.#terminate({ sessionId, to: TASK_STATE.CANCELED, reason, fallbackReason: 'CANCELED' });
  }

  #terminate({ sessionId, to, reason, fallbackReason }) {
    const session = this.#require(sessionId);
    this.#assertNotTerminal(session);
    const next = Object.freeze({
      ...session,
      state: to,
      terminalReason: isNonEmptyString(reason) ? reason : fallbackReason
    });
    this.#sessions.set(session.id, next);
    return next;
  }
}
