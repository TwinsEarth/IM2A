// 模块职责：五档信任矩阵（SELF/FAMILY/TRUSTED/KNOWN/STRANGER），熟人社交的权限数据源。
// 契约来源：contracts/INTERFACES.md §1。
// 关键不变量：①默认档位永远是 STRANGER(4)；②过期授权视同撤销（惰性撤销并留审计）；③一切授权变更只追加审计、绝不改写历史。
export const TRUST_LEVELS = Object.freeze({ SELF: 0, FAMILY: 1, TRUSTED: 2, KNOWN: 3, STRANGER: 4 });
export const TRUST_NAMES = Object.freeze(['SELF', 'FAMILY', 'TRUSTED', 'KNOWN', 'STRANGER']);

const MIN_LEVEL = 0;
const MAX_LEVEL = TRUST_NAMES.length - 1;
const SEP = '\u0000';

/** 非空字符串（避免 ''、纯空白、null 被当成合法主体） */
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class TrustZoneError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'TrustZoneError';
    this.code = code;
  }
}

export class TrustMatrix {
  #clock;
  /** @type {Map<string, Map<string, object>>} userId -> targetId -> 内部授权记录（含 grantedBy，不对外暴露） */
  #grants;
  /** @type {ReadonlyArray<object>} 只追加审计事件 */
  #history;

  constructor({ clock = () => new Date() } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#grants = new Map();
    this.#history = [];
  }

  // ── 内部工具 ────────────────────────────────────────────────────────────────

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #audit(action, actor, target, decision, detail) {
    const event = Object.freeze({
      at: this.#nowIso(),
      actor: actor ?? null,
      action,
      target: target ?? null,
      decision,
      detail: Object.freeze({ ...detail })
    });
    this.#history.push(event);
    return event;
  }

  #validateLevel(level) {
    if (!Number.isInteger(level) || level < MIN_LEVEL || level > MAX_LEVEL) {
      throw new TrustZoneError(
        `level 必须是 ${MIN_LEVEL}..${MAX_LEVEL} 的整数，收到 ${String(level)}`,
        'INVALID_LEVEL'
      );
    }
  }

  #validateEndpoints(userId, targetId) {
    if (!isNonEmptyString(userId) || !isNonEmptyString(targetId)) {
      throw new TrustZoneError('userId/targetId 缺失：无法定位信任关系（NOT_FOUND）', 'NOT_FOUND');
    }
  }

  #isExpired(record, nowMs) {
    if (record.expiresAt === null) return false;
    const expiresMs = Date.parse(record.expiresAt);
    return Number.isFinite(expiresMs) && expiresMs <= nowMs;
  }

  /** 惰性撤销：删除记录 + 追加审计。过期即视同撤销。 */
  #expire(record) {
    const bucket = this.#grants.get(record.userId);
    if (bucket) {
      bucket.delete(record.targetId);
      if (bucket.size === 0) this.#grants.delete(record.userId);
    }
    this.#audit('EXPIRE', record.userId, record.targetId, 'REVOKED', {
      level: record.level,
      name: record.name,
      expiresAt: record.expiresAt
    });
  }

  // ── 公开 API（契约 §1）────────────────────────────────────────────────────

  /**
   * 授权/覆盖授权。同一对 (userId,targetId) 重复 grant 为覆盖语义并追加审计。
   * @returns {Readonly<{userId:string,targetId:string,level:number,name:string,grantedAt:string,expiresAt:string|null}>}
   */
  grant({ userId, targetId, level, expiresAt = null, grantedBy = userId } = {}) {
    this.#validateEndpoints(userId, targetId);
    this.#validateLevel(level);
    if (level === TRUST_LEVELS.SELF && userId !== targetId) {
      throw new TrustZoneError('SELF 档只允许 userId === targetId', 'INVALID_LEVEL');
    }

    let normalizedExpiresAt = null;
    if (expiresAt !== null && expiresAt !== undefined) {
      const parsed = Date.parse(expiresAt);
      if (!Number.isFinite(parsed)) {
        throw new TrustZoneError('expiresAt 必须是 ISO-8601 字符串或 null', 'INVALID_LEVEL');
      }
      normalizedExpiresAt = new Date(parsed).toISOString();
    }

    let bucket = this.#grants.get(userId);
    if (!bucket) {
      bucket = new Map();
      this.#grants.set(userId, bucket);
    }
    const previous = bucket.get(targetId) ?? null;
    const grantedAt = this.#nowIso();
    const internal = Object.freeze({
      userId,
      targetId,
      level,
      name: TRUST_NAMES[level],
      grantedAt,
      expiresAt: normalizedExpiresAt,
      grantedBy: grantedBy ?? userId
    });
    bucket.set(targetId, internal);

    this.#audit(previous ? 'GRANT_OVERWRITE' : 'GRANT', internal.grantedBy, targetId, 'ALLOW', {
      userId,
      level,
      name: internal.name,
      previousLevel: previous?.level ?? null,
      expiresAt: internal.expiresAt
    });

    return Object.freeze({
      userId,
      targetId,
      level,
      name: internal.name,
      grantedAt,
      expiresAt: normalizedExpiresAt
    });
  }

  /** @returns {boolean} 不存在返回 false（不抛错，契约逐字） */
  revoke({ userId, targetId } = {}) {
    const bucket = this.#grants.get(userId);
    const existed = Boolean(bucket && bucket.has(targetId));
    if (existed) {
      const record = bucket.get(targetId);
      bucket.delete(targetId);
      if (bucket.size === 0) this.#grants.delete(userId);
      this.#audit('REVOKE', userId, targetId, 'REVOKED', {
        level: record.level,
        name: record.name,
        expiresAt: record.expiresAt
      });
    } else {
      this.#audit('REVOKE', userId ?? null, targetId ?? null, 'NOT_FOUND', {});
    }
    return existed;
  }

  /** @returns {number} 缺省/过期 => STRANGER(4)；过期时惰性撤销并留审计 */
  levelOf({ userId, targetId } = {}) {
    if (!isNonEmptyString(userId) || !isNonEmptyString(targetId)) return TRUST_LEVELS.STRANGER;
    const bucket = this.#grants.get(userId);
    if (!bucket) return TRUST_LEVELS.STRANGER;
    const record = bucket.get(targetId);
    if (!record) return TRUST_LEVELS.STRANGER;

    if (this.#isExpired(record, this.#now().getTime())) {
      this.#expire(record);
      return TRUST_LEVELS.STRANGER;
    }
    return record.level;
  }

  /** 是否“至少”达到给定信任档：档位数值越小越信任，故 currentLevel <= level 即为真 */
  atLeast({ userId, targetId, level } = {}) {
    this.#validateLevel(level);
    return this.levelOf({ userId, targetId }) <= level;
  }

  /** @returns {ReadonlyArray<{targetId:string,level:number,name:string,grantedAt:string,expiresAt:string|null}>} */
  list(userId) {
    const out = [];
    if (!isNonEmptyString(userId)) return Object.freeze(out);
    const bucket = this.#grants.get(userId);
    if (!bucket) return Object.freeze(out);
    const nowMs = this.#now().getTime();
    for (const record of [...bucket.values()]) {
      if (this.#isExpired(record, nowMs)) {
        this.#expire(record);
        continue;
      }
      out.push(
        Object.freeze({
          targetId: record.targetId,
          level: record.level,
          name: record.name,
          grantedAt: record.grantedAt,
          expiresAt: record.expiresAt
        })
      );
    }
    out.sort((a, b) => (a.targetId < b.targetId ? -1 : a.targetId > b.targetId ? 1 : 0));
    return Object.freeze(out);
  }

  /** @returns {ReadonlyArray<object>} 审计事件（只追加） */
  history() {
    return Object.freeze([...this.#history]);
  }
}
