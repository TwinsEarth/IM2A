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

// v0.0.1 §37 钱包：余额/充值/扣费/退款（金额真相仍在 §8 账本，钱包是投影 + 策略）
export {
  WALLET_CODE,
  WalletError,
  Wallet,
} from './wallet.mjs';

// v0.0.2 §39 退款争议与时间线（已回复争议走人工；批准后只能经账本幂等退款）
export {
  DISPUTE_STATE,
  DISPUTE_CODE,
  DisputeError,
  DisputeService,
} from './dispute.mjs';

// v0.0.9 §49 支付渠道抽象（回调验签 + 幂等入账；接真实渠道只换 provider）
export {
  CHANNEL_KIND,
  PAYMENT_STATE,
  PAYMENT_CODE,
  PaymentChannelError,
  PaymentIntentService,
} from './channel-adapter.mjs';

// v0.0.9 §50 日切对账（逐笔核对；只读）
export {
  RECONCILE_CODE,
  ReconcileError,
  Reconciler,
} from './reconcile.mjs';
