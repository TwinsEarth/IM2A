/**
 * 模块职责：im2agent 的大模型驱动层 —— OpenAI 兼容 client，**三档降级**（模板 → fallback → 真实 LLM）
 * 契约来源：本原型自述；纯 stdlib（node:fetch 内置），兼容 DeepSeek / 通义 / Ollama / vLLM
 *
 * 三档的含义（不是"重试三次"，而是三种**能力级别**）：
 *   1. template：无 API key / 显式 offline —— 用确定性模板产出（永远可用，Demo 默认）
 *   2. fallback：真实调用失败（超时/网络/上游 5xx/json 异常）—— 回落到 deterministic 模板，
 *      并把 failure 记入 stats，页面显示"已降级"，**绝不假装是模型输出**
 *   3. real：真实调用成功 —— 返回模型文本，记录 latency/tokens
 *
 * 铁律：模型只"起草"。它不能调用 Skill、不能扣费、不能审批（见 apps/im2agent/README.md 的边界表）。
 */
export const LLM_MODE = Object.freeze({ TEMPLATE: 'template', FALLBACK: 'fallback', REAL: 'real' });

export function resolveLlmConfig(env = process.env) {
  const apiKey = env.IM2AGENT_LLM_KEY ?? env.DEEPSEEK_API_KEY ?? env.OPENAI_API_KEY ?? null;
  return {
    apiKey,
    baseUrl: (env.IM2AGENT_LLM_BASE_URL ?? 'https://api.deepseek.com/v1').replace(/\/$/, ''),
    model: env.IM2AGENT_LLM_MODEL ?? 'deepseek-chat',
    timeoutMs: Number(env.IM2AGENT_LLM_TIMEOUT_MS ?? 8000),
    maxTokens: Number(env.IM2AGENT_LLM_MAX_TOKENS ?? 256),
    offline: String(env.IM2AGENT_LLM_OFFLINE ?? '0') === '1'
  };
}

const TEMPLATE_RULES = Object.freeze([
  { test: /审批|通过|同意|approve/i, reply: (ctx) => `已呈报：${ctx.topic ?? '待审批事项'}。通过后我会执行并回执。` },
  { test: /付费|扣费|价格|多少钱/, reply: () => '本条消息按"看到且回复才扣费"结算；未回复不扣费。' },
  { test: /陌生人|来意|筛选/, reply: () => '陌生人来意先由过滤 Agent 预审，通过后才进入你的审批队列。' },
  { test: /日程|时间|约/, reply: () => '我先看双方日程，再给出 2–3 个候选时段，由你确认。' }
]);

/** 确定性模板：同样的输入永远同样的输出（可测、可回放） */
export function templateReply({ text, topic } = {}) {
  const input = String(text ?? '');
  for (const rule of TEMPLATE_RULES) {
    if (rule.test.test(input)) return rule.reply({ topic });
  }
  return '收到。我会把这件事拆成步骤，涉及钱或对外承诺时先找你确认。';
}

/**
 * @param {object} [options]
 * @param {Function} [options.fetchImpl] 可注入 fetch（测试用；默认走 globalThis.fetch）
 */
export function createLlm({ config = resolveLlmConfig(), fetchImpl = null, clock = () => new Date() } = {}) {
  const stats = {
    mode: config.offline || config.apiKey === null ? LLM_MODE.TEMPLATE : LLM_MODE.REAL,
    model: config.model,
    calls: 0,
    failures: 0,
    templates: 0,
    tokens: 0,
    latencyMs: 0,
    lastError: null,
    lastAt: null
  };
  const doFetch = fetchImpl ?? globalThis.fetch;

  async function callUpstream({ text, topic, system }) {
    const body = {
      model: config.model,
      max_tokens: config.maxTokens,
      temperature: 0.3,
      messages: [
        {
          role: 'system',
          content:
            system ??
            '你是人机混合 IM 平台里的助手。只起草回复，不得声称已执行任何动作（扣费/审批/对外承诺必须由人确认）。用中文，简洁，最多 60 字。'
        },
        { role: 'user', content: `${topic ? `【事项】${topic}\n` : ''}${text}` }
      ]
    };
    const response = await doFetch(`${config.baseUrl}/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${config.apiKey}` },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(config.timeoutMs)
    });
    if (!response.ok) throw new Error(`upstream HTTP ${response.status}`);
    const payload = await response.json();
    const content = payload?.choices?.[0]?.message?.content;
    if (typeof content !== 'string' || content.trim() === '') throw new Error('上游返回为空');
    return { content: content.trim(), tokens: Number(payload?.usage?.total_tokens ?? 0) };
  }

  return {
    stats: () => ({ ...stats }),
    config: () => ({ baseUrl: config.baseUrl, model: config.model, hasKey: config.apiKey !== null, offline: config.offline }),

    /** 三档降级：template → real；real 失败 → fallback(template) */
    async draft({ text = '', topic = null, system = null } = {}) {
      stats.calls += 1;
      stats.lastAt = clock().toISOString();
      const startedAt = Date.now();

      if (config.offline || config.apiKey === null) {
        stats.mode = LLM_MODE.TEMPLATE;
        stats.templates += 1;
        return { tier: LLM_MODE.TEMPLATE, reply: templateReply({ text, topic }), latencyMs: Date.now() - startedAt, degraded: false };
      }

      try {
        const result = await callUpstream({ text, topic, system });
        stats.mode = LLM_MODE.REAL;
        stats.tokens += result.tokens;
        stats.latencyMs = Date.now() - startedAt;
        return { tier: LLM_MODE.REAL, reply: result.content, latencyMs: stats.latencyMs, degraded: false, tokens: result.tokens };
      } catch (error) {
        // 真实调用失败 → 降级到模板（标记 degraded，页面会显示"已降级"）
        stats.mode = LLM_MODE.FALLBACK;
        stats.failures += 1;
        stats.templates += 1;
        stats.lastError = String(error?.message ?? error);
        return {
          tier: LLM_MODE.FALLBACK,
          reply: templateReply({ text, topic }),
          latencyMs: Date.now() - startedAt,
          degraded: true,
          error: stats.lastError
        };
      }
    }
  };
}
