// 模块职责：account 服务 barrel（账户 / 手机号登录 / 实名与付费准入统一导出）
// 契约来源：contracts/INTERFACES.md §36（v0.0.1 产品版新增）
// 关键不变量：仅供 re-export；证件号只以 hash + 尾号掩码存在，明文不落库。
export {
  ACCOUNT_STATE,
  ACCOUNT_CODE,
  AccountError,
  AccountService,
} from './account.mjs';
