// 模块职责：payment 服务 barrel（账本 + 付费消息状态机统一导出）/ 契约来源：contracts/INTERFACES.md §8–§9 / 关键不变量：仅供 re-export，不引入任何额外状态。
export {
  ENTRY_TYPE,
  LedgerError,
  Ledger,
  assertPositiveCents,
} from './ledger.mjs';

export {
  MSG_STATE,
  BILLING,
  PaidMessageError,
  PaidMessageService,
} from './paid-message.mjs';
