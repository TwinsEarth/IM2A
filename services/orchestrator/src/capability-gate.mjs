// 模块职责：代码层权限门控（任何 Skill 调用发出前必须过此关）+ 按天预算 DailyBudget。
// 契约来源：contracts/INTERFACES.md §3。
// 关键不变量：①DENY 优先于 REQUIRE_APPROVAL 优先于 ALLOW；②判定必须严格按 §3 的 7 步顺序短路；
//             ③每次 evaluate 无论放行与否都产生一条审计事件；④evaluate() 永不因业务拒绝抛异常，只有 assertAllowed() 抛 GateDeniedError。
export const DECISION = Object.freeze({ ALLOW: 'ALLOW', REQUIRE_APPROVAL: 'REQUIRE_APPROVAL', DENY: 'DENY' });

export const GATE_CODE = Object.freeze({
  ALLOWED: 'ALLOWED',
  AGENT_UNKNOWN: 'AGENT_UNKNOWN',
  SKILL_NOT_ALLOWED: 'SKILL_NOT_ALLOWED',
  TRUST_TOO_LOW: 'TRUST_TOO_LOW',
  BUDGET_EXCEEDED: 'BUDGET_EXCEEDED',
  // 预留：生产按月预算，Phase2 启用；§3 的 7 步判定顺序中没有对应步骤，门控当前不产生此码。
  MONTHLY_CAP: 'MONTHLY_CAP',
  AMOUNT_THRESHOLD: 'AMOUNT_THRESHOLD',
  DESTRUCTIVE_ACTION: 'DESTRUCTIVE_ACTION'
});

const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class GateDeniedError extends Error {
  constructor(message, { code, decision = DECISION.DENY, checks = [], result = null } = {}) {
    super(message);
    this.name = 'GateDeniedError';
    this.code = code;
    this.decision = decision;
    this.checks = checks;
    this.result = result;
    this.audit = result?.audit ?? null;
  }
}

/**
 * 按天预算。cap 由调用方显式 setCap 注入（契约澄清：门控不自行从 registry 推导额度）。
 * 日切以注入时钟的 UTC 日期为准，保证超时类测试可确定性验证。
 */
export class DailyBudget {
  #clock;
  /** @type {Map<string, {day:string, spent:number, cap:number}>} */
  #state;

  constructor({ clock = () => new Date() } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#state = new Map();
  }

  #today() {
    const sampled = this.#clock();
    const now = sampled instanceof Date ? sampled : new Date(sampled);
    return now.toISOString().slice(0, 10);
  }

  #ensure(agentId) {
    let state = this.#state.get(agentId);
    const day = this.#today();
    if (!state) {
      state = { day, spent: 0, cap: 0 };
      this.#state.set(agentId, state);
      return state;
    }
    if (state.day !== day) {
      // 自然日切换：额度自动恢复
      state.day = day;
      state.spent = 0;
    }
    return state;
  }

  /** @returns {number} 当日剩余额度（分）；默认 cap 0 => 不允许花钱 */
  remaining(agentId) {
    if (!isNonEmptyString(agentId)) return 0;
    const state = this.#ensure(agentId);
    return Math.max(0, state.cap - state.spent);
  }

  /** 超限不扣减；非法金额（负数/NaN）视为 0，不产生消费 */
  trySpend(agentId, cents) {
    const state = this.#ensure(isNonEmptyString(agentId) ? agentId : '');
    const amount = Number.isFinite(cents) && cents > 0 ? Math.trunc(cents) : 0;
    const available = Math.max(0, state.cap - state.spent);
    if (amount > available) return Object.freeze({ ok: false, remaining: available });
    state.spent += amount;
    return Object.freeze({ ok: true, remaining: available - amount });
  }

  /** @returns {number} 当日 cap（分） */
  capOf(agentId) {
    if (!isNonEmptyString(agentId)) return 0;
    return this.#ensure(agentId).cap;
  }

  setCap(agentId, cents) {
    if (!isNonEmptyString(agentId)) throw new TypeError('setCap: agentId 必须是非空字符串');
    if (!Number.isFinite(cents) || cents < 0) {
      throw new TypeError('setCap: cents 必须是非负整数（分）');
    }
    const state = this.#ensure(agentId);
    state.cap = Math.trunc(cents);
    return state.cap;
  }

  /** 清空当日已消费（cap 保留），返回清空后的剩余额度 */
  reset(agentId) {
    if (!isNonEmptyString(agentId)) return 0;
    const state = this.#ensure(agentId);
    state.spent = 0;
    return Math.max(0, state.cap - state.spent);
  }
}

export class CapabilityGate {
  #registry;
  #trustMatrix;
  #budget;
  #clock;
  #onAudit;

  constructor({ registry, trustMatrix, budget = null, clock = () => new Date(), onAudit = () => {} } = {}) {
    if (!registry || typeof registry.get !== 'function') throw new TypeError('CapabilityGate: registry 必填');
    if (!trustMatrix || typeof trustMatrix.levelOf !== 'function') {
      throw new TypeError('CapabilityGate: trustMatrix 必填');
    }
    this.#registry = registry;
    this.#trustMatrix = trustMatrix;
    this.#budget = budget;
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#onAudit = typeof onAudit === 'function' ? onAudit : () => {};
  }

  #nowIso() {
    const sampled = this.#clock();
    const now = sampled instanceof Date ? sampled : new Date(sampled);
    return now.toISOString();
  }

  #finish({ agentId, userId, targetId, skillId, amountCents, decision, code, checks }) {
    const audit = Object.freeze({
      at: this.#nowIso(),
      actor: userId ?? agentId ?? null,
      action: 'SKILL_GATE',
      target: skillId ?? null,
      decision,
      detail: Object.freeze({
        agentId: agentId ?? null,
        userId: userId ?? null,
        targetId: targetId ?? null,
        skillId: skillId ?? null,
        amountCents,
        code,
        checkNames: Object.freeze(checks.map((check) => check.name))
      })
    });
    this.#onAudit(audit);
    return Object.freeze({
      decision,
      code,
      checks: Object.freeze(checks),
      audit
    });
  }

  /**
   * 契约 §3 的 7 步判定（严格顺序短路）。
   * 注意：DENY 不抛异常，返回 { decision:'DENY', code, checks, audit }。
   */
  evaluate({ agentId, userId, targetId = null, skillId, amountCents = 0, destructive = false } = {}) {
    const checks = [];
    const push = (name, ok, detail) => checks.push(Object.freeze({ name, ok, detail }));
    const amount = Number.isFinite(amountCents) && amountCents > 0 ? Math.trunc(amountCents) : 0;
    const hasTarget = targetId !== null && targetId !== undefined && targetId !== '';

    const deny = (code) =>
      this.#finish({ agentId, userId, targetId, skillId, amountCents: amount, decision: DECISION.DENY, code, checks });
    const requireApproval = (code) =>
      this.#finish({
        agentId,
        userId,
        targetId,
        skillId,
        amountCents: amount,
        decision: DECISION.REQUIRE_APPROVAL,
        code,
        checks
      });
    const allow = () =>
      this.#finish({
        agentId,
        userId,
        targetId,
        skillId,
        amountCents: amount,
        decision: DECISION.ALLOW,
        code: GATE_CODE.ALLOWED,
        checks
      });

    // 1) Agent 不存在
    const agent = this.#registry.get(agentId);
    if (!agent) {
      push('agent', false, `Agent 未注册: ${String(agentId)}`);
      return deny(GATE_CODE.AGENT_UNKNOWN);
    }
    push('agent', true, `kind=${agent.kind} owner=${agent.ownerUserId}`);

    // 2) skillId 不在 allowedSkills
    if (!this.#registry.isSkillAllowed({ agentId, skillId })) {
      push('skill', false, `skill 未授权: ${String(skillId)}`);
      return deny(GATE_CODE.SKILL_NOT_ALLOWED);
    }
    push('skill', true, `skill 已授权: ${String(skillId)}`);

    // 3) 信任档：数值越大信任越低；level > trustZoneMin 视为对方信任度不足
    if (hasTarget) {
      const level = this.#trustMatrix.levelOf({ userId, targetId });
      const ok = level <= agent.trustZoneMin;
      push('trust', ok, `level=${level} trustZoneMin=${agent.trustZoneMin}`);
      if (!ok) return deny(GATE_CODE.TRUST_TOO_LOW);
    } else {
      push('trust', true, '无 targetId，跳过信任档检查');
    }

    // 4) 破坏性动作（含 skill 自带 destructive 声明）
    const destructiveSkill = agent.destructiveSkills.includes(skillId);
    if (destructive === true || destructiveSkill) {
      push('destructive', false, `destructive=${destructive === true} declared=${destructiveSkill}`);
      return requireApproval(GATE_CODE.DESTRUCTIVE_ACTION);
    }
    push('destructive', true, '非破坏性动作');

    // 5) 日预算：未注入 DailyBudget 时预算视为 0（fail closed，绝不放行未知额度消费）
    const remaining = this.#budget ? this.#budget.remaining(agentId) : 0;
    const budgetOk = amount <= remaining;
    push('budget', budgetOk, `requested=${amount} remaining=${remaining}`);
    if (!budgetOk) return deny(GATE_CODE.BUDGET_EXCEEDED);

    // 6) 金额阈值（amount > 0 且 >= approvalThresholdCents 必须人工审批；0 => 任何花钱都要审批）
    const threshold = agent.approvalThresholdCents;
    const needsApproval = amount > 0 && amount >= threshold;
    push('threshold', !needsApproval, `requested=${amount} threshold=${threshold}`);
    if (needsApproval) return requireApproval(GATE_CODE.AMOUNT_THRESHOLD);

    // 7) ALLOW
    return allow();
  }

  /** 与 evaluate 同一判定，但 DENY 时抛 GateDeniedError（携带 .code/.decision/.checks） */
  assertAllowed(request = {}) {
    const result = this.evaluate(request);
    if (result.decision === DECISION.DENY) {
      throw new GateDeniedError(`权限门控拒绝: ${result.code}`, {
        code: result.code,
        decision: result.decision,
        checks: result.checks,
        result
      });
    }
    return result;
  }
}
