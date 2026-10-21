/**
 * 模块职责：Agent 的 Skill 执行层 —— 声明式权限清单 + 本地演示 Skill（无外部依赖、无网络）
 * 契约来源：services/orchestrator/src/capability-gate.mjs（门控语义）
 *
 * 为什么单独一层：模型只负责「选择 Skill + 填参数」，不拥有执行能力；
 * 每次调用都经过：Agent 白名单 → 信任档位 → 参数校验 → 执行 → 审计。
 * 生产替换点：把 execute() 换成 MCP / 外部工具插件适配器即可，治理语义不变。
 */
import { TRUST_LEVELS } from '../../services/orchestrator/src/trust-zone.mjs';

export const SKILLS = Object.freeze({
  'calendar.propose_slot': Object.freeze({
    kind: 'read',
    title: '建议一个家庭/工作会议时段',
    minTrustLevel: TRUST_LEVELS.FAMILY,
    description: '本地日程推理（演示实现），返回候选时段与冲突说明。'
  }),
  'mail.read': Object.freeze({
    kind: 'read',
    title: '读取未读邮件摘要',
    minTrustLevel: TRUST_LEVELS.FAMILY,
    description: '演示实现：返回收件箱摘要，不发送任何邮件。'
  }),
  'procurement.compare': Object.freeze({
    kind: 'read',
    title: '采购比价（三家）',
    minTrustLevel: TRUST_LEVELS.KNOWN,
    description: '演示实现：给出比价结果与建议，下单仍需审批。'
  })
});

export const SKILL_IDS = Object.freeze(Object.keys(SKILLS));

/** 创建 Skill 执行器；纯本地、零网络，便于审计与替换。 */
export function createSkillExecutor({ clock = () => new Date() } = {}) {
  const stats = { calls: 0, bySkill: {}, failures: 0, lastAt: null, lastError: null };

  function record(skillId, ok, detail = null) {
    stats.calls += 1;
    stats.bySkill[skillId] = (stats.bySkill[skillId] ?? 0) + 1;
    if (!ok) stats.failures += 1;
    stats.lastAt = clock().toISOString();
    if (!ok) stats.lastError = detail;
  }

  async function execute(skillId, args = {}) {
    try {
      if (skillId === 'calendar.propose_slot') {
        record(skillId, true);
        return { ok: true, skillId, source: 'local', data: { candidates: [{ day: '周六', time: '12:00', conflict: false, note: '家庭聚餐（妈妈提议）' }, { day: '周日', time: '10:30', conflict: true, note: '与出差航班冲突' }], note: '演示日程：生产接日历 Provider' } };
      }
      if (skillId === 'mail.read') {
        record(skillId, true);
        return { ok: true, skillId, source: 'local', data: { unread: 3, highlights: ['投资人来信：约下周沟通（陌生人询盘已走预审）', '物业：水费账单已生成', '学校：家长会通知'], note: '演示实现：只读摘要，不发送邮件' } };
      }
      if (skillId === 'procurement.compare') {
        record(skillId, true);
        return { ok: true, skillId, source: 'local', data: { item: args.item ?? '人体工学椅', quotes: [{ vendor: 'A', cents: 239900, note: '次日达含安装' }, { vendor: 'B', cents: 219900, note: '3 日达不含安装' }, { vendor: 'C', cents: 268000, note: '含 5 年保修' }], recommendation: 'B 最便宜，A 含安装；超过阈值必须本人审批', note: '演示实现：生产接供应商 API' } };
      }
      record(skillId, false, 'UNKNOWN_SKILL');
      return { ok: false, reason: 'UNKNOWN_SKILL' };
    } catch (error) {
      record(skillId, false, String(error?.message ?? error));
      return { ok: false, reason: 'SKILL_EXECUTION_FAILED', detail: String(error?.message ?? error) };
    }
  }

  return { execute, stats: () => Object.freeze({ ...stats, bySkill: { ...stats.bySkill } }) };
}

/** Skill 调用前的权限判定（代码层强制，不依赖提示词）。 */
export function checkSkillPermission({ skillId, agent, trustLevel }) {
  const skill = SKILLS[skillId];
  if (!skill) return { allowed: false, reason: 'UNKNOWN_SKILL', detail: { skillId } };
  if (typeof trustLevel !== 'number' || trustLevel > skill.minTrustLevel) {
    return { allowed: false, reason: 'TRUST_TOO_LOW', detail: { skillId, trustLevel, minTrustLevel: skill.minTrustLevel } };
  }
  const declared = Array.isArray(agent?.allowedSkills) ? agent.allowedSkills : [];
  if (!declared.includes(skillId)) return { allowed: false, reason: 'SKILL_NOT_ALLOWED', detail: { skillId, declared } };
  return { allowed: true };
}