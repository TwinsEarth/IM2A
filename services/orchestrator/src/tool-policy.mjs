// 模块职责：细粒度工具审批策略（每个 Agent × 每个 Skill 一条策略）——审批形态的判定层。
// 契约来源：contracts/INTERFACES.md §19。
// 关键不变量：①优先级冻结：skillId 精确 > agent 通配 '*' > 无策略；②判定顺序冻结：信任不足 DENY（不可批）→ 金额超限 APPROVE（可批）→ 双人审批缺失 APPROVE → 策略默认；
//             ③与 §3 CapabilityGate 分工不得混淆：Gate 判权限（能不能调/信任够不够/预算够不够），本模块判审批形态，ApprovalEngine 执行审批。顺序：Gate → ToolPolicy → ApprovalEngine。
export const POLICY_DECISION = Object.freeze({ AUTO: 'AUTO', APPROVE: 'APPROVE', DENY: 'DENY' });

export const TOOL_POLICY_CODE = Object.freeze({
  POLICY_MATCHED: 'POLICY_MATCHED',
  SKILL_NOT_COVERED: 'SKILL_NOT_COVERED',
  POLICY_NOT_FOUND: 'POLICY_NOT_FOUND',
  TRUST_TOO_LOW: 'TRUST_TOO_LOW',
  AMOUNT_EXCEEDED: 'AMOUNT_EXCEEDED',
  NEEDS_SECOND_APPROVER: 'NEEDS_SECOND_APPROVER'
});

const CODE = TOOL_POLICY_CODE;
const DECISION_VALUES = new Set(Object.values(POLICY_DECISION));
const WILDCARD_SKILL = '*';
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

const clampTrustLevel = (value) => {
  if (!Number.isInteger(value)) return 4;
  return Math.min(4, Math.max(0, value));
};

const normalizeAmount = (value) => (Number.isFinite(value) && value > 0 ? Math.trunc(value) : 0);

export class ToolPolicyError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'ToolPolicyError';
    if (!Object.values(CODE).includes(code)) throw new TypeError(`未知 TOOL_POLICY_CODE: ${String(code)}`);
    this.code = code;
  }
}

export class ToolPolicy {
  #clock;
  /** @type {Map<string, Map<string, object>>} agentId -> skillId -> frozen policy */
  #policies;

  constructor({ clock = () => new Date() } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#policies = new Map();
  }

  #nowIso() {
    const sampled = this.#clock();
    return (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
  }

  /**
   * 写入/覆盖策略（同一 agentId+skillId 覆盖语义）。
   * `skillId='*'` 表示该 Agent 的通配策略。
   */
  setPolicy({
    agentId,
    skillId,
    decision,
    maxAmountCents = null,
    trustLevelMax = 4,
    requiresDoubleApproval = false,
    note = null
  } = {}) {
    if (!isNonEmptyString(agentId)) throw new TypeError('setPolicy: agentId 必填');
    if (!isNonEmptyString(skillId)) throw new TypeError('setPolicy: skillId 必填');
    if (!DECISION_VALUES.has(decision)) {
      throw new TypeError(`setPolicy: decision 必须是 ${[...DECISION_VALUES].join('/')}`);
    }
    if (maxAmountCents !== null && (!Number.isInteger(maxAmountCents) || maxAmountCents < 0)) {
      throw new TypeError('setPolicy: maxAmountCents 必须是 null 或非负整数（分）');
    }
    const policy = Object.freeze({
      agentId,
      skillId,
      decision,
      maxAmountCents,
      trustLevelMax: clampTrustLevel(trustLevelMax),
      requiresDoubleApproval: requiresDoubleApproval === true,
      note: note ?? null,
      updatedAt: this.#nowIso()
    });
    let bucket = this.#policies.get(agentId);
    if (!bucket) {
      bucket = new Map();
      this.#policies.set(agentId, bucket);
    }
    bucket.set(skillId, policy);
    return policy;
  }

  /** @returns {boolean} 不存在的策略返回 false（契约 §19 签名即为 boolean） */
  removePolicy({ agentId, skillId } = {}) {
    const bucket = this.#policies.get(agentId);
    if (!bucket || !bucket.has(skillId)) return false;
    bucket.delete(skillId);
    if (bucket.size === 0) this.#policies.delete(agentId);
    return true;
  }

  /** @returns {ReadonlyArray<object>} 按 skillId 升序；未知 Agent 返回空数组 */
  policiesOf(agentId) {
    const bucket = this.#policies.get(agentId);
    if (!bucket) return Object.freeze([]);
    return Object.freeze([...bucket.values()].sort((a, b) => (a.skillId < b.skillId ? -1 : a.skillId > b.skillId ? 1 : 0)));
  }

  /** 附加方法：精确取一条策略（getPolicy 语义），不存在 => POLICY_NOT_FOUND */
  getPolicy({ agentId, skillId } = {}) {
    const policy = this.#policies.get(agentId)?.get(skillId);
    if (!policy) {
      throw new ToolPolicyError(`策略不存在: ${String(agentId)} / ${String(skillId)}`, CODE.POLICY_NOT_FOUND);
    }
    return policy;
  }

  /**
   * 判定审批形态（不抛业务异常；DENY 也是一条可审计结论）。
   * @returns {Readonly<{decision:'AUTO'|'APPROVE'|'DENY', code:string, policy:object|null, reasons:ReadonlyArray<string>}>}
   */
  evaluate({ agentId, skillId, amountCents = 0, trustLevel = 4, hasSecondApprover = false } = {}) {
    const bucket = this.#policies.get(agentId);
    const exact = bucket?.get(skillId) ?? null;
    const wildcard = bucket?.get(WILDCARD_SKILL) ?? null;
    const policy = exact ?? wildcard ?? null;

    if (!policy) {
      return Object.freeze({
        decision: POLICY_DECISION.AUTO,
        code: CODE.SKILL_NOT_COVERED,
        policy: null,
        reasons: Object.freeze(['no policy matched (exact or wildcard)'])
      });
    }

    const reasons = [];
    const amount = normalizeAmount(amountCents);
    const trust = clampTrustLevel(trustLevel);

    // 信任不可批：低于该策略允许的信任档 => DENY（金额/双人审批都不再检查）
    if (trust > policy.trustLevelMax) {
      reasons.push(`trustLevel ${trust} > trustLevelMax ${policy.trustLevelMax}`);
      return Object.freeze({
        decision: POLICY_DECISION.DENY,
        code: CODE.TRUST_TOO_LOW,
        policy,
        reasons: Object.freeze(reasons)
      });
    }

    // 金额可批：超过该策略上限 => APPROVE（绝不是 DENY）
    if (policy.maxAmountCents !== null && amount > policy.maxAmountCents) {
      reasons.push(`amountCents ${amount} > maxAmountCents ${policy.maxAmountCents}`);
      return Object.freeze({
        decision: POLICY_DECISION.APPROVE,
        code: CODE.AMOUNT_EXCEEDED,
        policy,
        reasons: Object.freeze(reasons)
      });
    }

    // 双人审批缺失 => APPROVE（等人补齐第二审批人）
    if (policy.requiresDoubleApproval && hasSecondApprover !== true) {
      reasons.push('requiresDoubleApproval=true but hasSecondApprover=false');
      return Object.freeze({
        decision: POLICY_DECISION.APPROVE,
        code: CODE.NEEDS_SECOND_APPROVER,
        policy,
        reasons: Object.freeze(reasons)
      });
    }

    reasons.push(`policy matched: skillId=${policy.skillId}`);
    return Object.freeze({
      decision: policy.decision,
      code: CODE.POLICY_MATCHED,
      policy,
      reasons: Object.freeze(reasons)
    });
  }
}
