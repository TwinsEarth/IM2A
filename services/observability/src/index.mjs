// 模块职责：observability 服务 barrel（运营指标/告警 + 全链审计追溯）
// 契约来源：contracts/INTERFACES.md §41–§42（v0.0.4 新增）
// 关键不变量：告警是纯函数；追溯只读，缺环必须显式列出。
export {
  METRIC,
  ALERT_SEVERITY,
  DEFAULT_ALERT_RULES,
  Metrics,
  evaluateAlerts,
} from './metrics.mjs';

export { AuditTrace } from './trace.mjs';
