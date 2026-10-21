// 模块职责：所有 Agent 流量的唯一入口（内部 Agent / 第三方 Agent / 外部 A2A / MCP）——认证、限流、前置门控、出口护栏、签名审计在同一数据平面收口。
// 契约来源：contracts/INTERFACES.md §12（依赖 §3 CapabilityGate 与 §7 EgressGuard 的既有签名，本模块只调用、不修改）。
// 关键不变量：①管线顺序冻结且短路：认证→限流→门控→出口→归一化→签名审计；②门控 DENY 才拒绝，REQUIRE_APPROVAL 不抛异常（decision=REQUIRE_APPROVAL、normalized.execute=false）；
//             ③成功路由必落一条签名审计并回传 auditSeq；④GatewayError.code 取自 GATEWAY_CODE，GATE_DENIED 额外保留底层 gateCode。
import { randomUUID } from 'node:crypto';

import { verifyAgentCard } from '../../identity/src/agent-id.mjs';

export const PROTOCOL = Object.freeze({
  AGENT_WS: 'AGENT_WS',
  IM_CUSTOM: 'IM_CUSTOM',
  A2A_JSONRPC: 'A2A_JSONRPC',
  MCP_TOOLS: 'MCP_TOOLS'
});

export const GATEWAY_CODE = Object.freeze({
  UNAUTHENTICATED: 'UNAUTHENTICATED',
  CARD_EXPIRED: 'CARD_EXPIRED',
  SIGNATURE_INVALID: 'SIGNATURE_INVALID',
  TOKEN_MISMATCH: 'TOKEN_MISMATCH',
  TOKEN_REVOKED: 'TOKEN_REVOKED',
  PRINCIPAL_MISMATCH: 'PRINCIPAL_MISMATCH',
  RATE_LIMITED: 'RATE_LIMITED',
  GATE_DENIED: 'GATE_DENIED',
  EGRESS_BLOCKED: 'EGRESS_BLOCKED',
  UNKNOWN_ROUTE: 'UNKNOWN_ROUTE',
  NOT_CONFIGURED: 'NOT_CONFIGURED'
});

const GATEWAY_CODE_VALUES = new Set(Object.values(GATEWAY_CODE));
const PROTOCOL_VALUES = new Set(Object.values(PROTOCOL));
const DEFAULT_RATE_LIMIT = Object.freeze({ windowMs: 60_000, max: 120 });
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class GatewayError extends Error {
  constructor(message, code, extra = {}) {
    super(message);
    this.name = 'GatewayError';
    if (!GATEWAY_CODE_VALUES.has(code)) throw new TypeError(`未知 GATEWAY_CODE: ${String(code)}`);
    this.code = code;
    Object.assign(this, extra);
  }
}

function normalizeStringArray(value) {
  const unique = [];
  for (const item of Array.isArray(value) ? value : []) {
    if (isNonEmptyString(item) && !unique.includes(item)) unique.push(item);
  }
  return Object.freeze(unique);
}

function clampTrustCeiling(value) {
  if (!Number.isInteger(value)) return 4;
  return Math.min(4, Math.max(0, value));
}

/** token 既可以是 issueToken 返回的对象，也可以是它里面的字符串标识 */
function tokenIdOf(token) {
  if (isNonEmptyString(token)) return token;
  if (token && typeof token === 'object' && isNonEmptyString(token.token)) return token.token;
  return null;
}

export class AgentGateway {
  #capabilityGate;
  #egressGuard;
  #signedAudit;
  #clock;
  #rateLimit;
  /** @type {Map<string, object>} tokenId -> 签发记录 */
  #tokens;
  /** @type {Set<string>} 已撤销 tokenId */
  #revoked;
  /** @type {Map<string, number[]>} agentId -> 窗口内请求时间戳（ms） */
  #rateHits;
  #events;

  constructor({
    capabilityGate = null,
    egressGuard = null,
    signedAudit = null,
    clock = () => new Date(),
    rateLimit = DEFAULT_RATE_LIMIT
  } = {}) {
    this.#capabilityGate = capabilityGate;
    this.#egressGuard = egressGuard;
    this.#signedAudit = signedAudit;
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    const windowMs =
      Number.isFinite(rateLimit?.windowMs) && rateLimit.windowMs > 0 ? Math.trunc(rateLimit.windowMs) : DEFAULT_RATE_LIMIT.windowMs;
    const max = Number.isInteger(rateLimit?.max) && rateLimit.max >= 0 ? rateLimit.max : DEFAULT_RATE_LIMIT.max;
    this.#rateLimit = Object.freeze({ windowMs, max });
    this.#tokens = new Map();
    this.#revoked = new Set();
    this.#rateHits = new Map();
    this.#events = [];
  }

  // ── 内部工具 ────────────────────────────────────────────────────────────────

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #emit({ action, actor = null, target = null, decision, detail = {} }) {
    const event = Object.freeze({
      at: this.#nowIso(),
      actor,
      action,
      target,
      decision,
      detail: Object.freeze({ ...detail })
    });
    this.#events.push(event);
    return event;
  }

  #fail({ message, code, actor = null, target = null, detail = {}, extra = {} }) {
    this.#emit({ action: 'DENY', actor, target, decision: 'DENY', detail: { code, ...detail } });
    throw new GatewayError(message, code, extra);
  }

  /** 解析 token 声明：优先服务端签发记录（防客户端自证），其次接受外部 token 对象 */
  #resolveClaims(token) {
    if (isNonEmptyString(token)) return this.#tokens.get(token) ?? null;
    if (!token || typeof token !== 'object') return null;
    const id = tokenIdOf(token);
    if (!id) return null;
    const issued = this.#tokens.get(id);
    if (issued) return issued;
    // 外部 token：必需字段齐全才可作为声明
    if (
      !isNonEmptyString(token.agentId) ||
      !isNonEmptyString(token.principalUserId) ||
      !Number.isFinite(Date.parse(token.expiresAt))
    ) {
      return null;
    }
    return token;
  }

  #enforceRateLimit(agentId) {
    const nowMs = this.#now().getTime();
    const windowStart = nowMs - this.#rateLimit.windowMs;
    const recent = (this.#rateHits.get(agentId) ?? []).filter((at) => at > windowStart);
    if (recent.length >= this.#rateLimit.max) {
      this.#rateHits.set(agentId, recent);
      this.#fail({
        message: `限流：${this.#rateLimit.windowMs}ms 内超过 ${this.#rateLimit.max} 次`,
        code: GATEWAY_CODE.RATE_LIMITED,
        actor: agentId,
        target: 'route',
        detail: { windowMs: this.#rateLimit.windowMs, max: this.#rateLimit.max }
      });
    }
    recent.push(nowMs);
    this.#rateHits.set(agentId, recent);
  }

  // ── 公开 API（契约 §12）────────────────────────────────────────────────────

  /** 签发短期 token（与 card.agentId / card.ownerUserId 绑定，认证时校验） */
  issueToken({ agentId, principalUserId, domains = [], trustCeiling = 4, ttlMs = 15 * 60e3 } = {}) {
    if (!isNonEmptyString(agentId) || !isNonEmptyString(principalUserId)) {
      throw new TypeError('issueToken: agentId/principalUserId 必填');
    }
    if (!Number.isFinite(ttlMs) || ttlMs < 0) throw new TypeError('issueToken: ttlMs 必须是非负毫秒数');
    const now = this.#now();
    const record = Object.freeze({
      token: `tok-${randomUUID()}`,
      agentId,
      principalUserId,
      domains: normalizeStringArray(domains),
      trustCeiling: clampTrustCeiling(trustCeiling),
      issuedAt: now.toISOString(),
      expiresAt: new Date(now.getTime() + Math.trunc(ttlMs)).toISOString()
    });
    this.#tokens.set(record.token, record);
    this.#emit({
      action: 'ISSUE_TOKEN',
      actor: agentId,
      target: record.token,
      decision: 'ALLOW',
      detail: { principalUserId, expiresAt: record.expiresAt }
    });
    return record;
  }

  /** @returns {boolean} 不存在的 token 返回 false（幂等） */
  revokeToken(token) {
    const id = tokenIdOf(token);
    if (!id || !this.#tokens.has(id)) return false;
    this.#revoked.add(id);
    this.#emit({
      action: 'REVOKE_TOKEN',
      actor: this.#tokens.get(id).agentId,
      target: id,
      decision: 'REVOKED',
      detail: {}
    });
    return true;
  }

  /** 认证：卡片验签 → token 存在/未撤销/未过期 → token.agentId 绑定 → principal 绑定 */
  authenticate({ signedCard, token, protocol } = {}) {
    const at = this.#now();

    if (!PROTOCOL_VALUES.has(protocol)) {
      this.#fail({
        message: `未知协议/路由: ${String(protocol)}`,
        code: GATEWAY_CODE.UNKNOWN_ROUTE,
        target: protocol ?? null
      });
    }
    if (!signedCard || !token) {
      this.#fail({
        message: '未认证：缺少 AgentCard 或 token',
        code: GATEWAY_CODE.UNAUTHENTICATED,
        target: protocol
      });
    }

    const verdict = verifyAgentCard({ signedCard, now: at });
    if (!verdict.ok) {
      const code =
        verdict.code === 'CARD_EXPIRED'
          ? GATEWAY_CODE.CARD_EXPIRED
          : verdict.code === 'SIGNATURE_INVALID' || verdict.code === 'HASH_MISMATCH'
            ? GATEWAY_CODE.SIGNATURE_INVALID
            : GATEWAY_CODE.UNAUTHENTICATED;
      this.#fail({
        message: `AgentCard 验签失败: ${verdict.code}`,
        code,
        target: protocol,
        detail: { cardCode: verdict.code },
        extra: { cardCode: verdict.code }
      });
    }
    const card = verdict.card;

    const claims = this.#resolveClaims(token);
    if (!claims) {
      this.#fail({
        message: '未认证：token 不存在或格式非法',
        code: GATEWAY_CODE.UNAUTHENTICATED,
        target: protocol
      });
    }
    if (this.#revoked.has(claims.token)) {
      this.#fail({
        message: 'token 已被撤销',
        code: GATEWAY_CODE.TOKEN_REVOKED,
        actor: claims.agentId,
        target: protocol
      });
    }
    if (claims.agentId !== card.agentId) {
      this.#fail({
        message: 'token.agentId 与卡片 agentId 不一致',
        code: GATEWAY_CODE.TOKEN_MISMATCH,
        actor: claims.agentId,
        target: protocol,
        detail: { tokenAgentId: claims.agentId, cardAgentId: card.agentId }
      });
    }
    if (claims.principalUserId !== card.ownerUserId) {
      this.#fail({
        message: 'token.principalUserId 与卡片 ownerUserId 不一致',
        code: GATEWAY_CODE.PRINCIPAL_MISMATCH,
        actor: card.agentId,
        target: protocol,
        detail: { tokenPrincipal: claims.principalUserId, cardOwner: card.ownerUserId }
      });
    }
    const expiresMs = Date.parse(claims.expiresAt);
    if (Number.isFinite(expiresMs) && expiresMs <= at.getTime()) {
      this.#fail({
        message: 'token 已过期',
        code: GATEWAY_CODE.UNAUTHENTICATED,
        actor: card.agentId,
        target: protocol,
        detail: { expiresAt: claims.expiresAt }
      });
    }

    const principal = Object.freeze({
      agentId: card.agentId,
      principalUserId: claims.principalUserId,
      domains: claims.domains,
      trustCeiling: claims.trustCeiling,
      protocol
    });
    this.#emit({
      action: 'AUTH',
      actor: principal.agentId,
      target: protocol,
      decision: 'ALLOW',
      detail: { principalUserId: principal.principalUserId }
    });
    return principal;
  }

  /**
   * 唯一数据平面入口。成功与 REQUIRE_APPROVAL 都返回 ok:true；
   * 认证/限流/门控 DENY/出口 BLOCK 抛 GatewayError。
   */
  route({ signedCard, token, protocol, action, payload = {} } = {}) {
    if (!this.#signedAudit) {
      this.#fail({
        message: '签名审计未配置：v2 起审计必须签名（SignedAuditLog）',
        code: GATEWAY_CODE.NOT_CONFIGURED,
        target: action ?? null
      });
    }
    if (!isNonEmptyString(action)) {
      this.#fail({ message: `未知路由 action: ${String(action)}`, code: GATEWAY_CODE.UNKNOWN_ROUTE });
    }

    const principal = this.authenticate({ signedCard, token, protocol });
    this.#enforceRateLimit(principal.agentId);

    let decision = 'ALLOW';
    let gateResult = null;
    const skillId = payload.skillId ?? null;
    if (skillId !== null) {
      if (!this.#capabilityGate) {
        this.#fail({
          message: '前置门控未配置（CapabilityGate）',
          code: GATEWAY_CODE.NOT_CONFIGURED,
          actor: principal.agentId,
          target: action
        });
      }
      gateResult = this.#capabilityGate.evaluate({
        agentId: principal.agentId,
        userId: principal.principalUserId,
        targetId: payload.targetId ?? null,
        skillId,
        amountCents: payload.amountCents ?? 0
      });
      if (gateResult.decision === 'DENY') {
        this.#fail({
          message: `前置门控拒绝: ${gateResult.code}`,
          code: GATEWAY_CODE.GATE_DENIED,
          actor: principal.agentId,
          target: action,
          detail: { skillId, gateCode: gateResult.code, checks: gateResult.checks },
          extra: { gateCode: gateResult.code, gateDecision: gateResult.decision, checks: gateResult.checks }
        });
      }
      if (gateResult.decision === 'REQUIRE_APPROVAL') decision = 'REQUIRE_APPROVAL';
    }

    let egressResult = null;
    if (payload.content !== undefined && payload.content !== null) {
      if (!this.#egressGuard) {
        this.#fail({
          message: '出口护栏未配置（EgressGuard）',
          code: GATEWAY_CODE.NOT_CONFIGURED,
          actor: principal.agentId,
          target: action
        });
      }
      egressResult = this.#egressGuard.inspect({
        content: payload.content,
        fromAgentId: principal.agentId,
        toKind: payload.toKind ?? 'agent',
        messageId: payload.messageId ?? null,
        trustZone: payload.trustZone ?? null
      });
      if (egressResult.verdict === 'BLOCK') {
        this.#fail({
          message: '出口内容被拦截',
          code: GATEWAY_CODE.EGRESS_BLOCKED,
          actor: principal.agentId,
          target: action,
          detail: { matched: egressResult.matched.map((item) => item.id), contentHash: egressResult.contentHash },
          extra: { egress: egressResult, matched: egressResult.matched, contentHash: egressResult.contentHash }
        });
      }
    }

    // 协议归一化：A2A / MCP 适配只在此收口，网关不执行业务
    const normalized = Object.freeze({
      action,
      protocol,
      execute: decision === 'ALLOW',
      skillId,
      targetId: payload.targetId ?? null,
      content: payload.content ?? null,
      meta: Object.freeze({ ...(payload.meta ?? {}) })
    });

    // 签名审计（成功 / REQUIRE_APPROVAL 都落审计）
    const auditEntry = this.#signedAudit.append({
      source: 'agent-gateway',
      action: 'route',
      actor: principal.agentId,
      target: action,
      decision,
      detail: {
        protocol,
        principalUserId: principal.principalUserId,
        skillId,
        targetId: payload.targetId ?? null,
        gateCode: gateResult?.code ?? null,
        egressVerdict: egressResult?.verdict ?? null,
        contentHash: egressResult?.contentHash ?? null
      }
    });

    this.#emit({
      action: 'ROUTE',
      actor: principal.agentId,
      target: action,
      decision,
      detail: { protocol, skillId, auditSeq: auditEntry.seq }
    });

    return Object.freeze({
      ok: true,
      agentId: principal.agentId,
      protocol,
      action,
      decision,
      auditSeq: auditEntry.seq,
      normalized
    });
  }

  /** 网关自身事件（认证/限流/拒绝/放行），只追加、返回冻结副本 */
  events({ limit = null } = {}) {
    const all = this.#events;
    const selected = Number.isInteger(limit) && limit > 0 && all.length > limit ? all.slice(-limit) : all;
    return Object.freeze([...selected]);
  }
}
