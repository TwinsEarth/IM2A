// 测试：Ed25519 身份与 AgentCard（契约 §11）。
// 覆盖：agentId 可重算、篡改任一字段、A 私钥签 B 卡片、过期、verification-is-a-verdict（不抛异常）、payload 签名。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  IDENTITY_CODE,
  IdentityError,
  KEY_ALG,
  agentIdFromPublicKey,
  canonicalize,
  cardHash,
  createAgentCard,
  createAgentIdentity,
  sha256Hex,
  signAgentCard,
  signPayload,
  verifyAgentCard,
  verifyPayload
} from '../src/agent-id.mjs';

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

const buildSigned = (overrides = {}) => {
  const clock = overrides.clock ?? makeClock();
  const identity = overrides.identity ?? createAgentIdentity({ clock });
  const card = createAgentCard({
    identity,
    ownerUserId: overrides.ownerUserId ?? 'user-a',
    displayName: overrides.displayName ?? '小助手',
    capabilities: overrides.capabilities ?? ['read', 'pay'],
    domains: overrides.domains ?? ['contact', 'task'],
    trustCeiling: overrides.trustCeiling ?? 4,
    endpoint: overrides.endpoint ?? 'wss://example.test/agent',
    ttlMs: overrides.ttlMs ?? 7 * 864e5,
    clock: overrides.cardClock ?? clock
  });
  const signedCard = signAgentCard({ card, identity });
  return { clock, identity, card, signedCard };
};

const cloneCard = (card, patch) => ({ ...card, ...patch });

test('KEY_ALG / IDENTITY_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...KEY_ALG }, { ED25519: 'ed25519' });
  assert.deepEqual({ ...IDENTITY_CODE }, {
    INVALID_KEY: 'INVALID_KEY',
    BAD_CARD: 'BAD_CARD',
    CARD_EXPIRED: 'CARD_EXPIRED',
    SIGNATURE_INVALID: 'SIGNATURE_INVALID',
    HASH_MISMATCH: 'HASH_MISMATCH'
  });
  assert.ok(Object.isFrozen(KEY_ALG));
  assert.ok(Object.isFrozen(IDENTITY_CODE));
});

test('createAgentIdentity：agentId 可由公钥重算（自证 id），身份对象冻结，createdAt 来自注入 clock', () => {
  const clock = makeClock();
  const identity = createAgentIdentity({ clock });
  assert.ok(Object.isFrozen(identity));
  assert.equal(identity.keyAlg, 'ed25519');
  assert.equal(identity.createdAt, '2026-10-21T00:00:00.000Z');
  assert.match(identity.agentId, /^agent-[A-Za-z0-9_-]{22}$/);
  assert.equal(identity.derivedId, identity.agentId);
  assert.equal(agentIdFromPublicKey(identity.publicKeyPem), identity.agentId);
  assert.match(identity.publicKeyPem, /BEGIN PUBLIC KEY/);
  assert.match(identity.privateKeyPem, /BEGIN PRIVATE KEY/);
});

test('平台名身份：显式 agentId 作为平台权威名保留，自证性由 derivedId 表达', () => {
  const identity = createAgentIdentity({ agentId: 'agent-lin-butler' });
  assert.equal(identity.agentId, 'agent-lin-butler');
  assert.equal(identity.derivedId, agentIdFromPublicKey(identity.publicKeyPem));
  assert.notEqual(identity.agentId, identity.derivedId);

  // 传的若正好是派生值（并用同一私钥恢复），则两态合一（自证 id）
  const selfCertifying = createAgentIdentity({ agentId: identity.derivedId, privateKeyPem: identity.privateKeyPem });
  assert.equal(selfCertifying.agentId, identity.derivedId);
  assert.equal(selfCertifying.agentId, selfCertifying.derivedId);

  // 空平台名仍属非法输入
  assert.throws(() => createAgentIdentity({ agentId: '   ' }), { code: 'INVALID_KEY' });
});

test('身份可只由私钥恢复：同一私钥 => 同一 agentId / 同一公钥', () => {
  const first = createAgentIdentity();
  const restored = createAgentIdentity({ privateKeyPem: first.privateKeyPem });
  assert.equal(restored.agentId, first.agentId);
  assert.equal(restored.publicKeyPem, first.publicKeyPem);
  assert.equal(restored.privateKeyPem, first.privateKeyPem);
});

test('INVALID_KEY：非法私钥 PEM / 非法公钥 PEM（平台名不再要求等于派生值）', () => {
  assert.throws(() => createAgentIdentity({ privateKeyPem: 'not-a-pem' }), { code: 'INVALID_KEY' });
  assert.throws(() => agentIdFromPublicKey('not-a-pem'), { code: 'INVALID_KEY' });
  assert.throws(() => agentIdFromPublicKey(''), { code: 'INVALID_KEY' });
  // 显式平台名是合法形态（契约 §11 裁定 1），不再是错误
  assert.doesNotThrow(() => createAgentIdentity({ agentId: 'agent-custom-string' }));
});

test('canonicalize 稳定序列化：键排序、嵌套、undefined→null；sha256Hex 为 sha256', () => {
  assert.equal(canonicalize({ b: 1, a: 2 }), '{"a":2,"b":1}');
  assert.equal(canonicalize({ a: [1, { d: 4, c: 3 }] }), '{"a":[1,{"c":3,"d":4}]}');
  assert.equal(canonicalize(undefined), 'null');
  assert.equal(canonicalize({ a: undefined }), '{"a":null}');
  assert.equal(sha256Hex('abc'), 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
  // 同一语义不同键序 => 同一哈希
  assert.equal(sha256Hex(canonicalize({ x: 1, y: 2 })), sha256Hex(canonicalize({ y: 2, x: 1 })));
});

test('createAgentCard：字段/默认值/过期时间符合契约，返回冻结对象', () => {
  const clock = makeClock();
  const identity = createAgentIdentity({ clock });
  const card = createAgentCard({ identity, ownerUserId: 'user-a', clock });
  assert.ok(Object.isFrozen(card));
  assert.equal(card.agentId, identity.agentId);
  assert.equal(card.derivedId, identity.derivedId);
  assert.equal(card.derivedId, agentIdFromPublicKey(identity.publicKeyPem));
  assert.equal(card.ownerUserId, 'user-a');
  assert.equal(card.displayName, null);
  assert.deepEqual([...card.capabilities], []);
  assert.deepEqual([...card.domains], []);
  assert.equal(card.trustCeiling, 4);
  assert.equal(card.endpoint, null);
  assert.equal(card.issuedAt, '2026-10-21T00:00:00.000Z');
  assert.equal(card.expiresAt, '2026-10-28T00:00:00.000Z'); // 默认 7 天
  assert.equal(cardHash(card), sha256Hex(canonicalize(card)));

  const custom = createAgentCard({
    identity,
    ownerUserId: 'user-a',
    displayName: '管家',
    capabilities: ['read', 'read', ''],
    domains: ['contact', 'contact'],
    trustCeiling: 2,
    endpoint: 'wss://x',
    ttlMs: 1000,
    clock
  });
  assert.deepEqual([...custom.capabilities], ['read']);
  assert.deepEqual([...custom.domains], ['contact']);
  assert.equal(custom.trustCeiling, 2);
  assert.equal(custom.expiresAt, '2026-10-21T00:00:01.000Z');
});

test('createAgentCard：缺 identity / ownerUserId / 非法 ttl 的输入错误', () => {
  assert.throws(() => createAgentCard({ ownerUserId: 'user-a' }), { code: 'INVALID_KEY' });
  const identity = createAgentIdentity();
  assert.throws(() => createAgentCard({ identity }), { code: 'BAD_CARD' });
  assert.throws(() => createAgentCard({ identity, ownerUserId: 'user-a', ttlMs: -1 }), { code: 'BAD_CARD' });
});

test('signAgentCard + verifyAgentCard 正常路径：ok、code=null、返回原 card', () => {
  const { signedCard, card } = buildSigned();
  assert.ok(Object.isFrozen(signedCard));
  assert.equal(signedCard.alg, 'ed25519');
  assert.equal(signedCard.cardHash, cardHash(card));
  assert.ok(typeof signedCard.signature === 'string' && signedCard.signature.length > 0);
  assert.equal(signedCard.signerPublicKeyPem, card.publicKeyPem);

  const verdict = verifyAgentCard({ signedCard });
  assert.ok(Object.isFrozen(verdict));
  assert.equal(verdict.ok, true);
  assert.equal(verdict.code, null);
  assert.equal(verdict.card, card);
});

test('篡改 card 任一字段 => HASH_MISMATCH（签后改内容必被抓住）', () => {
  const { signedCard, card } = buildSigned();
  const mutations = [
    { displayName: '冒充者' },
    { ownerUserId: 'user-b' },
    { trustCeiling: 0 },
    { capabilities: ['admin'] },
    { domains: ['memory'] },
    { endpoint: 'wss://evil' },
    { expiresAt: '2099-01-01T00:00:00.000Z' },
    { derivedId: 'agent-ZZZZZZZZZZZZZZZZZZZZZZ' }
  ];
  for (const patch of mutations) {
    const tampered = { ...signedCard, card: cloneCard(card, patch) };
    const verdict = verifyAgentCard({ signedCard: tampered });
    assert.equal(verdict.ok, false);
    assert.equal(verdict.code, 'HASH_MISMATCH', `字段 ${Object.keys(patch)[0]} 未被哈希保护`);
  }
});

test('篡改签名 => SIGNATURE_INVALID；篡改公钥 => BAD_CARD（agentId 不可重算）', () => {
  const { signedCard } = buildSigned();
  const signatureBytes = Buffer.from(signedCard.signature, 'base64');
  signatureBytes[0] ^= 0xff;
  const tamperedSig = { ...signedCard, signature: signatureBytes.toString('base64') };
  assert.equal(verifyAgentCard({ signedCard: tamperedSig }).code, 'SIGNATURE_INVALID');

  const other = createAgentIdentity();
  const swappedKey = { ...signedCard, signerPublicKeyPem: other.publicKeyPem };
  assert.equal(verifyAgentCard({ signedCard: swappedKey }).code, 'BAD_CARD');
});

test('A 的私钥签 B 的卡片 => 验签失败（不能借他人签名冒充）', () => {
  const identityA = createAgentIdentity();
  const identityB = createAgentIdentity();
  const cardB = createAgentCard({ identity: identityB, ownerUserId: 'user-b' });
  const forged = signAgentCard({ card: cardB, identity: identityA });
  const verdict = verifyAgentCard({ signedCard: forged });
  assert.equal(verdict.ok, false);
  // signerPublicKeyPem 是 A 的公钥，card.agentId 是 B 的 => agentId 无法由公钥重算
  assert.equal(verdict.code, 'BAD_CARD');
  assert.equal(forged.signerPublicKeyPem, identityA.publicKeyPem);
  assert.notEqual(identityA.agentId, cardB.agentId);
});

test('平台名卡片：agentId 为可读平台名，derivedId 承载自证性并可通过验签', () => {
  const clock = makeClock();
  const identity = createAgentIdentity({ agentId: 'agent-lin-butler', clock });
  const card = createAgentCard({ identity, ownerUserId: 'user-lin', clock });
  assert.equal(card.agentId, 'agent-lin-butler');
  assert.equal(card.derivedId, identity.derivedId);
  const signedCard = signAgentCard({ card, identity });
  const verdict = verifyAgentCard({ signedCard });
  assert.equal(verdict.ok, true);
  assert.equal(verdict.code, null);
  assert.equal(verdict.card.agentId, 'agent-lin-butler');
});

test('derivedId 校验：重签错误 derivedId => BAD_CARD；签后改 derivedId => HASH_MISMATCH；旧卡片缺 derivedId => 向后兼容通过', () => {
  const clock = makeClock();
  const identity = createAgentIdentity({ agentId: 'agent-lin-butler', clock });
  const card = createAgentCard({ identity, ownerUserId: 'user-lin', clock });

  // ⑤ 用错误 derivedId 重签（哈希与签名都有效）=> BAD_CARD
  const wrongDerivedCard = { ...card, derivedId: 'agent-ZZZZZZZZZZZZZZZZZZZZZZ' };
  const forged = signAgentCard({ card: wrongDerivedCard, identity });
  const forgedVerdict = verifyAgentCard({ signedCard: forged });
  assert.equal(forgedVerdict.ok, false);
  assert.equal(forgedVerdict.code, 'BAD_CARD');

  // 签后直接改 derivedId（不重签）=> 先被哈希检查抓住
  const tampered = { ...signAgentCard({ card, identity }), card: wrongDerivedCard };
  assert.equal(verifyAgentCard({ signedCard: tampered }).code, 'HASH_MISMATCH');

  // 旧版卡片没有 derivedId 字段 => 不进入第⑤步，保持向后兼容
  const { derivedId, ...legacyCard } = card;
  const legacySigned = signAgentCard({ card: legacyCard, identity });
  const legacyVerdict = verifyAgentCard({ signedCard: legacySigned });
  assert.equal(legacyVerdict.ok, true);
  assert.equal(legacyVerdict.code, null);
  assert.equal(legacyVerdict.card.derivedId, undefined);
});

test('边界：卡片过期 => CARD_EXPIRED；临界时刻（expiresAt === now）即过期', () => {
  const clock = makeClock();
  const identity = createAgentIdentity({ clock });
  const card = createAgentCard({ identity, ownerUserId: 'user-a', ttlMs: 1000, clock });
  const signedCard = signAgentCard({ card, identity });

  assert.equal(verifyAgentCard({ signedCard, now: '2026-10-21T00:00:00.999Z' }).ok, true);
  const expiredAtBoundary = verifyAgentCard({ signedCard, now: '2026-10-21T00:00:01.000Z' });
  assert.equal(expiredAtBoundary.ok, false);
  assert.equal(expiredAtBoundary.code, 'CARD_EXPIRED');

  // now 缺省时走注入 clock
  clock.advance(1000);
  assert.equal(verifyAgentCard({ signedCard, clock }).code, 'CARD_EXPIRED');
});

test('verification is a verdict：verifyAgentCard 对任何畸形输入都只返回 {ok,code} 不抛异常', () => {
  const malformed = [
    undefined,
    null,
    {},
    { card: null },
    { card: {}, cardHash: 'x', alg: 'ed25519', signature: 'x', signerPublicKeyPem: 'x' },
    { card: { agentId: 'agent-x', ownerUserId: 'user-a' }, cardHash: 'x', alg: 'rsa', signature: 'x', signerPublicKeyPem: 'x' }
  ];
  for (const input of malformed) {
    const verdict = verifyAgentCard({ signedCard: input });
    assert.equal(verdict.ok, false);
    assert.equal(typeof verdict.code, 'string');
    assert.ok(Object.isFrozen(verdict));
  }
});

test('signPayload / verifyPayload：正反例、键序无关、错误公钥', () => {
  const identity = createAgentIdentity();
  const payload = { action: 'read', targetId: 'user-b', nested: { b: 1, a: 2 } };
  const signed = signPayload({ identity, payload });
  assert.ok(Object.isFrozen(signed));
  assert.equal(signed.alg, 'ed25519');
  assert.equal(signed.payloadHash, sha256Hex(canonicalize(payload)));

  const ok = verifyPayload({ publicKeyPem: identity.publicKeyPem, payload, signature: signed.signature });
  assert.deepEqual({ ...ok }, { ok: true, code: null });

  // 键序不同但语义相同 => 验签仍通过
  const reordered = { nested: { a: 2, b: 1 }, targetId: 'user-b', action: 'read' };
  assert.equal(verifyPayload({ publicKeyPem: identity.publicKeyPem, payload: reordered, signature: signed.signature }).ok, true);

  // 篡改 payload
  const tampered = verifyPayload({
    publicKeyPem: identity.publicKeyPem,
    payload: { ...payload, action: 'pay' },
    signature: signed.signature
  });
  assert.equal(tampered.ok, false);
  assert.equal(tampered.code, 'SIGNATURE_INVALID');

  // 用别人的公钥验
  const other = createAgentIdentity();
  assert.equal(
    verifyPayload({ publicKeyPem: other.publicKeyPem, payload, signature: signed.signature }).code,
    'SIGNATURE_INVALID'
  );

  // 畸形输入不抛异常
  assert.equal(verifyPayload({ publicKeyPem: 'bad-pem', payload, signature: signed.signature }).code, 'INVALID_KEY');
  assert.equal(verifyPayload({ publicKeyPem: identity.publicKeyPem, payload }).code, 'BAD_CARD');
});

test('signPayload：缺 identity / 非法私钥抛 INVALID_KEY', () => {
  assert.throws(() => signPayload({ identity: null, payload: {} }), { code: 'INVALID_KEY' });
  assert.throws(() => signPayload({ identity: { privateKeyPem: 'bad' }, payload: {} }), { code: 'INVALID_KEY' });
});

test('IdentityError 带 code 且是 Error 子类', () => {
  try {
    createAgentIdentity({ privateKeyPem: 'bad' });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof IdentityError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'INVALID_KEY');
  }
});
