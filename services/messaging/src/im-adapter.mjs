// 模块职责：IM 渠道适配层——业务只面向统一 MessageEvent 编程，不直接依赖任何平台 SDK；PaaS（腾讯云 IM）与自托管内核靠适配器/能力声明可切换，平台能力缺口显式暴露（缺失即拒绝，不静默降级）/ 契约来源：contracts/INTERFACES.md §31 / 关键不变量：①dispatch 先 verifyEvent 再归一化，非法 → EVENT_INVALID；②send 抛错 → PLATFORM_ERROR（不吞）；③能力缺失必须显式 CAPABILITY_MISSING，例如 PaaS 未声明 UNREAD_SHAPING 时不得假装支持"协商频道人类未读恒 0"。
export const ADAPTER_CODE = Object.freeze({
  ADAPTER_UNKNOWN: 'ADAPTER_UNKNOWN',
  ADAPTER_EXISTS: 'ADAPTER_EXISTS',
  CAPABILITY_MISSING: 'CAPABILITY_MISSING',
  EVENT_INVALID: 'EVENT_INVALID',
  PLATFORM_ERROR: 'PLATFORM_ERROR',
});

export const ADAPTER_CAPABILITY = Object.freeze({
  UNREAD_SHAPING: 'UNREAD_SHAPING', // 能否按频道类型整形未读（AGENT_NEGOTIATION 恒 0 依赖它）
  CUSTOM_MESSAGE: 'CUSTOM_MESSAGE', // 自定义消息类型（Agent 轮次/CMD）
  AGENT_MEMBERSHIP: 'AGENT_MEMBERSHIP', // Agent 能否作为合法成员参与投递
  AUDIT_CHAIN: 'AUDIT_CHAIN', // 能否把平台事件纳入自有审计链
  STREAMING: 'STREAMING', // 流式回复
  CARD_CALLBACK: 'CARD_CALLBACK', // 卡片点击回调（审批输入源）
});

/** 参考适配器使用的平台标识（契约未冻结 platform 字符串，此处作为本实现的约定） */
export const IM_PLATFORM = Object.freeze({
  TENCENT_IM_PAAS: 'tencent-im-paas',
  SELF_HOSTED_KERNEL: 'self-hosted-kernel',
});

/** 统一 MessageEvent 的枚举（契约 §31 冻结字段） */
const SENDER_KINDS = new Set(['human', 'agent']);
const MESSAGE_KINDS = new Set(['TEXT', 'CARD', 'AGENT_TURN', 'CMD']);
const CAPABILITY_VALUES = new Set(Object.values(ADAPTER_CAPABILITY));

const CODE = ADAPTER_CODE;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isPlainObject = (value) => typeof value === 'object' && value !== null && !Array.isArray(value);

export class ImAdapterError extends Error {
  constructor(message, code = CODE.EVENT_INVALID) {
    super(message);
    this.name = 'ImAdapterError';
    this.code = code;
  }
}

// ── 统一 MessageEvent 归一化 ────────────────────────────────────────────────

function parseCustomData(value) {
  if (value === undefined || value === null) return {};
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value);
      if (!isPlainObject(parsed)) throw new Error('not object');
      return parsed;
    } catch {
      throw new ImAdapterError('CloudCustomData 不是合法 JSON 对象', CODE.EVENT_INVALID);
    }
  }
  if (isPlainObject(value)) return value;
  throw new ImAdapterError('CloudCustomData 必须是对象或 JSON 字符串', CODE.EVENT_INVALID);
}

function msToIso(ms) {
  try {
    return new Date(ms).toISOString();
  } catch {
    return null;
  }
}

/**
 * 腾讯云 IM PaaS 原始事件 → 规范化中间形态。
 * 约定 raw：{ MsgID, From_Account, GroupID|C2CId|channelId, MsgTime(秒), MsgBody:[{MsgContent:{Text}}],
 *             CloudCustomData(JSON 字符串/对象: { senderKind, kind, card, replyTo, metadata, trustZone }) }
 */
function fromTencentPaas(raw) {
  const custom = parseCustomData(raw.CloudCustomData);
  const firstBody = Array.isArray(raw.MsgBody) && raw.MsgBody.length > 0 ? raw.MsgBody[0] : null;
  const bodyText = firstBody && isPlainObject(firstBody.MsgContent) ? firstBody.MsgContent.Text : null;
  let at = raw.at ?? null;
  if (at === null && raw.MsgTime !== undefined) {
    const ms = typeof raw.MsgTime === 'number' && raw.MsgTime < 1e12 ? raw.MsgTime * 1000 : raw.MsgTime;
    at = Number.isFinite(ms) ? msToIso(ms) : null;
  }
  return {
    eventId: raw.MsgID ?? raw.eventId ?? raw.id ?? null,
    channelId: raw.GroupID ?? raw.C2CId ?? raw.channelId ?? null,
    senderId: raw.From_Account ?? raw.senderId ?? null,
    senderKind: custom.senderKind ?? raw.senderKind ?? 'human',
    kind: custom.kind ?? raw.kind ?? (custom.card !== undefined && custom.card !== null ? 'CARD' : 'TEXT'),
    text: raw.text ?? bodyText ?? null,
    card: custom.card ?? raw.card ?? null,
    at,
    replyTo: custom.replyTo ?? raw.replyTo ?? null,
    metadata: custom.metadata ?? raw.metadata ?? {},
    trustZone: custom.trustZone ?? raw.trustZone ?? 4,
  };
}

/** 自托管内核 / 任意自定义适配器的规范 raw（字段与 MessageEvent 同构，eventId 亦可写 id） */
function fromCanonical(raw) {
  return {
    eventId: raw.eventId ?? raw.id ?? null,
    channelId: raw.channelId ?? null,
    senderId: raw.senderId ?? raw.from ?? null,
    senderKind: raw.senderKind ?? 'human',
    kind: raw.kind ?? (raw.card !== undefined && raw.card !== null ? 'CARD' : 'TEXT'),
    text: raw.text ?? null,
    card: raw.card ?? null,
    at: raw.at ?? null,
    replyTo: raw.replyTo ?? null,
    metadata: raw.metadata ?? {},
    trustZone: raw.trustZone ?? 4,
  };
}

function finalizeEvent({ platform, adapterId, normalized }) {
  if (!isNonEmptyString(normalized.eventId)) throw new ImAdapterError('事件缺少 eventId', CODE.EVENT_INVALID);
  if (!isNonEmptyString(normalized.channelId)) throw new ImAdapterError('事件缺少 channelId', CODE.EVENT_INVALID);
  if (!isNonEmptyString(normalized.senderId)) throw new ImAdapterError('事件缺少 senderId', CODE.EVENT_INVALID);
  if (!SENDER_KINDS.has(normalized.senderKind)) {
    throw new ImAdapterError(`非法 senderKind: ${String(normalized.senderKind)}`, CODE.EVENT_INVALID);
  }
  if (!MESSAGE_KINDS.has(normalized.kind)) {
    throw new ImAdapterError(`非法 kind: ${String(normalized.kind)}`, CODE.EVENT_INVALID);
  }
  if (normalized.text !== null && typeof normalized.text !== 'string') {
    throw new ImAdapterError('text 必须是字符串或 null', CODE.EVENT_INVALID);
  }
  if (normalized.card !== null && !isPlainObject(normalized.card)) {
    throw new ImAdapterError('card 必须是普通对象或 null', CODE.EVENT_INVALID);
  }
  if (typeof normalized.at !== 'string' || !Number.isFinite(Date.parse(normalized.at))) {
    throw new ImAdapterError('at 必须是可解析的 ISO-8601 时间戳', CODE.EVENT_INVALID);
  }
  if (normalized.replyTo !== null && !isNonEmptyString(normalized.replyTo)) {
    throw new ImAdapterError('replyTo 必须是字符串或 null', CODE.EVENT_INVALID);
  }
  if (!isPlainObject(normalized.metadata)) throw new ImAdapterError('metadata 必须是普通对象', CODE.EVENT_INVALID);
  if (!Number.isInteger(normalized.trustZone) || normalized.trustZone < 0 || normalized.trustZone > 4) {
    throw new ImAdapterError('trustZone 必须是 0..4 的整数', CODE.EVENT_INVALID);
  }
  return Object.freeze({
    eventId: normalized.eventId,
    platform,
    adapterId,
    channelId: normalized.channelId,
    senderId: normalized.senderId,
    senderKind: normalized.senderKind,
    kind: normalized.kind,
    text: normalized.text,
    card: normalized.card === null ? null : Object.freeze({ ...normalized.card }),
    at: new Date(Date.parse(normalized.at)).toISOString(),
    replyTo: normalized.replyTo,
    metadata: Object.freeze({ ...normalized.metadata }),
    trustZone: normalized.trustZone,
  });
}

/**
 * 把任意平台原始事件归一化为统一、冻结的 MessageEvent。
 * 业务层只消费该对象，平台差异（腾讯 IM PaaS 的 MsgBody/From_Account/MsgTime、
 * 自托管内核的规范字段）全部在这一个函数里收口。
 * @returns 冻结 MessageEvent
 */
export function normalizeEvent({ platform, adapterId, raw } = {}) {
  if (!isNonEmptyString(platform) || !isNonEmptyString(adapterId) || !isPlainObject(raw)) {
    throw new ImAdapterError('normalizeEvent 需要非空 platform/adapterId 与对象 raw', CODE.EVENT_INVALID);
  }
  const normalized = platform === IM_PLATFORM.TENCENT_IM_PAAS ? fromTencentPaas(raw) : fromCanonical(raw);
  return finalizeEvent({ platform, adapterId, normalized });
}

// ── 参考适配器（同文件提供，便于切换底座时直接引用） ──────────────────────────

/**
 * 腾讯云 IM PaaS 参考适配器。
 * ⚠️ 现实缺口（故意不声明）：UNREAD_SHAPING（PaaS 没有"按频道类型整形未读"的第一等概念，
 * 协商频道人类未读恒 0 需要业务侧自行补偿）与 CUSTOM_MESSAGE（自定义消息/Agent 轮次语义受限）。
 */
export function createTencentImPaasAdapter({ send, adapterId = 'tencent-im-paas' } = {}) {
  if (typeof send !== 'function') throw new TypeError('send 必须是函数');
  if (!isNonEmptyString(adapterId)) throw new TypeError('adapterId 必须是非空字符串');
  return Object.freeze({
    adapterId,
    platform: IM_PLATFORM.TENCENT_IM_PAAS,
    capabilities: Object.freeze([ADAPTER_CAPABILITY.AUDIT_CHAIN, ADAPTER_CAPABILITY.CARD_CALLBACK]),
    send,
    verifyEvent(raw) {
      if (!isPlainObject(raw)) return false;
      const hasMessageId = isNonEmptyString(raw.MsgID ?? raw.eventId ?? raw.id);
      const hasSender = isNonEmptyString(raw.From_Account ?? raw.senderId);
      const hasChannel = isNonEmptyString(raw.GroupID ?? raw.C2CId ?? raw.channelId);
      return hasMessageId && hasSender && hasChannel;
    },
  });
}

/** 自托管内核参考适配器：声明全部能力（频道模型/自定义消息/Agent 成员/审计链/流式/卡片回调）。 */
export function createSelfHostedKernelAdapter({ send, adapterId = 'self-hosted-kernel' } = {}) {
  if (typeof send !== 'function') throw new TypeError('send 必须是函数');
  if (!isNonEmptyString(adapterId)) throw new TypeError('adapterId 必须是非空字符串');
  return Object.freeze({
    adapterId,
    platform: IM_PLATFORM.SELF_HOSTED_KERNEL,
    capabilities: Object.freeze(Object.values(ADAPTER_CAPABILITY)),
    send,
    verifyEvent(raw) {
      if (!isPlainObject(raw)) return false;
      return (
        isNonEmptyString(raw.eventId ?? raw.id) &&
        isNonEmptyString(raw.channelId) &&
        isNonEmptyString(raw.senderId)
      );
    },
  });
}

// ── 适配器注册表 ────────────────────────────────────────────────────────────

export class ImAdapterRegistry {
  /** @type {Map<string, {adapterId, platform, capabilities, send, verifyEvent}>} */
  #adapters = new Map();

  /**
   * 注册适配器。重复 adapterId → ADAPTER_EXISTS（不做静默覆盖）。
   * @returns 冻结的适配器公开视图
   */
  register({ adapterId, platform, capabilities = [], send, verifyEvent = null } = {}) {
    if (!isNonEmptyString(adapterId)) throw new TypeError('adapterId 必须是非空字符串');
    if (!isNonEmptyString(platform)) throw new TypeError('platform 必须是非空字符串');
    if (typeof send !== 'function') throw new TypeError('send 必须是函数');
    if (verifyEvent !== null && typeof verifyEvent !== 'function') {
      throw new TypeError('verifyEvent 必须是函数或 null');
    }
    if (!Array.isArray(capabilities)) throw new TypeError('capabilities 必须是数组');
    const normalizedCapabilities = [];
    for (const capability of capabilities) {
      if (!CAPABILITY_VALUES.has(capability)) {
        throw new TypeError(`未知能力声明: ${String(capability)}`);
      }
      if (!normalizedCapabilities.includes(capability)) normalizedCapabilities.push(capability);
    }
    if (this.#adapters.has(adapterId)) {
      throw new ImAdapterError(`适配器已注册: ${adapterId}`, CODE.ADAPTER_EXISTS);
    }
    const adapter = {
      adapterId,
      platform,
      capabilities: Object.freeze(normalizedCapabilities),
      send,
      verifyEvent,
    };
    this.#adapters.set(adapterId, adapter);
    return this.#adapterView(adapter);
  }

  /** @returns 冻结能力数组（未声明能力 → 空数组语义，但未知适配器显式 ADAPTER_UNKNOWN） */
  capabilitiesOf(adapterId) {
    return this.#require(adapterId).capabilities;
  }

  /** 按平台列出适配器（同平台可并存多个；未知平台返回空数组，不抛错） */
  adaptersOf(platform) {
    if (!isNonEmptyString(platform)) throw new TypeError('platform 必须是非空字符串');
    const result = [];
    for (const adapter of this.#adapters.values()) {
      if (adapter.platform === platform) result.push(this.#adapterView(adapter));
    }
    return Object.freeze(result);
  }

  /** 能力是否已声明；未知适配器 → ADAPTER_UNKNOWN（不返回 false 假装存在） */
  hasCapability({ adapterId, capability } = {}) {
    const adapter = this.#require(adapterId);
    this.#assertCapability(capability);
    return adapter.capabilities.includes(capability);
  }

  /**
   * 强校验能力。缺失 → throw CAPABILITY_MISSING（**不允许假装支持**）。
   * @returns 冻结的 { adapterId, platform, capability }
   */
  requireCapability({ adapterId, capability } = {}) {
    const adapter = this.#require(adapterId);
    this.#assertCapability(capability);
    if (!adapter.capabilities.includes(capability)) {
      throw new ImAdapterError(
        `适配器 ${adapterId}（${adapter.platform}）未声明能力 ${capability}：不允许静默降级`,
        CODE.CAPABILITY_MISSING
      );
    }
    return Object.freeze({ adapterId, platform: adapter.platform, capability });
  }

  /**
   * 入站事件统一入口：verifyEvent（可选）→ normalizeEvent → adapter.send（平台回执/回声）→ 冻结结果。
   * send 抛错 → PLATFORM_ERROR（保留原始 message，不吞异常）。
   * @returns 冻结的 { event, platformMessageId }
   */
  dispatch({ adapterId, raw } = {}) {
    const adapter = this.#require(adapterId);
    if (adapter.verifyEvent !== null) {
      let valid;
      try {
        valid = adapter.verifyEvent(raw);
      } catch {
        throw new ImAdapterError(`适配器 ${adapterId} 的 verifyEvent 抛错，事件非法`, CODE.EVENT_INVALID);
      }
      if (!valid) throw new ImAdapterError(`适配器 ${adapterId} 拒绝该原始事件`, CODE.EVENT_INVALID);
    }
    const event = normalizeEvent({ platform: adapter.platform, adapterId, raw });
    let sent;
    try {
      sent = adapter.send(event);
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error);
      throw new ImAdapterError(`平台发送失败（${adapterId}）: ${reason}`, CODE.PLATFORM_ERROR);
    }
    const platformMessageId =
      typeof sent === 'string' && sent.length > 0
        ? sent
        : isPlainObject(sent) && isNonEmptyString(sent.platformMessageId ?? sent.messageId ?? sent.id)
          ? (sent.platformMessageId ?? sent.messageId ?? sent.id)
          : event.eventId;
    return Object.freeze({ event, platformMessageId });
  }

  // ── 内部 ────────────────────────────────────────────────────────────────

  #require(adapterId) {
    if (!isNonEmptyString(adapterId)) throw new TypeError('adapterId 必须是非空字符串');
    const adapter = this.#adapters.get(adapterId);
    if (!adapter) throw new ImAdapterError(`适配器未注册: ${adapterId}`, CODE.ADAPTER_UNKNOWN);
    return adapter;
  }

  #assertCapability(capability) {
    if (!CAPABILITY_VALUES.has(capability)) throw new TypeError(`未知能力: ${String(capability)}`);
  }

  #adapterView(adapter) {
    return Object.freeze({
      adapterId: adapter.adapterId,
      platform: adapter.platform,
      capabilities: adapter.capabilities,
    });
  }
}
