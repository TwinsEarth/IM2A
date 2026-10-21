/**
 * 模块职责：Skill 执行层 + Agent Runtime 单测 —— 规划 / 权限门控 / 本地 Skill / 出口护栏
 * 覆盖：声明式权限清单、信任档位、规划器（离线规则 + 模型 JSON）、模型无法绕过白名单、护栏 BLOCK
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { createAgentRuntime, planOffline, skillCatalog, RUNTIME_VERSION } from '../apps/im2a/agent-runtime.mjs';
import { checkSkillPermission, createSkillExecutor, SKILLS, SKILL_IDS } from '../apps/im2a/skills.mjs';
import { createLlm, resolveLlmConfig } from '../apps/im2a/llm.mjs';
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
    allowedSkills: ['calendar.propose_slot', 'mail.read', 'procurement.compare'],
    ...overrides
  });
  return registry;
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

test('Skill 目录：声明式权限清单完整，纯本地无外部依赖', () => {
  const catalog = skillCatalog();
  assert.deepEqual([...SKILL_IDS].sort(), ['calendar.propose_slot', 'mail.read', 'procurement.compare']);
  assert.equal(catalog.length, 3);
  assert.equal(SKILLS['calendar.propose_slot'].minTrustLevel, TRUST_LEVELS.FAMILY);
});

test('权限判定：声明 + 信任档位双门控，拒绝时给出具名原因', () => {
  assert.equal(checkSkillPermission({ skillId: 'mail.read', agent: { allowedSkills: ['mail.read'] }, trustLevel: 0 }).allowed, true);
  assert.equal(checkSkillPermission({ skillId: 'procurement.compare', agent: { allowedSkills: ['procurement.compare'] }, trustLevel: TRUST_LEVELS.STRANGER }).reason, 'TRUST_TOO_LOW');
  assert.equal(checkSkillPermission({ skillId: 'mail.read', agent: { allowedSkills: [] }, trustLevel: 0 }).reason, 'SKILL_NOT_ALLOWED');
  assert.equal(checkSkillPermission({ skillId: 'nope', agent: { allowedSkills: [] }, trustLevel: 0 }).reason, 'UNKNOWN_SKILL');
});

test('离线规划器：按语义选择 Skill，无匹配时直接回复', () => {
  assert.equal(planOffline('读一下我的邮件').tool, 'mail.read');
  assert.equal(planOffline('帮我比价买把椅子').tool, 'procurement.compare');
  assert.equal(planOffline('安排一下周六的日程').tool, 'calendar.propose_slot');
  assert.equal(planOffline('你好呀').tool, null);
});

test('Runtime（离线）：规划 → 权限 → 本地 Skill 执行 → 汇报', async () => {
  const runtime = createAgentRuntime({
    llm: createLlm({ config: resolveLlmConfig({}) }),
    guard: new EgressGuard(),
    registry: makeRegistry(),
    trustMatrix: new TrustMatrix(),
    skills: createSkillExecutor()
  });
  const result = await runtime.runTurn({ agentId: 'agent-butler', ownerUserId: 'user-a', peerUserId: 'user-a', userText: '帮我读一下邮件' });
  assert.equal(result.ok, true);
  assert.equal(result.runtime, RUNTIME_VERSION);
  assert.equal(result.planner.source, 'offline-rules');
  assert.equal(result.toolCalls[0].skillId, 'mail.read');
  assert.equal(result.toolCalls[0].allowed, true);
  assert.match(result.reply, /未读 3 封/);
});

test('Runtime：未声明 Skill 不执行（模型计划无法绕过声明）', async () => {
  let executed = 0;
  const skills = { execute: async () => { executed += 1; return { ok: true, data: {} }; } };
  const runtime = createAgentRuntime({
    llm: createLlm({ config: resolveLlmConfig({}) }),
    guard: new EgressGuard(),
    registry: makeRegistry({ allowedSkills: [] }),
    trustMatrix: new TrustMatrix(),
    skills
  });
  const result = await runtime.runTurn({ agentId: 'agent-butler', ownerUserId: 'user-a', userText: '帮我读一下邮件' });
  assert.equal(executed, 0);
  assert.equal(result.toolCalls[0].allowed, false);
  assert.equal(result.toolCalls[0].reason, 'SKILL_NOT_ALLOWED');
  assert.equal(runtime.stats().denied, 1);
});

test('Runtime（模型在线）：模型 JSON 计划 → 执行允许的 Skill → 基于工具结果写最终回复', async () => {
  const llm = stubLlm([
    okReply('{"intent":"读邮件","tool":"mail.read","args":{}}'),
    okReply('你有 3 封未读邮件，最重要的是投资人约下周沟通。')
  ]);
  const runtime = createAgentRuntime({
    llm,
    guard: new EgressGuard(),
    registry: makeRegistry(),
    trustMatrix: new TrustMatrix(),
    skills: createSkillExecutor()
  });
  const result = await runtime.runTurn({ agentId: 'agent-butler', ownerUserId: 'user-a', userText: '看看邮件' });
  assert.equal(result.planner.source, 'llm');
  assert.equal(result.engine.source, 'llm');
  assert.equal(result.toolCalls[0].skillId, 'mail.read');
  assert.equal(llm.calls.length, 2);
  assert.match(result.reply, /3 封未读/);
});

test('Runtime：最终回复被出口护栏 BLOCK 时 ok=false、不返回内容', async () => {
  const llm = stubLlm([
    okReply('{"intent":"直接回复","tool":null,"args":{}}'),
    okReply('对方身份证号 110101199003071234')
  ]);
  const runtime = createAgentRuntime({ llm, guard: new EgressGuard(), registry: makeRegistry(), trustMatrix: new TrustMatrix(), skills: createSkillExecutor() });
  const result = await runtime.runTurn({ agentId: 'agent-butler', ownerUserId: 'user-a', userText: '你好' });
  assert.equal(result.ok, false);
  assert.equal(result.blocked, true);
  assert.equal(result.reason, 'EGRESS_BLOCKED');
  assert.equal(result.reply, null);
});

test('Skill 执行器：本地执行并计数；未知 Skill 具名拒绝', async () => {
  const executor = createSkillExecutor();
  const mail = await executor.execute('mail.read');
  assert.equal(mail.ok, true);
  assert.equal(mail.source, 'local');
  assert.equal(mail.data.unread, 3);
  const calendar = await executor.execute('calendar.propose_slot');
  assert.ok(calendar.data.candidates.length >= 2);
  const unknown = await executor.execute('nope');
  assert.equal(unknown.ok, false);
  assert.equal(unknown.reason, 'UNKNOWN_SKILL');
  assert.equal(executor.stats().calls, 3);
});
