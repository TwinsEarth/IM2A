// 模块职责：moderation 服务 barrel（付费消息审核链：机审 → 人审 → 申诉）
// 契约来源：contracts/INTERFACES.md §40（v0.0.3 新增）
// 关键不变量：机审不可用 fail-closed；只有 APPROVED/APPEAL_GRANTED 允许投递与扣费。
export {
  MODERATION_STATE,
  VERDICT,
  MODERATION_CODE,
  ModerationError,
  ModerationService,
} from './moderation.mjs';
