// 模块职责：总管 Agent / 子 Agent / Skill 的注册表，是权限门控的唯一配置来源。
// 契约来源：contracts/INTERFACES.md §2。
// 关键不变量：①Agent 只能属主于一个用户；②子 Agent 必须有已注册且同属主的 parentId；③能力声明（allowedSkills/destructiveSkills）是门控的唯一依据。
export const AGENT_KIND = Object.freeze({ BUTLER: 'butler', SUBAGENT: 'subagent', SKILL: 'skill' });

const KIND_VALUES = new Set(Object.values(AGENT_KIND));
const MAX_TRUST_LEVEL = 4;

const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

/** 金额/预算归一：只接受非负整数分，其它一律 0（0 => 不允许花钱，见契约 §2） */
const toCents = (value) => (Number.isFinite(value) && value > 0 ? Math.trunc(value) : 0);

/** 信任档归一：整数 0..4；非法配置回落到最保守的 4（STRANGER，仅陌生人只读） */
const toTrustZoneMin = (value) => {
  if (!Number.isInteger(value)) return MAX_TRUST_LEVEL;
  return Math.min(MAX_TRUST_LEVEL, Math.max(0, value));
};

const toSkillList = (value) => {
  if (!Array.isArray(value)) return Object.freeze([]);
  const unique = [];
  for (const item of value) {
    if (isNonEmptyString(item) && !unique.includes(item)) unique.push(item);
  }
  return Object.freeze(unique);
};

export class AgentRegistryError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'AgentRegistryError';
    this.code = code;
  }
}

export class AgentRegistry {
  /** @type {Map<string, object>} agentId -> frozen agent */
  #agents;
  /** @type {Map<string, Set<string>>} ownerUserId -> agentId set */
  #byOwner;

  constructor() {
    this.#agents = new Map();
    this.#byOwner = new Map();
  }

  /**
   * 注册 Agent。重复 agentId => AGENT_EXISTS（不做覆盖，避免静默提权）。
   * @returns {Readonly<object>} frozen agent
   */
  register({
    agentId,
    ownerUserId,
    kind,
    domain = null,
    parentId = null,
    allowedSkills = [],
    destructiveSkills = [],
    trustZoneMin = 4,
    budgetCentsPerDay = 0,
    approvalThresholdCents = 0,
    model = 'cloud-large'
  } = {}) {
    if (!isNonEmptyString(agentId)) {
      throw new AgentRegistryError('agentId 必填且必须是非空字符串', 'AGENT_UNKNOWN');
    }
    if (!isNonEmptyString(ownerUserId)) {
      throw new AgentRegistryError('每个 Agent 必须且只能属主于一个用户（ownerUserId 缺失）', 'OWNER_REQUIRED');
    }
    if (this.#agents.has(agentId)) {
      throw new AgentRegistryError(`Agent 已存在: ${agentId}`, 'AGENT_EXISTS');
    }
    if (!KIND_VALUES.has(kind)) {
      throw new AgentRegistryError(`未知的 Agent 类型: ${String(kind)}`, 'AGENT_UNKNOWN');
    }
    if (kind === AGENT_KIND.SUBAGENT) {
      if (!isNonEmptyString(parentId)) {
        throw new AgentRegistryError(`子 Agent 必须有 parentId: ${agentId}`, 'PARENT_REQUIRED');
      }
      const parent = this.#agents.get(parentId);
      if (!parent) {
        throw new AgentRegistryError(`父 Agent 未注册: ${parentId}`, 'PARENT_REQUIRED');
      }
      if (parent.ownerUserId !== ownerUserId) {
        throw new AgentRegistryError(`父 Agent 的属主与子 Agent 不一致: ${parentId}`, 'PARENT_REQUIRED');
      }
    }
    if (kind !== AGENT_KIND.SUBAGENT && isNonEmptyString(parentId) && !this.#agents.has(parentId)) {
      throw new AgentRegistryError(`parentId 指向未注册的 Agent: ${parentId}`, 'PARENT_REQUIRED');
    }

    const agent = Object.freeze({
      agentId,
      ownerUserId,
      kind,
      domain: domain === null || domain === undefined ? null : String(domain),
      parentId: isNonEmptyString(parentId) ? parentId : null,
      allowedSkills: toSkillList(allowedSkills),
      destructiveSkills: toSkillList(destructiveSkills),
      trustZoneMin: toTrustZoneMin(trustZoneMin),
      budgetCentsPerDay: toCents(budgetCentsPerDay),
      approvalThresholdCents: toCents(approvalThresholdCents),
      model: isNonEmptyString(model) ? model : 'cloud-large'
    });

    this.#agents.set(agentId, agent);
    let owned = this.#byOwner.get(ownerUserId);
    if (!owned) {
      owned = new Set();
      this.#byOwner.set(ownerUserId, owned);
    }
    owned.add(agentId);
    return agent;
  }

  /** @returns {Readonly<object>|undefined} */
  get(agentId) {
    if (!isNonEmptyString(agentId)) return undefined;
    return this.#agents.get(agentId);
  }

  /** @returns {ReadonlyArray<object>} 该属主名下的 Agent（按 agentId 升序） */
  listByOwner(ownerUserId) {
    const owned = this.#byOwner.get(ownerUserId);
    if (!owned) return Object.freeze([]);
    const agents = [...owned]
      .map((id) => this.#agents.get(id))
      .filter(Boolean)
      .sort((a, b) => (a.agentId < b.agentId ? -1 : a.agentId > b.agentId ? 1 : 0));
    return Object.freeze(agents);
  }

  /** @returns {ReadonlyArray<object>} 直接子 Agent（按 agentId 升序）；未知 parent => AGENT_UNKNOWN */
  children(butlerAgentId) {
    const parent = this.#agents.get(butlerAgentId);
    if (!parent) {
      throw new AgentRegistryError(`父 Agent 未注册: ${String(butlerAgentId)}`, 'AGENT_UNKNOWN');
    }
    const children = [...this.#agents.values()]
      .filter((agent) => agent.parentId === butlerAgentId)
      .sort((a, b) => (a.agentId < b.agentId ? -1 : a.agentId > b.agentId ? 1 : 0));
    return Object.freeze(children);
  }

  /** @returns {boolean} 未知 Agent 一律返回 false（门控第 1 步会先以 AGENT_UNKNOWN 拒绝） */
  isSkillAllowed({ agentId, skillId } = {}) {
    const agent = this.#agents.get(agentId);
    if (!agent) return false;
    return agent.allowedSkills.includes(skillId);
  }
}
