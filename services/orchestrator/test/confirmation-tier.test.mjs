// 测试：确认层（契约 §28）。
// 覆盖：未登记技能 fail-closed、PLANNER 自动、CONFIRMATION 只认 humanApproved、planner 自称批准被拒并留审计。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  ConfirmationTier,
  ConfirmationTierError,
  TIER_CODE,
  TOOL_TIER
} from '../src/confirmation-tier.mjs';

const AGENT = 'agent-lin-butler';
const CHARGE_SKILL = 'paid-message.charge';
const READ_SKILL = 'calendar.read';

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

const makeTier = (options = {}) => {
  const clock = options.clock ?? makeClock();
  const audits = [];
  const tier = new ConfirmationTier({ clock, onAudit: (event) => audits.push(event) });
  return { clock, tier, audits };
};

test('TOOL_TIER / TIER_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...TOOL_TIER }, { PLANNER: 'PLANNER', CONFIRMATION: 'CONFIRMATION' });
  assert.deepEqual({ ...TIER_CODE }, {
    PLANNER_ALLOWED: 'PLANNER_ALLOWED',
    CONFIRMATION_REQUIRED: 'CONFIRMATION_REQUIRED',
    PLANNER_PREAPPROVAL_REJECTED: 'PLANNER_PREAPPROVAL_REJECTED',
    TIER_SET: 'TIER_SET'
  });
  assert.ok(Object.isFrozen(TOOL_TIER));
  assert.ok(Object.isFrozen(TIER_CODE));
});

test('fail-closed：未登记技能默认 CONFIRMATION，evaluate => REQUIRE_APPROVAL / CONFIRMATION_REQUIRED', () => {
  const { tier } = makeTier();
  assert.equal(tier.tierOf({ agentId: AGENT, skillId: 'unknown.skill' }), TOOL_TIER.CONFIRMATION);
  const result = tier.evaluate({ agentId: AGENT, skillId: 'unknown.skill' });
  assert.equal(result.decision, 'REQUIRE_APPROVAL');
  assert.equal(result.code, 'CONFIRMATION_REQUIRED');
  assert.equal(result.tier, TOOL_TIER.CONFIRMATION);
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.reasons));
  // 连 agentId/skillId 都缺失也 fail-closed
  assert.equal(tier.evaluate({}).code, 'CONFIRMATION_REQUIRED');
});

test('setTier / tierOf：登记与覆盖，policy 冻结且标 TIER_SET；未登记技能仍 CONFIRMATION', () => {
  const { tier, clock } = makeTier();
  const policy = tier.setTier({ agentId: AGENT, skillId: READ_SKILL, tier: TOOL_TIER.PLANNER });
  assert.ok(Object.isFrozen(policy));
  assert.equal(policy.agentId, AGENT);
  assert.equal(policy.skillId, READ_SKILL);
  assert.equal(policy.tier, TOOL_TIER.PLANNER);
  assert.equal(policy.code, 'TIER_SET');
  assert.equal(policy.updatedAt, '2026-10-21T00:00:00.000Z');
  assert.equal(tier.tierOf({ agentId: AGENT, skillId: READ_SKILL }), TOOL_TIER.PLANNER);
  assert.equal(tier.tierOf({ agentId: AGENT, skillId: 'other' }), TOOL_TIER.CONFIRMATION);

  clock.advance(1000);
  tier.setTier({ agentId: AGENT, skillId: READ_SKILL, tier: TOOL_TIER.CONFIRMATION });
  assert.equal(tier.tierOf({ agentId: AGENT, skillId: READ_SKILL }), TOOL_TIER.CONFIRMATION);
  assert.equal(tier.setTier({ agentId: AGENT, skillId: READ_SKILL, tier: TOOL_TIER.CONFIRMATION }).updatedAt, '2026-10-21T00:00:01.000Z');

  // 按 agent × skill 隔离
  assert.equal(tier.tierOf({ agentId: 'agent-other', skillId: READ_SKILL }), TOOL_TIER.CONFIRMATION);
});

test('PLANNER 层：AUTO / PLANNER_ALLOWED（planner 声明与否都不影响）', () => {
  const { tier } = makeTier();
  tier.setTier({ agentId: AGENT, skillId: READ_SKILL, tier: TOOL_TIER.PLANNER });
  for (const plannerClaimsApproved of [false, true]) {
    const result = tier.evaluate({ agentId: AGENT, skillId: READ_SKILL, plannerClaimsApproved });
    assert.equal(result.decision, 'AUTO');
    assert.equal(result.code, 'PLANNER_ALLOWED');
    assert.equal(result.tier, TOOL_TIER.PLANNER);
  }
});

test('CONFIRMATION 层：只有 humanApproved=true 才 AUTO，且 code=CONFIRMATION_REQUIRED、reasons 注明"人确认"', () => {
  const { tier } = makeTier();
  tier.setTier({ agentId: AGENT, skillId: CHARGE_SKILL, tier: TOOL_TIER.CONFIRMATION });
  const result = tier.evaluate({ agentId: AGENT, skillId: CHARGE_SKILL, humanApproved: true });
  assert.equal(result.decision, 'AUTO');
  assert.equal(result.code, 'CONFIRMATION_REQUIRED');
  assert.match(result.reasons.join(' '), /人确认/);
  // 人已批准时 planner 的声明无意义（不会变成别的 code）
  assert.equal(
    tier.evaluate({ agentId: AGENT, skillId: CHARGE_SKILL, humanApproved: true, plannerClaimsApproved: true }).code,
    'CONFIRMATION_REQUIRED'
  );
});

test('红线：扣费技能 planner 声称"我已批准" => 仍 REQUIRE_APPROVAL + PLANNER_PREAPPROVAL_REJECTED（留审计）', () => {
  const { tier, audits } = makeTier();
  tier.setTier({ agentId: AGENT, skillId: CHARGE_SKILL, tier: TOOL_TIER.CONFIRMATION });

  // 未声明也没人批准 => CONFIRMATION_REQUIRED
  assert.equal(tier.evaluate({ agentId: AGENT, skillId: CHARGE_SKILL }).code, 'CONFIRMATION_REQUIRED');

  // planner 自称已批准 => 越权主张被拒
  const rejected = tier.evaluate({
    agentId: AGENT,
    skillId: CHARGE_SKILL,
    plannerClaimsApproved: true,
    humanApproved: false
  });
  assert.equal(rejected.decision, 'REQUIRE_APPROVAL');
  assert.equal(rejected.code, 'PLANNER_PREAPPROVAL_REJECTED');
  assert.equal(rejected.tier, TOOL_TIER.CONFIRMATION);
  assert.match(rejected.reasons.join(' '), /planner/);
  assert.match(rejected.reasons.join(' '), /越权/);

  // 越权主张进审计（记录这次尝试）
  assert.equal(audits.length, 1);
  assert.equal(audits[0].action, 'CONFIRMATION_PREAPPROVAL_REJECTED');
  assert.equal(audits[0].target, CHARGE_SKILL);
  assert.equal(audits[0].detail.code, 'PLANNER_PREAPPROVAL_REJECTED');
  assert.ok(Object.isFrozen(audits[0]));
});

test('CONFIRMATION 层无任何声明：REQUIRE_APPROVAL / CONFIRMATION_REQUIRED，不写越权审计', () => {
  const { tier, audits } = makeTier();
  tier.setTier({ agentId: AGENT, skillId: CHARGE_SKILL, tier: TOOL_TIER.CONFIRMATION });
  const result = tier.evaluate({ agentId: AGENT, skillId: CHARGE_SKILL });
  assert.equal(result.decision, 'REQUIRE_APPROVAL');
  assert.equal(result.code, 'CONFIRMATION_REQUIRED');
  assert.equal(audits.length, 0); // 没有越权主张就没有越权事件
});

test('判定顺序：CONFIRMATION 层先看 humanApproved，再区分 planner 越权/正常待确认', () => {
  const { tier } = makeTier();
  tier.setTier({ agentId: AGENT, skillId: CHARGE_SKILL, tier: TOOL_TIER.CONFIRMATION });
  assert.equal(
    tier.evaluate({ agentId: AGENT, skillId: CHARGE_SKILL, humanApproved: true, plannerClaimsApproved: true }).decision,
    'AUTO'
  );
  assert.equal(
    tier.evaluate({ agentId: AGENT, skillId: CHARGE_SKILL, humanApproved: false, plannerClaimsApproved: true }).code,
    'PLANNER_PREAPPROVAL_REJECTED'
  );
});

test('setTier 入参校验：tier 非法 / id 缺失 => TypeError（程序员错误）', () => {
  const { tier } = makeTier();
  assert.throws(() => tier.setTier({ agentId: AGENT, skillId: 'x', tier: 'AUTO' }), TypeError);
  assert.throws(() => tier.setTier({ agentId: AGENT, skillId: 'x' }), TypeError);
  assert.throws(() => tier.setTier({ skillId: 'x', tier: TOOL_TIER.PLANNER }), TypeError);
  assert.throws(() => tier.setTier({ agentId: AGENT, tier: TOOL_TIER.PLANNER }), TypeError);
});

test('ConfirmationTierError 是 Error 子类（契约导出；判定本身只返回结论不抛业务异常）', () => {
  const error = new ConfirmationTierError('x', 'SOME_CODE');
  assert.ok(error instanceof Error);
  assert.equal(error.name, 'ConfirmationTierError');
  assert.equal(error.code, 'SOME_CODE');
});
