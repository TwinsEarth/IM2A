// 测试文件：§22 服务端可信上下文注入（签名/篡改/过期/跨 Agent/候选可见域裁剪）/ 运行：node services/gateway/test/trusted-context.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { makeClock } from './_helpers.mjs';
import { createAgentIdentity } from '../../identity/src/agent-id.mjs';
import { AgentRegistry, TrustMatrix } from '../../orchestrator/src/index.mjs';
import { DOMAIN, VisibilityGate } from '../src/visibility.mjs';
import { CONTEXT_CODE, TrustedContextError, TrustedContextProvider } from '../src/trusted-context.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';

function setup({ trustZoneMin = 4 } = {}) {
  const clock = makeClock(BASE_ISO);
  const identity = createAgentIdentity({ clock });
  const registry = new AgentRegistry();
  registry.register({
    agentId: identity.agentId,
    ownerUserId: 'user-a',
    kind: 'butler',
    allowedSkills: ['read', 'pay'],
    trustZoneMin,
  });
  const trustMatrix = new TrustMatrix({ clock });
  trustMatrix.grant({ userId: 'user-a', targetId: 'user-b', level: 2 }); // TRUSTED
  const visibility = new VisibilityGate({ trustMatrix, clock });
  const provider = new TrustedContextProvider({ trustMatrix, registry, visibility, identity, clock, ttlMs: 120_000 });
  return { clock, identity, registry, trustMatrix, visibility, provider };
}

function buildContext({ provider, identity }, overrides = {}) {
  return provider.build({
    agentId: identity.agentId,
    principalUserId: 'user-a',
    conversationId: 'ch-direct-1',
    channelType: 'DIRECT',
    members: ['user-a', { memberId: 'user-b', kind: 'human' }],
    senderId: 'user-b',
    candidates: {
      contacts: [
        { id: 'user-b', displayName: '老王', detail: '同事', endpoint: 'wss://im/user-b' },
        { id: 'user-stranger', displayName: '陌生人', detail: 'secret', endpoint: 'wss://evil' },
      ],
      groups: [{ id: 'grp-1', displayName: '读书会' }],
      approvals: [{ id: 'tkt-1', title: '加好友审批' }],
    },
    ...overrides,
  });
}

test('build：冻结上下文包含会话/发送者/能力/候选/签名，verify 通过', () => {
  const system = setup();
  const context = buildContext(system);
  assert.match(context.contextId, /^ctx-/);
  assert.equal(context.issuedAt, BASE_ISO);
  assert.equal(context.expiresAt, '2026-10-21T10:02:00.000Z');
  assert.equal(context.agentId, system.identity.agentId);
  assert.equal(context.principalUserId, 'user-a');
  assert.equal(context.conversation.id, 'ch-direct-1');
  assert.equal(context.conversation.channelType, 'DIRECT');
  assert.deepEqual(
    context.conversation.members.map((member) => (typeof member === 'string' ? member : member.memberId)),
    ['user-a', 'user-b']
  );
  assert.deepEqual({ ...context.sender }, { id: 'user-b', trustLevel: 2 });
  assert.deepEqual([...context.capabilities], ['read', 'pay']);
  assert.equal(context.trustCeiling, 4);
  assert.equal(context.signature.alg, 'ed25519');
  assert.ok(context.signature.signature.length > 0);
  assert.ok(context.signature.payloadHash.length === 64);

  assert.ok(Object.isFrozen(context));
  assert.ok(Object.isFrozen(context.conversation));
  assert.ok(Object.isFrozen(context.conversation.members));
  assert.ok(Object.isFrozen(context.sender));
  assert.ok(Object.isFrozen(context.capabilities));
  assert.ok(Object.isFrozen(context.candidates));
  assert.ok(Object.isFrozen(context.signature));

  const verified = system.provider.verify({ context, agentId: system.identity.agentId });
  assert.deepEqual({ ok: verified.ok, code: verified.code }, { ok: true, code: null });
  assert.equal(verified.context, context);
});

test('篡改任意字段 → CONTEXT_FORGED（Agent 只读不可改）', () => {
  const system = setup();
  const context = buildContext(system);
  const tamperedVariants = [
    { ...context, trustCeiling: 0 },
    { ...context, principalUserId: 'user-evil' },
    { ...context, agentId: 'agent-evil' },
    { ...context, contextId: 'ctx-evil' },
    { ...context, sender: { ...context.sender, trustLevel: 0 } },
    { ...context, conversation: { ...context.conversation, id: 'ch-evil' } },
    { ...context, capabilities: ['pay'] },
    { ...context, candidates: { ...context.candidates, contacts: [] } },
    { ...context, signature: { ...context.signature, signature: 'AAAA' } },
  ];
  for (const tampered of tamperedVariants) {
    const result = system.provider.verify({ context: tampered });
    assert.equal(result.ok, false);
    assert.equal(result.code, CONTEXT_CODE.CONTEXT_FORGED, '任何字段被改都必须判定为伪造');
  }
});

test('换一个网关身份重新签发 → 原 provider 验签失败（CONTEXT_FORGED）', () => {
  const system1 = setup();
  const system2 = setup(); // 不同 Ed25519 identity
  const context = buildContext(system2);
  assert.equal(system1.provider.verify({ context }).code, CONTEXT_CODE.CONTEXT_FORGED);
});

test('TTL：过期 → CONTEXT_EXPIRED（注入 clock 推进，不 sleep）', () => {
  const system = setup();
  const context = buildContext(system);
  system.clock.advance(119_999);
  assert.equal(system.provider.verify({ context }).ok, true, '未到期仍有效');
  system.clock.advance(1);
  assert.equal(system.provider.verify({ context }).code, CONTEXT_CODE.CONTEXT_EXPIRED);
});

test('跨 Agent 复用 → CONTEXT_SCOPE_MISMATCH；不传 agentId 时不做归属校验', () => {
  const system = setup();
  const context = buildContext(system);
  const result = system.provider.verify({ context, agentId: 'agent-other' });
  assert.deepEqual({ ok: result.ok, code: result.code }, { ok: false, code: CONTEXT_CODE.CONTEXT_SCOPE_MISMATCH });
  assert.equal(system.provider.verify({ context }).ok, true);
  assert.equal(system.provider.verify({ context, agentId: null }).ok, true);
  assert.equal(
    system.provider.verify({ context, agentId: system.identity.agentId }).ok,
    true,
    '本人 Agent 复用合法'
  );
});

test('候选裁剪：STRANGER 档只回存在性（id/exists），绝不带出 detail', () => {
  const system = setup();
  const context = buildContext(system);
  const [known, stranger] = context.candidates.contacts;
  assert.equal(known.id, 'user-b');
  assert.equal(known.trustLevel, 2);
  assert.equal(known.displayName, '老王');
  assert.equal(known.detail, '同事');
  assert.equal(known.endpoint, 'wss://im/user-b');

  assert.equal(stranger.id, 'user-stranger');
  assert.deepEqual(Object.keys(stranger).sort(), ['exists', 'id'], '陌生人档只能看到存在性');
  assert.equal(stranger.detail, undefined, '陌生人 detail 不得带出');
  assert.equal(stranger.endpoint, undefined);
  assert.equal(stranger.displayName, undefined);
  assert.equal(context.candidates.contacts.length, 2, '两个候选都在，只是内容被裁剪');
  assert.ok(Object.isFrozen(stranger));
});

test('候选数量受可见域裁剪影响：超出 trustCeiling 的候选整项丢弃', () => {
  const system = setup({ trustZoneMin: 2 }); // context.trustCeiling=2 → 只允许 trustLevel<=2 的资源
  const context = buildContext(system);
  assert.equal(context.trustCeiling, 2);
  // user-b(TRUSTED=2) 保留；user-stranger(STRANGER=4) > 2 → §13 DENY → 整项消失
  assert.deepEqual(context.candidates.contacts.map((item) => item.id), ['user-b']);
  assert.equal(context.candidates.contacts[0].detail, '同事');
  // groups / approvals 的候选默认是 STRANGER(4)，同样被裁剪掉
  assert.deepEqual([...context.candidates.groups], []);
  assert.deepEqual([...context.candidates.approvals], []);
  assert.equal(system.provider.verify({ context }).ok, true);
});

test('未注入 visibility 时采用最保守口径：候选只保留存在性（注入 ≠ 越权）', () => {
  const system = setup();
  const provider = new TrustedContextProvider({
    trustMatrix: system.trustMatrix,
    registry: system.registry,
    identity: system.identity,
    clock: system.clock,
  });
  const context = provider.build({
    agentId: system.identity.agentId,
    principalUserId: 'user-a',
    conversationId: 'ch-direct-2',
    senderId: 'user-b',
    candidates: { contacts: [{ id: 'user-b', displayName: '老王', detail: '同事' }] },
  });
  assert.deepEqual(Object.keys(context.candidates.contacts[0]).sort(), ['exists', 'id']);
  assert.equal(provider.verify({ context }).ok, true);
});

test('sender：不传 senderId → null；传了按 trustMatrix 计算 trustLevel', () => {
  const system = setup();
  const context = buildContext(system, { senderId: null });
  assert.equal(context.sender, null);

  const strangerContext = system.provider.build({
    agentId: system.identity.agentId,
    principalUserId: 'user-a',
    conversationId: 'ch-direct-3',
    senderId: 'user-stranger',
  });
  assert.deepEqual({ ...strangerContext.sender }, { id: 'user-stranger', trustLevel: 4 });
  assert.deepEqual([...strangerContext.candidates.contacts], []);
});

test('verify 对任意输入只返回判定、绝不抛异常', () => {
  const system = setup();
  const garbage = [null, undefined, 'context', 42, [], {}, { contextId: 'ctx-1' }, { ...buildContext(system), contextId: 123 }];
  for (const input of garbage) {
    const result = system.provider.verify({ context: input });
    assert.equal(result.ok, false, `输入 ${JSON.stringify(input)} 不应通过`);
    assert.ok(Object.isFrozen(result));
    assert.ok(
      [CONTEXT_CODE.CONTEXT_INVALID, CONTEXT_CODE.CONTEXT_FORGED, CONTEXT_CODE.CONTEXT_SCOPE_MISMATCH, CONTEXT_CODE.CONTEXT_EXPIRED].includes(
        result.code
      )
    );
  }
  // 什么都不传也不能抛
  assert.equal(system.provider.verify().code, CONTEXT_CODE.CONTEXT_INVALID);
});

test('build 参数校验：必填缺失 / 未注册 Agent / 成员与候选非法 → 抛错', () => {
  const system = setup();
  assert.throws(() => system.provider.build({ principalUserId: 'user-a', conversationId: 'c' }), TypeError);
  assert.throws(() => system.provider.build({ agentId: system.identity.agentId, conversationId: 'c' }), TypeError);
  assert.throws(() => system.provider.build({ agentId: system.identity.agentId, principalUserId: 'user-a' }), TypeError);
  assert.throws(
    () => system.provider.build({ agentId: 'agent-unknown', principalUserId: 'user-a', conversationId: 'c' }),
    (error) => error instanceof TrustedContextError && error.code === CONTEXT_CODE.CONTEXT_INVALID
  );
  assert.throws(() => buildContext(system, { members: 'user-a' }), TypeError);
  assert.throws(() => buildContext(system, { members: [123] }), TypeError);
  assert.throws(() => buildContext(system, { senderId: 42 }), TypeError);
  assert.throws(() => buildContext(system, { channelType: 42 }), TypeError);
  assert.throws(() => buildContext(system, { candidates: { contacts: 'nope' } }), TypeError);
  assert.throws(() => buildContext(system, { candidates: { contacts: [{ displayName: '缺 id' }] } }), TypeError);
});

test('构造参数校验：依赖/identity/ttl/visibility 非法 → 抛错', () => {
  const system = setup();
  assert.throws(() => new TrustedContextProvider({}), TypeError);
  assert.throws(
    () => new TrustedContextProvider({ trustMatrix: system.trustMatrix, registry: system.registry }),
    TypeError
  );
  assert.throws(
    () =>
      new TrustedContextProvider({
        trustMatrix: system.trustMatrix,
        registry: system.registry,
        identity: system.identity,
        ttlMs: 0,
      }),
    RangeError
  );
  assert.throws(
    () =>
      new TrustedContextProvider({
        trustMatrix: system.trustMatrix,
        registry: system.registry,
        visibility: {},
        identity: system.identity,
      }),
    TypeError
  );
  assert.throws(
    () => new TrustedContextProvider({ trustMatrix: {}, registry: system.registry, identity: system.identity }),
    TypeError
  );
});
