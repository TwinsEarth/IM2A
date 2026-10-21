// 模块职责：messaging 服务 barrel（§10 envelope/seq/dedupe + v2 §14 频道模型 + §15 Agent 会话层）/ 契约来源：contracts/INTERFACES.md §10、§14、§15 / 关键不变量：仅供 re-export，不引入任何额外状态；§10 既有导出保持不变。
export { BIZ_TYPE, EnvelopeError, makeEnvelope, validateEnvelope } from './envelope.mjs';
export { fnv1a32, SeqAllocator } from './seq.mjs';
export { DedupeStore } from './dedupe.mjs';

// v2 §14 频道模型
export {
  CHANNEL_TYPE,
  MEMBER_KIND,
  MESSAGE_KIND,
  CHANNEL_CODE,
  ChannelError,
  ChannelRegistry,
} from './channel.mjs';

// v2 §15 Agent 会话层
export {
  SESSION_STATE,
  SESSION_CODE,
  SessionError,
  AgentSessionRegistry,
} from './session.mjs';

// v4 §25 轮次内投递（mid-turn delivery）
export {
  TURN_STATE,
  INBOX_CODE,
  TurnInboxError,
  TurnInbox,
} from './turn-inbox.mjs';

// v6 §29 Agent 持久邮箱（跨运行/跨机器的持久真相层）
export {
  MAILBOX_CODE,
  AgentMailboxError,
  AgentMailboxStore,
} from './agent-mailbox.mjs';

// v7 §31 IM 渠道适配层（统一 MessageEvent + 能力声明；底座可替换）
export {
  ADAPTER_CODE,
  ADAPTER_CAPABILITY,
  IM_PLATFORM,
  ImAdapterError,
  normalizeEvent,
  ImAdapterRegistry,
  createTencentImPaasAdapter,
  createSelfHostedKernelAdapter,
} from './im-adapter.mjs';

// v8（v0.0.8）§48 多节点广播总线（memory / tcp 同构；生产替换 Redis Pub/Sub）
export {
  BUS_KIND,
  BUS_CODE,
  BroadcastError,
  createBroadcastBus,
} from './broadcast.mjs';
