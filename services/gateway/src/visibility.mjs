// 模块职责：读侧可见域（Visibility）——权限不只是"能不能做"，还包括"以谁的视角、能看见哪些字段"。
// 契约来源：contracts/INTERFACES.md §13。
// 关键不变量：①判定顺序冻结：scope 过期 → domain 未授权 → trustCeiling 超限 → 按冻结表格裁剪；②MEMORY 域任何档都只回 agentId/lastAt/count/summary，永不返回原始内容；
//             ③裁决只返回 decision/code，不抛业务异常（DENY 也是一条可审计的判定）。
export const DOMAIN = Object.freeze({
  CONTACT: 'contact',
  GROUP: 'group',
  TASK: 'task',
  SCHEDULE: 'schedule',
  MEMORY: 'memory'
});

export const VISIBILITY_CODE = Object.freeze({
  VISIBILITY_DENIED: 'VISIBILITY_DENIED',
  DOMAIN_NOT_GRANTED: 'DOMAIN_NOT_GRANTED',
  TRUST_CEILING_EXCEEDED: 'TRUST_CEILING_EXCEEDED',
  SCOPE_EXPIRED: 'SCOPE_EXPIRED',
  RESOURCE_NOT_FOUND: 'RESOURCE_NOT_FOUND'
});

const CODE = VISIBILITY_CODE;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

/** 全字段通配标记（SELF/FAMILY 档） */
const ALL_FIELDS = Object.freeze(['*']);

/** 字段裁剪表（契约 §13 冻结） */
const TRUST_FIELD_TABLE = Object.freeze({
  0: ALL_FIELDS, // SELF
  1: ALL_FIELDS, // FAMILY
  2: Object.freeze(['id', 'displayName', 'tags', 'detail', 'endpoint']), // TRUSTED
  3: Object.freeze(['id', 'displayName', 'tags']), // KNOWN
  4: Object.freeze(['id', 'exists']) // STRANGER
});

/** MEMORY 域任何档：只回协调摘要字段，永不返回原始内容 */
const MEMORY_FIELDS = Object.freeze(['agentId', 'lastAt', 'count', 'summary']);

const DEFAULT_SCOPE_TTL_MS = 15 * 60e3;

const normalizeStringArray = (value) => {
  const unique = [];
  for (const item of Array.isArray(value) ? value : []) {
    if (isNonEmptyString(item) && !unique.includes(item)) unique.push(item);
  }
  return Object.freeze(unique);
};

const clampTrust = (value) => {
  if (!Number.isInteger(value)) return 4;
  return Math.min(4, Math.max(0, value));
};

export class VisibilityError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'VisibilityError';
    this.code = code;
  }
}

export class VisibilityGate {
  #trustMatrix;
  #clock;

  constructor({ trustMatrix = null, clock = () => new Date() } = {}) {
    this.#trustMatrix = trustMatrix;
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
  }

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  /** 构建短期读授权 scope（与网关 token 同构：principal + agent + domains + trustCeiling + TTL） */
  buildScope({ principalUserId, agentId, domains = [], trustCeiling = 4, ttlMs = DEFAULT_SCOPE_TTL_MS } = {}) {
    if (!isNonEmptyString(principalUserId) || !isNonEmptyString(agentId)) {
      throw new VisibilityError('buildScope: principalUserId/agentId 必填', CODE.VISIBILITY_DENIED);
    }
    if (!Number.isFinite(ttlMs) || ttlMs < 0) {
      throw new VisibilityError('buildScope: ttlMs 必须是非负毫秒数', CODE.VISIBILITY_DENIED);
    }
    const now = this.#now();
    return Object.freeze({
      principalUserId,
      agentId,
      domains: normalizeStringArray(domains),
      trustCeiling: clampTrust(trustCeiling),
      issuedAt: now.toISOString(),
      expiresAt: new Date(now.getTime() + Math.trunc(ttlMs)).toISOString()
    });
  }

  #trustLevelOf(scope, resource) {
    if (Number.isInteger(resource?.trustLevel)) return clampTrust(resource.trustLevel);
    if (this.#trustMatrix && resource && isNonEmptyString(resource.id) && scope && isNonEmptyString(scope.principalUserId)) {
      return this.#trustMatrix.levelOf({ userId: scope.principalUserId, targetId: resource.id });
    }
    return 4;
  }

  #deny(code, domain, extra = {}) {
    return Object.freeze({
      decision: 'DENY',
      code,
      domain: domain ?? null,
      allowedFields: Object.freeze([]),
      redactedFields: Object.freeze([]),
      ...extra
    });
  }

  /**
   * 裁决（不抛业务异常）。
   * @returns {Readonly<{decision:'ALLOW'|'REDACT'|'DENY', code:string|null, domain:string|null, allowedFields:ReadonlyArray<string>, redactedFields:ReadonlyArray<string>}>}
   */
  evaluate({ scope, resource, fields = null } = {}) {
    if (!scope || typeof scope !== 'object' || !isNonEmptyString(scope.principalUserId) || !isNonEmptyString(scope.agentId)) {
      return this.#deny(CODE.VISIBILITY_DENIED, null);
    }
    if (!resource || typeof resource !== 'object' || !isNonEmptyString(resource.domain)) {
      return this.#deny(CODE.RESOURCE_NOT_FOUND, resource?.domain ?? null);
    }

    // ① scope 过期
    const expiresMs = Date.parse(scope.expiresAt);
    if (!Number.isFinite(expiresMs)) return this.#deny(CODE.VISIBILITY_DENIED, resource.domain);
    if (expiresMs <= this.#now().getTime()) return this.#deny(CODE.SCOPE_EXPIRED, resource.domain);

    // ② 域未授权
    const domains = Array.isArray(scope.domains) ? scope.domains : [];
    if (!domains.includes(resource.domain)) return this.#deny(CODE.DOMAIN_NOT_GRANTED, resource.domain);

    // ③ 信任上限（数值越大信任越低：resource.trustLevel > ceiling 即越权）
    const trustLevel = this.#trustLevelOf(scope, resource);
    const ceiling = Number.isInteger(scope.trustCeiling) ? Math.min(4, Math.max(0, scope.trustCeiling)) : 4;
    if (trustLevel > ceiling) {
      return this.#deny(CODE.TRUST_CEILING_EXCEEDED, resource.domain, { trustLevel, trustCeiling: ceiling });
    }

    // ④ 按冻结表格裁剪
    const allowedFields = resource.domain === DOMAIN.MEMORY ? MEMORY_FIELDS : TRUST_FIELD_TABLE[trustLevel] ?? TRUST_FIELD_TABLE[4];
    const candidateKeys =
      fields && typeof fields === 'object'
        ? Object.keys(fields)
        : Array.isArray(resource.fields)
          ? resource.fields.filter(isNonEmptyString)
          : [];
    const redactedFields =
      allowedFields === ALL_FIELDS ? [] : candidateKeys.filter((key) => !allowedFields.includes(key));
    const decision = redactedFields.length > 0 ? 'REDACT' : trustLevel === 4 ? 'REDACT' : 'ALLOW';

    return Object.freeze({
      decision,
      code: null,
      domain: resource.domain,
      allowedFields,
      redactedFields: Object.freeze(redactedFields)
    });
  }

  /**
   * 投影：DENY 返回空结果；否则只保留 allowedFields，并给出 redactedFields。
   * @returns {Readonly<{decision, code, allowedFields, redactedFields, projected}>}
   */
  project({ scope, resource, fields = {} } = {}) {
    const evaluation = this.evaluate({ scope, resource, fields });
    if (evaluation.decision === 'DENY') {
      return Object.freeze({
        decision: 'DENY',
        code: evaluation.code,
        allowedFields: evaluation.allowedFields,
        redactedFields: evaluation.redactedFields,
        projected: Object.freeze({})
      });
    }
    const allowAll = evaluation.allowedFields === ALL_FIELDS;
    const projected = {};
    const redactedFields = [];
    for (const [key, value] of Object.entries(fields ?? {})) {
      if (allowAll || evaluation.allowedFields.includes(key)) projected[key] = value;
      else redactedFields.push(key);
    }
    const decision = redactedFields.length > 0 ? 'REDACT' : 'ALLOW';
    return Object.freeze({
      decision,
      code: null,
      allowedFields: evaluation.allowedFields,
      redactedFields: Object.freeze(redactedFields),
      projected: Object.freeze(projected)
    });
  }
}
