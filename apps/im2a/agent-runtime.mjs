/**
 * 模块职责：Harness 风格 Agent Runtime（规划 → 权限门控 → Skill → 起草 → 出口护栏）—— Agent 循环 = 规划 → 权限门控 → Skill 执行 → 起草 → 出口护栏
 * 契约来源：docs/02 §Harness 映射、apps/im2a/llm.mjs、apps/im2a/skills.mjs
 *
 * 与 Harness 八组件的映射（简化版，但每个组件都有代码位置）：
 *   systemPrompt   → AGENT_PERSONAS（llm.mjs）
 *   guardrails     → EgressGuard（本文件出口护栏）+ capability 权限门控（本文件）
 *   memory         → context.recentMessages（会话记忆，后续接 memory-store）
 *   toolGuidance   → planner 提示词（本文件）
 *   subagents      → AgentRegistry.children / 会话角色映射
 *   skills         → SKILLS 目录 + checkSkillPermission + createSkillExecutor（本地 Skill 执行层）
 *   scripts        → 离线确定性规划器（无 key/失败时仍可执行）
 *   plugins        → 生产替换点：把 skills.mjs 换成 MCP/工具插件适配器即可
 *
 * 硬约束：模型输出只作为「计划建议」，Skill 是否执行由 checkSkillPermission 决定；
 * 模型不能直接调用 Skill，也不能改 argv 白名单之外的东西。
 */
import { VERDICT } from '../../services/orchestrator/src/egress-guard.mjs';
import { AGENT_PERSONAS, offlineDraft } from './llm.mjs';
import { SKILLS, checkSkillPermission } from './skills.mjs';

export const RUNTIME_VERSION = 'im-live-agent-runtime/1';

/** 供 UI/运维展示的 Skill 目录（不含执行函数）。 */
export function skillCatalog() {
  return Object.freeze(
    Object.entries(SKILLS).map(([id, skill]) =>
      Object.freeze({ id, title: skill.title, kind: skill.kind, minTrustLevel: skill.minTrustLevel, requiresOperator: skill.requiresOperator, description: skill.description })
    )
  );
}

function parsePlan(content) {
  if (typeof content !== 'string') return null;
  const fenced = content.match(/```(?:json)?\s*([\s\S]*?)```/);
  const text = (fenced ? fenced[1] : content).trim();
  const start = text.indexOf('{');
  const end = text.lastIndexOf('}');
  if (start < 0 || end <= start) return null;
  try {
    const parsed = JSON.parse(text.slice(start, end + 1));
    return parsed !== null && typeof parsed === 'object' ? parsed : null;
  } catch {
    return null;
  }
}

/**
 * 离线确定性规划器：无模型时也能演示「Agent 会选 Skill」——这是 scripts 组件的演示实现。
 * 规则保守：只选择只读 Skill；可执行 Skill 需要显式机器 ID 才会被选择（避免误触发）。
 */
export function planOffline(userText, context = {}) {
  const text = String(userText ?? '');
  if (/邮件|收件箱|mail/i.test(text)) return { tool: 'mail.read', args: {}, intent: '读取邮件摘要' };
  if (/买|采购|比价/.test(text)) return { tool: 'procurement.compare', args: { item: text.slice(0, 20) }, intent: '采购比价' };
  if (/日程|约|时间|会议|吃饭/.test(text)) return { tool: 'calendar.propose_slot', args: {}, intent: '建议时段' };
  return { tool: null, args: {}, intent: '直接回复' };
}

function summarizeToolResult(skillId, result) {
  if (result?.ok !== true) return `Skill ${skillId} 未执行：${result?.reason ?? 'UNKNOWN'}（不会假装成功）`;
  if (skillId === 'calendar.propose_slot') return `建议时段：${(result.data?.candidates ?? []).map((c) => `${c.day} ${c.time}${c.conflict ? '（冲突）' : ''}`).join('；')}`;
  if (skillId === 'mail.read') return `未读 ${result.data?.unread ?? 0} 封：${(result.data?.highlights ?? []).join('；')}`;
  if (skillId === 'procurement.compare') return `${result.data?.note ?? ''} 建议：${result.data?.recommendation ?? ''}`;
  return JSON.stringify(result).slice(0, 300);
}

/**
 * 创建 Agent Runtime。
 * @param {object} options
 * @param {object} options.llm 大模型客户端（createLlm 产物）
 * @param {object} options.guard 出口护栏（EgressGuard 实例）
 * @param {object} options.registry AgentRegistry（读取 Agent 声明）
 * @param {object} options.trustMatrix TrustMatrix（信任档位）
 * @param {object} options.skills createSkillExecutor 产物
 * @param {object} [options.capabilityGate] 可选：既有 CapabilityGate（本轮只用声明校验）
 */
export function createAgentRuntime({ llm, guard, registry, trustMatrix, skills, capabilityGate = null, clock = () => new Date() } = {}) {
  const stats = { turns: 0, llmPlans: 0, offlinePlans: 0, toolCalls: 0, denied: 0, lastAt: null };

  function agentOf(agentId) {
    try {
      return registry?.get(agentId) ?? null;
    } catch {
      return null;
    }
  }

  function trustLevelFor({ ownerUserId, peerUserId }) {
    if (peerUserId === null || peerUserId === undefined || peerUserId === ownerUserId) return 0;
    try {
      return trustMatrix?.levelOf({ userId: ownerUserId, targetId: peerUserId }) ?? 4;
    } catch {
      return 4;
    }
  }

  async function runTurn({ agentId, ownerUserId, peerUserId = null, userText = '', context = {} } = {}) {
    stats.turns += 1;
    stats.lastAt = clock().toISOString();
    const agent = agentOf(agentId);
    if (agent === null) return { ok: false, reason: 'AGENT_UNKNOWN', reply: null, toolCalls: [] };
    const trustLevel = trustLevelFor({ ownerUserId, peerUserId });
    const toolCalls = [];

    // ① 规划：优先真实模型；无 key/失败时用确定性规划器（scripts 组件）
    let plan = null;
    let plannerEngine = null;
    const persona = AGENT_PERSONAS[agentId] ?? '你是用户的个人 Agent。';
    const toolGuidance = `可用 Skill：${skillCatalog().map((s) => `${s.id}(${s.kind})`).join('、')}。` +
      '只输出 JSON：{"intent":"一句话","tool":"skillId 或 null","args":{}}；禁止输出其他字段、禁止调用未列出的 Skill。';
    const planMessages = [
      { role: 'system', content: persona },
      { role: 'system', content: toolGuidance },
      { role: 'user', content: String(userText ?? '') }
    ];
    if (llm?.config?.mode === 'live') {
      const planned = await llm.chat(planMessages, { maxTokens: 300 });
      if (planned?.ok === true) {
        plan = parsePlan(planned.content);
        plannerEngine = { source: 'llm', model: planned.model, latencyMs: planned.latencyMs };
        stats.llmPlans += 1;
      }
    }
    if (plan === null) {
      plan = planOffline(userText, context);
      plannerEngine = plannerEngine ?? { source: 'offline-rules', model: 'offline-planner', latencyMs: 0 };
      stats.offlinePlans += 1;
    }

    // ② 权限门控 + ③ 执行（模型的选择只是「建议」）
    let toolContext = '';
    if (plan.tool !== null && plan.tool !== undefined) {
      const permission = checkSkillPermission({ skillId: plan.tool, agent, trustLevel });
      const call = { skillId: plan.tool, args: plan.args ?? {}, allowed: permission.allowed, reason: permission.reason ?? null };
      toolCalls.push(call);
      if (!permission.allowed) {
        stats.denied += 1;
      } else {
        stats.toolCalls += 1;
        const result = await skills.execute(plan.tool, plan.args ?? {});
        call.result = { ok: result.ok === true, source: result.source ?? null, reason: result.reason ?? null };
        toolContext = summarizeToolResult(plan.tool, result);
      }
    }

    // ④ 起草：模型基于工具结果写最终回复；失败回落离线模板
    let drafted;
    if (llm?.config?.mode === 'live') {
      const replyMessages = [
        { role: 'system', content: persona },
        { role: 'system', content: `工具结果（若为空表示没有调用工具）：${toolContext || '无'}。用不超过 120 字向用户汇报，不要编造结果，不要承诺金额或时间。` }
      ];
      const recent = Array.isArray(context.recentMessages) ? context.recentMessages.slice(-4) : [];
      for (const message of recent) replyMessages.push({ role: message.from === ownerUserId ? 'user' : 'assistant', content: String(message.text ?? '').slice(0, 200) });
      replyMessages.push({ role: 'user', content: String(userText ?? '') });
      drafted = await llm.chat(replyMessages);
    }
    if (drafted?.ok !== true) {
      drafted = toolContext === ''
        ? offlineDraft(agentId, userText, context, drafted?.reason ?? 'LLM_OFFLINE')
        : Object.freeze({ ok: true, source: 'tool-summary', model: 'offline-summary', latencyMs: 0, usage: { totalTokens: 0 }, content: `已完成：${toolContext}` });
    }

    // ⑤ 出口护栏：模型草稿一律过审
    if (drafted?.ok !== true) {
      return { ok: false, blocked: true, reason: drafted?.reason ?? 'EGRESS_BLOCKED', reply: null, toolCalls };
    }
    if (guard !== null && typeof guard?.inspect === 'function') {
      const inspection = guard.inspect({ content: drafted.content, fromAgentId: agentId, toKind: 'human', trustZone: String(trustLevel) });
      if (inspection?.verdict === VERDICT.BLOCK) {
        return { ok: false, blocked: true, reason: 'EGRESS_BLOCKED', matched: inspection.matched ?? [], reply: null, toolCalls };
      }
    }
    return {
      ok: true,
      reply: drafted.content,
      toolCalls,
      planner: plannerEngine,
      engine: { source: drafted.source ?? (llm?.config?.mode === 'live' ? 'llm' : 'template'), model: drafted.model ?? null, latencyMs: drafted.latencyMs ?? 0, tokens: drafted.usage?.totalTokens ?? 0 },
      runtime: RUNTIME_VERSION
    };
  }

  return { runTurn, stats: () => Object.freeze({ ...stats }), catalog: skillCatalog };
}
