// 模块职责：网关层 barrel 导出（AgentGateway 数据平面 + VisibilityGate 读侧可见域 + v4 TrustedContextProvider 可信上下文 + v6 DelegationService 带衰减委托）。
// 契约来源：contracts/INTERFACES.md §12、§13、§22、§30。
// 关键不变量：只做 re-export，不在此层改写任何语义。
export * from './agent-gateway.mjs';
export * from './visibility.mjs';
export * from './trusted-context.mjs';
export * from './delegation.mjs';
