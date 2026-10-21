// 模块职责：v3 多 Agent 结构化讨论引擎（DISCUSS / AUDIT / REVIEW，≥2 方；形成结论/发现/评审意见并**保留异议**）/ 契约来源：contracts/INTERFACES.md §18 / 关键不变量：参与者 ≥2 且 agentId 唯一、非参与者不得发言、每人每轮发言次数有上限且轮次有硬上限（超限转 ESCALATED，绝不静默丢弃）、AUDIT/REVIEW 必须 findings、DISCUSS 必须 conclusion、dissent 原样进入纪要。
import { randomUUID } from 'node:crypto';

export const DISCUSSION_MODE = Object.freeze({
  DISCUSS: 'DISCUSS',
  AUDIT: 'AUDIT',
  REVIEW: 'REVIEW',
});

export const DISCUSSION_STATE = Object.freeze({
  OPEN: 'OPEN',
  IN_ROUND: 'IN_ROUND',
  SYNTHESIZING: 'SYNTHESIZING',
  CONCLUDED: 'CONCLUDED',
  ESCALATED: 'ESCALATED',
  ABORTED: 'ABORTED',
});

export const STANCE = Object.freeze({
  SUPPORT: 'SUPPORT',
  OPPOSE: 'OPPOSE',
  QUESTION: 'QUESTION',
  EVIDENCE: 'EVIDENCE',
});

export const DISCUSSION_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  ILLEGAL_STATE: 'ILLEGAL_STATE',
  NOT_PARTICIPANT: 'NOT_PARTICIPANT',
  MODE_INVALID: 'MODE_INVALID',
  PARTICIPANTS_INVALID: 'PARTICIPANTS_INVALID',
  ROUND_LIMIT: 'ROUND_LIMIT',
  TURN_LIMIT: 'TURN_LIMIT',
  FINDINGS_REQUIRED: 'FINDINGS_REQUIRED',
  CONCLUSION_REQUIRED: 'CONCLUSION_REQUIRED',
});

const MODES = new Set(Object.values(DISCUSSION_MODE));
const STANCES = new Set(Object.values(STANCE));
const TERMINAL_STATES = new Set([
  DISCUSSION_STATE.CONCLUDED,
  DISCUSSION_STATE.ESCALATED,
  DISCUSSION_STATE.ABORTED,
]);

function isNonEmptyString(value) {
  return typeof value === 'string' && value.trim() !== '';
}

function isPlainObject(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/**
 * 原样保留列表：不合并、不去重、不排序，只做「数组冻结 + 普通对象一层冻结拷贝」，
 * 保证调用方之后修改入参不会破坏已形成的会议纪要（dissent 的存在理由就是不被综合掉）。
 */
function freezeVerbatim(list) {
  return Object.freeze(
    list.map((item) => (isPlainObject(item) ? Object.freeze({ ...item }) : item))
  );
}

export class DiscussionError extends Error {
  constructor(message, code = 'DISCUSSION_ERROR') {
    super(message);
    this.name = 'DiscussionError';
    this.code = code;
  }
}

export class DiscussionEngine {
  #clock;
  #maxRounds;
  #maxTurnsPerParticipantPerRound;
  /** @type {Map<string, object>} discussionId -> 当前冻结讨论对象 */
  #discussions = new Map();
  /** @type {string[]} 打开顺序，保证 list() 确定性 */
  #order = [];

  constructor({ clock = () => new Date(), maxRounds = 3, maxTurnsPerParticipantPerRound = 1 } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!Number.isInteger(maxRounds) || maxRounds <= 0) {
      throw new RangeError('maxRounds 必须是正整数');
    }
    if (!Number.isInteger(maxTurnsPerParticipantPerRound) || maxTurnsPerParticipantPerRound <= 0) {
      throw new RangeError('maxTurnsPerParticipantPerRound 必须是正整数');
    }
    this.#clock = clock;
    this.#maxRounds = maxRounds;
    this.#maxTurnsPerParticipantPerRound = maxTurnsPerParticipantPerRound;
  }

  #nowIso() {
    const sampled = this.#clock();
    const now = sampled instanceof Date ? sampled : new Date(sampled);
    return now.toISOString();
  }

  #require(discussionId) {
    const discussion = this.#discussions.get(discussionId);
    if (!discussion) {
      throw new DiscussionError(`讨论不存在: ${String(discussionId)}`, DISCUSSION_CODE.NOT_FOUND);
    }
    return discussion;
  }

  #assertNotTerminal(discussion) {
    if (TERMINAL_STATES.has(discussion.state)) {
      throw new DiscussionError(`讨论已处于终态 ${discussion.state}，不可再操作`, DISCUSSION_CODE.ILLEGAL_STATE);
    }
  }

  #isParticipant(discussion, agentId) {
    return discussion.participants.some((participant) => participant.agentId === agentId);
  }

  /**
   * 开启讨论。participants: [{ agentId, role }]，≥2 且 agentId 唯一。
   * 初始 state=OPEN、round=1；首个 speak 后进入 IN_ROUND。
   */
  open({ mode, topic, participants, ownerUserId, agenda = [] } = {}) {
    if (!isNonEmptyString(topic)) throw new TypeError('topic 必须是非空字符串');
    if (!isNonEmptyString(ownerUserId)) throw new TypeError('ownerUserId 必须是非空字符串');
    if (!MODES.has(mode)) {
      throw new DiscussionError(
        `非法讨论模式 mode=${String(mode)}（允许值 ${Object.values(DISCUSSION_MODE).join(' / ')}）`,
        DISCUSSION_CODE.MODE_INVALID
      );
    }
    if (!Array.isArray(participants) || participants.length < 2) {
      throw new DiscussionError('participants 必须是 ≥2 个参与者的数组', DISCUSSION_CODE.PARTICIPANTS_INVALID);
    }
    const seen = new Set();
    const normalized = [];
    for (const item of participants) {
      if (!isPlainObject(item) || !isNonEmptyString(item.agentId)) {
        throw new DiscussionError('participants 中每项必须是 { agentId, role? } 且 agentId 非空', DISCUSSION_CODE.PARTICIPANTS_INVALID);
      }
      if (item.role !== undefined && item.role !== null && typeof item.role !== 'string') {
        throw new TypeError('participant.role 必须是字符串或 null');
      }
      if (seen.has(item.agentId)) {
        throw new DiscussionError(`participants 中 agentId 重复: ${item.agentId}`, DISCUSSION_CODE.PARTICIPANTS_INVALID);
      }
      seen.add(item.agentId);
      normalized.push(Object.freeze({ agentId: item.agentId, role: item.role ?? null }));
    }
    if (!Array.isArray(agenda)) throw new TypeError('agenda 必须是数组');

    const at = this.#nowIso();
    const discussion = Object.freeze({
      id: `dis-${randomUUID()}`,
      mode,
      topic,
      ownerUserId,
      participants: Object.freeze(normalized),
      agenda: Object.freeze([...agenda]),
      state: DISCUSSION_STATE.OPEN,
      round: 1,
      speeches: Object.freeze([]),
      conclusion: null,
      findings: Object.freeze([]),
      dissent: Object.freeze([]),
      synthesizedBy: null,
      openedAt: at,
      concludedAt: null,
      escalatedAt: null,
      escalationReason: null,
      abortedAt: null,
      abortReason: null,
    });
    this.#discussions.set(discussion.id, discussion);
    this.#order.push(discussion.id);
    return discussion;
  }

  /**
   * 发言（每人每轮最多 maxTurnsPerParticipantPerRound 次，默认 1）。
   * round 显式指定时必须位于 1..maxRounds；指向已关闭轮次或未开启轮次都会被拒绝
   * （已关闭轮次里重复发言优先判定为 TURN_LIMIT，避免轮次被"补写"）。
   * 返回迁移后的冻结讨论对象。
   */
  speak({ discussionId, agentId, stance, content, evidence = [], round = null } = {}) {
    const discussion = this.#require(discussionId);
    this.#assertNotTerminal(discussion);
    if (!isNonEmptyString(agentId)) throw new TypeError('agentId 必须是非空字符串');
    if (!this.#isParticipant(discussion, agentId)) {
      throw new DiscussionError(`Agent 不是本讨论的参与者: ${agentId}`, DISCUSSION_CODE.NOT_PARTICIPANT);
    }
    if (!STANCES.has(stance)) {
      throw new TypeError(`stance 必须是 ${Object.values(STANCE).join(' / ')} 之一`);
    }
    if (!isNonEmptyString(content)) throw new TypeError('content 必须是非空字符串');
    if (!Array.isArray(evidence)) throw new TypeError('evidence 必须是数组');

    let targetRound = discussion.round;
    if (round !== null) {
      if (!Number.isInteger(round) || round < 1) throw new TypeError('round 必须是正整数或 null');
      if (round > this.#maxRounds) {
        throw new DiscussionError(
          `指定轮次 ${round} 超过硬上限 maxRounds=${this.#maxRounds}`,
          DISCUSSION_CODE.ROUND_LIMIT
        );
      }
      targetRound = round;
    }

    const spoken = discussion.speeches.filter(
      (speech) => speech.round === targetRound && speech.agentId === agentId
    ).length;
    if (spoken >= this.#maxTurnsPerParticipantPerRound) {
      throw new DiscussionError(
        `Agent ${agentId} 在第 ${targetRound} 轮已发言 ${spoken} 次，超过上限 ${this.#maxTurnsPerParticipantPerRound}`,
        DISCUSSION_CODE.TURN_LIMIT
      );
    }
    if (targetRound !== discussion.round) {
      throw new DiscussionError(
        targetRound < discussion.round
          ? `第 ${targetRound} 轮已关闭，不可补写发言`
          : `第 ${targetRound} 轮尚未开启（当前第 ${discussion.round} 轮）`,
        DISCUSSION_CODE.ILLEGAL_STATE
      );
    }

    const at = this.#nowIso();
    const speech = Object.freeze({
      id: `sp-${randomUUID()}`,
      discussionId: discussion.id,
      round: targetRound,
      agentId,
      stance,
      content,
      evidence: freezeVerbatim(evidence),
      at,
    });
    const next = Object.freeze({
      ...discussion,
      state: DISCUSSION_STATE.IN_ROUND,
      speeches: Object.freeze([...discussion.speeches, speech]),
    });
    this.#discussions.set(next.id, next);
    return next;
  }

  /**
   * 开新一轮。当前轮已到 maxRounds 时**不抛异常**，而是置 ESCALATED（交给人工/上级 Agent），
   * 已发生的发言全部保留（minutes 可完整回放）。
   */
  nextRound({ discussionId } = {}) {
    const discussion = this.#require(discussionId);
    this.#assertNotTerminal(discussion);
    if (discussion.round >= this.#maxRounds) {
      const next = Object.freeze({
        ...discussion,
        state: DISCUSSION_STATE.ESCALATED,
        escalatedAt: this.#nowIso(),
        escalationReason: 'ROUND_LIMIT',
      });
      this.#discussions.set(next.id, next);
      return next;
    }
    const next = Object.freeze({
      ...discussion,
      round: discussion.round + 1,
      state: DISCUSSION_STATE.IN_ROUND,
    });
    this.#discussions.set(next.id, next);
    return next;
  }

  /**
   * 综合/评审落槌：状态迁移 OPEN|IN_ROUND -> (SYNTHESIZING 内部过渡) -> CONCLUDED。
   * - DISCUSS 必须有 conclusion，否则 CONCLUSION_REQUIRED；
   * - AUDIT / REVIEW 必须至少 1 条 findings，否则 FINDINGS_REQUIRED；
   * - dissent 原样写入纪要（数量、顺序、内容都不许被"综合"）。
   */
  synthesize({ discussionId, byAgentId, conclusion = null, findings = [], dissent = [] } = {}) {
    const discussion = this.#require(discussionId);
    this.#assertNotTerminal(discussion);
    if (!isNonEmptyString(byAgentId)) throw new TypeError('byAgentId 必须是非空字符串');
    if (!this.#isParticipant(discussion, byAgentId)) {
      throw new DiscussionError(`Agent 不是本讨论的参与者: ${byAgentId}`, DISCUSSION_CODE.NOT_PARTICIPANT);
    }
    if (!Array.isArray(findings)) throw new TypeError('findings 必须是数组');
    if (!Array.isArray(dissent)) throw new TypeError('dissent 必须是数组');
    if (conclusion !== null && typeof conclusion !== 'string') {
      throw new TypeError('conclusion 必须是字符串或 null');
    }
    if (discussion.mode === DISCUSSION_MODE.DISCUSS && !isNonEmptyString(conclusion)) {
      throw new DiscussionError('DISCUSS 模式必须给出 conclusion 才能形成结论', DISCUSSION_CODE.CONCLUSION_REQUIRED);
    }
    if (
      (discussion.mode === DISCUSSION_MODE.AUDIT || discussion.mode === DISCUSSION_MODE.REVIEW) &&
      findings.length === 0
    ) {
      throw new DiscussionError(
        `${discussion.mode} 模式必须至少有一条 findings 才能形成结论`,
        DISCUSSION_CODE.FINDINGS_REQUIRED
      );
    }

    const next = Object.freeze({
      ...discussion,
      // SYNTHESIZING 是本次调用内部的过渡态（OPEN/IN_ROUND -> SYNTHESIZING -> CONCLUDED），
      // 对外只暴露最终 CONCLUDED；枚举完整保留供未来"异步综合/二审"扩展。
      state: DISCUSSION_STATE.CONCLUDED,
      conclusion: isNonEmptyString(conclusion) ? conclusion : null,
      findings: freezeVerbatim(findings),
      dissent: freezeVerbatim(dissent),
      synthesizedBy: byAgentId,
      concludedAt: this.#nowIso(),
    });
    this.#discussions.set(next.id, next);
    return next;
  }

  /** 人工/上级 Agent 主动升级（终态再调用 → ILLEGAL_STATE）。 */
  escalate({ discussionId, reason = null } = {}) {
    const discussion = this.#require(discussionId);
    this.#assertNotTerminal(discussion);
    if (reason !== null && typeof reason !== 'string') throw new TypeError('reason 必须是字符串或 null');
    const next = Object.freeze({
      ...discussion,
      state: DISCUSSION_STATE.ESCALATED,
      escalatedAt: this.#nowIso(),
      escalationReason: isNonEmptyString(reason) ? reason : 'MANUAL',
    });
    this.#discussions.set(next.id, next);
    return next;
  }

  /** 中止讨论（终态再调用 → ILLEGAL_STATE）。 */
  abort({ discussionId, reason = null } = {}) {
    const discussion = this.#require(discussionId);
    this.#assertNotTerminal(discussion);
    if (reason !== null && typeof reason !== 'string') throw new TypeError('reason 必须是字符串或 null');
    const next = Object.freeze({
      ...discussion,
      state: DISCUSSION_STATE.ABORTED,
      abortedAt: this.#nowIso(),
      abortReason: isNonEmptyString(reason) ? reason : 'MANUAL',
    });
    this.#discussions.set(next.id, next);
    return next;
  }

  get(discussionId) {
    return this.#require(discussionId);
  }

  /** 列出讨论（按 mode/state 过滤），按打开顺序，确定性。 */
  list({ mode = null, state = null } = {}) {
    const discussions = this.#order
      .map((id) => this.#discussions.get(id))
      .filter((discussion) => discussion && (mode === null || discussion.mode === mode) && (state === null || discussion.state === state));
    return Object.freeze(discussions);
  }

  /**
   * 结构化纪要：{ topic, mode, participants, rounds:[{round, speeches}], conclusion, findings, dissent, state, openedAt, concludedAt }。
   * minutes 在任何状态下都可读（ESCALATED 时用于回放全部已发生发言，证据不丢）。
   */
  minutes(discussionId) {
    const discussion = this.#require(discussionId);
    const grouped = new Map();
    for (const speech of discussion.speeches) {
      if (!grouped.has(speech.round)) grouped.set(speech.round, []);
      grouped.get(speech.round).push(speech);
    }
    const rounds = [...grouped.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([round, speeches]) => Object.freeze({ round, speeches: Object.freeze([...speeches]) }));
    return Object.freeze({
      topic: discussion.topic,
      mode: discussion.mode,
      participants: discussion.participants,
      rounds: Object.freeze(rounds),
      conclusion: discussion.conclusion,
      findings: discussion.findings,
      dissent: discussion.dissent,
      state: discussion.state,
      openedAt: discussion.openedAt,
      concludedAt: discussion.concludedAt,
    });
  }
}
