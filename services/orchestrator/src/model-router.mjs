// 模块职责：按敏感度路由模型（R7）——家庭/法律/医疗/记忆等敏感会话钉在本地或端侧模型，其余走云端；路由决策必须可审计。
// 契约来源：contracts/INTERFACES.md §24。
// 关键不变量：①判定顺序冻结：显式覆盖 EXPLICIT_OVERRIDE → 默认敏感域 DOMAIN_SENSITIVE(LOCAL) → 亲密信任档 TRUST_SENSITIVE(LOCAL) → 其余 ROUTED_CLOUD；
//             ②setRule 覆盖默认敏感域判断（ROUTED_LOCAL / ROUTED_CLOUD 带 rule 原因）；③每条决策都带可读 reasons，审计能解释"为什么这次走了本地"。
export const MODEL_TIER = Object.freeze({ LOCAL: 'LOCAL', CLOUD: 'CLOUD' });

export const ROUTE_CODE = Object.freeze({
  ROUTED_LOCAL: 'ROUTED_LOCAL',
  ROUTED_CLOUD: 'ROUTED_CLOUD',
  DOMAIN_SENSITIVE: 'DOMAIN_SENSITIVE',
  TRUST_SENSITIVE: 'TRUST_SENSITIVE',
  EXPLICIT_OVERRIDE: 'EXPLICIT_OVERRIDE'
});

const CODE = ROUTE_CODE;
const TIER_VALUES = new Set(Object.values(MODEL_TIER));
const FAMILY_TRUST_LEVEL = 1;
const MAX_TRUST_LEVEL = 4;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

const clampTrustLevel = (value) => {
  if (!Number.isInteger(value)) return MAX_TRUST_LEVEL;
  return Math.min(MAX_TRUST_LEVEL, Math.max(0, value));
};

export class ModelRouterError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'ModelRouterError';
    this.code = code;
  }
}

export class ModelRouter {
  #clock;
  #sensitiveDomains;
  #localModel;
  #cloudModel;
  /** @type {Map<string, object>} domain -> frozen rule（覆盖默认敏感域判断） */
  #rules;

  constructor({
    clock = () => new Date(),
    sensitiveDomains = ['family', 'legal', 'medical', 'memory'],
    localModel = 'local-small',
    cloudModel = 'cloud-large'
  } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#sensitiveDomains = new Set(
      (Array.isArray(sensitiveDomains) ? sensitiveDomains : []).filter((domain) => isNonEmptyString(domain))
    );
    this.#localModel = isNonEmptyString(localModel) ? localModel : 'local-small';
    this.#cloudModel = isNonEmptyString(cloudModel) ? cloudModel : 'cloud-large';
    this.#rules = new Map();
  }

  #nowIso() {
    const sampled = this.#clock();
    return (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
  }

  #modelOf(tier) {
    return tier === MODEL_TIER.LOCAL ? this.#localModel : this.#cloudModel;
  }

  /** 覆盖某 domain 的默认敏感域判断（tier=LOCAL 钉本地 / tier=CLOUD 放云端） */
  setRule({ domain, tier, note = null } = {}) {
    if (!isNonEmptyString(domain)) throw new TypeError('setRule: domain 必填');
    if (!TIER_VALUES.has(tier)) {
      throw new TypeError(`setRule: tier 必须是 ${[...TIER_VALUES].join('/')}`);
    }
    const rule = Object.freeze({
      domain,
      tier,
      note: note ?? null,
      updatedAt: this.#nowIso()
    });
    this.#rules.set(domain, rule);
    return rule;
  }

  /**
   * 路由判定（判定顺序冻结）。
   * @returns {Readonly<{tier:'LOCAL'|'CLOUD', model:string, code:string, reasons:ReadonlyArray<string>}>}
   */
  route({ domain, trustLevel = 4, explicitTier = null } = {}) {
    if (explicitTier !== null && explicitTier !== undefined && !TIER_VALUES.has(explicitTier)) {
      throw new TypeError(`route: explicitTier 必须是 null / ${[...TIER_VALUES].join('/')}`);
    }
    const trust = clampTrustLevel(trustLevel);
    const result = (tier, code, reasons) =>
      Object.freeze({
        tier,
        model: this.#modelOf(tier),
        code,
        reasons: Object.freeze(reasons)
      });

    // ① 显式覆盖：调用方明确指定，仍要给出可审计原因
    if (explicitTier !== null && explicitTier !== undefined) {
      return result(explicitTier, CODE.EXPLICIT_OVERRIDE, [
        `explicit override: tier=${explicitTier}（调用方显式指定，非默认路由）`,
        `domain=${domain ?? 'null'}, trustLevel=${trust}`
      ]);
    }

    // ② setRule 覆盖默认敏感域判断
    const rule = this.#rules.get(domain) ?? null;
    if (rule) {
      const code = rule.tier === MODEL_TIER.LOCAL ? CODE.ROUTED_LOCAL : CODE.ROUTED_CLOUD;
      return result(rule.tier, code, [
        `rule override: domain=${domain} 被策略钉到 ${rule.tier}`,
        `trustLevel=${trust}`
      ]);
    }

    // ③ 默认敏感域
    if (this.#sensitiveDomains.has(domain)) {
      return result(MODEL_TIER.LOCAL, CODE.DOMAIN_SENSITIVE, [
        `sensitive domain: ${domain} ∈ sensitiveDomains`,
        `trustLevel=${trust}`
      ]);
    }

    // ④ 亲密信任档（家庭事务不出端侧）：数值越小信任越高，<= FAMILY(1) 走本地
    if (trust <= FAMILY_TRUST_LEVEL) {
      return result(MODEL_TIER.LOCAL, CODE.TRUST_SENSITIVE, [
        `trust sensitive: trustLevel=${trust} <= FAMILY(${FAMILY_TRUST_LEVEL})`,
        `domain=${domain ?? 'null'}`
      ]);
    }

    // ⑤ 其余走云端
    return result(MODEL_TIER.CLOUD, CODE.ROUTED_CLOUD, [
      `default route: domain=${domain ?? 'null'} 非敏感且 trustLevel=${trust} > FAMILY`,
      `model=${this.#cloudModel}`
    ]);
  }
}
