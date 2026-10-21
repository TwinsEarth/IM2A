// 模块职责：服务端可信上下文注入——Agent 处理请求时不再自己查关系链/群成员/授权候选，服务端把「会话/发送者/信任档/能力/候选对象」签名后只读注入 / 契约来源：contracts/INTERFACES.md §22（依赖 §3 trust-zone、§13 visibility 的既有语义，只调用不改）/ 关键不变量：上下文由网关签名且 Agent 改写即 CONTEXT_FORGED、候选对象必须已过 §13 可见域裁剪（注入 ≠ 越权）、有 TTL 且跨 Agent 复用 → CONTEXT_SCOPE_MISMATCH、verify 只返回判定不抛异常。
import { randomUUID } from 'node:crypto';
import { canonicalize, sha256Hex, signPayload, verifyPayload } from '../../identity/src/agent-id.mjs';
import { DOMAIN } from './visibility.mjs';

export const CONTEXT_CODE = Object.freeze({
  CONTEXT_INVALID: 'CONTEXT_INVALID',
  CONTEXT_EXPIRED: 'CONTEXT_EXPIRED',
  CONTEXT_FORGED: 'CONTEXT_FORGED',
  CONTEXT_SCOPE_MISMATCH: 'CONTEXT_SCOPE_MISMATCH',
});

const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isPlainObject = (value) => typeof value === 'object' && value !== null && !Array.isArray(value);
const clampTrust = (value) => Math.min(4, Math.max(0, value));

/** candidates 三类 → §13 可见域 */
const CANDIDATE_CATEGORIES = Object.freeze([
  Object.freeze({ key: 'contacts', domain: DOMAIN.CONTACT }),
  Object.freeze({ key: 'groups', domain: DOMAIN.GROUP }),
  Object.freeze({ key: 'approvals', domain: DOMAIN.TASK }),
]);

const fail = (code) => Object.freeze({ ok: false, code, context: null });

export class TrustedContextError extends Error {
  constructor(message, code = CONTEXT_CODE.CONTEXT_INVALID) {
    super(message);
    this.name = 'TrustedContextError';
    this.code = code;
  }
}

export class TrustedContextProvider {
  #trustMatrix;
  #registry;
  #visibility;
  #identity;
  #clock;
  #ttlMs;

  constructor({ trustMatrix, registry, visibility = null, identity, clock = () => new Date(), ttlMs = 120_000 } = {}) {
    if (!trustMatrix || typeof trustMatrix.levelOf !== 'function') throw new TypeError('trustMatrix 必填且需实现 levelOf');
    if (!registry || typeof registry.get !== 'function') throw new TypeError('registry 必填且需实现 get');
    if (!identity || !isNonEmptyString(identity.privateKeyPem) || !isNonEmptyString(identity.publicKeyPem)) {
      throw new TypeError('identity 必填（需含 Ed25519 publicKeyPem/privateKeyPem）');
    }
    if (visibility !== null && (typeof visibility !== 'object' || typeof visibility.project !== 'function')) {
      throw new TypeError('visibility 必须是 VisibilityGate 实例或 null');
    }
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!Number.isInteger(ttlMs) || ttlMs <= 0) throw new RangeError('ttlMs 必须是正整数（毫秒）');
    this.#trustMatrix = trustMatrix;
    this.#registry = registry;
    this.#visibility = visibility;
    this.#identity = identity;
    this.#clock = clock;
    this.#ttlMs = ttlMs;
  }

  /**
   * 构建并签名一份可信上下文。
   * 派生规则（§22 只冻结字段、未冻结来源）：
   *  - capabilities  ← registry 中该 Agent 的 allowedSkills；
   *  - trustCeiling   ← registry 中该 Agent 的 trustZoneMin（§2/§3 语义：可触达的最亲近信任档，数值越小越亲）；
   *  - sender.trustLevel ← trustMatrix.levelOf({ principalUserId, targetId: senderId })；
   *  - candidates     ← 逐项按 §13 可见域裁剪（STRANGER 档只留 {id, exists}，超出 ceiling 的整项丢弃）。
   * @returns 冻结 context
   */
  build({
    agentId,
    principalUserId,
    conversationId,
    channelType = null,
    members = [],
    senderId = null,
    candidates = {},
  } = {}) {
    if (!isNonEmptyString(agentId)) throw new TypeError('agentId 必须是非空字符串');
    if (!isNonEmptyString(principalUserId)) throw new TypeError('principalUserId 必须是非空字符串');
    if (!isNonEmptyString(conversationId)) throw new TypeError('conversationId 必须是非空字符串');
    if (channelType !== null && typeof channelType !== 'string') throw new TypeError('channelType 必须是字符串或 null');
    if (!Array.isArray(members)) throw new TypeError('members 必须是数组');
    if (senderId !== null && !isNonEmptyString(senderId)) throw new TypeError('senderId 必须是字符串或 null');
    if (!isPlainObject(candidates)) throw new TypeError('candidates 必须是普通对象');

    const agent = this.#registry.get(agentId);
    if (!agent) {
      throw new TrustedContextError(`Agent 未注册，无法构建可信上下文: ${agentId}`, CONTEXT_CODE.CONTEXT_INVALID);
    }
    const now = this.#clock();
    if (!(now instanceof Date) || Number.isNaN(now.getTime())) throw new TypeError('clock 必须返回合法的 Date 实例');

    const trustCeiling = Number.isInteger(agent.trustZoneMin) ? clampTrust(agent.trustZoneMin) : 4;
    const capabilities = Object.freeze([...(Array.isArray(agent.allowedSkills) ? agent.allowedSkills : [])]);
    const sender =
      senderId === null
        ? null
        : Object.freeze({
            id: senderId,
            trustLevel: this.#trustMatrix.levelOf({ userId: principalUserId, targetId: senderId }),
          });

    const unsigned = {
      contextId: `ctx-${randomUUID()}`,
      issuedAt: now.toISOString(),
      expiresAt: new Date(now.getTime() + this.#ttlMs).toISOString(),
      agentId,
      principalUserId,
      conversation: Object.freeze({
        id: conversationId,
        channelType,
        members: Object.freeze([...this.#normalizeMembers(members)]),
      }),
      sender,
      capabilities,
      trustCeiling,
      candidates: this.#clipCandidates({ principalUserId, agentId, trustCeiling, candidates }),
    };
    const signature = signPayload({ identity: this.#identity, payload: unsigned });
    return Object.freeze({ ...unsigned, signature });
  }

  /**
   * 校验可信上下文（**只返回判定，绝不抛异常**）。
   * 顺序：结构(CONTEXT_INVALID) → 签名完整性/真实性(CONTEXT_FORGED) → 跨 Agent 复用(CONTEXT_SCOPE_MISMATCH) → TTL(CONTEXT_EXPIRED)。
   * 先验签后判作用域，保证「只要被改过任何字段」一律 CONTEXT_FORGED。
   * @returns {Readonly<{ok:boolean, code:string|null, context:object|null}>}
   */
  verify({ context, agentId = null } = {}) {
    try {
      if (!isPlainObject(context)) return fail(CONTEXT_CODE.CONTEXT_INVALID);
      const { signature, ...payload } = context;
      if (!isPlainObject(signature)) return fail(CONTEXT_CODE.CONTEXT_INVALID);
      for (const key of ['contextId', 'issuedAt', 'expiresAt', 'agentId', 'principalUserId']) {
        if (!isNonEmptyString(payload[key])) return fail(CONTEXT_CODE.CONTEXT_INVALID);
      }
      if (!isPlainObject(payload.conversation) || !Array.isArray(payload.capabilities)) {
        return fail(CONTEXT_CODE.CONTEXT_INVALID);
      }
      if (!isPlainObject(payload.candidates) || !Number.isInteger(payload.trustCeiling)) {
        return fail(CONTEXT_CODE.CONTEXT_INVALID);
      }
      if (signature.alg !== 'ed25519' || !isNonEmptyString(signature.signature)) {
        return fail(CONTEXT_CODE.CONTEXT_INVALID);
      }

      // ② 篡改检测：payloadHash 与 Ed25519 签名都必须匹配（任何字段被改 → FORGED）
      const expectedHash = sha256Hex(canonicalize(payload));
      if (signature.payloadHash !== expectedHash) return fail(CONTEXT_CODE.CONTEXT_FORGED);
      const verified = verifyPayload({
        publicKeyPem: this.#identity.publicKeyPem,
        payload,
        signature: signature.signature,
      });
      if (!verified.ok) return fail(CONTEXT_CODE.CONTEXT_FORGED);

      // ③ 跨 Agent 复用
      if (agentId !== null && payload.agentId !== agentId) return fail(CONTEXT_CODE.CONTEXT_SCOPE_MISMATCH);

      // ④ TTL
      const expiresMs = Date.parse(payload.expiresAt);
      if (!Number.isFinite(expiresMs)) return fail(CONTEXT_CODE.CONTEXT_INVALID);
      if (expiresMs <= this.#now().getTime()) return fail(CONTEXT_CODE.CONTEXT_EXPIRED);

      return Object.freeze({ ok: true, code: null, context });
    } catch {
      return fail(CONTEXT_CODE.CONTEXT_INVALID);
    }
  }

  // ── 内部 ────────────────────────────────────────────────────────────────

  #now() {
    const sampled = this.#clock();
    if (!(sampled instanceof Date) || Number.isNaN(sampled.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return sampled;
  }

  #normalizeMembers(members) {
    return members.map((member) => {
      if (isNonEmptyString(member)) return member;
      if (isPlainObject(member)) return Object.freeze({ ...member });
      throw new TypeError('members 每项必须是成员 id 字符串或普通对象');
    });
  }

  /**
   * 候选对象按 §13 可见域裁剪：注入 ≠ 越权。
   * 无 visibility 注入时采用最保守口径：只保留存在性 {id, exists}（宁可少给，不可越权）。
   */
  #clipCandidates({ principalUserId, agentId, trustCeiling, candidates }) {
    const scope =
      this.#visibility === null
        ? null
        : this.#visibility.buildScope({
            principalUserId,
            agentId,
            domains: CANDIDATE_CATEGORIES.map((category) => category.domain),
            trustCeiling,
            ttlMs: this.#ttlMs,
          });
    const clipped = {};
    for (const { key, domain } of CANDIDATE_CATEGORIES) {
      const rawList = candidates[key] ?? [];
      if (!Array.isArray(rawList)) throw new TypeError(`candidates.${key} 必须是数组`);
      const items = [];
      for (const raw of rawList) {
        if (!isPlainObject(raw) || !isNonEmptyString(raw.id)) {
          throw new TypeError(`candidates.${key} 中每项必须是含非空 id 的普通对象`);
        }
        const trustLevel = Number.isInteger(raw.trustLevel)
          ? clampTrust(raw.trustLevel)
          : this.#trustMatrix.levelOf({ userId: principalUserId, targetId: raw.id });
        if (scope === null) {
          items.push(Object.freeze({ id: raw.id, exists: true }));
          continue;
        }
        const projection = this.#visibility.project({ scope, resource: { domain, trustLevel }, fields: raw });
        if (projection.decision === 'DENY') continue;
        if (trustLevel === 4) {
          // STRANGER 档：只给存在性，绝不带出 detail 等内容
          items.push(Object.freeze({ id: raw.id, exists: true }));
        } else {
          items.push(Object.freeze({ ...projection.projected, trustLevel }));
        }
      }
      clipped[key] = Object.freeze(items);
    }
    return Object.freeze({
      contacts: clipped.contacts,
      groups: clipped.groups,
      approvals: clipped.approvals,
    });
  }
}
