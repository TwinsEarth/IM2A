// 测试：细粒度工具审批策略（契约 §19）。
// 覆盖：精确 > 通配 > 无策略的优先级；信任不足 DENY（不可批）vs 金额超限 APPROVE（可批）；双人审批；与 Gate/ApprovalEngine 的分工。
import test from 'node:test';
import assert from 'node:assert/strict';

import { POLICY_DECISION, ToolPolicy, ToolPolicyError, TOOL_POLICY_CODE } from '../src/tool-policy.mjs';

const AGENT = 'agent-lin-butler';
const USER = 'user-lin';

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

const makePolicy = (clock = makeClock()) => ({ clock, policy: new ToolPolicy({ clock }) });

test('POLICY_DECISION / TOOL_POLICY_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...POLICY_DECISION }, { AUTO: 'AUTO', APPROVE: 'APPROVE', DENY: 'DENY' });
  assert.deepEqual({ ...TOOL_POLICY_CODE }, {
    POLICY_MATCHED: 'POLICY_MATCHED',
    SKILL_NOT_COVERED: 'SKILL_NOT_COVERED',
    POLICY_NOT_FOUND: 'POLICY_NOT_FOUND',
    TRUST_TOO_LOW: 'TRUST_TOO_LOW',
    AMOUNT_EXCEEDED: 'AMOUNT_EXCEEDED',
    NEEDS_SECOND_APPROVER: 'NEEDS_SECOND_APPROVER'
  });
  assert.ok(Object.isFrozen(POLICY_DECISION));
  assert.ok(Object.isFrozen(TOOL_POLICY_CODE));
});

test('无策略 => AUTO / SKILL_NOT_COVERED（ToolPolicy 不替 Gate 做权限判断）', () => {
  const { policy } = makePolicy();
  const result = policy.evaluate({ agentId: AGENT, skillId: 'read', amountCents: 0, trustLevel: 4 });
  assert.equal(result.decision, POLICY_DECISION.AUTO);
  assert.equal(result.code, 'SKILL_NOT_COVERED');
  assert.equal(result.policy, null);
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.reasons));
});

test('setPolicy：精确策略命中 => 按策略 decision，code=POLICY_MATCHED，返回冻结 policy', () => {
  const { policy, clock } = makePolicy();
  const stored = policy.setPolicy({
    agentId: AGENT,
    skillId: 'pay',
    decision: POLICY_DECISION.APPROVE,
    maxAmountCents: 10000,
    trustLevelMax: 2,
    requiresDoubleApproval: true,
    note: '高风险支付'
  });
  assert.ok(Object.isFrozen(stored));
  assert.equal(stored.agentId, AGENT);
  assert.equal(stored.skillId, 'pay');
  assert.equal(stored.decision, 'APPROVE');
  assert.equal(stored.maxAmountCents, 10000);
  assert.equal(stored.trustLevelMax, 2);
  assert.equal(stored.requiresDoubleApproval, true);
  assert.equal(stored.note, '高风险支付');
  assert.equal(stored.updatedAt, '2026-10-21T00:00:00.000Z');

  const result = policy.evaluate({ agentId: AGENT, skillId: 'pay', amountCents: 100, trustLevel: 2, hasSecondApprover: true });
  assert.equal(result.decision, POLICY_DECISION.APPROVE);
  assert.equal(result.code, 'POLICY_MATCHED');
  assert.equal(result.policy, stored);
  assert.ok(Object.isFrozen(result.reasons));
  clock.advance(1000);
  const updated = policy.setPolicy({ agentId: AGENT, skillId: 'pay', decision: POLICY_DECISION.AUTO });
  assert.equal(updated.updatedAt, '2026-10-21T00:00:01.000Z');
});

test('通配 fallback：无精确策略时用 agent 的 * 策略', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, skillId: '*', decision: POLICY_DECISION.APPROVE });
  const result = policy.evaluate({ agentId: AGENT, skillId: 'send-email' });
  assert.equal(result.decision, POLICY_DECISION.APPROVE);
  assert.equal(result.code, 'POLICY_MATCHED');
  assert.equal(result.policy.skillId, '*');
  // 别的 Agent 不受影响
  assert.equal(policy.evaluate({ agentId: 'agent-other', skillId: 'send-email' }).code, 'SKILL_NOT_COVERED');
});

test('优先级：skillId 精确策略覆盖 agent 通配策略（双向）', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, skillId: '*', decision: POLICY_DECISION.DENY });
  policy.setPolicy({ agentId: AGENT, skillId: 'read', decision: POLICY_DECISION.AUTO });
  const exact = policy.evaluate({ agentId: AGENT, skillId: 'read' });
  assert.equal(exact.decision, POLICY_DECISION.AUTO);
  assert.equal(exact.policy.skillId, 'read');
  assert.equal(policy.evaluate({ agentId: AGENT, skillId: 'write' }).decision, POLICY_DECISION.DENY);
});

test('信任不足 => DENY / TRUST_TOO_LOW（信任不可批，金额/双人审批不再检查）', () => {
  const { policy } = makePolicy();
  policy.setPolicy({
    agentId: AGENT,
    skillId: 'pay',
    decision: POLICY_DECISION.AUTO,
    trustLevelMax: 2,
    maxAmountCents: 100,
    requiresDoubleApproval: true
  });
  const denied = policy.evaluate({ agentId: AGENT, skillId: 'pay', amountCents: 999, trustLevel: 3 });
  assert.equal(denied.decision, POLICY_DECISION.DENY);
  assert.equal(denied.code, 'TRUST_TOO_LOW');
  assert.match(denied.reasons.join(' '), /trustLevel 3 > trustLevelMax 2/);

  const allowed = policy.evaluate({ agentId: AGENT, skillId: 'pay', amountCents: 50, trustLevel: 2, hasSecondApprover: true });
  assert.equal(allowed.decision, POLICY_DECISION.AUTO);
});

test('金额超限 → APPROVE 而非 DENY（金额是可批的，信任才不可批）', () => {
  const { policy } = makePolicy();
  policy.setPolicy({
    agentId: AGENT,
    skillId: 'pay',
    decision: POLICY_DECISION.AUTO,
    maxAmountCents: 1000
  });
  const exceeded = policy.evaluate({ agentId: AGENT, skillId: 'pay', amountCents: 1001, trustLevel: 1 });
  assert.equal(exceeded.decision, POLICY_DECISION.APPROVE); // 关键：不是 DENY
  assert.notEqual(exceeded.decision, POLICY_DECISION.DENY);
  assert.equal(exceeded.code, 'AMOUNT_EXCEEDED');
  assert.match(exceeded.reasons.join(' '), /amountCents 1001 > maxAmountCents 1000/);

  // 恰好等于上限不超限
  const atLimit = policy.evaluate({ agentId: AGENT, skillId: 'pay', amountCents: 1000, trustLevel: 1 });
  assert.equal(atLimit.decision, POLICY_DECISION.AUTO);
  assert.equal(atLimit.code, 'POLICY_MATCHED');

  // 即便策略默认是 DENY，金额超限仍走 APPROVE（顺序：金额可批先于默认拒绝）
  policy.setPolicy({
    agentId: AGENT,
    skillId: 'transfer',
    decision: POLICY_DECISION.DENY,
    maxAmountCents: 100
  });
  assert.equal(
    policy.evaluate({ agentId: AGENT, skillId: 'transfer', amountCents: 101 }).decision,
    POLICY_DECISION.APPROVE
  );
});

test('双人审批：requiresDoubleApproval 且无第二审批人 => APPROVE / NEEDS_SECOND_APPROVER', () => {
  const { policy } = makePolicy();
  policy.setPolicy({
    agentId: AGENT,
    skillId: 'delete-account',
    decision: POLICY_DECISION.AUTO,
    requiresDoubleApproval: true
  });
  const missing = policy.evaluate({ agentId: AGENT, skillId: 'delete-account', hasSecondApprover: false });
  assert.equal(missing.decision, POLICY_DECISION.APPROVE);
  assert.equal(missing.code, 'NEEDS_SECOND_APPROVER');
  const present = policy.evaluate({ agentId: AGENT, skillId: 'delete-account', hasSecondApprover: true });
  assert.equal(present.decision, POLICY_DECISION.AUTO);
  assert.equal(present.code, 'POLICY_MATCHED');
});

test('判定顺序：信任 DENY 先于金额 APPROVE 先于双人审批 APPROVE', () => {
  const { policy } = makePolicy();
  policy.setPolicy({
    agentId: AGENT,
    skillId: 'pay',
    decision: POLICY_DECISION.AUTO,
    trustLevelMax: 1,
    maxAmountCents: 10,
    requiresDoubleApproval: true
  });
  assert.equal(
    policy.evaluate({ agentId: AGENT, skillId: 'pay', trustLevel: 4, amountCents: 99 }).code,
    'TRUST_TOO_LOW'
  );
  assert.equal(
    policy.evaluate({ agentId: AGENT, skillId: 'pay', trustLevel: 1, amountCents: 99 }).code,
    'AMOUNT_EXCEEDED'
  );
  assert.equal(
    policy.evaluate({
      agentId: AGENT,
      skillId: 'pay',
      trustLevel: 1,
      amountCents: 5,
      hasSecondApprover: false
    }).code,
    'NEEDS_SECOND_APPROVER'
  );
});

test('removePolicy：存在返回 true、不存在 false；移除后回落到通配/无策略', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, skillId: '*', decision: POLICY_DECISION.APPROVE });
  policy.setPolicy({ agentId: AGENT, skillId: 'read', decision: POLICY_DECISION.AUTO });
  assert.equal(policy.removePolicy({ agentId: AGENT, skillId: 'read' }), true);
  assert.equal(policy.removePolicy({ agentId: AGENT, skillId: 'read' }), false);
  assert.equal(policy.evaluate({ agentId: AGENT, skillId: 'read' }).policy.skillId, '*');
  assert.equal(policy.removePolicy({ agentId: AGENT, skillId: '*' }), true);
  assert.equal(policy.evaluate({ agentId: AGENT, skillId: 'read' }).code, 'SKILL_NOT_COVERED');
  assert.equal(policy.removePolicy({ agentId: 'agent-unknown', skillId: 'x' }), false);
});

test('policiesOf：按 skillId 升序、冻结；未知 Agent 返回空数组', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, skillId: 'write', decision: POLICY_DECISION.AUTO });
  policy.setPolicy({ agentId: AGENT, skillId: '*', decision: POLICY_DECISION.APPROVE });
  const list = policy.policiesOf(AGENT);
  assert.deepEqual(list.map((item) => item.skillId), ['*', 'write']);
  assert.ok(Object.isFrozen(list));
  assert.deepEqual(policy.policiesOf('agent-unknown'), []);
});

test('getPolicy：附加方法，缺失策略 => POLICY_NOT_FOUND；存在则返回同一引用', () => {
  const { policy } = makePolicy();
  const stored = policy.setPolicy({ agentId: AGENT, skillId: 'pay', decision: POLICY_DECISION.APPROVE });
  assert.equal(policy.getPolicy({ agentId: AGENT, skillId: 'pay' }), stored);
  assert.throws(() => policy.getPolicy({ agentId: AGENT, skillId: 'missing' }), {
    code: 'POLICY_NOT_FOUND'
  });
  assert.throws(() => policy.getPolicy({ agentId: 'agent-unknown', skillId: 'pay' }), {
    code: 'POLICY_NOT_FOUND'
  });
});

test('setPolicy 入参校验：非法 decision/skillId/金额 => TypeError（程序员错误）', () => {
  const { policy } = makePolicy();
  assert.throws(() => policy.setPolicy({ skillId: 'x', decision: 'AUTO' }), TypeError);
  assert.throws(() => policy.setPolicy({ agentId: AGENT, skillId: '', decision: 'AUTO' }), TypeError);
  assert.throws(() => policy.setPolicy({ agentId: AGENT, skillId: 'x', decision: 'MAYBE' }), TypeError);
  assert.throws(
    () => policy.setPolicy({ agentId: AGENT, skillId: 'x', decision: 'AUTO', maxAmountCents: -1 }),
    TypeError
  );
  assert.throws(
    () => policy.setPolicy({ agentId: AGENT, skillId: 'x', decision: 'AUTO', maxAmountCents: 1.5 }),
    TypeError
  );
});

test('边界：金额/信任档非法输入归一，且 evaluate 不抛业务异常', () => {
  const { policy } = makePolicy();
  policy.setPolicy({ agentId: AGENT, skillId: 'read', decision: POLICY_DECISION.AUTO, trustLevelMax: 4 });
  assert.doesNotThrow(() => policy.evaluate({ agentId: AGENT, skillId: 'read', amountCents: -5, trustLevel: 99 }));
  const negative = policy.evaluate({ agentId: AGENT, skillId: 'read', amountCents: -5 });
  assert.equal(negative.decision, POLICY_DECISION.AUTO);
  // trustLevel 99 被钳制为 4，> trustLevelMax 4 不成立 => 不误判 DENY
  const clamped = policy.evaluate({ agentId: AGENT, skillId: 'read', trustLevel: 99 });
  assert.equal(clamped.decision, POLICY_DECISION.AUTO);
});

test('ToolPolicyError 带 code 且是 Error 子类', () => {
  const { policy } = makePolicy();
  try {
    policy.getPolicy({ agentId: AGENT, skillId: 'nope' });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof ToolPolicyError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'POLICY_NOT_FOUND');
  }
});
