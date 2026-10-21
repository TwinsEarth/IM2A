// 模块职责：v2 频道模型——人类与 Agent 共享同一条消息内核，靠「频道类型」隔离未读与投递（替代 Agent 协商独立物理通道）/ 契约来源：contracts/INTERFACES.md §14 / 关键不变量：AGENT_NEGOTIATION 对人类成员未读恒为 0 且人类不可 post；CMD stored=false、不计未读、只投递已订阅 Agent 成员；STRANGER_QUEUE 入队不产生对方未读；只有频道成员参与投递。
import { randomUUID } from 'node:crypto';

export const CHANNEL_TYPE = Object.freeze({
  DIRECT: 'DIRECT',
  GROUP: 'GROUP',
  TASK: 'TASK',
  AGENT_NEGOTIATION: 'AGENT_NEGOTIATION',
  STRANGER_QUEUE: 'STRANGER_QUEUE',
});

export const MEMBER_KIND = Object.freeze({ HUMAN: 'human', AGENT: 'agent' });

export const MESSAGE_KIND = Object.freeze({
  TEXT: 'TEXT',
  CMD: 'CMD',
  AGENT_TURN: 'AGENT_TURN',
  APPROVAL_CARD: 'APPROVAL_CARD',
  INTENT: 'INTENT',
});

export const CHANNEL_CODE = Object.freeze({
  CHANNEL_EXISTS: 'CHANNEL_EXISTS',
  CHANNEL_UNKNOWN: 'CHANNEL_UNKNOWN',
  TYPE_INVALID: 'TYPE_INVALID',
  MEMBER_EXISTS: 'MEMBER_EXISTS',
  NOT_MEMBER: 'NOT_MEMBER',
  POST_DENIED: 'POST_DENIED',
  AGENT_NOT_SUBSCRIBED: 'AGENT_NOT_SUBSCRIBED',
  CMD_NOT_PERSISTED: 'CMD_NOT_PERSISTED',
});

const CHANNEL_TYPES = new Set(Object.values(CHANNEL_TYPE));
const MEMBER_KINDS = new Set(Object.values(MEMBER_KIND));
const MESSAGE_KINDS = new Set(Object.values(MESSAGE_KIND));
const TRUST_LEVEL_MIN = 0;
const TRUST_LEVEL_MAX = 4;

export class ChannelError extends Error {
  constructor(message, code = 'CHANNEL_ERROR') {
    super(message);
    this.name = 'ChannelError';
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

export class ChannelRegistry {
  #clock;
  /** @type {Map<string, { channelId, type, ownerUserId, title, createdAt, members: Map, seq: number, messages: object[] }>} */
  #channels = new Map();
  /** @type {Map<string, Map<string, number>>} memberId -> (channelId -> 未读数)（仅人类成员会累加） */
  #unread = new Map();

  constructor({ clock = () => new Date() } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    this.#clock = clock;
  }

  /** 创建频道（owner 不会自动加入，需显式 join——成员关系与归属分离便于 Agent 代管）。 */
  create({ channelId, type, ownerUserId, title = null } = {}) {
    if (!isNonEmptyString(channelId)) throw new TypeError('channelId 必须是非空字符串');
    if (!isNonEmptyString(ownerUserId)) throw new TypeError('ownerUserId 必须是非空字符串');
    if (!CHANNEL_TYPES.has(type)) {
      throw new ChannelError(
        `非法频道类型 type=${String(type)}（允许值 ${Object.values(CHANNEL_TYPE).join(' / ')}）`,
        CHANNEL_CODE.TYPE_INVALID
      );
    }
    if (title !== null && typeof title !== 'string') throw new TypeError('title 必须是字符串或 null');
    if (this.#channels.has(channelId)) {
      throw new ChannelError(`频道已存在: ${channelId}`, CHANNEL_CODE.CHANNEL_EXISTS);
    }
    const record = {
      channelId,
      type,
      ownerUserId,
      title,
      createdAt: toIso(this.#clock),
      members: new Map(),
      seq: 0,
      messages: [],
    };
    this.#channels.set(channelId, record);
    return this.#channelView(record);
  }

  /** 加入频道。Agent 成员的订阅状态初始为 false，由 AgentSessionRegistry.subscribe 传播置位。 */
  join({ channelId, memberId, kind, agentId = null, principalUserId = null, trustLevel = 4 } = {}) {
    const record = this.#requireChannel(channelId);
    if (!isNonEmptyString(memberId)) throw new TypeError('memberId 必须是非空字符串');
    if (!MEMBER_KINDS.has(kind)) {
      throw new TypeError(`kind 必须是 ${Object.values(MEMBER_KIND).join(' / ')} 之一`);
    }
    if (!Number.isInteger(trustLevel) || trustLevel < TRUST_LEVEL_MIN || trustLevel > TRUST_LEVEL_MAX) {
      throw new TypeError(`trustLevel 必须是 ${TRUST_LEVEL_MIN}..${TRUST_LEVEL_MAX} 的整数`);
    }
    if (agentId !== null && typeof agentId !== 'string') throw new TypeError('agentId 必须是字符串或 null');
    if (principalUserId !== null && typeof principalUserId !== 'string') {
      throw new TypeError('principalUserId 必须是字符串或 null');
    }
    if (record.members.has(memberId)) {
      throw new ChannelError(`成员已存在: ${memberId} @ ${channelId}`, CHANNEL_CODE.MEMBER_EXISTS);
    }
    const member = {
      memberId,
      kind,
      agentId: kind === MEMBER_KIND.AGENT ? (agentId ?? memberId) : (agentId ?? null),
      principalUserId,
      trustLevel,
      subscribed: false,
      joinedAt: toIso(this.#clock),
    };
    record.members.set(memberId, member);
    return this.#memberView(record.channelId, member);
  }

  /** 离开频道；非成员返回 false（不存在该成员不视为错误）。 */
  leave({ channelId, memberId } = {}) {
    const record = this.#requireChannel(channelId);
    if (!isNonEmptyString(memberId)) throw new TypeError('memberId 必须是非空字符串');
    if (!record.members.has(memberId)) return false;
    record.members.delete(memberId);
    const counts = this.#unread.get(memberId);
    if (counts) {
      counts.delete(channelId);
      if (counts.size === 0) this.#unread.delete(memberId);
    }
    return true;
  }

  /** 频道视图；不存在返回 undefined（契约 §14 明确 get 返回 channel | undefined）。 */
  get(channelId) {
    const record = this.#channels.get(channelId);
    return record ? this.#channelView(record) : undefined;
  }

  /** 成员列表（新数组，元素冻结）；频道不存在 → CHANNEL_UNKNOWN。 */
  members(channelId) {
    const record = this.#requireChannel(channelId);
    return [...record.members.values()].map((member) => this.#memberView(record.channelId, member));
  }

  /** 该成员加入的所有频道 id（新数组）。 */
  channelsOf(memberId) {
    if (!isNonEmptyString(memberId)) throw new TypeError('memberId 必须是非空字符串');
    const result = [];
    for (const record of this.#channels.values()) {
      if (record.members.has(memberId)) result.push(record.channelId);
    }
    return result;
  }

  /**
   * 投递前权限检查（不合规抛 ChannelError）：
   * - 非成员 → NOT_MEMBER
   * - AGENT_NEGOTIATION 频道由人类 POST → POST_DENIED（协商只能由 Agent 发起）
   * - 未订阅的 Agent 成员发 CMD → AGENT_NOT_SUBSCRIBED（CMD 是唤醒/指令，须在订阅链路上）
   */
  assertCanPost({ channelId, memberId, kind } = {}) {
    const record = this.#requireChannel(channelId);
    if (!isNonEmptyString(memberId)) throw new TypeError('memberId 必须是非空字符串');
    if (!MESSAGE_KINDS.has(kind)) {
      throw new TypeError(`kind 必须是 ${Object.values(MESSAGE_KIND).join(' / ')} 之一`);
    }
    const sender = record.members.get(memberId);
    if (!sender) {
      throw new ChannelError(`非频道成员不可 POST: ${memberId} @ ${channelId}`, CHANNEL_CODE.NOT_MEMBER);
    }
    if (record.type === CHANNEL_TYPE.AGENT_NEGOTIATION && sender.kind === MEMBER_KIND.HUMAN) {
      throw new ChannelError(
        `AGENT_NEGOTIATION 频道禁止人类成员 POST（协商由 Agent 发起，人类只在巡视态读取）: ${memberId}`,
        CHANNEL_CODE.POST_DENIED
      );
    }
    if (kind === MESSAGE_KIND.CMD && sender.kind === MEMBER_KIND.AGENT && !sender.subscribed) {
      throw new ChannelError(
        `Agent 成员未订阅频道，不可发送 CMD: ${memberId} @ ${channelId}`,
        CHANNEL_CODE.AGENT_NOT_SUBSCRIBED
      );
    }
    return true;
  }

  /**
   * 投递一条消息。
   * - CMD：stored=false、不计未读、只投递已订阅的 Agent 成员；
   * - AGENT_NEGOTIATION：不投递给人类成员（未读恒 0），只投递已订阅 Agent；
   * - STRANGER_QUEUE：入队，不投递人类（不打扰对方，不产生未读），已订阅 Agent 可取队列；
   * - DIRECT / GROUP / TASK：投递给除发送者外的全体人类成员；Agent 成员须已订阅才投递。
   * unreadDelta = 本次被 +1 未读的人类成员数（AGC 与队列消息恒为 0）。
   */
  post({ channelId, memberId, kind, payload = {}, seq = null, persist = null } = {}) {
    const record = this.#requireChannel(channelId);
    if (!isNonEmptyString(memberId)) throw new TypeError('memberId 必须是非空字符串');
    if (!MESSAGE_KINDS.has(kind)) {
      throw new TypeError(`kind 必须是 ${Object.values(MESSAGE_KIND).join(' / ')} 之一`);
    }
    if (!isPlainObject(payload)) throw new TypeError('payload 必须是普通对象');
    if (seq !== null && (!Number.isInteger(seq) || seq <= 0)) throw new TypeError('seq 必须是正整数或 null');
    if (persist !== null && typeof persist !== 'boolean') throw new TypeError('persist 必须是布尔值或 null');
    this.assertCanPost({ channelId, memberId, kind });

    const sender = record.members.get(memberId);
    const stored = persist === null ? kind !== MESSAGE_KIND.CMD : persist;
    if (kind === MESSAGE_KIND.CMD && stored) {
      // 契约 §14：CMD 类消息不下发不入库；显式要求持久化是调用方错误
      throw new ChannelError('CMD 指令消息不得入库（stored 恒为 false）', CHANNEL_CODE.CMD_NOT_PERSISTED);
    }

    let resolvedSeq;
    if (seq === null) {
      record.seq += 1;
      resolvedSeq = record.seq;
    } else {
      resolvedSeq = seq;
      if (seq > record.seq) record.seq = seq;
    }
    const message = Object.freeze({
      id: `msg-${randomUUID()}`,
      channelId: record.channelId,
      channelType: record.type,
      seq: resolvedSeq,
      senderId: memberId,
      senderKind: sender.kind,
      kind,
      payload: Object.freeze({ ...payload }),
      stored,
      at: toIso(this.#clock),
    });

    const deliveredTo = this.#computeDeliveredTo(record, memberId, kind);
    if (stored) record.messages.push(message);

    let unreadDelta = 0;
    for (const recipientId of deliveredTo) {
      const recipient = record.members.get(recipientId);
      if (!recipient || recipient.kind !== MEMBER_KIND.HUMAN) continue;
      this.#bumpUnread(recipientId, record.channelId);
      unreadDelta += 1;
    }

    return Object.freeze({
      stored,
      message,
      deliveredTo: Object.freeze([...deliveredTo]),
      unreadDelta,
    });
  }

  /** 未读聚合（人类视角）：{ total, byChannel }，byChannel 只包含未读 > 0 的频道。 */
  unreadFor({ memberId } = {}) {
    if (!isNonEmptyString(memberId)) throw new TypeError('memberId 必须是非空字符串');
    const counts = this.#unread.get(memberId);
    const byChannel = {};
    let total = 0;
    if (counts) {
      for (const [channelId, count] of counts) {
        if (count <= 0) continue;
        byChannel[channelId] = count;
        total += count;
      }
    }
    return Object.freeze({ total, byChannel: Object.freeze(byChannel) });
  }

  /** 已读清零，返回被清零的未读数（0 表示本来就没有未读）。 */
  markRead({ memberId, channelId } = {}) {
    const record = this.#requireChannel(channelId);
    if (!isNonEmptyString(memberId)) throw new TypeError('memberId 必须是非空字符串');
    if (!record.members.has(memberId)) {
      throw new ChannelError(`非频道成员不可 markRead: ${memberId} @ ${channelId}`, CHANNEL_CODE.NOT_MEMBER);
    }
    const counts = this.#unread.get(memberId);
    if (!counts) return 0;
    const cleared = counts.get(channelId) ?? 0;
    counts.delete(channelId);
    if (counts.size === 0) this.#unread.delete(memberId);
    return cleared;
  }

  /**
   * 设置成员的订阅状态（供 §15 AgentSessionRegistry 传播；也可直接调用）。
   * 频道/成员不存在返回 false，不抛错（跨层传播是尽力而为，不因未 join 而打断会话建立）。
   */
  setSubscription({ channelId, memberId, subscribed = true } = {}) {
    const record = this.#channels.get(channelId);
    if (!record) return false;
    const member = record.members.get(memberId);
    if (!member) return false;
    member.subscribed = subscribed === true;
    return true;
  }

  isSubscribed({ channelId, memberId } = {}) {
    const record = this.#channels.get(channelId);
    if (!record) return false;
    const member = record.members.get(memberId);
    return member ? member.subscribed === true : false;
  }

  /**
   * 已入库消息（实现层补充查询，供 demo/测试验证 CMD `stored=false` 不入库；
   * 契约 §14 未列消息历史 API，故作为加法提供）。
   */
  messages({ channelId, limit = null } = {}) {
    const record = this.#requireChannel(channelId);
    if (limit !== null && (!Number.isInteger(limit) || limit <= 0)) {
      throw new TypeError('limit 必须是正整数或 null');
    }
    const all = record.messages;
    return limit === null ? [...all] : all.slice(-limit);
  }

  #computeDeliveredTo(record, senderId, kind) {
    const result = [];
    for (const member of record.members.values()) {
      if (member.memberId === senderId) continue;
      if (member.kind === MEMBER_KIND.HUMAN) {
        // CMD 不下发人类；协商中间态不打扰人；陌生人队列入队待审批，不打扰对方
        if (kind === MESSAGE_KIND.CMD) continue;
        if (record.type === CHANNEL_TYPE.AGENT_NEGOTIATION) continue;
        if (record.type === CHANNEL_TYPE.STRANGER_QUEUE) continue;
        result.push(member.memberId);
      } else if (member.subscribed) {
        // Agent 成员必须已订阅才出现在 deliveredTo（未订阅 → 跳过，不报错）
        result.push(member.memberId);
      }
    }
    return result;
  }

  #bumpUnread(memberId, channelId) {
    let counts = this.#unread.get(memberId);
    if (!counts) {
      counts = new Map();
      this.#unread.set(memberId, counts);
    }
    counts.set(channelId, (counts.get(channelId) ?? 0) + 1);
  }

  #requireChannel(channelId) {
    const record = this.#channels.get(channelId);
    if (!record) {
      throw new ChannelError(`频道不存在: ${String(channelId)}`, CHANNEL_CODE.CHANNEL_UNKNOWN);
    }
    return record;
  }

  #channelView(record) {
    return Object.freeze({
      channelId: record.channelId,
      type: record.type,
      ownerUserId: record.ownerUserId,
      title: record.title,
      createdAt: record.createdAt,
      lastSeq: record.seq,
      memberCount: record.members.size,
    });
  }

  #memberView(channelId, member) {
    return Object.freeze({ channelId, ...member });
  }
}
