// 模块职责：v2 Agent 会话层——Agent 作为消息内核的一等客户端（独立长连接/在线状态/订阅；传输层抽象，内存与 SSE 适配同一接口）/ 契约来源：contracts/INTERFACES.md §15 / 关键不变量：同一 agent 只允许一个在线会话（重复 connect → ALREADY_ONLINE）、push 只发「在线 + 已订阅」且 send 抛错即置 OFFLINE 并计入 failed、心跳超时由注入 clock 判定（不真 sleep）。

export const SESSION_STATE = Object.freeze({
  CONNECTING: 'CONNECTING',
  ONLINE: 'ONLINE',
  OFFLINE: 'OFFLINE',
  REVOKED: 'REVOKED',
});

export const SESSION_CODE = Object.freeze({
  UNKNOWN_AGENT: 'UNKNOWN_AGENT',
  ALREADY_ONLINE: 'ALREADY_ONLINE',
  NOT_ONLINE: 'NOT_ONLINE',
  TRANSPORT_REQUIRED: 'TRANSPORT_REQUIRED',
  SEND_FAILED: 'SEND_FAILED',
});

export class SessionError extends Error {
  constructor(message, code = 'SESSION_ERROR') {
    super(message);
    this.name = 'SessionError';
    this.code = code;
  }
}

function isNonEmptyString(value) {
  return typeof value === 'string' && value.length > 0;
}

function isTransport(transport) {
  return (
    typeof transport === 'object' &&
    transport !== null &&
    typeof transport.send === 'function' &&
    typeof transport.close === 'function'
  );
}

function toIsoFromMs(ms) {
  return new Date(ms).toISOString();
}

/**
 * AgentSessionRegistry：内存实现。
 * transport 只需实现 { send(obj), close() }：
 *   - 内存适配器：send 把对象 push 进数组；
 *   - SSE 适配器：send 把对象 JSON.stringify 后写入 SSE 流，close 结束响应。
 * push 时 transport.send 收到的载荷形如 { channelId, message }（冻结对象）。
 */
export class AgentSessionRegistry {
  #clock;
  #heartbeatTimeoutMs;
  #channelRegistry;
  /** @type {Map<string, object>} agentId -> 会话记录（内部可变；对外一律返回冻结视图） */
  #sessions = new Map();

  constructor({ clock = () => new Date(), heartbeatTimeoutMs = 90_000, channelRegistry = null } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!Number.isInteger(heartbeatTimeoutMs) || heartbeatTimeoutMs <= 0) {
      throw new RangeError('heartbeatTimeoutMs 必须是正整数（毫秒）');
    }
    if (channelRegistry !== null && typeof channelRegistry !== 'object') {
      throw new TypeError('channelRegistry 必须是对象或 null');
    }
    this.#clock = clock;
    this.#heartbeatTimeoutMs = heartbeatTimeoutMs;
    this.#channelRegistry = channelRegistry;
  }

  /**
   * 建立会话。transport 缺失或不实现 { send, close } → TRANSPORT_REQUIRED；
   * 同一 agent 已在线 → ALREADY_ONLINE；OFFLINE 状态可重连。
   */
  connect({ agentId, principalUserId, transport, subscriptions = [], protocol = 'AGENT_WS' } = {}) {
    if (!isNonEmptyString(agentId)) throw new TypeError('agentId 必须是非空字符串');
    if (!isNonEmptyString(principalUserId)) throw new TypeError('principalUserId 必须是非空字符串');
    if (!isNonEmptyString(protocol)) throw new TypeError('protocol 必须是非空字符串');
    if (!Array.isArray(subscriptions) || subscriptions.some((channelId) => !isNonEmptyString(channelId))) {
      throw new TypeError('subscriptions 必须是频道 id 字符串数组');
    }
    if (!isTransport(transport)) {
      throw new SessionError('transport 必须实现 { send(obj), close() }', SESSION_CODE.TRANSPORT_REQUIRED);
    }
    const existing = this.#sessions.get(agentId);
    if (existing && (existing.state === SESSION_STATE.ONLINE || existing.state === SESSION_STATE.CONNECTING)) {
      throw new SessionError(`agent 已在线: ${agentId}`, SESSION_CODE.ALREADY_ONLINE);
    }

    const nowMs = this.#nowMs();
    const record = {
      agentId,
      state: SESSION_STATE.ONLINE,
      protocol,
      principalUserId,
      subscriptions: new Set(subscriptions),
      transport,
      connectedAt: toIsoFromMs(nowMs),
      lastSeenAt: toIsoFromMs(nowMs),
      lastSeenMs: nowMs,
      disconnectReason: null,
      lastError: null,
    };
    this.#sessions.set(agentId, record);
    for (const channelId of record.subscriptions) this.#propagate(agentId, channelId, true);
    return this.#view(record);
  }

  /** 心跳续期；未知 agent → UNKNOWN_AGENT，非在线 → NOT_ONLINE。 */
  heartbeat({ agentId } = {}) {
    const record = this.#requireOnline(agentId);
    const nowMs = this.#nowMs();
    record.lastSeenMs = nowMs;
    record.lastSeenAt = toIsoFromMs(nowMs);
    return this.#view(record);
  }

  /** 主动断开；关闭 transport（close 失败不影响状态机），保留 OFFLINE 记录以支持重连。 */
  disconnect({ agentId, reason = 'client_closed' } = {}) {
    if (!isNonEmptyString(agentId)) throw new TypeError('agentId 必须是非空字符串');
    const record = this.#sessions.get(agentId);
    if (!record) throw new SessionError(`未知 agent: ${agentId}`, SESSION_CODE.UNKNOWN_AGENT);
    if (record.state !== SESSION_STATE.ONLINE) {
      throw new SessionError(`agent 不在线: ${agentId}`, SESSION_CODE.NOT_ONLINE);
    }
    if (reason !== null && typeof reason !== 'string') throw new TypeError('reason 必须是字符串或 null');
    for (const channelId of record.subscriptions) this.#propagate(agentId, channelId, false);
    this.#closeTransport(record);
    record.state = SESSION_STATE.OFFLINE;
    record.transport = null;
    record.disconnectReason = reason;
    return this.#view(record);
  }

  /** 订阅频道（仅在线会话可订阅）；订阅关系会传播给 channelRegistry 的投递过滤。 */
  subscribe({ agentId, channelId } = {}) {
    if (!isNonEmptyString(channelId)) throw new TypeError('channelId 必须是非空字符串');
    const record = this.#requireOnline(agentId);
    record.subscriptions.add(channelId);
    this.#propagate(agentId, channelId, true);
    return Object.freeze([...record.subscriptions]);
  }

  unsubscribe({ agentId, channelId } = {}) {
    if (!isNonEmptyString(channelId)) throw new TypeError('channelId 必须是非空字符串');
    const record = this.#requireOnline(agentId);
    record.subscriptions.delete(channelId);
    this.#propagate(agentId, channelId, false);
    return Object.freeze([...record.subscriptions]);
  }

  /** 在线状态视图（含 OFFLINE 记录；按连接顺序）。 */
  presence() {
    const result = [];
    for (const record of this.#sessions.values()) result.push(this.#view(record));
    return result;
  }

  /**
   * 心跳超时扫描：lastSeen 距今 >= heartbeatTimeoutMs 的在线会话置 OFFLINE，返回被置离线的 agentId[]。
   * nowMs 可显式传入（测试注入），否则用注入 clock。
   */
  sweepStale(nowMs = null) {
    if (nowMs !== null && (typeof nowMs !== 'number' || !Number.isFinite(nowMs))) {
      throw new TypeError('nowMs 必须是有限数字或 null');
    }
    const now = nowMs === null ? this.#nowMs() : nowMs;
    const offline = [];
    for (const record of this.#sessions.values()) {
      if (record.state !== SESSION_STATE.ONLINE) continue;
      if (now - record.lastSeenMs < this.#heartbeatTimeoutMs) continue;
      for (const channelId of record.subscriptions) this.#propagate(record.agentId, channelId, false);
      this.#closeTransport(record);
      record.state = SESSION_STATE.OFFLINE;
      record.transport = null;
      record.lastError = { code: 'HEARTBEAT_TIMEOUT', message: '心跳超时，会话被置为 OFFLINE' };
      offline.push(record.agentId);
    }
    return offline;
  }

  /**
   * 向「在线 + 已订阅该频道」的 agent 推送消息。
   * transport.send 抛错 → 该 agent 置 OFFLINE、计入 failed，其余 agent 继续投递。
   * throwOnFailure=true（加法可选参数）时若有失败则抛 SEND_FAILED（携带 .delivered/.failed）。
   */
  push({ channelId, message, throwOnFailure = false } = {}) {
    if (!isNonEmptyString(channelId)) throw new TypeError('channelId 必须是非空字符串');
    if (message === null || message === undefined) throw new TypeError('message 不可为空');
    if (typeof throwOnFailure !== 'boolean') throw new TypeError('throwOnFailure 必须是布尔值');
    const payload = Object.freeze({ channelId, message });
    const delivered = [];
    const failed = [];
    for (const record of this.#sessions.values()) {
      if (record.state !== SESSION_STATE.ONLINE) continue;
      if (!record.subscriptions.has(channelId)) continue;
      try {
        record.transport.send(payload);
        delivered.push(record.agentId);
      } catch (error) {
        failed.push(record.agentId);
        record.lastError = {
          code: SESSION_CODE.SEND_FAILED,
          message: error instanceof Error ? error.message : String(error),
        };
        for (const subscription of record.subscriptions) this.#propagate(record.agentId, subscription, false);
        this.#closeTransport(record);
        record.state = SESSION_STATE.OFFLINE;
        record.transport = null;
      }
    }
    const result = Object.freeze({
      delivered: Object.freeze(delivered),
      failed: Object.freeze(failed),
    });
    if (throwOnFailure && failed.length > 0) {
      const error = new SessionError(`push 失败 ${failed.length} 个 agent`, SESSION_CODE.SEND_FAILED);
      error.delivered = result.delivered;
      error.failed = result.failed;
      throw error;
    }
    return result;
  }

  #requireOnline(agentId) {
    if (!isNonEmptyString(agentId)) throw new TypeError('agentId 必须是非空字符串');
    const record = this.#sessions.get(agentId);
    if (!record) throw new SessionError(`未知 agent: ${agentId}`, SESSION_CODE.UNKNOWN_AGENT);
    if (record.state !== SESSION_STATE.ONLINE) {
      throw new SessionError(`agent 不在线: ${agentId}`, SESSION_CODE.NOT_ONLINE);
    }
    return record;
  }

  /** 会话视图（§15 presence 的字段集：agentId/state/protocol/principalUserId/subscriptions/lastSeenAt）。 */
  #view(record) {
    return Object.freeze({
      agentId: record.agentId,
      state: record.state,
      protocol: record.protocol,
      principalUserId: record.principalUserId,
      subscriptions: Object.freeze([...record.subscriptions]),
      lastSeenAt: record.lastSeenAt,
    });
  }

  /** 订阅状态向频道层传播（尽力而为：channelRegistry 未提供或成员不存在时安全跳过）。 */
  #propagate(agentId, channelId, subscribed) {
    const registry = this.#channelRegistry;
    if (!registry || typeof registry.setSubscription !== 'function') return;
    registry.setSubscription({ channelId, memberId: agentId, subscribed });
  }

  #closeTransport(record) {
    const transport = record.transport;
    if (!transport || typeof transport.close !== 'function') return;
    try {
      transport.close();
    } catch {
      // close 失败不改变会话状态机语义（连接已不可用，状态照常置 OFFLINE）
    }
  }

  #nowMs() {
    const now = this.#clock();
    if (!(now instanceof Date) || Number.isNaN(now.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return now.getTime();
  }
}
