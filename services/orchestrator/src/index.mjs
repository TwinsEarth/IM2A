// 模块职责：Agent 编排层 barrel 汇总导出（demo-server/flow.mjs 与前端控制台逐字 import 这些命名导出）。
// 契约来源：contracts/INTERFACES.md §1–§7（v1）、§17–§19（v3：人格记忆 / 讨论模式 / 工具审批）。
// 关键不变量：本文件不含任何业务逻辑，只做 re-export；契约签名与各模块实现一一对应，不在此层改写任何语义。
//
// 注意：GATE_CODE 中的 MONTHLY_CAP 为「预留码」——生产按月预算（Phase2 启用），
// §3 的 7 步判定顺序中没有对应步骤，CapabilityGate 当前不会产生该码，调用方不要把它当可达分支处理。
export * from './trust-zone.mjs';
export * from './agent-registry.mjs';
export * from './capability-gate.mjs';
export * from './approval-engine.mjs';
export * from './negotiation-engine.mjs';
export * from './memory-store.mjs';
export * from './egress-guard.mjs';
// v3：人格与记忆生命周期（§17）
export * from './persona-memory.mjs';
// v3：多 Agent 结构化讨论（§18，discuss/audit/review）
export * from './discussion-engine.mjs';
// v3：细粒度工具审批（§19，Agent × Skill 策略）
export * from './tool-policy.mjs';
// v4：宿主能力（§23 旁观与主动沉默、§24 敏感场景模型路由）
export * from './presence-policy.mjs';
export * from './model-router.mjs';
// v5：可信传达（§27 第三人称转达、§28 确认层不可被 planner 预批准）
export * from './relay-summary.mjs';
export * from './confirmation-tier.mjs';
