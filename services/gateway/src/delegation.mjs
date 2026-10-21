// 模块职责：带衰减的委托（AGORA 治理原语）——总 Agent 把某件事委托给子 Agent 时，scope 只能收窄、时限不能变长、信任上限不能放宽、跳数受限，父级撤销可级联。
// 契约来源：contracts/INTERFACES.md §30。
// 关键不变量：①子 domains ⊆ 父 domains；②子 trustCeiling <= 父 trustCeiling（数值越大越宽松，不得放宽）；
//             ③子 expiresAt <= 父 expiresAt；④hopLimit 每层 -1，0 不可再委托；⑤verify 只返回判定不抛异常，effectiveScope 取整条链的交集。
import { randomUUID } from 'node:crypto';

export const DELEGATION_CODE = Object.freeze({
  DELEGATION_DENIED: 'DELEGATION_DENIED',
  SCOPE_ESCALATION: 'SCOPE_ESCALATION',
  EXPIRY_EXCEEDED: 'EXPIRY_EXCEEDED',
  HOP_LIMIT: 'HOP_LIMIT',
  PARENT_REVOKED: 'PARENT_REVOKED',
  TOKEN_INVALID: 'TOKEN_INVALID'
});

const CODE = DELEGATION_CODE;
const CODE_VALUES = new Set(Object.values(CODE));
const MAX_TRUST_LEVEL = 4;
const MAX_CHAIN_DEPTH = 64;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class DelegationError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'DelegationError';
    if (!CODE_VALUES.has(code)) throw new TypeError(`未知 DELEGATION_CODE: ${String(code)}`);
    this.code = code;
  }
}

const normalizeDomains = (value) => {
  if (!Array.isArray(value)) throw new TypeError('domains 必须是数组');
  const unique = [];
  for (const item of value) {
    if (!isNonEmptyString(item)) throw new TypeError('domains 元素必须是非空字符串');
    if (!unique.includes(item)) unique.push(item);
  }
  return Object.freeze(unique);
};

const tokenIdOf = (token) => (isNonEmptyString(token?.id) ? token.id : isNonEmptyString(token?.token) ? token.token : null);

const clampTrust = (value) => {
  if (!Number.isInteger(value)) return MAX_TRUST_LEVEL;
  return Math.min(MAX_TRUST_LEVEL, Math.max(0, value));
};

const earlierIso = (a, b) => (Date.parse(a) <= Date.parse(b) ? a : b);

/** 从父 token（可能是 AgentGateway.issueToken 的返回对象，也可能是上一级委托 token）提取衰减依据 */
function normalizeParentClaims(parentToken) {
  const parentId = tokenIdOf(parentToken);
  if (!parentId || !parentToken || typeof parentToken !== 'object') {
    throw new DelegationError('父 token 缺失或缺少 id', CODE.TOKEN_INVALID);
  }
  if (!isNonEmptyString(parentToken.agentId) || !isNonEmptyString(parentToken.principalUserId)) {
    throw new DelegationError('父 token 缺少 agentId/principalUserId', CODE.TOKEN_INVALID);
  }
  if (!Array.isArray(parentToken.domains) || !Number.isInteger(parentToken.trustCeiling)) {
    throw new DelegationError('父 token 缺少 domains/trustCeiling', CODE.TOKEN_INVALID);
  }
  const expiresMs = Date.parse(parentToken.expiresAt);
  if (!Number.isFinite(expiresMs)) {
    throw new DelegationError('父 token 的 expiresAt 非法', CODE.TOKEN_INVALID);
  }
  let parentDomains;
  try {
    parentDomains = normalizeDomains(parentToken.domains);
  } catch {
    throw new DelegationError('父 token 的 domains 非法', CODE.TOKEN_INVALID);
  }
  return Object.freeze({
    id: parentId,
    agentId: parentToken.agentId,
    principalUserId: parentToken.principalUserId,
    domains: parentDomains,
    trustCeiling: clampTrust(parentToken.trustCeiling),
    expiresAt: new Date(expiresMs).toISOString(),
    hopLimit: Number.isInteger(parentToken.hopLimit) ? parentToken.hopLimit : null
  });
}

export class DelegationService {
  #clock;
  /** @type {Map<string, object>} tokenId -> frozen delegation token */
  #tokens;
  /** @type {Map<string, object>} tokenId -> 父 token 快照（根 token 由网关签发，不在本服务登记） */
  #parents;
  /** @type {Map<string, Set<string>>} parentId -> child tokenId 集合（级联撤销用） */
  #children;
  /** @type {Map<string, 'DIRECT'|'CASCADE'>} tokenId -> 撤销方式 */
  #revoked;

  constructor({ clock = () => new Date() } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#tokens = new Map();
    this.#parents = new Map();
    this.#children = new Map();
    this.#revoked = new Map();
  }

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #rememberParent(claims) {
    if (!this.#tokens.has(claims.id) && !this.#parents.has(claims.id)) {
      this.#parents.set(
        claims.id,
        Object.freeze({
          id: claims.id,
          agentId: claims.agentId,
          principalUserId: claims.principalUserId,
          domains: claims.domains,
          trustCeiling: claims.trustCeiling,
          expiresAt: claims.expiresAt
        })
      );
    }
  }

  #registerChild(parentId, childId) {
    let set = this.#children.get(parentId);
    if (!set) {
      set = new Set();
      this.#children.set(parentId, set);
    }
    set.add(childId);
  }

  /** 父 token 的权威数据：上一级委托 token 优先，其次父快照 */
  #parentClaims(token) {
    if (this.#tokens.has(token.parentId)) return this.#tokens.get(token.parentId);
    return this.#parents.get(token.parentId) ?? null;
  }

  /**
   * 解析整条链的生效 scope（父 ∩ 子，逐级收窄）与委托链。
   * @returns {{scope: object, chain: object[]}}
   */
  #resolve(token, depth = 0) {
    if (depth >= MAX_CHAIN_DEPTH) {
      throw new DelegationError('委托链过深（疑似环）', CODE.TOKEN_INVALID);
    }
    const parent = this.#parentClaims(token);
    if (!parent) throw new DelegationError(`委托链断裂: ${String(token.parentId)}`, CODE.TOKEN_INVALID);

    // 根 token 快照没有 parentId，作为链底
    if (!('parentId' in parent)) {
      return {
        chain: [token],
        scope: Object.freeze({
          // 交集保留父 scope 的域顺序（证明是 parent ∩ child，而非子集拷贝）
          domains: Object.freeze(parent.domains.filter((domain) => token.domains.includes(domain))),
          trustCeiling: Math.min(parent.trustCeiling, token.trustCeiling),
          expiresAt: earlierIso(parent.expiresAt, token.expiresAt),
          hopLimit: token.hopLimit
        })
      };
    }

    // 上一级是委托 token：先算出父的生效 scope，再与本级求交（effectiveScope = 整条链的交集）
    const parentResolved = this.#resolve(parent, depth + 1);
    const domains = parentResolved.scope.domains.filter((domain) => token.domains.includes(domain));
    return {
      chain: [...parentResolved.chain, token],
      scope: Object.freeze({
        domains: Object.freeze(domains),
        trustCeiling: Math.min(parentResolved.scope.trustCeiling, token.trustCeiling),
        expiresAt: earlierIso(parentResolved.scope.expiresAt, token.expiresAt),
        hopLimit: token.hopLimit
      })
    };
  }

  #validateStoredToken(token) {
    if (
      !token ||
      typeof token !== 'object' ||
      !isNonEmptyString(token.id) ||
      !isNonEmptyString(token.agentId) ||
      !isNonEmptyString(token.principalUserId) ||
      !Array.isArray(token.domains) ||
      !Number.isInteger(token.trustCeiling) ||
      !isNonEmptyString(token.expiresAt) ||
      !Number.isInteger(token.hopLimit) ||
      !isNonEmptyString(token.parentId)
    ) {
      throw new DelegationError('委托 token 结构非法', CODE.TOKEN_INVALID);
    }
  }

  /**
   * 创建收窄后的子 token。
   * 跳数语义：子 hopLimit = min(请求 hopLimit, 父剩余跳数) - 1（每层 -1）；父为根 token 时以其请求值作为预算。
   * 因而 hopLimit=1 得到 hopLimit=0 的叶子（可验证但不可再委托）；hopLimit=2 可再委托一层，孙 token 为 0。
   */
  delegate({ parentToken, childAgentId, principalUserId, domains, trustCeiling, ttlMs, hopLimit = 1 } = {}) {
    const parent = normalizeParentClaims(parentToken);
    this.#rememberParent(parent);
    const parentId = parent.id;

    if (this.#revoked.has(parentId)) {
      throw new DelegationError(`父 token 已被撤销: ${parentId}`, CODE.PARENT_REVOKED);
    }
    if (Date.parse(parent.expiresAt) <= this.#now().getTime()) {
      throw new DelegationError(`父 token 已过期: ${parentId}`, CODE.TOKEN_INVALID);
    }

    if (!isNonEmptyString(childAgentId) || !isNonEmptyString(principalUserId)) {
      throw new DelegationError('委托必须有 childAgentId/principalUserId', CODE.DELEGATION_DENIED);
    }
    if (principalUserId !== parent.principalUserId) {
      // 委托不能换 principal（否则等价于冒充另一个用户）
      throw new DelegationError('委托不得更换 principalUserId', CODE.DELEGATION_DENIED);
    }

    // ① domains 不得扩张
    let childDomains;
    if (domains === undefined || domains === null) {
      childDomains = parent.domains;
    } else {
      try {
        childDomains = normalizeDomains(domains);
      } catch {
        throw new DelegationError('子 token 的 domains 非法', CODE.DELEGATION_DENIED);
      }
    }
    const escalatedDomain = childDomains.find((domain) => !parent.domains.includes(domain));
    if (escalatedDomain) {
      throw new DelegationError(
        `域名扩张被拒绝: ${escalatedDomain} ∉ 父 domains [${parent.domains.join(', ')}]`,
        CODE.SCOPE_ESCALATION
      );
    }

    // ② 信任上限不得放宽（数值越大越宽松）
    let childTrust;
    if (trustCeiling === undefined || trustCeiling === null) {
      childTrust = parent.trustCeiling;
    } else {
      if (!Number.isInteger(trustCeiling) || trustCeiling < 0 || trustCeiling > MAX_TRUST_LEVEL) {
        throw new DelegationError('trustCeiling 必须是 0..4 的整数', CODE.DELEGATION_DENIED);
      }
      childTrust = trustCeiling;
    }
    if (childTrust > parent.trustCeiling) {
      throw new DelegationError(
        `信任上限放宽被拒绝: child=${childTrust} > parent=${parent.trustCeiling}`,
        CODE.SCOPE_ESCALATION
      );
    }

    // ③ 时限不得长于父
    const now = this.#now();
    let expiresAt;
    if (ttlMs === undefined || ttlMs === null) {
      expiresAt = parent.expiresAt; // 不传即继承父的剩余时限（不得延长）
    } else {
      if (!Number.isFinite(ttlMs) || ttlMs < 0) {
        throw new DelegationError('ttlMs 必须是非负毫秒数', CODE.DELEGATION_DENIED);
      }
      expiresAt = new Date(now.getTime() + Math.trunc(ttlMs)).toISOString();
    }
    if (Date.parse(expiresAt) > Date.parse(parent.expiresAt)) {
      throw new DelegationError(
        `委托时限超过父 token: ${expiresAt} > ${parent.expiresAt}`,
        CODE.EXPIRY_EXCEEDED
      );
    }

    // ④ 跳数：父剩余跳数（根 token 以其请求值为预算）每层 -1，减到 0 以下即 HOP_LIMIT
    if (!Number.isInteger(hopLimit) || hopLimit < 1) {
      throw new DelegationError(`hopLimit 必须 ≥1 的整数，收到 ${String(hopLimit)}`, CODE.HOP_LIMIT);
    }
    const parentHopBudget = Number.isInteger(parent.hopLimit) ? parent.hopLimit : hopLimit;
    const childHopLimit = Math.min(hopLimit, parentHopBudget) - 1;
    if (childHopLimit < 0) {
      throw new DelegationError(`跳数已耗尽（parent hopLimit=${parentHopBudget}）`, CODE.HOP_LIMIT);
    }

    const token = Object.freeze({
      id: `del-${randomUUID()}`,
      agentId: childAgentId,
      principalUserId,
      domains: childDomains,
      trustCeiling: childTrust,
      issuedAt: now.toISOString(),
      expiresAt,
      hopLimit: childHopLimit,
      parentId
    });
    this.#tokens.set(token.id, token);
    this.#registerChild(parentId, token.id);
    return token;
  }

  /**
   * 验签/校验委托 token（只返回判定，不抛业务异常）。
   * @returns {Readonly<{ok:boolean, code:string|null, effectiveScope:object|null, chain:ReadonlyArray<object>}>}
   */
  verify({ token, agentId = null } = {}) {
    const fail = (code, chain = []) =>
      Object.freeze({
        ok: false,
        code,
        effectiveScope: null,
        chain: Object.freeze([...chain])
      });

    const tokenId = isNonEmptyString(token) ? token : tokenIdOf(token);
    if (!tokenId) return fail(CODE.TOKEN_INVALID);
    const stored = this.#tokens.get(tokenId);
    if (!stored) return fail(CODE.TOKEN_INVALID); // 只认本服务签发过的 token，外部伪造 id 一律无效
    try {
      this.#validateStoredToken(stored);
    } catch {
      return fail(CODE.TOKEN_INVALID);
    }

    // 持有者绑定：token.agentId 必须等于调用方 agentId
    if (isNonEmptyString(agentId) && agentId !== stored.agentId) {
      return fail(CODE.TOKEN_INVALID);
    }

    // 撤销：本 token 被级联撤销 => PARENT_REVOKED；被直接撤销 => TOKEN_INVALID
    const revokedReason = this.#revoked.get(tokenId);
    if (revokedReason === 'CASCADE') return fail(CODE.PARENT_REVOKED);
    if (revokedReason === 'DIRECT') return fail(CODE.TOKEN_INVALID);

    // 过期
    if (Date.parse(stored.expiresAt) <= this.#now().getTime()) {
      return fail(CODE.TOKEN_INVALID);
    }

    let resolved;
    try {
      resolved = this.#resolve(stored);
    } catch {
      return fail(CODE.TOKEN_INVALID);
    }

    return Object.freeze({
      ok: true,
      code: null,
      effectiveScope: resolved.scope,
      chain: Object.freeze(resolved.chain)
    });
  }

  /** 撤销 token；cascade=true（默认）时级联撤销全部后代（后代标记为 CASCADE，verify 返回 PARENT_REVOKED） */
  revoke({ tokenId, cascade = true } = {}) {
    if (!isNonEmptyString(tokenId)) throw new TypeError('revoke: tokenId 必填');
    const target = this.#tokens.get(tokenId);
    if (!target) return Object.freeze({ revoked: Object.freeze([]) });

    const revoked = [];
    if (!this.#revoked.has(tokenId)) {
      this.#revoked.set(tokenId, 'DIRECT');
    }
    revoked.push(tokenId);

    if (cascade === true) {
      const queue = [...(this.#children.get(tokenId) ?? [])];
      while (queue.length > 0) {
        const childId = queue.shift();
        if (childId === tokenId) continue;
        if (!this.#revoked.has(childId)) this.#revoked.set(childId, 'CASCADE');
        revoked.push(childId);
        for (const grandChild of this.#children.get(childId) ?? []) queue.push(grandChild);
      }
    }
    return Object.freeze({ revoked: Object.freeze(revoked) });
  }

  /**
   * 委托链（根在前的委托 token 序列，不含网关签发的根 token 快照）；未知 token 返回空数组。
   * @returns {ReadonlyArray<object>}
   */
  chainOf(tokenId) {
    const token = isNonEmptyString(tokenId) ? this.#tokens.get(tokenId) : null;
    if (!token) return Object.freeze([]);
    try {
      return Object.freeze(this.#resolve(token).chain);
    } catch {
      return Object.freeze([]);
    }
  }
}
