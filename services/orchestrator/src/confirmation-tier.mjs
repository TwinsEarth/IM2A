// 模块职责：确认层（Confirmation Tier）——把工具分成 PLANNER（可自动执行）与 CONFIRMATION（必须人确认）；
//           模型/规划器的"我已批准"一律无效，这是 planner 无法自我授权的红线。
// 契约来源：contracts/INTERFACES.md §28。
// 关键不变量：①未登记技能默认 CONFIRMATION（fail-closed，不假设它安全）；②只有 humanApproved=true 能让 CONFIRMATION 层 AUTO；
//             ③plannerClaimsApproved=true 但无人批准 => REQUIRE_APPROVAL + PLANNER_PREAPPROVAL_REJECTED（越权主张进审计）。
export const TOOL_TIER = Object.freeze({ PLANNER: 'PLANNER', CONFIRMATION: 'CONFIRMATION' });

export const TIER_CODE = Object.freeze({
  PLANNER_ALLOWED: 'PLANNER_ALLOWED',
  CONFIRMATION_REQUIRED: 'CONFIRMATION_REQUIRED',
  PLANNER_PREAPPROVAL_REJECTED: 'PLANNER_PREAPPROVAL_REJECTED',
  TIER_SET: 'TIER_SET'
});

const CODE = TIER_CODE;
const TIER_VALUES = new Set(Object.values(TOOL_TIER));
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class ConfirmationTierError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'ConfirmationTierError';
    this.code = code;
  }
}

export class ConfirmationTier {
  #clock;
  #onAudit;
  /** @type {Map<string, Map<string, object>>} agentId -> skillId -> frozen policy */
  #policies;

  constructor({ clock = () => new Date(), onAudit = () => {} } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#onAudit = typeof onAudit === 'function' ? onAudit : () => {};
    this.#policies = new Map();
  }

  #nowIso() {
    const sampled = this.#clock();
    return (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
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
    this.#onAudit(event);
    return event;
  }

  /** 登记工具的确认层级；未登记的技能在 evaluate 时默认 CONFIRMATION（fail-closed） */
  setTier({ agentId, skillId, tier } = {}) {
    if (!isNonEmptyString(agentId)) throw new TypeError('setTier: agentId 必填');
    if (!isNonEmptyString(skillId)) throw new TypeError('setTier: skillId 必填');
    if (!TIER_VALUES.has(tier)) {
      throw new TypeError(`setTier: tier 必须是 ${[...TIER_VALUES].join('/')}`);
    }
    const policy = Object.freeze({
      agentId,
      skillId,
      tier,
      code: CODE.TIER_SET,
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

  /** @returns {string} 未登记 => CONFIRMATION（契约 §28 fail-closed） */
  tierOf({ agentId, skillId } = {}) {
    const policy = this.#policies.get(agentId)?.get(skillId);
    return policy?.tier ?? TOOL_TIER.CONFIRMATION;
  }

  /**
   * 确认层判定（不抛业务异常）。
   * @returns {Readonly<{decision:'AUTO'|'REQUIRE_APPROVAL', code:string, tier:string, reasons:ReadonlyArray<string>}>}
   */
  evaluate({ agentId, skillId, plannerClaimsApproved = false, humanApproved = false } = {}) {
    const tier = this.tierOf({ agentId, skillId });
    const result = (decision, code, reasons) =>
      Object.freeze({ decision, code, tier, reasons: Object.freeze(reasons) });

    // PLANNER 层：规划器可自动执行
    if (tier === TOOL_TIER.PLANNER) {
      return result('AUTO', CODE.PLANNER_ALLOWED, ['tier=PLANNER: 规划器可自动执行']);
    }

    // CONFIRMATION 层：唯一放行条件是"人确认"（planner 的声明不算）
    if (humanApproved === true) {
      return result('AUTO', CODE.CONFIRMATION_REQUIRED, ['人确认（humanApproved=true），可执行']);
    }

    if (plannerClaimsApproved === true) {
      // 越权主张：记录进审计，但绝不生效
      this.#emit({
        action: 'CONFIRMATION_PREAPPROVAL_REJECTED',
        actor: agentId ?? null,
        target: skillId ?? null,
        decision: 'REQUIRE_APPROVAL',
        detail: {
          code: CODE.PLANNER_PREAPPROVAL_REJECTED,
          tier,
          plannerClaimsApproved: true
        }
      });
      return result('REQUIRE_APPROVAL', CODE.PLANNER_PREAPPROVAL_REJECTED, [
        'planner 声称已批准，但 planner 无权预批准 CONFIRMATION 层（越权主张已记录）',
        `skillId=${String(skillId)} 仍需 humanApproved=true`
      ]);
    }

    return result('REQUIRE_APPROVAL', CODE.CONFIRMATION_REQUIRED, [
      'tier=CONFIRMATION（未登记技能默认 fail-closed）: 必须人工确认',
      `skillId=${String(skillId)}`
    ]);
  }
}
