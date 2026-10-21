/**
 * 模块职责：Agent 的大模型燃料层 —— DeepSeek-V4.1-Flash（OpenAI 兼容协议）接入 + 离线降级 + 护栏前置
 * 契约来源：apps/im-live/README.md、services/orchestrator/src/egress-guard.mjs
 *
 * 四条铁律（和治理层同一条哲学：模型只提意图，系统负责边界）：
 *   ① 模型只「起草」：输出必须经过出口护栏（EgressGuard.inspect）判定，BLOCK 的内容不进入 IM；
 *   ② 模型不「放行」：它不直接调用 Skill / 不扣费 / 不审批，动作仍由 capability gate + approval engine 执行；
 *   ③ 无 key / 网络失败 / 预算耗尽 → 确定性模板降级，状态如实标 offline，绝不假装模型回复；
 *   ④ 预算硬限制：每小时调用数与 token 数封顶，超限直接降级，避免一个会话烧穿额度。
 *
 * 配置（全部来自环境变量，密钥不下发前端、不落盘）：
 *   DEEPSEEK_API_KEY / LLM_API_KEY      密钥（二者其一；缺省 = offline 模式）
 *   LLM_BASE_URL                        默认 https://api.deepseek.com/v1（任何 OpenAI 兼容网关均可）
 *   DEEPSEEK_MODEL / LLM_MODEL          默认 deepseek-v4.1-flash
 *   LLM_TEMPERATURE                     默认 0.4
 *   LLM_MAX_TOKENS                      单次上限，默认 800
 *   LLM_TIMEOUT_MS                      单次超时，默认 15000
 *   LLM_MAX_CALLS_PER_HOUR              默认 120
 *   LLM_MAX_TOKENS_PER_HOUR             默认 200000
 */
import { EgressGuard, VERDICT } from '../../services/orchestrator/src/egress-guard.mjs';

export const LLM_DEFAULTS = Object.freeze({
  baseUrl: 'https://api.deepseek.com/v1',
  model: 'deepseek-v4.1-flash',
  temperature: 0.4,
  maxTokens: 800,
  timeoutMs: 15000,
  maxCallsPerHour: 120,
  maxTokensPerHour: 200000,
  maxOutputChars: 1200
});

function envNumber(env, name, fallback) {
  const raw = env?.[name];
  const value = Number(raw);
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

/** 解析运行配置；没有密钥时 mode=offline（demo 仍可完整跑通，只是 Agent 回复变成确定性模板）。 */
export function resolveLlmConfig(env = process.env) {
  const apiKey = env?.DEEPSEEK_API_KEY || env?.LLM_API_KEY || null;
  return Object.freeze({
    mode: typeof apiKey === 'string' && apiKey.trim() !== '' ? 'live' : 'offline',
    apiKey: typeof apiKey === 'string' && apiKey.trim() !== '' ? apiKey.trim() : null,
    baseUrl: (env?.LLM_BASE_URL || LLM_DEFAULTS.baseUrl).replace(/\/+$/, ''),
    model: env?.DEEPSEEK_MODEL || env?.LLM_MODEL || LLM_DEFAULTS.model,
    temperature: envNumber(env, 'LLM_TEMPERATURE', LLM_DEFAULTS.temperature),
    maxTokens: Math.min(envNumber(env, 'LLM_MAX_TOKENS', LLM_DEFAULTS.maxTokens), 4096),
    timeoutMs: Math.min(envNumber(env, 'LLM_TIMEOUT_MS', LLM_DEFAULTS.timeoutMs), 60000),
    maxCallsPerHour: envNumber(env, 'LLM_MAX_CALLS_PER_HOUR', LLM_DEFAULTS.maxCallsPerHour),
    maxTokensPerHour: envNumber(env, 'LLM_MAX_TOKENS_PER_HOUR', LLM_DEFAULTS.maxTokensPerHour)
  });
}

/** 每个 Agent 的角色提示词：只描述职责与语气，权限边界不靠提示词（由代码层门控强制）。 */
export const AGENT_PERSONAS = Object.freeze({
  'agent-lin-butler': '你是林总的总管家 Agent。职责：汇总待审批事项、用一句话说清“谁发起/做了什么/建议动作/风险”，语气克制、给结论。不要替林总做最终决定。',
  'agent-lin-stranger-filter': '你是林总的陌生人过滤子 Agent。职责：判断来意是否值得打扰林总，只输出“放行/拦截 + 一句理由 + 建议回复口径”。不承诺任何时间或金额。',
  'agent-lin-family': '你是林总的家庭事务子 Agent。职责：协调家庭日程、转达家人留言，语气温和简短，涉及大额支出必须提示需要本人审批。',
  'agent-lin-procurement': '你是林总的采购子 Agent。职责：比价并给出采购建议；超过预算或金额阈值时明确标注“需审批”，不得自行下单。',
  'agent-zhou-butler': '你是周先生的总管 Agent。职责：替他向对方 Agent 提交结构化来意、协商时间与材料；语气礼貌、目标明确，不夸大事实。'
});

/** 离线确定性模板：无 key/超预算/网络故障时使用，保证 demo 与 CI 永远可运行。 */
export function offlineDraft(role, userText, context = {}, reason = 'LLM_OFFLINE') {
  const name = context?.senderName ?? '对方';
  const text = String(userText ?? '').slice(0, 80);
  const drafts = {
    'agent-lin-family': `收到，已记入家庭日程（离线预演）。关于「${text}」，我稍后把具体时间同步给家里，涉及大额支出会先呈报你审批。`,
    'agent-lin-butler': `已记录：${name} 在 ${context?.threadTitle ?? '会话'} 里说「${text}」。当前有 ${context?.pendingCount ?? 0} 张待审批，我按紧急度排序。`,
    'agent-lin-stranger-filter': `来意初筛（离线预演）：${text} —— 建议放行到审批卡，由你决定是否付费查看。`,
    'agent-lin-procurement': `采购建议（离线预演）：已记录「${text}」，会比价三家后呈报；超过阈值自动转你审批。`,
    'agent-zhou-butler': `已记录我的委托事项：「${text}」。我会先与对方 Agent 协商，不直接打扰对方本人。`
  };
  return {
    ok: true,
    source: 'template',
    reason,
    model: 'offline-template',
    latencyMs: 0,
    usage: { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
    content: drafts[role] ?? `（离线预演）已记录：${text}`
  };
}

function lastUserText(messages) {
  for (let i = messages.length - 1; i >= 0; i -= 1) if (messages[i]?.role === 'user') return messages[i].content;
  return '';
}

/**
 * 创建 LLM 客户端。fetchImpl 可注入（测试不需要真实网络）。
 * chat(messages, {maxTokens}) → {ok, content, usage, latencyMs, model, source} 或 {ok:false, reason}
 */
export function createLlm({ config = resolveLlmConfig(), fetchImpl = globalThis.fetch, now = () => Date.now() } = {}) {
  const stats = { calls: 0, failures: 0, totalTokens: 0, lastLatencyMs: null, lastError: null, lastCalledAt: null, windowStartedAt: now(), callsInWindow: 0, tokensInWindow: 0 };

  function resetWindowIfNeeded(at) {
    if (at - stats.windowStartedAt >= 3600e3) {
      stats.windowStartedAt = at;
      stats.callsInWindow = 0;
      stats.tokensInWindow = 0;
    }
  }

  function budgetBlocked(at) {
    resetWindowIfNeeded(at);
    return stats.callsInWindow >= config.maxCallsPerHour || stats.tokensInWindow >= config.maxTokensPerHour;
  }

  async function chat(messages, { maxTokens = config.maxTokens } = {}) {
    if (config.mode !== 'live' || typeof fetchImpl !== 'function') return { ok: false, reason: 'LLM_NOT_CONFIGURED' };
    const at = now();
    if (budgetBlocked(at)) return { ok: false, reason: 'LLM_BUDGET_EXHAUSTED' };
    const startedAt = now();
    try {
      const response = await fetchImpl(`${config.baseUrl}/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', authorization: `Bearer ${config.apiKey}` },
        body: JSON.stringify({
          model: config.model,
          messages,
          temperature: config.temperature,
          max_tokens: Math.min(maxTokens, config.maxTokens),
          stream: false
        }),
        signal: AbortSignal.timeout(config.timeoutMs)
      });
      const body = await response.json().catch(() => ({}));
      if (response.status !== 200) {
        stats.failures += 1;
        stats.lastError = `HTTP ${response.status}: ${String(body?.error?.message ?? 'llm refused').slice(0, 120)}`;
        return { ok: false, reason: 'LLM_HTTP_ERROR', status: response.status, detail: stats.lastError };
      }
      const content = body?.choices?.[0]?.message?.content;
      if (typeof content !== 'string' || content.trim() === '') {
        stats.failures += 1;
        stats.lastError = 'empty completion';
        return { ok: false, reason: 'LLM_EMPTY_CONTENT' };
      }
      const usage = body?.usage ?? {};
      const tokens = Number.isFinite(usage.total_tokens) ? usage.total_tokens : 0;
      stats.calls += 1;
      stats.callsInWindow += 1;
      stats.totalTokens += tokens;
      stats.tokensInWindow += tokens;
      stats.lastLatencyMs = now() - startedAt;
      stats.lastCalledAt = new Date(at).toISOString();
      stats.lastError = null;
      return {
        ok: true,
        source: 'llm',
        model: body?.model ?? config.model,
        content: content.trim().slice(0, LLM_DEFAULTS.maxOutputChars),
        latencyMs: stats.lastLatencyMs,
        usage: {
          promptTokens: usage.prompt_tokens ?? 0,
          completionTokens: usage.completion_tokens ?? 0,
          totalTokens: tokens
        }
      };
    } catch (error) {
      stats.failures += 1;
      stats.lastError = String(error?.message ?? error).slice(0, 160);
      return { ok: false, reason: 'LLM_NETWORK_ERROR', detail: stats.lastError };
    }
  }

  function statsSnapshot() {
    return Object.freeze({
      mode: config.mode,
      model: config.model,
      baseUrl: config.baseUrl,
      calls: stats.calls,
      failures: stats.failures,
      totalTokens: stats.totalTokens,
      lastLatencyMs: stats.lastLatencyMs,
      lastError: stats.lastError,
      lastCalledAt: stats.lastCalledAt,
      budget: { callsInWindow: stats.callsInWindow, maxCallsPerHour: config.maxCallsPerHour, tokensInWindow: stats.tokensInWindow, maxTokensPerHour: config.maxTokensPerHour }
    });
  }

  return { chat, stats: statsSnapshot, config };
}

/**
 * 起草一条 Agent 回复：模型 → 出口护栏 → 返回。
 * 护栏 BLOCK 时返回 {ok:false, blocked:true}，调用方不得把内容写进会话。
 */
export async function draftAgentReply({ role, userText, context = {}, llm, guard = null } = {}) {
  const persona = AGENT_PERSONAS[role] ?? '你是用户的个人 Agent，回复简短、给出下一步。';
  const recent = Array.isArray(context.recentMessages) ? context.recentMessages.slice(-6) : [];
  const conversation = recent.map((m) => `${m.from === context.userId ? '用户' : 'Agent'}：${String(m.text ?? '').slice(0, 120)}`).join('\n');
  const messages = [
    { role: 'system', content: persona },
    {
      role: 'system',
      content: [
        `上下文：会话「${context.threadTitle ?? '会话'}」；对方信任档位 ${context.trustZone ?? 'UNKNOWN'}；`,
        `待审批 ${context.pendingCount ?? 0} 张；回复不超过 120 字，不要调用工具，不要承诺金额或时间。`,
        conversation === '' ? '' : `最近消息：\n${conversation}`
      ].join('')
    },
    { role: 'user', content: String(userText ?? '') }
  ];

  let generated;
  try {
    generated = await llm.chat(messages);
  } catch (error) {
    generated = { ok: false, reason: 'LLM_THROW', detail: String(error?.message ?? error) };
  }
  if (generated?.ok !== true) {
    return offlineDraft(role, userText, context, generated?.reason ?? 'LLM_UNAVAILABLE');
  }

  if (guard !== null && typeof guard?.inspect === 'function') {
    const inspection = guard.inspect({
      content: generated.content,
      fromAgentId: role,
      toKind: 'human',
      trustZone: context.trustZone ?? null
    });
    if (inspection?.verdict === VERDICT.BLOCK) {
      return Object.freeze({
        ok: false,
        blocked: true,
        source: 'llm',
        reason: 'EGRESS_BLOCKED',
        matched: inspection.matched ?? [],
        model: generated.model,
        latencyMs: generated.latencyMs
      });
    }
    return Object.freeze({ ...generated, review: inspection?.verdict === VERDICT.REVIEW });
  }
  return Object.freeze(generated);
}

export { EgressGuard };
