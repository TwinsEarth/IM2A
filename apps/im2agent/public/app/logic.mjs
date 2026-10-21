// 模块职责：Web 客户端的纯函数层 —— 无 DOM、无网络，可被浏览器与 Node 测试共同导入
// 契约来源：contracts/INTERFACES.md §47（logic.mjs 冻结导出）
// 关键不变量：纯函数；非法输入不抛（UI 不该因为脏数据白屏）。

/** 金额分 → 展示串；非法输入返回 '—' */
export function formatCents(cents) {
  const value = Number(cents);
  if (!Number.isFinite(value)) return '—';
  return `¥${(value / 100).toFixed(2)}`;
}

/** 审批单归纳：待审 / 高优待审 / 已完成 */
export function summarizeTickets(state) {
  const tickets = Array.isArray(state?.tickets) ? state.tickets : [];
  const pending = tickets.filter((ticket) => ticket.state === 'PENDING');
  return {
    pending: pending.length,
    pendingHigh: pending.filter((ticket) => ticket.priority === 'high').length,
    done: tickets.filter((ticket) => ticket.state === 'COMPLETED').length
  };
}

/** 角色决定能否在 UI 上渲染写操作（服务端仍二次校验） */
export function canDecide(role) {
  return role === 'operator';
}

/** 巡视态：人审队列 */
export function moderationQueue(state) {
  const cases = Array.isArray(state?.moderation) ? state.moderation : [];
  return cases.filter((item) => item.state === 'HUMAN_PENDING');
}

/** 漏斗摘要：缺数据返回空数组，不抛 */
export function funnelSummary(metrics) {
  const stages = metrics?.funnel?.stages;
  if (!Array.isArray(stages)) return [];
  return stages.map((stage) => ({
    stage: String(stage.stage ?? ''),
    count: Number(stage.count ?? 0),
    rate: Number(stage.conversionFromPrevious ?? 0)
  }));
}
