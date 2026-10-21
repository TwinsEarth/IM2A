// 模块职责：发布候选版本契约 —— 版本三元组 / 能力声明 / 构建信息
// 契约来源：contracts/INTERFACES.md §51（v0.1.0）
// 关键不变量：CAPABILITIES 是"声明"，不是"证明"；每项能力在文档里必须能对应到 Gate/测试。
export const VERSION = '0.1.2';
export const CONTRACT_REVISION = 'R23';
export const SCHEMA_VERSION = 2;

/** 对外可验证能力清单（与契约章节对应；document 里必须写清各自由哪个 Gate 验证） */
export const CAPABILITIES = Object.freeze([
  'account-otp',          // §36 手机 OTP 登录
  'realname-hash',        // §36 实名只存 hash + 掩码
  'wallet-ops',           // §37 充值/扣费/退款幂等
  'sms-channel',          // §38 短信通道（mock/dev；真实供应商未接）
  'dispute',              // §39 已回复争议人工退款
  'moderation',           // §40 审核链（机审/人审/申诉，fail-closed）
  'metrics-trace',        // §41–§42 漏斗/告警/全链追溯
  'resilience-backup',    // §43–§44 熔断降级/备份恢复
  'acceptance',           // §45–§46 预检/验收执行器
  'web-client',           // §47 Web/PWA 三态客户端
  'multi-node-bus',       // §48 多节点广播（memory/tcp）
  'payment-channel',      // §49 支付渠道（mock 验签；真实渠道未接）
  'reconcile'             // §50 日切对账
]);

export function buildInfo(env = process.env) {
  return Object.freeze({
    version: VERSION,
    contractRevision: CONTRACT_REVISION,
    schemaVersion: SCHEMA_VERSION,
    capabilities: CAPABILITIES.length,
    node: process.versions.node,
    build: env.IM2AGENT_BUILD ?? 'local'
  });
}
