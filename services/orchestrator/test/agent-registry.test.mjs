// 测试：Agent 注册表（契约 §2）。覆盖正常路径 / 全部 4 个错误码 / 能力声明 / 属主与父子约束。
import test from 'node:test';
import assert from 'node:assert/strict';

import { AGENT_KIND, AgentRegistry, AgentRegistryError } from '../src/agent-registry.mjs';

test('AGENT_KIND 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...AGENT_KIND }, { BUTLER: 'butler', SUBAGENT: 'subagent', SKILL: 'skill' });
  assert.ok(Object.isFrozen(AGENT_KIND));
});

test('register 返回冻结 agent，默认值与契约 §2 一致', () => {
  const registry = new AgentRegistry();
  const agent = registry.register({ agentId: 'agent-b', ownerUserId: 'user-a', kind: AGENT_KIND.BUTLER });
  assert.ok(Object.isFrozen(agent));
  assert.equal(agent.agentId, 'agent-b');
  assert.equal(agent.ownerUserId, 'user-a');
  assert.equal(agent.kind, 'butler');
  assert.equal(agent.domain, null);
  assert.equal(agent.parentId, null);
  assert.deepEqual([...agent.allowedSkills], []);
  assert.deepEqual([...agent.destructiveSkills], []);
  assert.equal(agent.trustZoneMin, 4);
  assert.equal(agent.budgetCentsPerDay, 0);
  assert.equal(agent.approvalThresholdCents, 0);
  assert.equal(agent.model, 'cloud-large');
});

test('AGENT_EXISTS：重复 agentId（含跨属主）拒绝，且不覆盖旧记录', () => {
  const registry = new AgentRegistry();
  registry.register({ agentId: 'agent-b', ownerUserId: 'user-a', kind: AGENT_KIND.BUTLER });
  assert.throws(
    () => registry.register({ agentId: 'agent-b', ownerUserId: 'user-a', kind: AGENT_KIND.BUTLER }),
    { code: 'AGENT_EXISTS' }
  );
  assert.throws(
    () => registry.register({ agentId: 'agent-b', ownerUserId: 'user-b', kind: AGENT_KIND.BUTLER }),
    { code: 'AGENT_EXISTS' }
  );
  assert.equal(registry.get('agent-b').ownerUserId, 'user-a');
});

test('OWNER_REQUIRED：ownerUserId 缺失/空白/非字符串', () => {
  const registry = new AgentRegistry();
  for (const ownerUserId of [undefined, null, '', '   ', 42]) {
    assert.throws(
      () => registry.register({ agentId: 'agent-x', ownerUserId, kind: AGENT_KIND.BUTLER }),
      { code: 'OWNER_REQUIRED' }
    );
  }
  assert.equal(registry.get('agent-x'), undefined);
});

test('PARENT_REQUIRED：子 Agent 缺 parentId / 父未注册 / 父属主不一致', () => {
  const registry = new AgentRegistry();
  assert.throws(
    () => registry.register({ agentId: 'agent-s', ownerUserId: 'user-a', kind: AGENT_KIND.SUBAGENT }),
    { code: 'PARENT_REQUIRED' }
  );
  assert.throws(
    () =>
      registry.register({
        agentId: 'agent-s',
        ownerUserId: 'user-a',
        kind: AGENT_KIND.SUBAGENT,
        parentId: 'agent-missing'
      }),
    { code: 'PARENT_REQUIRED' }
  );
  registry.register({ agentId: 'agent-other', ownerUserId: 'user-b', kind: AGENT_KIND.BUTLER });
  assert.throws(
    () =>
      registry.register({
        agentId: 'agent-s',
        ownerUserId: 'user-a',
        kind: AGENT_KIND.SUBAGENT,
        parentId: 'agent-other'
      }),
    { code: 'PARENT_REQUIRED' }
  );
});

test('AGENT_UNKNOWN：children 查询未知父 Agent；非法 kind；缺失 agentId', () => {
  const registry = new AgentRegistry();
  assert.throws(() => registry.children('agent-missing'), { code: 'AGENT_UNKNOWN' });
  assert.throws(
    () => registry.register({ agentId: 'agent-x', ownerUserId: 'user-a', kind: 'robot' }),
    { code: 'AGENT_UNKNOWN' }
  );
  assert.throws(
    () => registry.register({ agentId: '', ownerUserId: 'user-a', kind: AGENT_KIND.BUTLER }),
    { code: 'AGENT_UNKNOWN' }
  );
});

test('get 未知名返回 undefined；isSkillAllowed 未知名返回 false', () => {
  const registry = new AgentRegistry();
  assert.equal(registry.get('agent-missing'), undefined);
  assert.equal(registry.isSkillAllowed({ agentId: 'agent-missing', skillId: 'x' }), false);
});

test('listByOwner：只返回本属主 Agent，按 agentId 升序；未知属主返回空数组', () => {
  const registry = new AgentRegistry();
  registry.register({ agentId: 'agent-c', ownerUserId: 'user-a', kind: AGENT_KIND.SKILL });
  registry.register({ agentId: 'agent-a', ownerUserId: 'user-a', kind: AGENT_KIND.BUTLER });
  registry.register({ agentId: 'agent-b', ownerUserId: 'user-a', kind: AGENT_KIND.SUBAGENT, parentId: 'agent-a' });
  registry.register({ agentId: 'agent-z', ownerUserId: 'user-b', kind: AGENT_KIND.BUTLER });

  const list = registry.listByOwner('user-a');
  assert.deepEqual(
    list.map((agent) => agent.agentId),
    ['agent-a', 'agent-b', 'agent-c']
  );
  assert.ok(Object.isFrozen(list));
  assert.deepEqual(registry.listByOwner('user-none'), []);
  assert.deepEqual(registry.listByOwner('user-b').map((agent) => agent.agentId), ['agent-z']);
});

test('children：按 agentId 升序返回直接子 Agent，不包含孙子', () => {
  const registry = new AgentRegistry();
  registry.register({ agentId: 'agent-b', ownerUserId: 'user-a', kind: AGENT_KIND.BUTLER });
  registry.register({ agentId: 'agent-s2', ownerUserId: 'user-a', kind: AGENT_KIND.SUBAGENT, parentId: 'agent-b' });
  registry.register({ agentId: 'agent-s1', ownerUserId: 'user-a', kind: AGENT_KIND.SUBAGENT, parentId: 'agent-b' });
  registry.register({ agentId: 'agent-g', ownerUserId: 'user-a', kind: AGENT_KIND.SUBAGENT, parentId: 'agent-s1' });
  assert.deepEqual(
    registry.children('agent-b').map((agent) => agent.agentId),
    ['agent-s1', 'agent-s2']
  );
  assert.deepEqual(registry.children('agent-s1').map((agent) => agent.agentId), ['agent-g']);
});

test('能力声明是门控唯一依据：allowedSkills/destructiveSkills 去重、冻结、可查询', () => {
  const registry = new AgentRegistry();
  registry.register({
    agentId: 'agent-b',
    ownerUserId: 'user-a',
    kind: AGENT_KIND.BUTLER,
    allowedSkills: ['read', 'pay', 'read', '', null, 'delete-file'],
    destructiveSkills: ['delete-file', 'delete-file']
  });
  const agent = registry.get('agent-b');
  assert.deepEqual([...agent.allowedSkills], ['read', 'pay', 'delete-file']);
  assert.deepEqual([...agent.destructiveSkills], ['delete-file']);
  assert.ok(Object.isFrozen(agent.allowedSkills));
  assert.equal(registry.isSkillAllowed({ agentId: 'agent-b', skillId: 'read' }), true);
  assert.equal(registry.isSkillAllowed({ agentId: 'agent-b', skillId: 'transfer' }), false);
  assert.equal(registry.isSkillAllowed({ agentId: 'agent-b', skillId: undefined }), false);
});

test('配置归一：trustZoneMin 钳制 0..4，预算/阈值非负整数，model 保底', () => {
  const registry = new AgentRegistry();
  registry.register({
    agentId: 'agent-b',
    ownerUserId: 'user-a',
    kind: AGENT_KIND.BUTLER,
    trustZoneMin: 9,
    budgetCentsPerDay: -50,
    approvalThresholdCents: 1234.9,
    model: ''
  });
  const agent = registry.get('agent-b');
  assert.equal(agent.trustZoneMin, 4);
  assert.equal(agent.budgetCentsPerDay, 0);
  assert.equal(agent.approvalThresholdCents, 1234);
  assert.equal(agent.model, 'cloud-large');

  registry.register({
    agentId: 'agent-c',
    ownerUserId: 'user-a',
    kind: AGENT_KIND.SKILL,
    trustZoneMin: 2,
    budgetCentsPerDay: 5000
  });
  assert.equal(registry.get('agent-c').trustZoneMin, 2);
  assert.equal(registry.get('agent-c').budgetCentsPerDay, 5000);
});

test('AgentRegistryError 带 code 且是 Error 子类', () => {
  const registry = new AgentRegistry();
  try {
    registry.register({ agentId: 'agent-b', ownerUserId: '', kind: AGENT_KIND.BUTLER });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof AgentRegistryError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'OWNER_REQUIRED');
  }
});
