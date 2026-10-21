// 测试文件：§35 PACT 式 scoped 授权 + 签名回执（付费不扩大 scope / 单次消耗 / 过期撤销 / 回执绑定）/ 运行：node services/gateway/test/scoped-grant.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { canonicalize, createAgentIdentity, sha256Hex } from '../../identity/src/agent-id.mjs';
import { GRANT_CODE, GRANT_SCOPE, ScopedGrant, ScopedGrantError } from '../src/scoped-grant.mjs';

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

function makeService({ identity = null, maxTtlMs = 10 * 60e3 } = {}) {
  const clock = makeClock();
  return { clock, identity, service: new ScopedGrant({ clock, identity, maxTtlMs }) };
}

function makeGrant(service, overrides = {}) {
  return service.grant({
    messageId: 'msg-1',
    granteeAgentId: 'agent-b',
    scopes: [GRANT_SCOPE.VIEW_SUMMARY, GRANT_SCOPE.VIEW_ORIGINAL, GRANT_SCOPE.REPLY],
    ...overrides,
  });
}

test('grant：冻结授权视图、字段完整、默认 5min TTL，priceCents 仅记录', () => {
  const { service } = makeService();
  const grant = makeGrant(service, { scopes: [GRANT_SCOPE.VIEW_SUMMARY, GRANT_SCOPE.VIEW_ORIGINAL], priceCents: 500 });
  assert.match(grant.id, /^grant-/);
  assert.deepEqual(Object.keys(grant), ['id', 'messageId', 'granteeAgentId', 'scopes', 'priceCents', 'issuedAt', 'expiresAt']);
  assert.equal(grant.messageId, 'msg-1');
  assert.equal(grant.granteeAgentId, 'agent-b');
  assert.deepEqual([...grant.scopes], ['view_summary', 'view_original']);
  assert.equal(grant.priceCents, 500);
  assert.equal(grant.issuedAt, BASE_ISO);
  assert.equal(grant.expiresAt, '2026-10-21T10:05:00.000Z');
  assert.ok(Object.isFrozen(grant));
  assert.ok(Object.isFrozen(grant.scopes));
});

test('付费不扩大 scope：只授 VIEW_SUMMARY，即使 priceCents 很高也不能 consume(VIEW_ORIGINAL)', () => {
  const { service } = makeService();
  const grant = service.grant({
    messageId: 'msg-paid',
    granteeAgentId: 'agent-b',
    scopes: [GRANT_SCOPE.VIEW_SUMMARY],
    priceCents: 999_999,
  });
  assert.equal(grant.priceCents, 999_999, '价格只被记录');
  assert.deepEqual([...grant.scopes], ['view_summary'], '价格不改变授权集合');
  assert.throws(
    () => service.consume({ grantId: grant.id, action: GRANT_SCOPE.VIEW_ORIGINAL }),
    (error) => error instanceof ScopedGrantError && error.code === GRANT_CODE.SCOPE_NOT_GRANTED
  );
  assert.throws(
    () => service.consume({ grantId: grant.id, action: GRANT_SCOPE.REPLY }),
    (error) => error instanceof ScopedGrantError && error.code === GRANT_CODE.SCOPE_NOT_GRANTED
  );
  // 已授予的 scope 仍可按次消费
  assert.equal(service.consume({ grantId: grant.id, action: GRANT_SCOPE.VIEW_SUMMARY }).receipt.scope, 'view_summary');
});

test('每个 scope 单次消耗：同一 scope 二次 → ALREADY_USED，不同 scope 各自独立', () => {
  const { service } = makeService();
  const grant = makeGrant(service);
  const summary = service.consume({ grantId: grant.id, action: GRANT_SCOPE.VIEW_SUMMARY });
  assert.equal(summary.receipt.scope, 'view_summary');
  assert.equal(summary.receipt.action, 'view_summary');
  assert.equal(summary.receipt.messageId, 'msg-1');
  assert.equal(summary.receipt.at, BASE_ISO);
  assert.match(summary.receipt.hash, /^[0-9a-f]{64}$/);
  assert.ok(Object.isFrozen(summary));
  assert.ok(Object.isFrozen(summary.receipt));

  const original = service.consume({ grantId: grant.id, action: GRANT_SCOPE.VIEW_ORIGINAL });
  assert.equal(original.receipt.scope, 'view_original');
  const reply = service.consume({ grantId: grant.id, action: GRANT_SCOPE.REPLY });
  assert.equal(reply.receipt.scope, 'reply');

  for (const action of [GRANT_SCOPE.VIEW_SUMMARY, GRANT_SCOPE.VIEW_ORIGINAL, GRANT_SCOPE.REPLY]) {
    assert.throws(
      () => service.consume({ grantId: grant.id, action }),
      (error) => error instanceof ScopedGrantError && error.code === GRANT_CODE.ALREADY_USED,
      `${action} 第二次必须 ALREADY_USED`
    );
  }
});

test('过期边界（注入 clock 推进）：ttl-1ms 可消费，恰满 ttl → EXPIRED', () => {
  const { service, clock } = makeService();
  const grant = makeGrant(service, { ttlMs: 1000, scopes: [GRANT_SCOPE.VIEW_SUMMARY, GRANT_SCOPE.REPLY] });
  assert.equal(grant.expiresAt, '2026-10-21T10:00:01.000Z');
  clock.advance(999);
  assert.equal(service.consume({ grantId: grant.id, action: GRANT_SCOPE.VIEW_SUMMARY }).receipt.at, '2026-10-21T10:00:00.999Z');
  clock.advance(1);
  assert.throws(
    () => service.consume({ grantId: grant.id, action: GRANT_SCOPE.REPLY }),
    (error) => error instanceof ScopedGrantError && error.code === GRANT_CODE.EXPIRED
  );
});

test('撤销：revoke 后 consume → REVOKED；重复 revoke 幂等；list 可见撤销状态', () => {
  const { service } = makeService();
  const grant = makeGrant(service);
  assert.deepEqual({ ...service.revoke({ grantId: grant.id }) }, { grantId: grant.id, revoked: true });
  assert.throws(
    () => service.consume({ grantId: grant.id, action: GRANT_SCOPE.VIEW_SUMMARY }),
    (error) => error instanceof ScopedGrantError && error.code === GRANT_CODE.REVOKED
  );
  assert.deepEqual({ ...service.revoke({ grantId: grant.id }) }, { grantId: grant.id, revoked: true }, '撤销幂等');
  const listed = service.list({ messageId: 'msg-1' });
  assert.equal(listed.length, 1);
  assert.equal(listed[0].revoked, true);
  assert.equal(listed[0].revokedAt, BASE_ISO);
});

test('回执绑定：正常回执可验；改 messageId/scope/action/at/hash/签名任一字段 → RECEIPT_MISMATCH', () => {
  const { service } = makeService();
  const grant = makeGrant(service);
  const { receipt } = service.consume({ grantId: grant.id, action: GRANT_SCOPE.VIEW_SUMMARY });
  assert.equal(receipt.hash, sha256Hex(canonicalize({ messageId: 'msg-1', scope: 'view_summary', action: 'view_summary', at: BASE_ISO })));
  assert.deepEqual({ ...service.verifyReceipt({ receipt }) }, { ok: true, code: null });
  assert.ok(Object.isFrozen(service.verifyReceipt({ receipt })));

  const tamperedReceipts = [
    { ...receipt, messageId: 'msg-2' },
    { ...receipt, scope: GRANT_SCOPE.REPLY },
    { ...receipt, action: GRANT_SCOPE.REPLY },
    { ...receipt, at: '2026-10-21T10:00:01.000Z' },
    { ...receipt, hash: '0'.repeat(64) },
    { ...receipt, signature: 'AAAA' },
  ];
  for (const tampered of tamperedReceipts) {
    const verdict = service.verifyReceipt({ receipt: tampered });
    assert.equal(verdict.ok, false);
    assert.equal(verdict.code, GRANT_CODE.RECEIPT_MISMATCH);
  }
});

test('注入 identity：回执带签名且可验；换身份/无签名能力时验签失败', () => {
  const identity = createAgentIdentity({ clock: makeClock() });
  const { service } = makeService({ identity });
  const grant = makeGrant(service);
  const { receipt } = service.consume({ grantId: grant.id, action: GRANT_SCOPE.VIEW_ORIGINAL });
  assert.equal(receipt.signature !== undefined, true);
  assert.ok(receipt.signature.length > 0);
  assert.deepEqual({ ...service.verifyReceipt({ receipt }) }, { ok: true, code: null });

  const otherIdentity = createAgentIdentity({ clock: makeClock() });
  const foreignService = makeService({ identity: otherIdentity }).service;
  assert.equal(foreignService.verifyReceipt({ receipt }).code, GRANT_CODE.RECEIPT_MISMATCH, '换身份验签必须失败');

  // 无 identity 的服务无法验证带签名回执
  const unsignedService = makeService().service;
  assert.equal(unsignedService.verifyReceipt({ receipt }).code, GRANT_CODE.RECEIPT_MISMATCH);

  // 未注入 identity 的回执无 signature，仅靠 hash 完整性校验也可验
  const plainReceipt = unsignedService.consume({
    grantId: unsignedService.grant({ messageId: 'm', granteeAgentId: 'a', scopes: ['reply'] }).id,
    action: 'reply',
  }).receipt;
  assert.equal(Object.hasOwn(plainReceipt, 'signature'), false);
  assert.equal(unsignedService.verifyReceipt({ receipt: plainReceipt }).ok, true);
});

test('verifyReceipt 对垃圾输入只返回判定、绝不抛异常', () => {
  const { service } = makeService();
  const garbage = [null, undefined, 'receipt', 42, [], {}, { messageId: 'm1' }, { messageId: 'm1', scope: 'nope', action: 'reply', at: BASE_ISO, hash: 'x' }];
  for (const receipt of garbage) {
    const verdict = service.verifyReceipt({ receipt });
    assert.equal(verdict.ok, false);
    assert.equal(verdict.code, GRANT_CODE.RECEIPT_MISMATCH);
    assert.ok(Object.isFrozen(verdict));
  }
  assert.equal(service.verifyReceipt().code, GRANT_CODE.RECEIPT_MISMATCH);
});

test('非法 scopes / action / 未知 grantId → SCOPE_INVALID / NOT_FOUND', () => {
  const { service } = makeService();
  for (const scopes of [[], ['view_all'], 'view_summary', [null], [1], {}, [GRANT_SCOPE.REPLY, 'delete']]) {
    assert.throws(
      () => service.grant({ messageId: 'msg-1', granteeAgentId: 'agent-b', scopes }),
      (error) => error instanceof ScopedGrantError && error.code === GRANT_CODE.SCOPE_INVALID,
      `应拒绝 scopes=${JSON.stringify(scopes)}`
    );
  }
  const grant = makeGrant(service);
  assert.throws(
    () => service.consume({ grantId: grant.id, action: 'delete_message' }),
    (error) => error instanceof ScopedGrantError && error.code === GRANT_CODE.SCOPE_INVALID
  );
  assert.throws(
    () => service.consume({ grantId: 'grant-unknown', action: GRANT_SCOPE.REPLY }),
    (error) => error instanceof ScopedGrantError && error.code === GRANT_CODE.NOT_FOUND
  );
  assert.throws(
    () => service.revoke({ grantId: 'grant-unknown' }),
    (error) => error instanceof ScopedGrantError && error.code === GRANT_CODE.NOT_FOUND
  );
});

test('list：按 messageId 过滤、按创建顺序、返回冻结数组', () => {
  const { service } = makeService();
  const first = makeGrant(service, { messageId: 'msg-1' });
  const second = makeGrant(service, { messageId: 'msg-2' });
  assert.equal(service.list().length, 2);
  assert.deepEqual(service.list({ messageId: 'msg-1' }).map((item) => item.id), [first.id]);
  assert.deepEqual(service.list({ messageId: 'msg-2' }).map((item) => item.id), [second.id]);
  assert.deepEqual([...service.list({ messageId: 'msg-none' })], []);
  const all = service.list();
  assert.ok(Object.isFrozen(all));
  assert.ok(Object.isFrozen(all[0]));
  assert.deepEqual(all[0].usedScopes, []);
});

test('参数校验：ttl / priceCents / identity / clock / maxTtlMs 非法', () => {
  assert.throws(() => new ScopedGrant({ clock: 'now' }), TypeError);
  assert.throws(() => new ScopedGrant({ maxTtlMs: 0 }), RangeError);
  assert.throws(() => new ScopedGrant({ identity: {} }), TypeError);
  const { service } = makeService({ maxTtlMs: 1000 });
  assert.throws(() => makeGrant(service, { ttlMs: 0 }), RangeError);
  assert.throws(() => makeGrant(service, { ttlMs: 1001 }), RangeError, 'ttl 超过 maxTtlMs 必须显式报错');
  assert.throws(() => makeGrant(service, { ttlMs: 1.5 }), RangeError);
  assert.throws(() => makeGrant(service, { priceCents: -1, ttlMs: 1000 }), TypeError);
  assert.throws(() => makeGrant(service, { priceCents: 1.5, ttlMs: 1000 }), TypeError);
  assert.throws(() => service.grant({ granteeAgentId: 'agent-b', scopes: ['reply'] }), TypeError);
  assert.throws(() => service.grant({ messageId: 'msg-1', scopes: ['reply'] }), TypeError);
  assert.throws(() => service.list({ messageId: 123 }), TypeError);
  assert.throws(() => service.consume({ grantId: '', action: 'reply' }), TypeError);
});
