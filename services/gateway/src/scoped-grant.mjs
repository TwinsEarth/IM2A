// 模块职责：PACT 式 scoped 授权 + 每次动作的签名回执——付费解锁的不是"通路"，而是"被允许查看本条消息原文/回复"这一特定 scope；回执绑定 messageId+scope+action+at 进不可变审计 / 契约来源：contracts/INTERFACES.md §35（复用 §11 identity 的 canonicalize/sha256Hex/signPayload/verifyPayload，只调用不改）/ 关键不变量：①scopes ⊆ 白名单否则 SCOPE_INVALID；②每个 scope 单次消耗（重复 ALREADY_USED）；③过期 EXPIRED / 撤销 REVOKED；④回执 hash 绑定 messageId+scope+action+at，注入 identity 时同时签名且 verifyReceipt 只返回判定不抛；⑤**付费不扩大 scope**——priceCents 只是记录，不参与授权判定。
import { randomUUID } from 'node:crypto';
import { canonicalize, sha256Hex, signPayload, verifyPayload } from '../../identity/src/agent-id.mjs';

export const GRANT_SCOPE = Object.freeze({
  VIEW_SUMMARY: 'view_summary',
  VIEW_ORIGINAL: 'view_original',
  REPLY: 'reply',
});

export const GRANT_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  SCOPE_INVALID: 'SCOPE_INVALID',
  SCOPE_NOT_GRANTED: 'SCOPE_NOT_GRANTED',
  EXPIRED: 'EXPIRED',
  ALREADY_USED: 'ALREADY_USED',
  RECEIPT_MISMATCH: 'RECEIPT_MISMATCH',
  REVOKED: 'REVOKED',
});

const CODE = GRANT_CODE;
const SCOPE_VALUES = new Set(Object.values(GRANT_SCOPE));
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isPlainObject = (value) => typeof value === 'object' && value !== null && !Array.isArray(value);
const receiptVerdict = (ok, code) => Object.freeze({ ok, code });

export class ScopedGrantError extends Error {
  constructor(message, code = CODE.SCOPE_INVALID) {
    super(message);
    this.name = 'ScopedGrantError';
    this.code = code;
  }
}

/** 回执签名/校验的绑定载荷：hash 必须恰好覆盖这四个字段 */
const receiptPayloadOf = ({ messageId, scope, action, at }) => ({ messageId, scope, action, at });

export class ScopedGrant {
  #clock;
  #identity;
  #maxTtlMs;
  /** @type {Map<string, object>} grantId -> 内部 grant 记录（对外只暴露冻结视图） */
  #grants = new Map();
  /** @type {string[]} 创建顺序，保证 list() 确定性 */
  #order = [];

  constructor({ clock = () => new Date(), identity = null, maxTtlMs = 10 * 60e3 } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!Number.isInteger(maxTtlMs) || maxTtlMs <= 0) throw new RangeError('maxTtlMs 必须是正整数（毫秒）');
    if (identity !== null) {
      if (
        typeof identity !== 'object' ||
        !isNonEmptyString(identity.publicKeyPem) ||
        !isNonEmptyString(identity.privateKeyPem)
      ) {
        throw new TypeError('identity 必须是含 Ed25519 publicKeyPem/privateKeyPem 的对象或 null');
      }
    }
    this.#clock = clock;
    this.#identity = identity;
    this.#maxTtlMs = maxTtlMs;
  }

  /**
   * 授予 scoped 权限。priceCents 仅记录（计费结果），绝不参与授权判定。
   * @returns 冻结 grant { id, messageId, granteeAgentId, scopes, priceCents, issuedAt, expiresAt }
   */
  grant({ messageId, granteeAgentId, scopes, ttlMs = 5 * 60e3, priceCents = 0 } = {}) {
    if (!isNonEmptyString(messageId)) throw new TypeError('messageId 必须是非空字符串');
    if (!isNonEmptyString(granteeAgentId)) throw new TypeError('granteeAgentId 必须是非空字符串');
    if (!Array.isArray(scopes) || scopes.length === 0) {
      throw new ScopedGrantError('scopes 必须是非空数组（且 ⊆ GRANT_SCOPE 白名单）', CODE.SCOPE_INVALID);
    }
    const uniqueScopes = [];
    for (const scope of scopes) {
      if (!SCOPE_VALUES.has(scope)) {
        throw new ScopedGrantError(
          `非法 scope: ${String(scope)}（允许 ${Object.values(GRANT_SCOPE).join(' / ')}）`,
          CODE.SCOPE_INVALID
        );
      }
      if (!uniqueScopes.includes(scope)) uniqueScopes.push(scope);
    }
    if (!Number.isInteger(ttlMs) || ttlMs <= 0 || ttlMs > this.#maxTtlMs) {
      throw new RangeError(`ttlMs 必须是 1..${this.#maxTtlMs} 的整数（毫秒）`);
    }
    if (!Number.isInteger(priceCents) || priceCents < 0) {
      throw new TypeError('priceCents 必须是非负整数分（仅记录，不参与授权）');
    }

    const now = this.#now();
    const record = {
      id: `grant-${randomUUID()}`,
      messageId,
      granteeAgentId,
      scopes: Object.freeze(uniqueScopes),
      priceCents,
      issuedAt: now.toISOString(),
      expiresAt: new Date(now.getTime() + ttlMs).toISOString(),
      expiresMs: now.getTime() + ttlMs,
      revoked: false,
      revokedAt: null,
      usedScopes: new Set(),
      receipts: [],
    };
    this.#grants.set(record.id, record);
    this.#order.push(record.id);
    return this.#grantView(record);
  }

  /**
   * 消费某个 scope（每个 scope 单次）。返回 `{ receipt }`，receipt 的 hash 绑定
   * messageId+scope+action+at；注入 identity 时额外签名。
   * 判定顺序：NOT_FOUND → REVOKED → EXPIRED → SCOPE_INVALID(action 非法名) →
   *           SCOPE_NOT_GRANTED(action 未授予) → ALREADY_USED(已用过)。
   */
  consume({ grantId, action } = {}) {
    const record = this.#require(grantId);
    if (record.revoked) throw new ScopedGrantError(`授权已被撤销: ${grantId}`, CODE.REVOKED);
    if (this.#now().getTime() >= record.expiresMs) {
      throw new ScopedGrantError(`授权已过期: ${grantId}`, CODE.EXPIRED);
    }
    if (!SCOPE_VALUES.has(action)) {
      throw new ScopedGrantError(`非法 action/scope 名: ${String(action)}`, CODE.SCOPE_INVALID);
    }
    if (!record.scopes.includes(action)) {
      throw new ScopedGrantError(
        `scope 未被授予: ${action}（已授予 ${record.scopes.join(', ')}）——付费不能换 scope`,
        CODE.SCOPE_NOT_GRANTED
      );
    }
    if (record.usedScopes.has(action)) {
      throw new ScopedGrantError(`scope 已被消费过（每个 scope 单次）: ${action}`, CODE.ALREADY_USED);
    }

    record.usedScopes.add(action);
    const at = this.#now().toISOString();
    const payload = receiptPayloadOf({ messageId: record.messageId, scope: action, action, at });
    const hash = sha256Hex(canonicalize(payload));
    const signedPayload = Object.freeze({ ...payload, hash });
    const receipt =
      this.#identity === null
        ? Object.freeze({ ...signedPayload })
        : Object.freeze({ ...signedPayload, signature: signPayload({ identity: this.#identity, payload: signedPayload }).signature });
    record.receipts.push(receipt);
    return Object.freeze({ receipt });
  }

  /**
   * 校验回执（只返回判定，绝不抛异常）：hash 必须绑定 messageId+scope+action+at；
   * 回执带 signature 时用本服务注入的 identity 验签。失败一律 RECEIPT_MISMATCH。
   * @returns {Readonly<{ok:boolean, code:string|null}>}
   */
  verifyReceipt({ receipt } = {}) {
    try {
      if (!isPlainObject(receipt)) return receiptVerdict(false, CODE.RECEIPT_MISMATCH);
      const { messageId, scope, action, at, hash, signature } = receipt;
      if (
        !isNonEmptyString(messageId) ||
        !SCOPE_VALUES.has(scope) ||
        !SCOPE_VALUES.has(action) ||
        !isNonEmptyString(at) ||
        !Number.isFinite(Date.parse(at)) ||
        !isNonEmptyString(hash)
      ) {
        return receiptVerdict(false, CODE.RECEIPT_MISMATCH);
      }
      // 回执绑定：hash 不匹配说明任一绑定字段被改
      if (sha256Hex(canonicalize(receiptPayloadOf({ messageId, scope, action, at }))) !== hash) {
        return receiptVerdict(false, CODE.RECEIPT_MISMATCH);
      }
      // 有签名时验签（无签名则仅校验 hash 完整性）
      if (signature !== undefined && signature !== null) {
        if (this.#identity === null || !isNonEmptyString(signature)) {
          return receiptVerdict(false, CODE.RECEIPT_MISMATCH);
        }
        const signedPayload = Object.freeze({ messageId, scope, action, at, hash });
        const verified = verifyPayload({
          publicKeyPem: this.#identity.publicKeyPem,
          payload: signedPayload,
          signature,
        });
        if (!verified.ok) return receiptVerdict(false, CODE.RECEIPT_MISMATCH);
      }
      return receiptVerdict(true, null);
    } catch {
      return receiptVerdict(false, CODE.RECEIPT_MISMATCH);
    }
  }

  /** 撤销授权（幂等）；未知 grantId → NOT_FOUND。 */
  revoke({ grantId } = {}) {
    const record = this.#require(grantId);
    if (!record.revoked) {
      record.revoked = true;
      record.revokedAt = this.#now().toISOString();
    }
    return Object.freeze({ grantId: record.id, revoked: true });
  }

  /** 列出授权（可按 messageId 过滤）；只读视图，按创建顺序。 */
  list({ messageId = null } = {}) {
    if (messageId !== null && !isNonEmptyString(messageId)) throw new TypeError('messageId 必须是字符串或 null');
    const result = [];
    for (const grantId of this.#order) {
      const record = this.#grants.get(grantId);
      if (messageId !== null && record.messageId !== messageId) continue;
      result.push(
        Object.freeze({
          id: record.id,
          messageId: record.messageId,
          granteeAgentId: record.granteeAgentId,
          scopes: record.scopes,
          priceCents: record.priceCents,
          issuedAt: record.issuedAt,
          expiresAt: record.expiresAt,
          revoked: record.revoked,
          revokedAt: record.revokedAt,
          usedScopes: Object.freeze([...record.usedScopes]),
        })
      );
    }
    return Object.freeze(result);
  }

  // ── 内部 ────────────────────────────────────────────────────────────────

  #now() {
    const sampled = this.#clock();
    if (!(sampled instanceof Date) || Number.isNaN(sampled.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return sampled;
  }

  #require(grantId) {
    if (!isNonEmptyString(grantId)) throw new TypeError('grantId 必须是非空字符串');
    const record = this.#grants.get(grantId);
    if (!record) throw new ScopedGrantError(`授权不存在: ${grantId}`, CODE.NOT_FOUND);
    return record;
  }

  #grantView(record) {
    return Object.freeze({
      id: record.id,
      messageId: record.messageId,
      granteeAgentId: record.granteeAgentId,
      scopes: record.scopes,
      priceCents: record.priceCents,
      issuedAt: record.issuedAt,
      expiresAt: record.expiresAt,
    });
  }
}
