// SPDX-License-Identifier: MIT
//
// 关系档位门控：IM2A 核心语义的 TypeScript 实现（纯函数、零 IO）。
//
// 语义源（以 core 为准，本文件不得单方改变规则）：
//   - internal/trustzone/trust.go（5 档 × 7 领域 × 3 结果，缺省 forbidden；五步 Check）
//   - 协议规范：spec/relation-gating.md（v1，实现无关；reasonCode 是跨实现比对口径）
//
// 本模块不依赖 OpenClaw SDK，可被插件、测试、第三方一致性适配器直接 import。

export type TrustLevel = 0 | 1 | 2 | 3 | 4;

export type Scope =
  | "schedule"
  | "family"
  | "purchase"
  | "workmail"
  | "stranger"
  | "finance"
  | "external";

export type Permission = "auto_execute" | "requires_approval" | "forbidden";

/** 信任档位名称（文档别名；协议输入是整数）。 */
export const TRUST_LEVEL_NAMES = ["self", "family", "trusted", "known", "stranger"] as const;

/** 规范定义的 7 个能力领域（scope 是开放字符串，未知 scope 缺省 forbidden）。 */
export const SCOPES: readonly Scope[] = [
  "schedule",
  "family",
  "purchase",
  "workmail",
  "stranger",
  "finance",
  "external",
];

/**
 * 5×7 权限矩阵（与 internal/trustzone/trust.go 逐格一致）。
 * 缺省 = forbidden：表内未列出的格子（含未知 scope/level）一律 forbidden。
 */
export const PERMISSION_MATRIX: Readonly<Record<number, Readonly<Partial<Record<string, Permission>>>>> = {
  0: {
    schedule: "auto_execute",
    family: "auto_execute",
    purchase: "auto_execute",
    workmail: "auto_execute",
    stranger: "auto_execute",
    finance: "auto_execute",
    external: "auto_execute",
  },
  1: {
    schedule: "auto_execute",
    family: "auto_execute",
    purchase: "auto_execute",
    workmail: "forbidden",
    stranger: "requires_approval",
    finance: "requires_approval",
    external: "requires_approval",
  },
  2: {
    schedule: "auto_execute",
    family: "requires_approval",
    purchase: "requires_approval",
    workmail: "forbidden",
    stranger: "requires_approval",
    finance: "requires_approval",
    external: "requires_approval",
  },
  3: {
    schedule: "requires_approval",
    family: "forbidden",
    purchase: "forbidden",
    workmail: "forbidden",
    stranger: "requires_approval",
    finance: "forbidden",
    external: "requires_approval",
  },
  4: {
    schedule: "forbidden",
    family: "forbidden",
    purchase: "forbidden",
    workmail: "forbidden",
    stranger: "requires_approval",
    finance: "forbidden",
    external: "forbidden",
  },
};

/** 返回矩阵中某格权限；未知 level/scope 缺省 forbidden。 */
export function permissionsFor(trustLevel: number, scope: string): Permission {
  const row = PERMISSION_MATRIX[trustLevel];
  if (!row) return "forbidden";
  return row[scope] ?? "forbidden";
}

/** 协议 reasonCode（与 spec/relation-gating.md §2.1 一致，跨实现比对口径）。 */
export type ReasonCode =
  | "OK"
  | "SCOPE_REQUIRES_APPROVAL"
  | "AMOUNT_ABOVE_THRESHOLD"
  | "SKILL_NOT_ALLOWED"
  | "TRUST_BELOW_MIN"
  | "SCOPE_FORBIDDEN"
  | "AMOUNT_ABOVE_BUDGET";

export interface Capability {
  agentId?: string;
  trustLevelMin?: number;
  allowedSkills?: readonly string[];
  /** 单次调用金额上限（分）；0/缺省 = 不启用（协议 §6.3 预算语义澄清）。 */
  budgetCentsPerDay?: number;
  /** 金额审批阈值（分）；0/缺省 = 不启用；严格大于才触发。 */
  requiresApprovalAboveCents?: number;
}

export interface CallContext {
  skillId?: string;
  scope: string;
  trustLevel: number;
  amountCents?: number;
  targetUid?: number;
}

export interface Decision {
  allowed: boolean;
  requiresApproval: boolean;
  reasonCode: ReasonCode;
  /** 人读文本（不属于协议，仅日志/调试）。 */
  reason: string;
}

/**
 * 五步判定（normative：顺序影响结果，第一个命中即返回）。
 *
 * 1) Skill 白名单 → 2) 档位下限 → 3) 领域矩阵 → 4) 金额审批阈值 → 5) 单次预算上限。
 * 任何实现不得重排；金额比较一律"严格大于"，0 阈值/预算 = 不启用。
 */
export function decide(capability: Capability, call: CallContext): Decision {
  const allowedSkills = capability.allowedSkills ?? [];
  const skillId = call.skillId ?? "";
  const trustLevelMin = capability.trustLevelMin ?? 0;
  const trustLevel = call.trustLevel;
  const amountCents = call.amountCents ?? 0;
  const budgetCentsPerDay = capability.budgetCentsPerDay ?? 0;
  const requiresApprovalAboveCents = capability.requiresApprovalAboveCents ?? 0;

  // 1) Skill 白名单（空 = 不限制；精确匹配、区分大小写）。
  if (allowedSkills.length > 0 && !allowedSkills.includes(skillId)) {
    return deny("SKILL_NOT_ALLOWED", `skill ${JSON.stringify(skillId)} not in whitelist`);
  }

  // 2) 信任档位下限。
  if (trustLevel < trustLevelMin) {
    return deny("TRUST_BELOW_MIN", `trust ${levelName(trustLevel)} below min ${levelName(trustLevelMin)}`);
  }

  // 3) 领域矩阵（未知 level/scope 缺省 forbidden）。
  const perm = permissionsFor(trustLevel, call.scope);
  if (perm === "forbidden") {
    return deny("SCOPE_FORBIDDEN", `scope ${call.scope} forbidden at trust ${levelName(trustLevel)}`);
  }
  if (perm === "requires_approval") {
    return approval("SCOPE_REQUIRES_APPROVAL", `scope ${call.scope} requires approval at trust ${levelName(trustLevel)}`);
  }

  // 4) 金额审批阈值（严格大于）。
  if (requiresApprovalAboveCents > 0 && amountCents > requiresApprovalAboveCents) {
    return approval("AMOUNT_ABOVE_THRESHOLD", `amount ${amountCents} exceeds threshold ${requiresApprovalAboveCents}`);
  }

  // 5) 单次预算上限（严格大于）。
  if (budgetCentsPerDay > 0 && amountCents > budgetCentsPerDay) {
    return deny("AMOUNT_ABOVE_BUDGET", `amount ${amountCents} exceeds daily budget ${budgetCentsPerDay}`);
  }

  return allow();
}

/** 与 Go `Capability.Check` 同义的别名（插件/适配器可用任一名字）。 */
export const check = decide;

function allow(): Decision {
  return { allowed: true, requiresApproval: false, reasonCode: "OK", reason: "ok" };
}

function deny(code: ReasonCode, reason: string): Decision {
  return { allowed: false, requiresApproval: false, reasonCode: code, reason };
}

function approval(code: ReasonCode, reason: string): Decision {
  return { allowed: false, requiresApproval: true, reasonCode: code, reason };
}

function levelName(level: number): string {
  return TRUST_LEVEL_NAMES[level] ?? `level(${level})`;
}
