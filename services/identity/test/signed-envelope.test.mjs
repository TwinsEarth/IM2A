// 测试文件：§26 签名信封（Ed25519 消息级签名 / 时间窗 / nonce 重放 / TOFU）/ 运行：node services/identity/test/signed-envelope.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { createAgentIdentity, signPayload } from '../src/agent-id.mjs';
import { ENVELOPE_CODE, SignedEnvelopeError, SignedEnvelopeService } from '../src/signed-envelope.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';

function makeClock(startIso = BASE_ISO) {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

function makeService(options = {}) {
  const clock = makeClock();
  const identity = createAgentIdentity({ clock });
  const service = new SignedEnvelopeService({ identity, clock, ...options });
  return { clock, identity, service };
}

function makePair(options = {}) {
  const sender = makeService();
  const receiver = makeService(options);
  receiver.service.learnKey({ agentId: sender.identity.agentId, publicKeyPem: sender.identity.publicKeyPem });
  return { sender, receiver };
}

function buildEnvelope(sender, toAgentId, overrides = {}) {
  return sender.service.sign({
    toAgentId,
    kind: 'PAID_MESSAGE',
    payload: { text: '你好', amountCents: 100 },
    meta: { channelId: 'ch-1' },
    ...overrides,
  });
}

test('正常签名/验签：信封字段完整、冻结，verify 返回 {ok,code,from,payload}', () => {
  const { sender, receiver } = makePair();
  const envelope = buildEnvelope(sender, receiver.identity.agentId);
  assert.equal(envelope.v, 1);
  assert.equal(envelope.from, sender.identity.agentId);
  assert.equal(envelope.to, receiver.identity.agentId);
  assert.match(envelope.nonce, /^[0-9a-f-]{36}$/);
  assert.equal(envelope.at, BASE_ISO);
  assert.equal(envelope.kind, 'PAID_MESSAGE');
  assert.deepEqual({ ...envelope.payload }, { text: '你好', amountCents: 100 });
  assert.deepEqual({ ...envelope.meta }, { channelId: 'ch-1' });
  assert.equal(envelope.alg, 'ed25519');
  assert.equal(envelope.publicKeyPem, sender.identity.publicKeyPem);
  assert.ok(envelope.signature.length > 0);
  assert.ok(Object.isFrozen(envelope));
  assert.ok(Object.isFrozen(envelope.payload));
  assert.ok(Object.isFrozen(envelope.meta));

  const result = receiver.service.verify({ envelope, expectedTo: receiver.identity.agentId });
  assert.equal(result.ok, true);
  assert.equal(result.code, null);
  assert.equal(result.from, sender.identity.agentId);
  assert.deepEqual({ ...result.payload }, { text: '你好', amountCents: 100 });
  assert.ok(Object.isFrozen(result));

  // 每次签名 nonce 唯一；payload 为浅拷贝，调用方事后改原对象不影响信封
  assert.notEqual(buildEnvelope(sender, receiver.identity.agentId).nonce, envelope.nonce);
  const payload = { text: '原始' };
  const copied = sender.service.sign({ toAgentId: receiver.identity.agentId, kind: 'TEXT', payload });
  payload.text = 'changed';
  assert.equal(copied.payload.text, '原始');
});

test('篡改 payload 任一字段 → SIGNATURE_INVALID（优先于 CLOCK_SKEW / REPLAY）', () => {
  const { sender, receiver } = makePair();
  const envelope = buildEnvelope(sender, receiver.identity.agentId);

  // 先让信封超出时间窗：仍然必须报 SIGNATURE_INVALID（说明验签在时间窗之前）
  receiver.clock.advance(10 * 60_000);
  const tamperedPayload = { ...envelope, payload: { ...envelope.payload, text: 'hacked', amountCents: 1 } };
  assert.equal(
    receiver.service.verify({ envelope: tamperedPayload, expectedTo: receiver.identity.agentId }).code,
    ENVELOPE_CODE.SIGNATURE_INVALID
  );
  for (const tampered of [
    { ...envelope, to: 'agent-evil' },
    { ...envelope, kind: 'TEXT' },
    { ...envelope, at: '2026-10-21T10:00:00.001Z' },
    { ...envelope, meta: { channelId: 'ch-evil' } },
    { ...envelope, from: 'agent-evil' },
    { ...envelope, publicKeyPem: 'not-a-pem' },
  ]) {
    assert.equal(receiver.service.verify({ envelope: tampered }).code, ENVELOPE_CODE.SIGNATURE_INVALID);
  }

  // 已被成功验过的 nonce 被篡改后重投：仍先报 SIGNATURE_INVALID，而不是 REPLAY_DETECTED
  const pair = makePair();
  const fresh = buildEnvelope(pair.sender, pair.receiver.identity.agentId);
  assert.equal(pair.receiver.service.verify({ envelope: fresh }).ok, true);
  assert.equal(
    pair.receiver.service.verify({ envelope: { ...fresh, payload: { text: 'changed' } } }).code,
    ENVELOPE_CODE.SIGNATURE_INVALID
  );
});

test('重放：同一条信封 verify 两次 → 第二次 REPLAY_DETECTED；expectedTo 不符 → ENVELOPE_INVALID 且不消耗 nonce', () => {
  const { sender, receiver } = makePair();
  const envelope = buildEnvelope(sender, receiver.identity.agentId);
  assert.equal(receiver.service.verify({ envelope, expectedTo: receiver.identity.agentId }).ok, true);
  const replayed = receiver.service.verify({ envelope, expectedTo: receiver.identity.agentId });
  assert.equal(replayed.ok, false);
  assert.equal(replayed.code, ENVELOPE_CODE.REPLAY_DETECTED);

  const other = buildEnvelope(sender, receiver.identity.agentId);
  const wrongTarget = receiver.service.verify({ envelope: other, expectedTo: 'agent-other' });
  assert.equal(wrongTarget.code, ENVELOPE_CODE.ENVELOPE_INVALID);
  assert.equal(
    receiver.service.verify({ envelope: other, expectedTo: receiver.identity.agentId }).ok,
    true,
    'expectedTo 失败不得烧掉 nonce'
  );
});

test('时钟窗：±windowMs 边界（差 1ms）→ CLOCK_SKEW（注入 clock 推进，不 sleep）', () => {
  const { sender, receiver } = makePair();
  const envelope = buildEnvelope(sender, receiver.identity.agentId);
  receiver.clock.advance(300_000);
  assert.equal(receiver.service.verify({ envelope, expectedTo: receiver.identity.agentId }).ok, true, '恰好 300s 边界内仍通过');
  receiver.clock.advance(1);
  assert.equal(
    receiver.service.verify({ envelope, expectedTo: receiver.identity.agentId }).code,
    ENVELOPE_CODE.CLOCK_SKEW
  );

  // 负方向：发送方时钟比收件方快 300_001ms
  const futureSender = makeService();
  const receiverService = makeService();
  futureSender.clock.advance(300_001);
  const futureEnvelope = buildEnvelope(futureSender, receiverService.identity.agentId);
  receiverService.service.learnKey({
    agentId: futureSender.identity.agentId,
    publicKeyPem: futureSender.identity.publicKeyPem,
  });
  assert.equal(receiverService.service.verify({ envelope: futureEnvelope }).code, ENVELOPE_CODE.CLOCK_SKEW);
});

test('TOFU：未知发送方 → KEY_UNKNOWN；learnKey 后可验；不同公钥 → TOFU_CONFLICT 且不覆盖', () => {
  const sender = makeService();
  const receiver = makeService();
  const envelope = buildEnvelope(sender, receiver.identity.agentId);

  assert.equal(receiver.service.knownKey(sender.identity.agentId), null);
  assert.equal(
    receiver.service.verify({ envelope, expectedTo: receiver.identity.agentId }).code,
    ENVELOPE_CODE.KEY_UNKNOWN
  );
  // KEY_UNKNOWN 失败不得消耗 nonce：学习公钥后同一信封仍可验
  const learned = receiver.service.learnKey({
    agentId: sender.identity.agentId,
    publicKeyPem: sender.identity.publicKeyPem,
  });
  assert.equal(learned.changed, true);
  assert.equal(learned.publicKeyPem, sender.identity.publicKeyPem);
  assert.ok(Object.isFrozen(learned));
  assert.equal(receiver.service.knownKey(sender.identity.agentId), sender.identity.publicKeyPem);
  assert.equal(receiver.service.verify({ envelope, expectedTo: receiver.identity.agentId }).ok, true);

  // 幂等学习
  assert.equal(
    receiver.service.learnKey({ agentId: sender.identity.agentId, publicKeyPem: sender.identity.publicKeyPem }).changed,
    false
  );
  // TOFU 冲突：同一 agentId 换公钥必须拒绝，不能静默覆盖
  const otherIdentity = createAgentIdentity({ clock: makeClock() });
  assert.throws(
    () =>
      receiver.service.learnKey({
        agentId: sender.identity.agentId,
        publicKeyPem: otherIdentity.publicKeyPem,
      }),
    (error) => error instanceof SignedEnvelopeError && error.code === ENVELOPE_CODE.TOFU_CONFLICT
  );
  assert.equal(receiver.service.knownKey(sender.identity.agentId), sender.identity.publicKeyPem);
});

test('已知密钥被冒充：信封携带攻击者公钥 / 已知钥与携带钥不一致 → SIGNATURE_INVALID', () => {
  const victim = makeService();
  const attacker = makeService();
  const receiver = makeService();
  receiver.service.learnKey({ agentId: victim.identity.agentId, publicKeyPem: victim.identity.publicKeyPem });

  const unsigned = {
    v: 1,
    from: victim.identity.agentId,
    to: receiver.identity.agentId,
    nonce: randomUUID(),
    at: BASE_ISO,
    kind: 'TEXT',
    payload: { text: '冒充' },
    meta: {},
    alg: 'ed25519',
    publicKeyPem: attacker.identity.publicKeyPem,
  };
  const { signature } = signPayload({ identity: attacker.identity, payload: unsigned });
  const forged = { ...unsigned, signature };
  assert.equal(
    receiver.service.verify({ envelope: forged, expectedTo: receiver.identity.agentId }).code,
    ENVELOPE_CODE.SIGNATURE_INVALID
  );
});

test('nonce TTL：到期后可复用（推进会注入 clock，不 sleep）', () => {
  const sender = makeService();
  const receiver = makeService({ nonceTtlMs: 1000 });
  receiver.service.learnKey({ agentId: sender.identity.agentId, publicKeyPem: sender.identity.publicKeyPem });
  const envelope = buildEnvelope(sender, receiver.identity.agentId);

  assert.equal(receiver.service.verify({ envelope }).ok, true);
  assert.equal(receiver.service.verify({ envelope }).code, ENVELOPE_CODE.REPLAY_DETECTED);
  receiver.clock.advance(999);
  assert.equal(receiver.service.verify({ envelope }).code, ENVELOPE_CODE.REPLAY_DETECTED, 'TTL 内仍判重放');
  receiver.clock.advance(1);
  assert.equal(receiver.service.verify({ envelope }).ok, true, 'nonce TTL 到期后可复用');
});

test('verify 对任意垃圾输入只返回判定、绝不抛异常', () => {
  const { sender, receiver } = makePair();
  const valid = buildEnvelope(sender, receiver.identity.agentId);
  const garbage = [
    null,
    undefined,
    'x',
    42,
    [],
    {},
    { v: 1 },
    { ...valid, payload: [] },
    { ...valid, signature: 123 },
  ];
  for (const input of garbage) {
    const result = receiver.service.verify({ envelope: input });
    assert.equal(result.ok, false);
    assert.equal(typeof result.code, 'string');
    assert.ok(Object.isFrozen(result));
  }
  assert.equal(receiver.service.verify().code, ENVELOPE_CODE.ENVELOPE_INVALID);
  assert.equal(receiver.service.verify({ envelope: valid, expectedTo: 42 }).code, ENVELOPE_CODE.ENVELOPE_INVALID);
});

test('结构校验：缺字段 / 版本错 / alg 错 / 时间戳非法 → ENVELOPE_INVALID', () => {
  const { sender, receiver } = makePair();
  const envelope = buildEnvelope(sender, receiver.identity.agentId);
  const invalidVariants = [
    { ...envelope, v: 2 },
    { ...envelope, alg: 'rsa' },
    { ...envelope, signature: '' },
    { ...envelope, payload: null },
    { ...envelope, meta: [] },
    { ...envelope, at: 'not-a-date' },
  ];
  for (const variant of invalidVariants) {
    assert.equal(receiver.service.verify({ envelope: variant }).code, ENVELOPE_CODE.ENVELOPE_INVALID);
  }
  for (const field of ['from', 'to', 'nonce', 'kind', 'at', 'signature', 'publicKeyPem']) {
    const missing = { ...envelope };
    delete missing[field];
    assert.equal(receiver.service.verify({ envelope: missing }).code, ENVELOPE_CODE.ENVELOPE_INVALID);
  }
});

test('参数校验：sign / learnKey / 构造函数非法输入', () => {
  const { sender } = makePair();
  assert.throws(() => sender.service.sign({ kind: 'TEXT' }), TypeError);
  assert.throws(() => sender.service.sign({ toAgentId: 'agent-b' }), TypeError);
  assert.throws(() => sender.service.sign({ toAgentId: 'agent-b', kind: 'TEXT', payload: [] }), TypeError);
  assert.throws(() => sender.service.sign({ toAgentId: 'agent-b', kind: 'TEXT', meta: 'x' }), TypeError);

  assert.throws(() => new SignedEnvelopeService({}), TypeError);
  assert.throws(
    () => new SignedEnvelopeService({ identity: sender.identity, windowMs: 0 }),
    RangeError
  );
  assert.throws(
    () => new SignedEnvelopeService({ identity: sender.identity, nonceTtlMs: -1 }),
    RangeError
  );
  assert.throws(
    () => new SignedEnvelopeService({ identity: sender.identity, clock: 'now' }),
    TypeError
  );

  assert.throws(() => sender.service.learnKey({ agentId: 'agent-x' }), TypeError);
  assert.throws(
    () => sender.service.learnKey({ agentId: 'agent-x', publicKeyPem: 'not-a-pem' }),
    (error) => error instanceof SignedEnvelopeError && error.code === ENVELOPE_CODE.ENVELOPE_INVALID
  );
  assert.equal(sender.service.knownKey('agent-unknown'), null);
});
