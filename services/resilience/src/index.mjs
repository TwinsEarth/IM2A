// 模块职责：resilience 服务 barrel（外部依赖熔断与降级）
// 契约来源：contracts/INTERFACES.md §43（v0.0.5 新增）
// 关键不变量：OPEN 时快速失败不调用主方；降级必须标记 usedFallback 与 causeCode。
export {
  CIRCUIT_STATE,
  GUARD_CODE,
  GuardError,
  CircuitBreaker,
  ProviderGuard,
} from './provider-guard.mjs';
