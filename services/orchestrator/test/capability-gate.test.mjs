// 测试：代码层权限门控 + DailyBudget（契约 §3）。
// 覆盖 7 步短路、DENY/REQUIRE_APPROVAL/ALLOW、全部可达 GATE_CODE、信任档方向、预算耗尽、evaluate 不抛异常、审计。
import test from 'node:test';
import assert from 'node:assert/strict';

import { AgentRegistry, AGENT_KIND } from '../src/agent-registry.mjs';
import { TrustMatrix, TRUST_LEVELS } from '../src/trust-zone.mjs';
import {
  CapabilityGate,
  DailyBudget,
  DECISION,
  GATE_CODE,
  GateDeniedError
} from '../src/capability-gate.mjs';
import { makeClock } from './_helpers.mjs';

function setup(options = {}) {
  const clock = options.clock ?? makeClock();
  const registry = new AgentRegistry();
  const trustMatrix = new TrustMatrix({ clock });
  const budget = new DailyBudget({ clock });
  const audit = [];

  registry.register({
    agentId: 'agent-butler',
    ownerUserId: 'user-a',
    kind: AGENT_KIND.BUTLER,
    allowedSkills: ['read', 'pay', 'delete-file', 'send'],
    destructiveSkills: ['delete-file'],
    trustZoneMin: 4,
    budgetCentsPerDay: 10000,
    approvalThresholdCents: 5000
  });
  registry.register({
    agentId: 'agent-sub',
    ownerUserId: 'user-a',
    kind: AGENT_KIND.SUBAGENT,
    parentId: 'agent-butler',
    allowedSkills: ['read'],
    trustZoneMin: 2
  });

  budget.setCap('agent-butler', 10000);
  budget.setCap('agent-sub', 1000);
  trustMatrix.grant({ userId: 'user-a', targetId: 'user-trusted', level: TRUST_LEVELS.TRUSTED });
  trustMatrix.grant({ userId: 'user-a', targetId: 'user-family', level: TRUST_LEVELS.FAMILY });

  const gate = new CapabilityGate({
    registry,
    trustMatrix,
    budget,
    clock,
    onAudit: (event) => audit.push(event)
  });
  return { clock, registry, trustMatrix, budget, gate, audit };
}

test('DECISION/GATE_CODE 常量冻结且与契约逐字一致', () => {
  assert.deepEqual({ ...DECISION }, { ALLOW: 'ALLOW', REQUIRE_APPROVAL: 'REQUIRE_APPROVAL', DENY: 'DENY' });
  assert.deepEqual({ ...GATE_CODE }, {
    ALLOWED: 'ALLOWED',
    AGENT_UNKNOWN: 'AGENT_UNKNOWN',
    SKILL_NOT_ALLOWED: 'SKILL_NOT_ALLOWED',
    TRUST_TOO_LOW: 'TRUST_TOO_LOW',
    BUDGET_EXCEEDED: 'BUDGET_EXCEEDED',
    MONTHLY_CAP: 'MONTHLY_CAP',
    AMOUNT_THRESHOLD: 'AMOUNT_THRESHOLD',
    DESTRUCTIVE_ACTION: 'DESTRUCTIVE_ACTION'
  });
  assert.ok(Object.isFrozen(DECISION));
  assert.ok(Object.isFrozen(GATE_CODE));
});

test('正常路径 ALLOW：7 项检查全部通过，返回 audit 且 onAudit 被调用一次', () => {
  const { gate, audit } = setup();
  const result = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    targetId: 'user-trusted',
    skillId: 'read',
    amountCents: 0
  });
  assert.equal(result.decision, DECISION.ALLOW);
  assert.equal(result.code, GATE_CODE.ALLOWED);
  assert.deepEqual(
    result.checks.map((check) => check.name),
    ['agent', 'skill', 'trust', 'destructive', 'budget', 'threshold']
  );
  assert.ok(result.checks.every((check) => check.ok === true));
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.checks));
  assert.ok(Object.isFrozen(result.audit));
  assert.equal(result.audit.decision, 'ALLOW');
  assert.equal(result.audit.target, 'read');
  assert.equal(audit.length, 1);
  assert.equal(audit[0], result.audit);
});

test('evaluate 在 DENY 时不抛异常，返回结论对象（只有 assertAllowed 抛）', () => {
  const { gate } = setup();
  let result;
  assert.doesNotThrow(() => {
    result = gate.evaluate({ agentId: 'agent-butler', userId: 'user-a', skillId: 'transfer' });
  });
  assert.equal(result.decision, DECISION.DENY);
  assert.equal(result.code, GATE_CODE.SKILL_NOT_ALLOWED);
  assert.equal(result.checks.at(-1).ok, false);
});

test('第 1 步 AGENT_UNKNOWN：DENY，后续步骤不评估', () => {
  const { gate, audit } = setup();
  const result = gate.evaluate({ agentId: 'agent-missing', userId: 'user-a', skillId: 'read' });
  assert.equal(result.decision, DECISION.DENY);
  assert.equal(result.code, GATE_CODE.AGENT_UNKNOWN);
  assert.deepEqual(result.checks.map((check) => check.name), ['agent']);
  assert.equal(audit.at(-1).decision, 'DENY');
  assert.throws(() => gate.assertAllowed({ agentId: 'agent-missing', skillId: 'read' }), {
    code: 'AGENT_UNKNOWN'
  });
});

test('第 2 步 SKILL_NOT_ALLOWED：未知 skill / 空 skill 均拒绝', () => {
  const { gate } = setup();
  assert.equal(
    gate.evaluate({ agentId: 'agent-butler', userId: 'user-a', skillId: 'transfer' }).code,
    GATE_CODE.SKILL_NOT_ALLOWED
  );
  assert.equal(
    gate.evaluate({ agentId: 'agent-butler', userId: 'user-a', skillId: undefined }).code,
    GATE_CODE.SKILL_NOT_ALLOWED
  );
});

test('第 3 步信任档方向：trustZoneMin=2 时 STRANGER(4) 拒绝、TRUSTED(2) 允许、FAMILY(1) 允许', () => {
  const { gate, trustMatrix } = setup();
  trustMatrix.grant({ userId: 'user-a', targetId: 'user-stranger', level: TRUST_LEVELS.STRANGER });
  assert.equal(
    gate.evaluate({
      agentId: 'agent-sub',
      userId: 'user-a',
      targetId: 'user-stranger',
      skillId: 'read'
    }).code,
    GATE_CODE.TRUST_TOO_LOW
  );
  assert.equal(
    gate.evaluate({
      agentId: 'agent-sub',
      userId: 'user-a',
      targetId: 'user-trusted',
      skillId: 'read'
    }).decision,
    DECISION.ALLOW
  );
  assert.equal(
    gate.evaluate({
      agentId: 'agent-sub',
      userId: 'user-a',
      targetId: 'user-family',
      skillId: 'read'
    }).decision,
    DECISION.ALLOW
  );
  // 未授权的目标默认 STRANGER(4)，对 trustZoneMin=2 同样拒绝
  assert.equal(
    gate.evaluate({
      agentId: 'agent-sub',
      userId: 'user-a',
      targetId: 'user-unknown',
      skillId: 'read'
    }).code,
    GATE_CODE.TRUST_TOO_LOW
  );
});

test('第 3 步：trustZoneMin=4 表示对任何人开放；无 targetId 跳过信任检查', () => {
  const { gate, trustMatrix } = setup();
  trustMatrix.grant({ userId: 'user-a', targetId: 'user-stranger', level: TRUST_LEVELS.STRANGER });
  assert.equal(
    gate.evaluate({
      agentId: 'agent-butler',
      userId: 'user-a',
      targetId: 'user-stranger',
      skillId: 'read'
    }).decision,
    DECISION.ALLOW
  );
  assert.equal(
    gate.evaluate({ agentId: 'agent-sub', userId: 'user-a', targetId: null, skillId: 'read' }).decision,
    DECISION.ALLOW
  );
});

test('第 4 步 DESTRUCTIVE_ACTION：destructive 参数与 skill 声明两条路径都 REQUIRE_APPROVAL', () => {
  const { gate } = setup();
  const byFlag = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    skillId: 'read',
    destructive: true
  });
  assert.equal(byFlag.decision, DECISION.REQUIRE_APPROVAL);
  assert.equal(byFlag.code, GATE_CODE.DESTRUCTIVE_ACTION);

  const byDeclaredSkill = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    skillId: 'delete-file',
    amountCents: 0
  });
  assert.equal(byDeclaredSkill.decision, DECISION.REQUIRE_APPROVAL);
  assert.equal(byDeclaredSkill.code, GATE_CODE.DESTRUCTIVE_ACTION);
});

test('第 5 步 BUDGET_EXCEEDED：请求超剩余额度；耗尽后任何消费都拒绝', () => {
  const { gate, budget } = setup();
  budget.setCap('agent-butler', 1000);
  const over = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    targetId: 'user-trusted',
    skillId: 'pay',
    amountCents: 1001
  });
  assert.equal(over.decision, DECISION.DENY);
  assert.equal(over.code, GATE_CODE.BUDGET_EXCEEDED);

  const exactly = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    skillId: 'pay',
    amountCents: 1000
  });
  assert.equal(exactly.decision, DECISION.ALLOW); // 1000 < threshold 5000

  assert.deepEqual(budget.trySpend('agent-butler', 1000), { ok: true, remaining: 0 });
  const drained = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    skillId: 'pay',
    amountCents: 1
  });
  assert.equal(drained.code, GATE_CODE.BUDGET_EXCEEDED);
});

test('第 6 步 AMOUNT_THRESHOLD：>= 阈值且 >0 需审批，低于阈值放行', () => {
  const { gate } = setup();
  const atThreshold = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    skillId: 'pay',
    amountCents: 5000
  });
  assert.equal(atThreshold.decision, DECISION.REQUIRE_APPROVAL);
  assert.equal(atThreshold.code, GATE_CODE.AMOUNT_THRESHOLD);

  const below = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    skillId: 'pay',
    amountCents: 4999
  });
  assert.equal(below.decision, DECISION.ALLOW);

  const zero = gate.evaluate({ agentId: 'agent-butler', userId: 'user-a', skillId: 'pay', amountCents: 0 });
  assert.equal(zero.decision, DECISION.ALLOW);
});

test('短路优先级：DENY 优先于 REQUIRE_APPROVAL，第 4 步优先于第 5 步', () => {
  const { gate, budget } = setup();
  // skill 不允许 + destructive => 第 2 步就 DENY
  const denied = gate.evaluate({
    agentId: 'agent-sub',
    userId: 'user-a',
    skillId: 'delete-file',
    destructive: true
  });
  assert.equal(denied.decision, DECISION.DENY);
  assert.equal(denied.code, GATE_CODE.SKILL_NOT_ALLOWED);

  // 预算不足 + destructive => 第 4 步先返回 REQUIRE_APPROVAL
  budget.setCap('agent-butler', 10);
  const approvalFirst = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    skillId: 'delete-file',
    amountCents: 1000
  });
  assert.equal(approvalFirst.decision, DECISION.REQUIRE_APPROVAL);
  assert.equal(approvalFirst.code, GATE_CODE.DESTRUCTIVE_ACTION);
});

test('未注入 DailyBudget 时 fail closed：金额 >0 一律 BUDGET_EXCEEDED，0 金额按阈值判定', () => {
  const clock = makeClock();
  const registry = new AgentRegistry();
  const trustMatrix = new TrustMatrix({ clock });
  registry.register({
    agentId: 'agent-b',
    ownerUserId: 'user-a',
    kind: AGENT_KIND.BUTLER,
    allowedSkills: ['pay'],
    trustZoneMin: 4,
    approvalThresholdCents: 0
  });
  const gate = new CapabilityGate({ registry, trustMatrix, clock });
  assert.equal(
    gate.evaluate({ agentId: 'agent-b', userId: 'user-a', skillId: 'pay', amountCents: 1 }).code,
    GATE_CODE.BUDGET_EXCEEDED
  );
  assert.equal(
    gate.evaluate({ agentId: 'agent-b', userId: 'user-a', skillId: 'pay', amountCents: 0 }).decision,
    DECISION.ALLOW
  );
});

test('assertAllowed：DENY 抛 GateDeniedError（带 code/decision/checks），ALLOW 原样返回', () => {
  const { gate } = setup();
  let caught = null;
  try {
    gate.assertAllowed({ agentId: 'agent-butler', userId: 'user-a', skillId: 'transfer' });
  } catch (error) {
    caught = error;
  }
  assert.ok(caught instanceof GateDeniedError);
  assert.ok(caught instanceof Error);
  assert.equal(caught.code, 'SKILL_NOT_ALLOWED');
  assert.equal(caught.decision, DECISION.DENY);
  assert.ok(Array.isArray(caught.checks));
  assert.ok(Object.isFrozen(caught.checks));

  const ok = gate.assertAllowed({ agentId: 'agent-butler', userId: 'user-a', skillId: 'read' });
  assert.equal(ok.decision, DECISION.ALLOW);
});

test('审计：每次 evaluate（含 DENY/REQUIRE_APPROVAL）都产生一条事件且绑定 agent/skill', () => {
  const { gate, audit } = setup();
  gate.evaluate({ agentId: 'agent-butler', userId: 'user-a', skillId: 'read' });
  gate.evaluate({ agentId: 'agent-butler', userId: 'user-a', skillId: 'transfer' });
  gate.evaluate({ agentId: 'agent-butler', userId: 'user-a', skillId: 'delete-file' });
  gate.evaluate({ agentId: 'agent-missing', userId: 'user-a', skillId: 'read' });
  assert.equal(audit.length, 4);
  assert.deepEqual(
    audit.map((event) => event.decision),
    ['ALLOW', 'DENY', 'REQUIRE_APPROVAL', 'DENY']
  );
  assert.equal(audit[0].actor, 'user-a');
  assert.equal(audit[0].detail.agentId, 'agent-butler');
  assert.equal(audit[0].detail.skillId, 'read');
});

test('MONTHLY_CAP 为预留码：常量存在，但 7 步判定路径永不产生（Phase2 月预算）', () => {
  const { gate } = setup();
  assert.equal(GATE_CODE.MONTHLY_CAP, 'MONTHLY_CAP');
  const probes = [
    { agentId: 'agent-missing', userId: 'user-a', skillId: 'read' },
    { agentId: 'agent-butler', userId: 'user-a', skillId: 'transfer' },
    { agentId: 'agent-butler', userId: 'user-a', skillId: 'read', destructive: true },
    { agentId: 'agent-butler', userId: 'user-a', skillId: 'pay', amountCents: 999999 },
    { agentId: 'agent-butler', userId: 'user-a', skillId: 'pay', amountCents: 6000 },
    { agentId: 'agent-butler', userId: 'user-a', skillId: 'read' }
  ];
  for (const probe of probes) {
    assert.notEqual(gate.evaluate(probe).code, GATE_CODE.MONTHLY_CAP);
  }
});

test('DailyBudget：setCap/remaining/trySpend/超限不扣减/日切恢复/reset/capOf', () => {
  const clock = makeClock();
  const budget = new DailyBudget({ clock });
  assert.equal(budget.remaining('agent-x'), 0);
  assert.equal(budget.capOf('agent-x'), 0);
  assert.equal(budget.setCap('agent-x', 1000), 1000);
  assert.equal(budget.capOf('agent-x'), 1000);
  assert.equal(budget.remaining('agent-x'), 1000);

  assert.deepEqual(budget.trySpend('agent-x', 400), { ok: true, remaining: 600 });
  assert.deepEqual(budget.trySpend('agent-x', 700), { ok: false, remaining: 600 });
  assert.equal(budget.remaining('agent-x'), 600);
  assert.deepEqual(budget.trySpend('agent-x', 600), { ok: true, remaining: 0 });

  // 日切（UTC 日期变化）后额度恢复
  clock.advance(24 * 60 * 60e3);
  assert.equal(budget.remaining('agent-x'), 1000);

  assert.equal(budget.trySpend('agent-x', 250).ok, true);
  assert.equal(budget.reset('agent-x'), 1000);
  assert.equal(budget.remaining('agent-x'), 1000);

  // 非法金额不消费
  assert.deepEqual(budget.trySpend('agent-x', -100), { ok: true, remaining: 1000 });
  assert.deepEqual(budget.trySpend('agent-x', Number.NaN), { ok: true, remaining: 1000 });
  assert.equal(budget.remaining('agent-x'), 1000);

  assert.throws(() => budget.setCap('agent-x', -1), TypeError);
  assert.throws(() => budget.setCap('agent-x', Number.NaN), TypeError);
});
