// 模块职责：messaging 服务 barrel（envelope / seq / dedupe 统一导出）/ 契约来源：contracts/INTERFACES.md §10 / 关键不变量：仅供 re-export，不引入任何额外状态。
export { BIZ_TYPE, EnvelopeError, makeEnvelope, validateEnvelope } from './envelope.mjs';
export { fnv1a32, SeqAllocator } from './seq.mjs';
export { DedupeStore } from './dedupe.mjs';
