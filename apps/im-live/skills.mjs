/**
 * 模块职责：Agent 的 Skill 执行层 —— 把 W2M relay 的真实能力包装成「受权限门控的 Skill」
 * 契约来源：services/orchestrator/src/capability-gate.mjs（门控语义）、apps/im-live/README.md
 *
 * 为什么单独一层：
 *   · 模型只负责「选择 Skill + 填参数」，不直接拥有网络/执行能力；
 *   · 每次 Skill 调用都经过：Agent 白名单 → 信任档位 → 参数白名单 → 执行 → 审计；
 *   · W2M 工具（devices / run / wait / report）在这里被收敛成只读或受限可执行 Skill，
 *     即使模型被提示注入，也只能在声明范围内动作（这是「把 w2m 工具作为 Agent 的 Skill 执行层」的落地）。
 */
import { TRUST_LEVELS } from '../../services/orchestrator/src/trust-zone.mjs';

/** Skill 目录：声明式权限清单（kind / 是否需要 operator token / 允许的 argv / 最低信任档位）。 */
export const SKILLS = Object.freeze({
  'fleet.devices': Object.freeze({
    kind: 'read',
    title: '查看 W2M fleet 在线机器',
    requiresOperator: false,
    minTrustLevel: TRUST_LEVELS.SELF,
    description: '读取 relay /healthz 与在线机器列表；只读，不改变任何状态。'
  }),
  'fleet.run': Object.freeze({
    kind: 'execute',
    title: '在指定机器上执行一次只读命令',
    requiresOperator: true,
    minTrustLevel: TRUST_LEVELS.FAMILY,
    allowedArgv: Object.freeze(['node --version']), // argv 以「空格连接后的命令」比对，避免多 token 白名单被别名绕过
    description: '通过 relay POST /v1/task 在真实机器上执行一次命令；argv 白名单限定为只读演示命令。'
  }),
  'calendar.propose_slot': Object.freeze({
    kind: 'read',
    title: '建议一个家庭/工作会议时段',
    requiresOperator: false,
    minTrustLevel: TRUST_LEVELS.FAMILY,
    description: '本地日程推理（演示实现），返回候选时段与冲突说明。'
  }),
  'mail.read': Object.freeze({
    kind: 'read',
    title: '读取未读邮件摘要',
    requiresOperator: false,
    minTrustLevel: TRUST_LEVELS.FAMILY,
    description: '演示实现：返回收件箱摘要，不发送任何邮件。'
  }),
  'procurement.compare': Object.freeze({
    kind: 'read',
    title: '采购比价（三家）',
    requiresOperator: false,
    minTrustLevel: TRUST_LEVELS.KNOWN,
    description: '演示实现：给出比价结果与建议，下单仍需审批。'
  })
});

export const SKILL_IDS = Object.freeze(Object.keys(SKILLS));

/** 允许 Agent 在参数里指定的命令：只接受白名单 argv，且必须是「read 语义」。 */
export function isArgvAllowed(skillId, argv) {
  const skill = SKILLS[skillId];
  if (!skill || skill.kind !== 'execute') return false;
  if (!Array.isArray(argv) || argv.length === 0) return false;
  const allowed = skill.allowedArgv ?? [];
  return allowed.some((entry) => entry === argv.join(' '));
}

/**
 * 创建 Skill 执行器。
 * @param {object} options
 * @param {string} [options.relayUrl]
 * @param {string|null} [options.operatorToken]
 * @param {string|null} [options.deviceToken]
 * @param {(machineId: string, argv: string[]) => Promise<object>} [options.dispatchProbe] 注入的 W2M 派发器
 * @param {() => Date} [options.clock]
 */
export function createSkillExecutor({ relayUrl = 'http://127.0.0.1:8787', operatorToken = null, deviceToken = null, dispatchProbe = null, clock = () => new Date(), fetchImpl = globalThis.fetch } = {}) {
  const stats = { calls: 0, bySkill: {}, failures: 0, lastAt: null, lastError: null };

  async function relayGet(pathname, token) {
    const response = await fetchImpl(relayUrl.replace(/\/+$/, '') + pathname, {
      headers: token ? { authorization: `Bearer ${token}` } : {},
      signal: AbortSignal.timeout(4000)
    });
    return { status: response.status, body: await response.json().catch(() => ({})) };
  }

  function record(skillId, ok, detail) {
    stats.calls += 1;
    stats.bySkill[skillId] = (stats.bySkill[skillId] ?? 0) + 1;
    if (!ok) stats.failures += 1;
    stats.lastAt = clock().toISOString();
    if (!ok) stats.lastError = detail ?? null;
  }

  async function execute(skillId, args = {}) {
    const skill = SKILLS[skillId];
    if (!skill) return { ok: false, reason: 'UNKNOWN_SKILL' };
    try {
      if (skillId === 'fleet.devices') {
        const simulated = operatorToken === null;
        if (simulated) {
          record(skillId, true);
          return { ok: true, skillId, source: 'simulated', data: { relayUrl, devices: [], note: '未配置 W2M_OPERATOR_TOKEN：fleet.devices 返回模拟空列（不假装在线机器）' } };
        }
        const health = await relayGet('/healthz', null);
        const members = deviceToken !== null ? await relayGet('/v1/fleet/members', deviceToken) : { body: {} };
        const devices = (Array.isArray(members.body?.members) ? members.body.members : []).map((m) => ({ machine_id: m.machine_id ?? null, name: m.name ?? null, online: m.online === true }));
        record(skillId, health.status === 200);
        return {
          ok: true,
          skillId,
          source: 'relay',
          data: { relayUrl, relayId: health.body?.relay_id ?? null, devices, online: devices.filter((d) => d.online).length, note: '在线列表来自 relay /v1/fleet/members（未推进心跳的机器仍可能 refuse 命令）' }
        };
      }
      if (skillId === 'fleet.run') {
        const machineId = typeof args.machineId === 'string' ? args.machineId : '';
        const argv = Array.isArray(args.argv) ? args.argv.map(String) : ['node', '--version'];
        if (machineId === '') { record(skillId, false, 'MACHINE_REQUIRED'); return { ok: false, reason: 'MACHINE_REQUIRED' }; }
        if (!isArgvAllowed(skillId, argv)) { record(skillId, false, 'ARGV_NOT_ALLOWED'); return { ok: false, reason: 'ARGV_NOT_ALLOWED', allowed: skill.allowedArgv }; }
        if (operatorToken === null) { record(skillId, false, 'FLEET_NOT_CONFIGURED'); return { ok: false, reason: 'FLEET_NOT_CONFIGURED' }; }
        const dispatch = dispatchProbe ?? (async () => ({ ok: false, reason: 'DISPATCHER_MISSING' }));
        const result = await dispatch(machineId, argv);
        record(skillId, result?.ok === true, result?.reason ?? null);
        return { ok: result?.ok === true, skillId, source: 'relay', machineId, argv, result };
      }
      if (skillId === 'calendar.propose_slot') {
        record(skillId, true);
        return { ok: true, skillId, source: 'local', data: { candidates: [{ day: '周六', time: '12:00', conflict: false, note: '家庭聚餐（妈妈提议）' }, { day: '周日', time: '10:30', conflict: true, note: '与出差航班冲突' }], note: '演示日程：真实接入需接日历 Skill Provider' } };
      }
      if (skillId === 'mail.read') {
        record(skillId, true);
        return { ok: true, skillId, source: 'local', data: { unread: 3, highlights: ['投资人来信：约下周沟通（陌生人询盘已走预审）', '物业：水费账单已生成', '学校：家长会通知'], note: '演示实现：只读摘要，不发送邮件' } };
      }
      if (skillId === 'procurement.compare') {
        record(skillId, true);
        return { ok: true, skillId, source: 'local', data: { item: args.item ?? '人体工学椅', quotes: [{ vendor: 'A', cents: 239900, note: '次日达含安装' }, { vendor: 'B', cents: 219900, note: '3 日达不含安装' }, { vendor: 'C', cents: 268000, note: '含 5 年保修' }], recommendation: 'B 最便宜，A 含安装；超过 ¥500 必须本人审批', note: '演示实现：真实接入需供应商 API' } };
      }
      return { ok: false, reason: 'UNIMPLEMENTED_SKILL' };
    } catch (error) {
      record(skillId, false, String(error?.message ?? error));
      return { ok: false, reason: 'SKILL_EXECUTION_FAILED', detail: String(error?.message ?? error) };
    }
  }

  return { execute, stats: () => Object.freeze({ ...stats, bySkill: { ...stats.bySkill } }), relayUrl, configured: operatorToken !== null };
}

/**
 * Skill 调用前的权限判定（与 capability-gate 同一哲学：代码层强制，不依赖提示词）。
 * @returns {{allowed: boolean, reason?: string, detail?: object}}
 */
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
