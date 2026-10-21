// 模块职责：人类 IM 通道业务消息封装（bizType/bizId/payload/traceId/trustZone/agentContext/bizSeq）+ 结构校验 / 契约来源：contracts/INTERFACES.md §10 envelope.mjs / 关键不变量：合法 bizType 白名单、必填字段非空、返回浅冻结对象、校验器不抛异常只返回 errors。

export const BIZ_TYPE = Object.freeze({
  APPROVAL_CARD: 'APPROVAL_CARD',
  STRANGER_INTENT: 'STRANGER_INTENT',
  AGENT_DIGEST: 'AGENT_DIGEST',
  NORMAL_TEXT: 'NORMAL_TEXT',
  SYSTEM_NOTICE: 'SYSTEM_NOTICE',
});

const BIZ_TYPES = new Set(Object.values(BIZ_TYPE));
const TRUST_LEVEL_MIN = 0;
const TRUST_LEVEL_MAX = 4;

export class EnvelopeError extends Error {
  constructor(message, code = 'ENVELOPE_ERROR') {
    super(message);
    this.name = 'EnvelopeError';
    this.code = code;
  }
}

function isPlainObject(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isTrustZone(value) {
  return Number.isInteger(value) && value >= TRUST_LEVEL_MIN && value <= TRUST_LEVEL_MAX;
}

function isBizSeq(value) {
  return Number.isInteger(value) && value > 0;
}

/**
 * 构造业务消息封装。必填：bizType（白名单）、bizId（非空字符串）。
 * payload 会被浅拷贝后冻结，避免调用方后续修改污染消息对象。
 */
export function makeEnvelope({
  bizType,
  bizId,
  payload = {},
  traceId = null,
  trustZone = null,
  agentContext = null,
  bizSeq = null,
} = {}) {
  if (bizType === undefined || bizType === null) {
    throw new EnvelopeError('缺少必填字段 bizType', 'MISSING_FIELD');
  }
  if (!BIZ_TYPES.has(bizType)) {
    throw new EnvelopeError(
      `非法 bizType: ${String(bizType)}（允许值 ${Object.values(BIZ_TYPE).join(' / ')}）`,
      'BAD_BIZ_TYPE'
    );
  }
  if (typeof bizId !== 'string' || bizId.trim().length === 0) {
    throw new EnvelopeError('缺少必填字段 bizId（必须是非空字符串）', 'MISSING_FIELD');
  }
  // 可选字段的类型错误属于调用方编程错误（契约 §10 只定义 BAD_BIZ_TYPE / MISSING_FIELD 两个 code）
  if (payload === null || !isPlainObject(payload)) throw new TypeError('payload 必须是普通对象');
  if (traceId !== null && (typeof traceId !== 'string' || traceId.length === 0)) {
    throw new TypeError('traceId 必须是非空字符串或 null');
  }
  if (trustZone !== null && !isTrustZone(trustZone)) {
    throw new TypeError('trustZone 必须是 0..4 的整数或 null');
  }
  if (agentContext !== null && !isPlainObject(agentContext)) {
    throw new TypeError('agentContext 必须是普通对象或 null');
  }
  if (bizSeq !== null && !isBizSeq(bizSeq)) {
    throw new TypeError('bizSeq 必须是正整数或 null');
  }

  return Object.freeze({
    bizType,
    bizId,
    payload: Object.freeze({ ...payload }),
    traceId,
    trustZone,
    agentContext: agentContext === null ? null : Object.freeze({ ...agentContext }),
    bizSeq,
  });
}

/**
 * 校验任意对象是否符合 envelope 结构（不抛异常）。
 * 返回 { ok, errors }；errors 为字符串数组（前缀即错误类别，便于上层断言/打点）。
 */
export function validateEnvelope(envelope) {
  const errors = [];
  if (!isPlainObject(envelope)) {
    return Object.freeze({ ok: false, errors: Object.freeze(['MISSING_FIELD: envelope 必须是普通对象']) });
  }
  if (envelope.bizType === undefined || envelope.bizType === null) {
    errors.push('MISSING_FIELD: bizType');
  } else if (!BIZ_TYPES.has(envelope.bizType)) {
    errors.push(`BAD_BIZ_TYPE: ${String(envelope.bizType)}`);
  }
  if (typeof envelope.bizId !== 'string' || envelope.bizId.trim().length === 0) {
    errors.push('MISSING_FIELD: bizId');
  }
  if (!isPlainObject(envelope.payload)) {
    errors.push('MISSING_FIELD: payload 必须是普通对象');
  }
  const { traceId = null, trustZone = null, agentContext = null, bizSeq = null } = envelope;
  if (traceId !== null && (typeof traceId !== 'string' || traceId.length === 0)) {
    errors.push('INVALID_FIELD: traceId 必须是非空字符串或 null');
  }
  if (trustZone !== null && !isTrustZone(trustZone)) {
    errors.push('INVALID_FIELD: trustZone 必须是 0..4 的整数或 null');
  }
  if (agentContext !== null && !isPlainObject(agentContext)) {
    errors.push('INVALID_FIELD: agentContext 必须是普通对象或 null');
  }
  if (bizSeq !== null && !isBizSeq(bizSeq)) {
    errors.push('INVALID_FIELD: bizSeq 必须是正整数或 null');
  }
  return Object.freeze({ ok: errors.length === 0, errors: Object.freeze(errors) });
}
