/**
 * 模块职责：Agent 大模型燃料层的单元测试 —— 配置解析 / 离线降级 / 真实调用（注入 fetch）/ 预算 / 出口护栏
 * 契约来源：apps/im2a/llm.mjs 头注释
 *
 * 设计要点：全部用注入的 fetch 与 now，不依赖网络与真实密钥；离线模式必须可完整运行。
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { createLlm, draftAgentReply, offlineDraft, resolveLlmConfig, AGENT_PERSONAS } from '../apps/im2a/llm.mjs';
import { EgressGuard } from '../services/orchestrator/src/egress-guard.mjs';

function stubFetch({ status = 200, content = '收到，我帮你记下来了。', model = 'deepseek-v4.1-flash', tokens = 42, throws = null } = {}) {
  const calls = [];
  const fetchImpl = async (url, init) => {
    calls.push({ url, init, body: JSON.parse(init.body) });
    if (throws) throw new Error(throws);
    return {
      status,
      async json() {
        if (status !== 200) return { error: { message: 'refused by provider' } };
        return { model, choices: [{ message: { content } }], usage: { prompt_tokens: 20, completion_tokens: 22, total_tokens: tokens } };
      }
    };
  };
  return { fetchImpl, calls };
}

const liveConfig = (env = {}) => resolveLlmConfig({ DEEPSEEK_API_KEY: 'test-key', ...env });

test('配置解析：无密钥 → offline；有密钥 → live；模型/端点可覆写', () => {
  const offline = resolveLlmConfig({});
  assert.equal(offline.mode, 'offline');
  assert.equal(offline.model, 'deepseek-v4.1-flash');
  assert.equal(offline.baseUrl, 'https://api.deepseek.com/v1');
  assert.equal(offline.apiKey, null);

  const live = resolveLlmConfig({
    DEEPSEEK_API_KEY: 'k',
    DEEPSEEK_MODEL: 'deepseek-v4.1-flash-preview',
    LLM_BASE_URL: 'https://llm.example.com/v1/',
    LLM_MAX_TOKENS: '300',
    LLM_TIMEOUT_MS: '5000'
  });
  assert.equal(live.mode, 'live');
  assert.equal(live.model, 'deepseek-v4.1-flash-preview');
  assert.equal(live.baseUrl, 'https://llm.example.com/v1');
  assert.equal(live.maxTokens, 300);
  assert.equal(live.timeoutMs, 5000);
});

test('offline 模式：chat 明确返回未配置，模板回复按角色生成且不假装模型', async () => {
  const llm = createLlm({ config: resolveLlmConfig({}) });
  const chat = await llm.chat([{ role: 'user', content: 'hi' }]);
  assert.equal(chat.ok, false);
  assert.equal(chat.reason, 'LLM_NOT_CONFIGURED');
  assert.equal(llm.stats().mode, 'offline');

  const drafted = await draftAgentReply({ role: 'agent-lin-family', userText: '周六回来吃饭吗', llm });
  assert.equal(drafted.ok, true);
  assert.equal(drafted.source, 'template');
  assert.match(drafted.content, /离线预演/);
  assert.equal(drafted.model, 'offline-template');

  for (const role of Object.keys(AGENT_PERSONAS)) {
    const draft = offlineDraft(role, '测试内容');
    assert.ok(draft.content.includes('测试内容'), role + ' 必须回显用户内容');
  }
});

test('live 模式：真实请求体包含模型/消息/温度，返回内容与用量被记录', async () => {
  const { fetchImpl, calls } = stubFetch();
  const nowValues = [1000, 1035];
  const llm = createLlm({ config: liveConfig({ LLM_TEMPERATURE: '0.2' }), fetchImpl, now: () => nowValues.shift() ?? 1035 });
  const result = await llm.chat([{ role: 'system', content: 'sys' }, { role: 'user', content: '你好' }]);
  assert.equal(result.ok, true);
  assert.equal(result.content, '收到，我帮你记下来了。');
  assert.equal(result.source, 'llm');
  assert.equal(result.model, 'deepseek-v4.1-flash');
  assert.equal(calls[0].url, 'https://api.deepseek.com/v1/chat/completions');
  assert.equal(calls[0].body.model, 'deepseek-v4.1-flash');
  assert.equal(calls[0].body.temperature, 0.2);
  assert.equal(calls[0].init.headers.authorization, 'Bearer test-key');
  const stats = llm.stats();
  assert.equal(stats.calls, 1);
  assert.equal(stats.totalTokens, 42);
  assert.equal(typeof stats.lastLatencyMs, 'number');
  assert.ok(stats.lastLatencyMs >= 0);
  assert.equal(stats.failures, 0);
});

test('provider 拒绝 / 网络异常：返回具名失败原因并计入 failures，不抛穿', async () => {
  const refused = createLlm({ config: liveConfig(), fetchImpl: stubFetch({ status: 500 }).fetchImpl });
  const bad = await refused.chat([{ role: 'user', content: 'hi' }]);
  assert.equal(bad.ok, false);
  assert.equal(bad.reason, 'LLM_HTTP_ERROR');
  assert.match(bad.detail, /HTTP 500/);
  assert.equal(refused.stats().failures, 1);
  assert.match(refused.stats().lastError, /HTTP 500/);

  const broken = createLlm({ config: liveConfig(), fetchImpl: stubFetch({ throws: 'ECONNRESET' }).fetchImpl });
  const failed = await broken.chat([{ role: 'user', content: 'hi' }]);
  assert.equal(failed.ok, false);
  assert.equal(failed.reason, 'LLM_NETWORK_ERROR');
  assert.match(failed.detail, /ECONNRESET/);
});

test('预算硬限制：每小时调用数耗尽后降级为具名拒绝，不再请求 provider', async () => {
  const { fetchImpl, calls } = stubFetch();
  const llm = createLlm({ config: liveConfig({ LLM_MAX_CALLS_PER_HOUR: '1' }), fetchImpl, now: () => 0 });
  const first = await llm.chat([{ role: 'user', content: '1' }]);
  const second = await llm.chat([{ role: 'user', content: '2' }]);
  assert.equal(first.ok, true);
  assert.equal(second.ok, false);
  assert.equal(second.reason, 'LLM_BUDGET_EXHAUSTED');
  assert.equal(calls.length, 1, '超预算后不应继续打 provider');
  assert.equal(llm.stats().budget.callsInWindow, 1);
});

test('draftAgentReply：模型输出先过出口护栏，身份证号被 BLOCK 时内容不进入会话', async () => {
  const guard = new EgressGuard({ clock: () => new Date('2026-10-21T00:00:00Z') });
  const { fetchImpl } = stubFetch({ content: '对方身份证号 110101199003071234，建议直接联系。' });
  const llm = createLlm({ config: liveConfig(), fetchImpl });
  const drafted = await draftAgentReply({ role: 'agent-lin-butler', userText: '帮我看看这个', llm, guard });
  assert.equal(drafted.ok, false);
  assert.equal(drafted.blocked, true);
  assert.equal(drafted.reason, 'EGRESS_BLOCKED');
  assert.ok(Array.isArray(drafted.matched) && drafted.matched.length > 0, '必须给出命中的规则');
});

test('draftAgentReply：正常模型内容经护栏 PASS 后返回 llm 原文；失败时回落模板', async () => {
  const guard = new EgressGuard();
  const { fetchImpl } = stubFetch({ content: '已记录：周六 12:00 家庭聚餐，我会提前一天提醒你。' });
  const llm = createLlm({ config: liveConfig(), fetchImpl });
  const drafted = await draftAgentReply({ role: 'agent-lin-family', userText: '周六回家吃饭', llm, guard });
  assert.equal(drafted.ok, true);
  assert.equal(drafted.source, 'llm');
  assert.match(drafted.content, /周六 12:00/);

  const broken = createLlm({ config: liveConfig(), fetchImpl: stubFetch({ status: 503 }).fetchImpl });
  const fallback = await draftAgentReply({ role: 'agent-lin-family', userText: '周六回家吃饭', llm: broken, guard });
  assert.equal(fallback.ok, true);
  assert.equal(fallback.source, 'template');
  assert.equal(fallback.reason, 'LLM_HTTP_ERROR');
});
