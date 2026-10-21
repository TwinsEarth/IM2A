#!/usr/bin/env node
/**
 * 模块职责：LLM 三档降级的**压测与降级验证**（⑥ 的真实 key 前置演练）
 * 契约来源：apps/im2agent/llm.mjs（template → fallback → real）
 *
 * 两种模式：
 *   · 默认 fake：注入可控上游（失败率/延迟），**不需要网络与密钥**，验证"失败必降级、绝不抛穿"
 *   · --real：用 IM2AGENT_LLM_KEY 打真实上游，测真实 p50/p95 与超时降级
 *
 * 用法：
 *   node scripts/llm-stress.mjs --calls 200 --fail-rate 0.3 --latency 40
 *   IM2AGENT_LLM_KEY=sk-xxx node scripts/llm-stress.mjs --real --calls 20
 */
import { createLlm, LLM_MODE } from '../apps/im2agent/llm.mjs';

const argv = process.argv.slice(2);
const arg = (name, fallback) => {
  const index = argv.indexOf(`--${name}`);
  return index >= 0 && argv[index + 1] !== undefined ? argv[index + 1] : fallback;
};
const flag = (name) => argv.includes(`--${name}`);

const CALLS = Number(arg('calls', 120));
const FAIL_RATE = Number(arg('fail-rate', 0.3));
const LATENCY = Number(arg('latency', 40));
const REAL = flag('real');

/** 确定性伪随机（同参数两次跑结果一致，便于对比） */
function mulberry32(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function percentile(sorted, p) {
  if (sorted.length === 0) return 0;
  const index = Math.min(sorted.length - 1, Math.floor((p / 100) * sorted.length));
  return sorted[index];
}

async function runFake() {
  const rand = mulberry32(42);
  let calls = 0;
  let failures = 0;
  const fetchImpl = async () => {
    calls += 1;
    await new Promise((resolve) => setTimeout(resolve, LATENCY));
    const shouldFail = rand() < FAIL_RATE;
    if (shouldFail) {
      failures += 1;
      // 三种典型上游故障：5xx / 空内容 / 网络异常
      const kind = calls % 3;
      if (kind === 0) return { ok: false, status: 500, json: async () => ({}) };
      if (kind === 1) return { ok: true, status: 200, json: async () => ({ choices: [{ message: { content: '' } }] }) };
      throw new Error('network timeout');
    }
    return {
      ok: true,
      status: 200,
      json: async () => ({ choices: [{ message: { content: `草案#${calls}：建议周五 16:00，待你确认。` } }], usage: { total_tokens: 42 } })
    };
  };

  const llm = createLlm({
    config: { apiKey: 'fake-key', baseUrl: 'http://fake', model: 'fake-model', timeoutMs: 1000, maxTokens: 64, offline: false },
    fetchImpl
  });

  const latencies = [];
  const tiers = { [LLM_MODE.TEMPLATE]: 0, [LLM_MODE.FALLBACK]: 0, [LLM_MODE.REAL]: 0 };
  let emptyReplies = 0;
  let thrown = 0;

  for (let i = 0; i < CALLS; i += 1) {
    const startedAt = Date.now();
    try {
      const draft = await llm.draft({ text: `第 ${i} 次：帮我约个时间`, topic: '压测' });
      latencies.push(Date.now() - startedAt);
      tiers[draft.tier] = (tiers[draft.tier] ?? 0) + 1;
      if (typeof draft.reply !== 'string' || draft.reply.trim() === '') emptyReplies += 1;
      // 降级时必须明确标记，不允许把 fallback 当 real
      if (draft.tier === LLM_MODE.FALLBACK && draft.degraded !== true) throw new Error('fallback 未标记 degraded');
    } catch (error) {
      thrown += 1;
    }
  }

  const sorted = [...latencies].sort((a, b) => a - b);
  const stats = llm.stats();
  const report = {
    mode: 'fake',
    calls: CALLS,
    plannedFailRate: FAIL_RATE,
    upstreamCalls: calls,
    upstreamFailures: failures,
    tiers,
    emptyReplies,
    thrown,
    latency: { p50: percentile(sorted, 50), p95: percentile(sorted, 95), max: sorted.at(-1) ?? 0 },
    engine: { mode: stats.mode, failures: stats.failures, templates: stats.templates, lastError: stats.lastError }
  };
  console.log(JSON.stringify(report, null, 2));

  const ok =
    thrown === 0 &&
    emptyReplies === 0 &&
    tiers[LLM_MODE.REAL] + tiers[LLM_MODE.FALLBACK] === CALLS &&
    stats.failures === tiers[LLM_MODE.FALLBACK];
  console.log(ok ? '\n✔ 降级验证通过：无异常穿透、无空回复、失败一律标记 degraded' : '\n✘ 降级验证失败');
  process.exitCode = ok ? 0 : 1;
}

async function runReal() {
  const llm = createLlm({});
  const config = llm.config();
  if (config.hasKey !== true) {
    console.error('未检测到 IM2AGENT_LLM_KEY / DEEPSEEK_API_KEY，无法跑 --real。先跑默认 fake 模式。');
    process.exitCode = 1;
    return;
  }
  const latencies = [];
  const tiers = {};
  for (let i = 0; i < CALLS; i += 1) {
    const startedAt = Date.now();
    const draft = await llm.draft({ text: `第 ${i} 次：用一句话说明今天该先处理哪件事`, topic: '真实压测' });
    latencies.push(Date.now() - startedAt);
    tiers[draft.tier] = (tiers[draft.tier] ?? 0) + 1;
  }
  const sorted = [...latencies].sort((a, b) => a - b);
  console.log(JSON.stringify({ mode: 'real', model: config.model, baseUrl: config.baseUrl, calls: CALLS, tiers, latency: { p50: percentile(sorted, 50), p95: percentile(sorted, 95), max: sorted.at(-1) ?? 0 }, stats: llm.stats() }, null, 2));
}

await (REAL ? runReal() : runFake());
