// 模块职责：第三人称转达（Relay Summary）——AI 作为人际会话里的合法第三方节点注入摘要，绝不冒充任何一方。
// 契约来源：contracts/INTERFACES.md §27。
// 关键不变量：①notFromHuman 恒为 true、attribution 恒为 AGENT_SUMMARY（不允许生成"像是人发的"消息）；
//             ②summarizerAgentId 必须是参与方，participants ≥ 2；③provenance 必须能追溯到协商出结论的 Agent；
//             ④文本一律由本模块以第三人称重新生成，render 绝不直接透传外部提供的 text。
import { canonicalize, sha256Hex } from '../../identity/src/agent-id.mjs';

export const RELAY_KIND = Object.freeze({ THIRD_PERSON_RELAY: 'THIRD_PERSON_RELAY' });

export const RELAY_CODE = Object.freeze({
  RELAY_INVALID: 'RELAY_INVALID',
  IMPERSONATION_BLOCKED: 'IMPERSONATION_BLOCKED',
  PARTICIPANTS_REQUIRED: 'PARTICIPANTS_REQUIRED',
  NOT_PARTICIPANT: 'NOT_PARTICIPANT',
  SUMMARY_TOO_LONG: 'SUMMARY_TOO_LONG'
});

const CODE = RELAY_CODE;
const ATTRIBUTION = 'AGENT_SUMMARY';
const DEFAULT_MAX_LENGTH = 600;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class RelaySummaryError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'RelaySummaryError';
    this.code = code;
  }
}

function normalizeParticipants(list) {
  if (!Array.isArray(list)) {
    throw new RelaySummaryError('participants 必须是数组', CODE.PARTICIPANTS_REQUIRED);
  }
  const seen = new Set();
  const out = [];
  for (const item of list) {
    let participant;
    if (isNonEmptyString(item)) {
      participant = { agentId: item, displayName: null, userId: null };
    } else if (item && typeof item === 'object' && isNonEmptyString(item.agentId)) {
      participant = {
        agentId: item.agentId,
        displayName: isNonEmptyString(item.displayName) ? item.displayName : null,
        userId: isNonEmptyString(item.userId) ? item.userId : null
      };
    } else {
      throw new RelaySummaryError('participant 结构非法（需要 agentId）', CODE.RELAY_INVALID);
    }
    if (seen.has(participant.agentId)) {
      throw new RelaySummaryError(`participant 重复: ${participant.agentId}`, CODE.RELAY_INVALID);
    }
    seen.add(participant.agentId);
    out.push(Object.freeze(participant));
  }
  if (out.length < 2) {
    throw new RelaySummaryError(`participants 至少需要 2 方，收到 ${out.length}`, CODE.PARTICIPANTS_REQUIRED);
  }
  return Object.freeze(out);
}

function normalizeItems(list, fieldName, { allowString = true } = {}) {
  if (!Array.isArray(list)) {
    throw new RelaySummaryError(`${fieldName} 必须是数组`, CODE.RELAY_INVALID);
  }
  return Object.freeze(
    list.map((item) => {
      if (allowString && isNonEmptyString(item)) return Object.freeze({ value: item });
      if (item && typeof item === 'object') return Object.freeze({ ...item });
      throw new RelaySummaryError(`${fieldName} 元素结构非法`, CODE.RELAY_INVALID);
    })
  );
}

export class RelaySummaryBuilder {
  #clock;
  #maxLength;

  constructor({ clock = () => new Date(), maxLength = DEFAULT_MAX_LENGTH } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    if (!Number.isInteger(maxLength) || maxLength <= 0) throw new TypeError('maxLength 必须是正整数');
    this.#maxLength = maxLength;
  }

  #nowIso() {
    const sampled = this.#clock();
    return (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
  }

  /** 第三人称文本（可复用于 build 与 render；render 永远重新生成，不透传外部 text） */
  #compose({ participants, topic, conclusion, pendingActions, approvals }) {
    const names = participants.map((participant) => participant.displayName ?? participant.agentId);
    const who = names.length === 2 ? `${names[0]}与${names[1]}` : names.join('、');
    const parts = [`转达：${who} 的 Agent 已就「${topic}」达成一致：${conclusion}。`];
    if (pendingActions.length > 0) parts.push(`待办事项 ${pendingActions.length} 项。`);
    if (approvals.length > 0) parts.push(`待确认 ${approvals.length} 项。`);
    parts.push('本消息由 Agent 以第三人称转达，不代表任何一方人类发言。');
    return parts.join('');
  }

  /**
   * 构建冻结 relay。绝不接受 human userId 作为 from：from 只能是参与方 Agent。
   * @returns {Readonly<object>}
   */
  build({
    conversationId,
    participants,
    summarizerAgentId,
    topic,
    conclusion,
    pendingActions = [],
    approvals = [],
    provenance = []
  } = {}) {
    if (!isNonEmptyString(conversationId)) {
      throw new RelaySummaryError('conversationId 必填', CODE.RELAY_INVALID);
    }
    const normalizedParticipants = normalizeParticipants(participants);
    if (!isNonEmptyString(summarizerAgentId)) {
      throw new RelaySummaryError('summarizerAgentId 必填', CODE.RELAY_INVALID);
    }
    if (!normalizedParticipants.some((participant) => participant.agentId === summarizerAgentId)) {
      throw new RelaySummaryError(
        `summarizerAgentId 必须是参与方: ${summarizerAgentId}`,
        CODE.NOT_PARTICIPANT
      );
    }
    if (!isNonEmptyString(topic) || !isNonEmptyString(conclusion)) {
      throw new RelaySummaryError('topic/conclusion 必须是非空字符串', CODE.RELAY_INVALID);
    }

    const normalizedActions = normalizeItems(pendingActions, 'pendingActions');
    const normalizedApprovals = normalizeItems(approvals, 'approvals');
    // provenance 默认可追溯到全部参与方；显式传入时逐条归一
    const normalizedProvenance =
      Array.isArray(provenance) && provenance.length > 0
        ? normalizeItems(provenance, 'provenance')
        : Object.freeze(
            normalizedParticipants.map((participant) =>
              Object.freeze({ agentId: participant.agentId, role: 'participant' })
            )
          );
    if (normalizedProvenance.length < 2) {
      throw new RelaySummaryError('provenance 必须可追溯至少 2 个协商 Agent', CODE.RELAY_INVALID);
    }

    const text = this.#compose({
      participants: normalizedParticipants,
      topic,
      conclusion,
      pendingActions: normalizedActions,
      approvals: normalizedApprovals
    });
    if (text.length > this.#maxLength) {
      throw new RelaySummaryError(
        `第三人称摘要超长: ${text.length} > maxLength ${this.#maxLength}（摘要不是聊天记录）`,
        CODE.SUMMARY_TOO_LONG
      );
    }

    const at = this.#nowIso();
    const hash = sha256Hex(
      canonicalize({
        kind: RELAY_KIND.THIRD_PERSON_RELAY,
        attribution: ATTRIBUTION,
        notFromHuman: true,
        from: summarizerAgentId,
        conversationId,
        participants: normalizedParticipants,
        topic,
        conclusion,
        pendingActions: normalizedActions,
        approvals: normalizedApprovals,
        provenance: normalizedProvenance,
        text,
        at
      })
    );

    return Object.freeze({
      kind: RELAY_KIND.THIRD_PERSON_RELAY,
      attribution: ATTRIBUTION,
      notFromHuman: true,
      from: summarizerAgentId,
      conversationId,
      participants: normalizedParticipants,
      topic,
      conclusion,
      pendingActions: normalizedActions,
      approvals: normalizedApprovals,
      provenance: normalizedProvenance,
      text,
      hash,
      at
    });
  }

  /**
   * 红线：from 落在人类集合（或伪造了 notFromHuman/attribution）=> IMPERSONATION_BLOCKED。
   * @returns {Readonly<object>} 校验通过的 relay
   */
  assertNotImpersonation({ relay, humanUserIds = [] } = {}) {
    if (!relay || typeof relay !== 'object') {
      throw new RelaySummaryError('relay 必须是对象', CODE.RELAY_INVALID);
    }
    const humans = Array.isArray(humanUserIds) ? humanUserIds.filter(isNonEmptyString) : [];
    if (relay.notFromHuman !== true || relay.attribution !== ATTRIBUTION) {
      throw new RelaySummaryError('relay 携带了人类作者属性（notFromHuman/attribution 被篡改）', CODE.IMPERSONATION_BLOCKED);
    }
    if (!isNonEmptyString(relay.from)) {
      throw new RelaySummaryError('relay.from 缺失', CODE.RELAY_INVALID);
    }
    if (relay.kind !== RELAY_KIND.THIRD_PERSON_RELAY) {
      throw new RelaySummaryError(`relay.kind 非法: ${String(relay.kind)}`, CODE.RELAY_INVALID);
    }
    if (humans.includes(relay.from)) {
      throw new RelaySummaryError(
        `禁止冒充人类发言：from=${relay.from} 是 human userId（第三人称转达只能由 Agent 发出）`,
        CODE.IMPERSONATION_BLOCKED
      );
    }
    return relay;
  }

  /**
   * 渲染第三人称文本。始终由本模块重新生成，不读取外部传入的 relay.text（防注入第一人称文本）。
   * @returns {string}
   */
  render({ relay } = {}) {
    if (!relay || typeof relay !== 'object' || relay.kind !== RELAY_KIND.THIRD_PERSON_RELAY) {
      throw new RelaySummaryError('render: relay 必须是 THIRD_PERSON_RELAY', CODE.RELAY_INVALID);
    }
    const participants = normalizeParticipants(relay.participants);
    const pendingActions = Array.isArray(relay.pendingActions) ? relay.pendingActions : [];
    const approvals = Array.isArray(relay.approvals) ? relay.approvals : [];
    if (!isNonEmptyString(relay.topic) || !isNonEmptyString(relay.conclusion)) {
      throw new RelaySummaryError('render: topic/conclusion 缺失', CODE.RELAY_INVALID);
    }
    return this.#compose({
      participants,
      topic: relay.topic,
      conclusion: relay.conclusion,
      pendingActions,
      approvals
    });
  }
}
