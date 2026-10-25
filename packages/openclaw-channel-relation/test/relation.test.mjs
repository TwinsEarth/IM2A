// SPDX-License-Identifier: MIT
//
// 关系档位门控的单元测试。
//
// 期望值来源：
//   - 5×7 矩阵：spec/relation-gating.md §4（normative 表格），与 src/relation.ts 的
//     PERMISSION_MATRIX 逐格对照；测试里保留一份独立期望表，避免"实现即期望"的同义反复。
//   - 五步顺序：spec/relation-gating.md §3 / §3.1。
//   - 未知 scope/level 缺省：§5 的 I1 / I2。
//
// 运行：node --test test/relation.test.mjs

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  PERMISSION_MATRIX,
  SCOPES,
  TRUST_LEVEL_NAMES,
  check,
  decide,
  permissionsFor,
} from "../src/relation.ts";

// ---- 协议 §4 的 5×7 矩阵（独立期望表；A/P/F 三态） ----
const A = "auto_execute";
const P = "requires_approval";
const F = "forbidden";

const EXPECTED_MATRIX = {
  0: { schedule: A, family: A, purchase: A, workmail: A, stranger: A, finance: A, external: A },
  1: { schedule: A, family: A, purchase: A, workmail: F, stranger: P, finance: P, external: P },
  2: { schedule: A, family: P, purchase: P, workmail: F, stranger: P, finance: P, external: P },
  3: { schedule: P, family: F, purchase: F, workmail: F, stranger: P, finance: F, external: P },
  4: { schedule: F, family: F, purchase: F, workmail: F, stranger: P, finance: F, external: F },
};

const EXPECTED_OUTCOME = {
  [A]: { allowed: true, requiresApproval: false, reasonCode: "OK" },
  [P]: { allowed: false, requiresApproval: true, reasonCode: "SCOPE_REQUIRES_APPROVAL" },
  [F]: { allowed: false, requiresApproval: false, reasonCode: "SCOPE_FORBIDDEN" },
};

const ALLOW = { allowed: true, requiresApproval: false, reasonCode: "OK" };
const APPROVAL = (reasonCode) => ({ allowed: false, requiresApproval: true, reasonCode });
const DENY = (reasonCode) => ({ allowed: false, requiresApproval: false, reasonCode });

/** 断言 Decision 的三元组，并校验 spec §2.1 的三态一致性（I6）。 */
function assertDecision(decision, expected, label) {
  assert.ok(decision && typeof decision === "object", `${label}: decision 不是对象`);
  const actual = {
    allowed: decision.allowed,
    requiresApproval: decision.requiresApproval,
    reasonCode: decision.reasonCode,
  };
  assert.deepEqual(actual, expected, `${label}: 判定不一致`);
  assert.equal(typeof decision.reason, "string", `${label}: reason 必须是人读字符串`);

  // I6：allowed=true 只能是 OK；requiresApproval=true 只能配两种审批 reasonCode。
  if (decision.allowed) {
    assert.equal(decision.requiresApproval, false, `${label}: allowed 与 requiresApproval 冲突`);
    assert.equal(decision.reasonCode, "OK", `${label}: allowed=true 时 reasonCode 必须是 OK`);
  } else if (decision.requiresApproval) {
    assert.ok(
      ["SCOPE_REQUIRES_APPROVAL", "AMOUNT_ABOVE_THRESHOLD"].includes(decision.reasonCode),
      `${label}: requiresApproval=true 的 reasonCode 非法：${decision.reasonCode}`,
    );
  } else {
    assert.notEqual(decision.reasonCode, "OK", `${label}: 拒绝时不能返回 OK`);
  }
}

function call(overrides = {}) {
  return {
    skillId: "calendar.read",
    scope: "schedule",
    trustLevel: 0,
    amountCents: 0,
    ...overrides,
  };
}

function capability(overrides = {}) {
  return {
    trustLevelMin: 0,
    budgetCentsPerDay: 0,
    requiresApprovalAboveCents: 0,
    ...overrides,
  };
}

// ---- 1) 35 格矩阵 ----

test("permission matrix: 5 levels × 7 scopes = 35 cells match protocol v1", () => {
  assert.deepEqual(
    SCOPES,
    ["schedule", "family", "purchase", "workmail", "stranger", "finance", "external"],
    "SCOPES 必须恰好是协议定义的 7 个领域（顺序固定）",
  );
  assert.deepEqual(TRUST_LEVEL_NAMES, ["self", "family", "trusted", "known", "stranger"]);
  assert.equal(TRUST_LEVEL_NAMES.length, 5);
  assert.deepEqual(Object.keys(PERMISSION_MATRIX).sort(), ["0", "1", "2", "3", "4"]);

  let checked = 0;
  for (const level of [0, 1, 2, 3, 4]) {
    const expectedRow = EXPECTED_MATRIX[level];
    assert.deepEqual(
      Object.keys(PERMISSION_MATRIX[level]).sort(),
      [...SCOPES].sort(),
      `L${level} 行必须显式覆盖全部 7 个 scope（不得靠缺省蒙混）`,
    );

    for (const scope of SCOPES) {
      checked += 1;
      const expectedPermission = expectedRow[scope];
      assert.equal(
        permissionsFor(level, scope),
        expectedPermission,
        `permissionsFor(${level}, ${scope}) 与协议矩阵不一致`,
      );
      assert.equal(
        PERMISSION_MATRIX[level][scope],
        expectedPermission,
        `PERMISSION_MATRIX[${level}][${scope}] 与协议矩阵不一致`,
      );

      const decision = decide(
        capability(),
        call({ scope, trustLevel: level, amountCents: 0 }),
      );
      assertDecision(
        decision,
        EXPECTED_OUTCOME[expectedPermission],
        `矩阵 L${level}/${scope} (${expectedPermission})`,
      );
    }
  }
  assert.equal(checked, 35, "必须覆盖 5×7=35 格");
});

// ---- 2) 未知 scope / 未知 trust level 的缺省 forbidden ----

test("unknown scope defaults to forbidden (I1), including case variants", () => {
  for (const [level, scope] of [
    [0, "travel"],
    [4, "travel"],
    [2, ""],
    [2, "Schedule"], // 协议不做大小写归一化
    [2, "schedule "],
  ]) {
    assert.equal(permissionsFor(level, scope), F, `permissionsFor(${level}, ${scope}) 应为 forbidden`);
    assertDecision(
      decide(capability(), call({ scope, trustLevel: level, amountCents: 0 })),
      DENY("SCOPE_FORBIDDEN"),
      `未知 scope ${JSON.stringify(scope)} @L${level}`,
    );
  }
  // L0 只对 7 个已知 scope 全 auto；未知 scope 仍 forbidden。
  assertDecision(decide(capability(), call({ scope: "travel", trustLevel: 0 })), DENY("SCOPE_FORBIDDEN"), "L0/unknown");
});

test("unknown trust level defaults to forbidden (I2), with step-2 precedence for negative", () => {
  for (const level of [5, 99, 42]) {
    assert.equal(permissionsFor(level, "schedule"), F, `未知档位 ${level} 应为 forbidden`);
    assertDecision(
      decide(capability(), call({ trustLevel: level })),
      DENY("SCOPE_FORBIDDEN"),
      `未知档位 ${level} 走矩阵缺省`,
    );
  }
  // -1 在 trustLevelMin=0 时先命中步骤 2（TRUST_BELOW_MIN），不是矩阵缺省。
  assertDecision(
    decide(capability({ trustLevelMin: 0 }), call({ trustLevel: -1 })),
    DENY("TRUST_BELOW_MIN"),
    "档位 -1 低于下限",
  );
  // -1 且下限更低时，才轮到矩阵缺省。
  assertDecision(
    decide(capability({ trustLevelMin: -5 }), call({ trustLevel: -1 })),
    DENY("SCOPE_FORBIDDEN"),
    "档位 -1 通过步骤 2 后矩阵缺省 forbidden",
  );
});

// ---- 3) 五步顺序判定 ----

test("five-step order: first hit wins (skill → trust → matrix → amount → budget)", () => {
  const cases = [
    {
      name: "1) 白名单不命中：压过档位/矩阵/金额",
      cap: capability({ allowedSkills: ["allowed.skill"], trustLevelMin: 1, requiresApprovalAboveCents: 100 }),
      call: call({ skillId: "other.skill", scope: "finance", trustLevel: 0, amountCents: 500 }),
      expected: DENY("SKILL_NOT_ALLOWED"),
    },
    {
      name: "2) 档位低于下限：压过矩阵 forbidden",
      cap: capability({ trustLevelMin: 1 }),
      call: call({ scope: "workmail", trustLevel: 0 }),
      expected: DENY("TRUST_BELOW_MIN"),
    },
    {
      name: "3a) 矩阵 forbidden",
      cap: capability(),
      call: call({ scope: "workmail", trustLevel: 2 }),
      expected: DENY("SCOPE_FORBIDDEN"),
    },
    {
      name: "3b) 矩阵 requires_approval",
      cap: capability(),
      call: call({ scope: "finance", trustLevel: 2 }),
      expected: APPROVAL("SCOPE_REQUIRES_APPROVAL"),
    },
    {
      name: "4) 金额超审批阈值",
      cap: capability({ requiresApprovalAboveCents: 100 }),
      call: call({ scope: "schedule", trustLevel: 0, amountCents: 101 }),
      expected: APPROVAL("AMOUNT_ABOVE_THRESHOLD"),
    },
    {
      name: "5) 超单次预算",
      cap: capability({ budgetCentsPerDay: 100 }),
      call: call({ scope: "schedule", trustLevel: 0, amountCents: 101 }),
      expected: DENY("AMOUNT_ABOVE_BUDGET"),
    },
    {
      name: "全通过",
      cap: capability({
        allowedSkills: ["calendar.read"],
        trustLevelMin: 0,
        requiresApprovalAboveCents: 100,
        budgetCentsPerDay: 200,
      }),
      call: call({ scope: "schedule", trustLevel: 0, amountCents: 50 }),
      expected: ALLOW,
    },
  ];

  for (const c of cases) {
    assertDecision(decide(c.cap, c.call), c.expected, c.name);
  }
});

test("order precedence table (§3.1): each conflict resolves to the earlier step", () => {
  const cases = [
    {
      name: "金额同时超阈值与预算 → 审批（步骤 4 先于 5）",
      cap: capability({ requiresApprovalAboveCents: 100, budgetCentsPerDay: 200 }),
      call: call({ amountCents: 500 }),
      expected: APPROVAL("AMOUNT_ABOVE_THRESHOLD"),
    },
    {
      name: "矩阵审批 + 超预算 → 矩阵审批",
      cap: capability({ budgetCentsPerDay: 10 }),
      call: call({ scope: "stranger", trustLevel: 4, amountCents: 1000 }),
      expected: APPROVAL("SCOPE_REQUIRES_APPROVAL"),
    },
    {
      name: "矩阵禁止 + 超阈值 → forbidden",
      cap: capability({ requiresApprovalAboveCents: 100 }),
      call: call({ scope: "workmail", trustLevel: 2, amountCents: 500 }),
      expected: DENY("SCOPE_FORBIDDEN"),
    },
    {
      name: "档位不足 + 超阈值 → 档位不足",
      cap: capability({ trustLevelMin: 1, requiresApprovalAboveCents: 10 }),
      call: call({ scope: "family", trustLevel: 0, amountCents: 100 }),
      expected: DENY("TRUST_BELOW_MIN"),
    },
    {
      name: "白名单不命中 + 其余全违 → 白名单",
      cap: capability({ allowedSkills: ["allowed.skill"], trustLevelMin: 4, requiresApprovalAboveCents: 1, budgetCentsPerDay: 1 }),
      call: call({ skillId: "other.skill", scope: "workmail", trustLevel: 0, amountCents: 999 }),
      expected: DENY("SKILL_NOT_ALLOWED"),
    },
  ];
  for (const c of cases) assertDecision(decide(c.cap, c.call), c.expected, c.name);
});

test("strict-greater boundaries and disabled thresholds (§I7)", () => {
  const base = capability({ requiresApprovalAboveCents: 100, budgetCentsPerDay: 100 });

  assertDecision(decide(base, call({ amountCents: 100 })), ALLOW, "等于阈值/预算 → 放行");
  assertDecision(decide(base, call({ amountCents: 0 })), ALLOW, "金额 0 永不因金额被拒");
  assertDecision(
    decide(base, call({ amountCents: 101 })),
    APPROVAL("AMOUNT_ABOVE_THRESHOLD"),
    "阈值 +1 → 审批（步骤 4 先于 5）",
  );
  assertDecision(
    decide(capability({ budgetCentsPerDay: 100 }), call({ amountCents: 101 })),
    DENY("AMOUNT_ABOVE_BUDGET"),
    "预算 +1 → 预算拒绝",
  );
  assertDecision(
    decide(capability(), call({ amountCents: 10 ** 9 })),
    ALLOW,
    "阈值/预算均为 0 = 不启用",
  );
  // 协议 §6.2：负数金额属输入域外，参考实现按"未超阈值/预算"放行。
  assertDecision(
    decide(base, call({ amountCents: -1 })),
    ALLOW,
    "负数金额与参考实现一致（v1 输入域外语义）",
  );
  // 阈值未超、预算超 → 预算拒绝。
  assertDecision(
    decide(capability({ requiresApprovalAboveCents: 100, budgetCentsPerDay: 50 }), call({ amountCents: 60 })),
    DENY("AMOUNT_ABOVE_BUDGET"),
    "阈值未触发但预算触发",
  );
});

// ---- 4) 缺省值、白名单语义、纯函数性 ----

test("defaults: empty capability means allow; empty whitelist means unrestricted", () => {
  assertDecision(decide({}, { scope: "schedule", trustLevel: 0 }), ALLOW, "全部缺省");
  assertDecision(
    decide(capability({ allowedSkills: [] }), call({ skillId: "any.skill" })),
    ALLOW,
    "空白名单 = 不限制",
  );
  assertDecision(
    decide(capability({ allowedSkills: [""] }), call({ skillId: "" })),
    ALLOW,
    "空字符串是合法 skillId，且可被白名单显式列出",
  );
  assertDecision(
    decide(capability({ allowedSkills: ["a"] }), call({ skillId: "" })),
    DENY("SKILL_NOT_ALLOWED"),
    "空 skillId 不在非空白名单中",
  );
  assertDecision(
    decide(capability({ allowedSkills: ["Calendar.read"] }), call({ skillId: "calendar.read" })),
    DENY("SKILL_NOT_ALLOWED"),
    "白名单精确匹配、区分大小写",
  );
});

test("pure function: repeatable, targetUid/agentId do not affect the decision", () => {
  const cap = capability({ allowedSkills: ["calendar.read"], trustLevelMin: 0 });
  const first = decide(cap, call({ scope: "finance", trustLevel: 2, targetUid: 1 }));
  const second = decide(cap, call({ scope: "finance", trustLevel: 2, targetUid: 987654321 }));
  assert.deepEqual(first, second, "相同输入（targetUid 不同）必须得到相同输出");
  assert.deepEqual(
    decide({ ...cap, agentId: "agent-a" }, call({ scope: "finance", trustLevel: 2 })),
    first,
    "agentId 不参与判定",
  );
  assert.deepEqual(decide(cap, call({ scope: "finance", trustLevel: 2 })), first, "重复调用结果一致");
  assert.equal(check, decide, "check 必须是 decide 的别名");
});
