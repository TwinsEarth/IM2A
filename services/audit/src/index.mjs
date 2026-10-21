// 模块职责：审计层 barrel 导出（哈希链 AuditLog + Ed25519 签名封装 SignedAuditLog）。
// 契约来源：contracts/INTERFACES.md §16。
// 关键不变量：只做 re-export；AuditLog 仅作为底层链实现，新代码一律用 SignedAuditLog（契约 §17.9）。
export * from './audit-log.mjs';
export * from './signed-audit.mjs';
