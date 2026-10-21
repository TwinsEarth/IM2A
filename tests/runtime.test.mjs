/**
 * 模块职责：DSH 风格 Agent Runtime 的单元测试 —— 规划 / 权限门控 / W2M Skill 执行 / 出口护栏 / 离线降级
 * 契约来源：apps/im-live/agent-runtime.mjs、apps/im-live/skills.mjs 头注释
 *
 * 全部注入替身（llm / skills / guard），不依赖真实模型、网络与 relay。
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { createAgentRuntime, planOffline, skillCatalog, RUNTIME_VERSION } from '../apps/im-live/agent-runtime.mjs';
import { checkSkillPermission, createSkillExecutor, isArgvAllowed, SKILLS } from '../apps/im-live/skills.mjs';
import { createLlm, resolveLlmConfig } from '../apps/im-live/llm.mjs';
import { EgressGuard } from '../services/orchestrator/src/egress-guard.mjs';
import { AgentRegistry } from '../services/orchestrator/src/agent-registry.mjs';
import { TrustMatrix, TRUST_LEVELS } from '../services/orchestrator/src/trust-zone.mjs';

function makeRegistry(overrides = {}) {
  const registry = new AgentRegistry();
  registry.register({
    agentId: 'agent-butler',
    ownerUserId: 'user-a',
    kind: 'butler',
    domain: 'butler',
    allowedSkills: ['fleet.devices', 'calendar.propose_slot'],
    ...overrides
  });
  return registry;
}

function recordedSkills({ result = { ok: true, source: 'stub', data: { online: 2, devices: [{ name: 'macmini', online: true }] } } } = {}) {
  const calls = [];
  return {
    calls,
    execute: async (skillId, args) => {
      calls.push({ skillId, args });
      return result;
    }
  };
}

function stubLlm(responses) {
  const queue = [...responses];
  const calls = [];
  return {
    calls,
    config: { mode: 'live' },
    async chat(messages) {
      calls.push(messages);
      const next = queue.shift();
      if (next === undefined) return { ok: false, reason: 'LLM_STUB_EXHAUSTED' };
      return next;
    }
  };
}

const okReply = (content) => ({ ok: true, source: 'llm', model: 'deepseek-v4.1-flash', latencyMs: 12, content, usage: { totalTokens: 20 } });

test('Skill 目录：声明式权限清单完整，argv 白名单只允许只读演示命令', () => {
  const catalog = skillCatalog();
  assert.ok(catalog.length >= 5);
  assert.ok(catalog.some((s) => s.id === 'fleet.run' && s.requiresOperator === true));
  assert.equal(isArgvAllowed('fleet.run', ['node', '--version']), true);
  assert.equal(isArgvAllowed('fleet.run', ['rm', '-rf', '/']), false);
  assert.equal(isArgvAllowed('fleet.devices', ['node', '--version']), false, '只读 Skill 不接受 argv');
  assert.equal(SKILLS['fleet.run'].kind, 'execute');
});

test('权限判定：未声明 / 信任档位过低 / 未知 Skill 都被拒绝，且给出具名原因', () => {
  assert.equal(checkSkillPermission({ skillId: 'fleet.devices', agent: { allowedSkills: ['fleet.devices'] }, trustLevel: 0 }).allowed, true);
  assert.equal(checkSkillPermission({ skillId: 'fleet.run', agent: { allowedSkills: ['fleet.run'] }, trustLevel: TRUST_LEVELS.STRANGER }).reason, 'TRUST_TOO_LOW');
  assert.equal(checkSkillPermission({ skillId: 'fleet.devices', agent: { allowedSkills: [] }, trustLevel: 0 }).reason, 'SKILL_NOT_ALLOWED');
  assert.equal(checkSkillPermission({ skillId: 'nope', agent: { allowedSkills: [] }, trustLevel: 0 }).reason, 'UNKNOWN_SKILL');
});

test('离线规划器：按语义选择只读 Skill，无匹配时直接回复', () => {
  assert.equal(planOffline('帮我看看哪些机器在线').tool, 'fleet.devices');
  assert.equal(planOffline('读一下我的邮件').tool, 'mail.read');
  assert.equal(planOffline('帮我比价买把椅子').tool, 'procurement.compare');
  assert.equal(planOffline('安排一下周六的日程').tool, 'calendar.propose_slot');
  assert.equal(planOffline('你好呀').tool, null);
});

test('Runtime（离线）：规划 → 权限 → Skill 执行 → 汇报；审计由调用方负责，runtime 只返回工具调用', async () => {
  const skills = recordedSkills();
  const runtime = createAgentRuntime({ llm: createLlm({ config: resolveLlmConfig({}) }), guard: new EgressGuard(), registry: makeRegistry(), trustMatrix: new TrustMatrix(), skills });
  const result = await runtime.runTurn({ agentId: 'agent-butler', ownerUserId: 'user-a', peerUserId: 'user-a', userText: '帮我看看哪些机器在线' });
  assert.equal(result.ok, true);
  assert.equal(result.runtime, RUNTIME_VERSION);
  assert.equal(result.planner.source, 'offline-rules');
  assert.equal(skills.calls.length, 1);
  assert.equal(skills.calls[0].skillId, 'fleet.devices');
  assert.equal(result.toolCalls[0].allowed, true);
  assert.match(result.reply, /fleet 状态/);
  assert.equal(runtime.stats().toolCalls, 1);
  assert.equal(runtime.stats().turns, 1);
});

test('Runtime：Agent 未声明该 Skill 时拒绝执行，模型计划无法绕过声明', async () => {
  const skills = recordedSkills();
  const runtime = createAgentRuntime({
    llm: createLlm({ config: resolveLlmConfig({}) }),
    guard: new EgressGuard(),
    registry: makeRegistry({ allowedSkills: [] }),
    trustMatrix: new TrustMatrix(),
    skills
  });
  const result = await runtime.runTurn({ agentId: 'agent-butler', ownerUserId: 'user-a', userText: '帮我看看哪些机器在线' });
  assert.equal(result.ok, true, '拒绝 Skill 不代表不能回复');
  assert.equal(skills.calls.length, 0, '未授权 Skill 绝不执行');
  assert.equal(result.toolCalls[0].allowed, false);
  assert.equal(result.toolCalls[0].reason, 'SKILL_NOT_ALLOWED');
  assert.equal(runtime.stats().denied, 1);
});

test('Runtime（模型在线）：模型输出 JSON 计划 → 执行被允许的 Skill → 基于工具结果写最终回复', async () => {
  const skills = recordedSkills();
  const llm = stubLlm([
    okReply('{"intent":"查看 fleet","tool":"fleet.devices","args":{}}'),
    okReply('fleet 里有 2 台在线：macmini。要不要我继续查任务队列？')
  ]);
  const runtime = createAgentRuntime({ llm, guard: new EgressGuard(), registry: makeRegistry(), trustMatrix: new TrustMatrix(), skills });
  const result = await runtime.runTurn({ agentId: 'agent-butler', ownerUserId: 'user-a', userText: '现在机器都在线吗' });
  assert.equal(result.ok, true);
  assert.equal(result.planner.source, 'llm');
  assert.equal(result.engine.source, 'llm');
  assert.equal(skills.calls.length, 1);
  assert.equal(llm.calls.length, 2, '一次规划 + 一次起草');
  assert.match(result.reply, /2 台在线/);
});

test('Runtime：模型计划里的未授权 Skill 被拒绝执行；模型直接给最终回复时不调用工具', async () => {
  const skills = recordedSkills();
  const llm = stubLlm([okReply('{"intent":"想直接转账","tool":"fleet.run","args":{"machineId":"m1","argv":["rm","-rf","/"]}}'), okReply('抱歉，我不能执行这个操作。')]);
  const runtime = createAgentRuntime({ llm, guard: new EgressGuard(), registry: makeRegistry(), trustMatrix: new TrustMatrix(), skills });
  const result = await runtime.runTurn({ agentId: 'agent-butler', ownerUserId: 'user-a', userText: '帮我转账' });
  assert.equal(result.ok, true);
  assert.equal(skills.calls.length, 0);
  assert.equal(result.toolCalls[0].allowed, false);
  assert.equal(result.toolCalls[0].reason, 'SKILL_NOT_ALLOWED', '未声明的 Skill 先被声明层拒绝');
});

test('Runtime：最终回复被出口护栏 BLOCK 时 ok=false、不返回内容', async () => {
  const guard = new EgressGuard();
  const skills = recordedSkills();
  const runtime = createAgentRuntime({ llm: createLlm({ config: resolveLlmConfig({}) }), guard, registry: makeRegistry(), trustMatrix: new TrustMatrix(), skills });
  // 让离线模板带上身份证号（不能直接改模板，所以走 live 模式模型草稿）
  const blockedLlm = stubLlm([
    okReply('{"intent":"直接回复","tool":null,"args":{}}'),
    { ok: true, source: 'llm', model: 'm', latencyMs: 1, content: '这是对方身份证号 110101199003071234', usage: { totalTokens: 5 } }
  ]);
  const runtime2 = createAgentRuntime({ llm: blockedLlm, guard: new EgressGuard(), registry: makeRegistry(), trustMatrix: new TrustMatrix(), skills });
  const result = await runtime2.runTurn({ agentId: 'agent-butler', ownerUserId: 'user-a', userText: '你好' });
  assert.equal(result.ok, false);
  assert.equal(result.blocked, true);
  assert.equal(result.reason, 'EGRESS_BLOCKED');
  assert.equal(result.reply, null);
  assert.ok(runtime.stats().turns >= 0);
});

test('Skill 执行器：fleet.devices 未配置 token 时返回 simulated 空列，不虚构在线机器', async () => {
  const executor = createSkillExecutor({ relayUrl: 'http://relay.test', operatorToken: null });
  const result = await executor.execute('fleet.devices', {});
  assert.equal(result.ok, true);
  assert.equal(result.source, 'simulated');
  assert.ok(Array.isArray(result.data.devices));
  assert.equal(result.data.devices.length, 0);
  assert.match(result.data.note, /不假装/);
  const stats = executor.stats();
  assert.equal(stats.calls, 1);
});

test('Skill 执行器：fleet.run 参数越界 / 未配置 token 都拒绝；注入派发器后返回真实结果', async () => {
  const offline = createSkillExecutor({ relayUrl: 'http://relay.test', operatorToken: null });
  assert.equal((await offline.execute('fleet.run', { machineId: 'm1', argv: ['rm', '-rf', '/'] })).reason, 'ARGV_NOT_ALLOWED');
  assert.equal((await offline.execute('fleet.run', { machineId: 'm1' })).reason, 'FLEET_NOT_CONFIGURED', '默认 argv 合法但缺 token 也拒绝');

  const dispatched = [];
  const online = createSkillExecutor({
    relayUrl: 'http://relay.test',
    operatorToken: 'op-token',
    dispatchProbe: async (machineId, argv) => {
      dispatched.push({ machineId, argv });
      return { ok: true, status: 'done', outcome: 'ok', exitCode: 0, stdout: 'v24.21.0' };
    }
  });
  const result = await online.execute('fleet.run', { machineId: 'm1', argv: ['node', '--version'] });
  assert.equal(result.ok, true);
  assert.equal(result.machineId, 'm1');
  assert.equal(dispatched[0].machineId, 'm1');
  assert.equal(result.result.stdout, 'v24.21.0');
});
