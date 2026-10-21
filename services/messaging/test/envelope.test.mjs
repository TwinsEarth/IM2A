// 测试文件：业务消息封装 envelope §10（bizType 白名单 / 必填字段 / validateEnvelope）/ 运行：node services/messaging/test/envelope.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { BIZ_TYPE, EnvelopeError, makeEnvelope, validateEnvelope } from '../src/envelope.mjs';
import * as barrel from '../src/index.mjs';

test('makeEnvelope 正常路径：七个业务字段齐备且浅冻结，payload 为拷贝', () => {
  const payload = { text: '你好' };
  const agentContext = { agentId: 'agent-a', owner: 'user-a' };
  const envelope = makeEnvelope({
    bizType: BIZ_TYPE.NORMAL_TEXT,
    bizId: 'msg-1',
    payload,
    traceId: 'trace-1',
    trustZone: 3,
    agentContext,
    bizSeq: 7,
  });
  assert.equal(envelope.bizType, 'NORMAL_TEXT');
  assert.equal(envelope.bizId, 'msg-1');
  assert.deepEqual(envelope.payload, { text: '你好' });
  assert.equal(envelope.traceId, 'trace-1');
  assert.equal(envelope.trustZone, 3);
  assert.deepEqual(envelope.agentContext, { agentId: 'agent-a', owner: 'user-a' });
  assert.equal(envelope.bizSeq, 7);
  // 顶层必须带 bizId / bizSeq（控制台与去重直接读顶层）
  assert.ok(Object.hasOwn(envelope, 'bizId'));
  assert.ok(Object.hasOwn(envelope, 'bizSeq'));
  assert.ok(Object.isFrozen(envelope));
  assert.ok(Object.isFrozen(envelope.payload));
  assert.ok(Object.isFrozen(envelope.agentContext));
  // 浅拷贝：改原 payload 不影响 envelope
  payload.text = 'changed';
  assert.equal(envelope.payload.text, '你好');
});

test('makeEnvelope 默认值：payload={}、traceId/trustZone/agentContext/bizSeq 为 null', () => {
  const envelope = makeEnvelope({ bizType: BIZ_TYPE.SYSTEM_NOTICE, bizId: 'sys-1' });
  assert.deepEqual(envelope.payload, {});
  assert.equal(envelope.traceId, null);
  assert.equal(envelope.trustZone, null);
  assert.equal(envelope.agentContext, null);
  assert.equal(envelope.bizSeq, null);
  assert.ok(Object.isFrozen(envelope.payload));
});

test('BIZ_TYPE 白名单内的五种类型都能构造', () => {
  for (const bizType of Object.values(BIZ_TYPE)) {
    const envelope = makeEnvelope({ bizType, bizId: `biz-${bizType}` });
    assert.equal(envelope.bizType, bizType);
    assert.equal(validateEnvelope(envelope).ok, true);
  }
});

test('非法 bizType → BAD_BIZ_TYPE', () => {
  assert.throws(
    () => makeEnvelope({ bizType: 'UNKNOWN_TYPE', bizId: 'biz-1' }),
    (error) => error instanceof EnvelopeError && error.code === 'BAD_BIZ_TYPE'
  );
});

test('缺字段 → MISSING_FIELD（bizType / bizId / 空 bizId）', () => {
  for (const args of [{ bizId: 'biz-1' }, { bizType: BIZ_TYPE.NORMAL_TEXT }, { bizType: BIZ_TYPE.NORMAL_TEXT, bizId: '' }, { bizType: BIZ_TYPE.NORMAL_TEXT, bizId: '   ' }]) {
    assert.throws(
      () => makeEnvelope(args),
      (error) => error instanceof EnvelopeError && error.code === 'MISSING_FIELD',
      `应拒绝 ${JSON.stringify(args)}`
    );
  }
});

test('可选字段类型非法 → TypeError（契约 §10 只定义两个 code，类型错误不扩张错误码）', () => {
  assert.throws(() => makeEnvelope({ bizType: BIZ_TYPE.NORMAL_TEXT, bizId: 'biz-1', trustZone: 9 }), TypeError);
  assert.throws(() => makeEnvelope({ bizType: BIZ_TYPE.NORMAL_TEXT, bizId: 'biz-1', trustZone: 1.5 }), TypeError);
  assert.throws(() => makeEnvelope({ bizType: BIZ_TYPE.NORMAL_TEXT, bizId: 'biz-1', bizSeq: 0 }), TypeError);
  assert.throws(() => makeEnvelope({ bizType: BIZ_TYPE.NORMAL_TEXT, bizId: 'biz-1', traceId: 123 }), TypeError);
  assert.throws(() => makeEnvelope({ bizType: BIZ_TYPE.NORMAL_TEXT, bizId: 'biz-1', payload: [] }), TypeError);
  assert.throws(() => makeEnvelope({ bizType: BIZ_TYPE.NORMAL_TEXT, bizId: 'biz-1', agentContext: 'agent' }), TypeError);
});

test('validateEnvelope：合法 envelope / 手写对象 → { ok:true, errors:[] } 且冻结', () => {
  const valid = makeEnvelope({ bizType: BIZ_TYPE.AGENT_DIGEST, bizId: 'digest-1', payload: { lines: 3 } });
  const result = validateEnvelope(valid);
  assert.deepEqual({ ok: result.ok, errors: [...result.errors] }, { ok: true, errors: [] });
  assert.ok(Object.isFrozen(result));

  const handwritten = { bizType: BIZ_TYPE.NORMAL_TEXT, bizId: 'msg-9', payload: {} };
  assert.equal(validateEnvelope(handwritten).ok, true);
});

test('validateEnvelope：非法 bizType / 缺字段 / 字段类型错误 → ok:false 且 errors 可定位', () => {
  const valid = makeEnvelope({ bizType: BIZ_TYPE.NORMAL_TEXT, bizId: 'msg-1' });

  const badBizType = validateEnvelope({ ...valid, bizType: 'NOPE' });
  assert.equal(badBizType.ok, false);
  assert.ok(badBizType.errors.some((message) => message.startsWith('BAD_BIZ_TYPE')));

  const missingId = validateEnvelope({ ...valid, bizId: '' });
  assert.equal(missingId.ok, false);
  assert.ok(missingId.errors.some((message) => message.startsWith('MISSING_FIELD: bizId')));

  const missingPayload = validateEnvelope({ bizType: BIZ_TYPE.NORMAL_TEXT, bizId: 'msg-1' });
  assert.equal(missingPayload.ok, false);
  assert.ok(missingPayload.errors.some((message) => message.includes('payload')));

  const badTrustZone = validateEnvelope({ ...valid, trustZone: 9 });
  assert.equal(badTrustZone.ok, false);
  assert.ok(badTrustZone.errors.some((message) => message.startsWith('INVALID_FIELD: trustZone')));

  const badBizSeq = validateEnvelope({ ...valid, bizSeq: -1 });
  assert.equal(badBizSeq.ok, false);
  assert.ok(badBizSeq.errors.some((message) => message.startsWith('INVALID_FIELD: bizSeq')));

  // 非对象也必须是 ok:false（不抛异常）
  for (const value of [null, undefined, 'envelope', 42, []]) {
    const result = validateEnvelope(value);
    assert.equal(result.ok, false);
    assert.ok(result.errors.length > 0);
  }
});

test('barrel index.mjs 导出 messaging 公共 API（demo-server 逐字 import 的名字）', () => {
  for (const name of ['BIZ_TYPE', 'makeEnvelope', 'validateEnvelope', 'EnvelopeError', 'fnv1a32', 'SeqAllocator', 'DedupeStore']) {
    assert.ok(Object.hasOwn(barrel, name), `index.mjs 缺少导出 ${name}`);
    assert.notEqual(barrel[name], undefined);
  }
});
