// 模块职责：身份层 barrel 导出（Ed25519 身份 / AgentCard 签发验签 / payload 签名）。
// 契约来源：contracts/INTERFACES.md §11。
// 关键不变量：只做 re-export，不在此层改写任何语义；验签函数的「只返回判定、不抛异常」契约原样透出。
export * from './agent-id.mjs';
