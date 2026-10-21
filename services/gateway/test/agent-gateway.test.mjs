// 测试：Agent Gateway 数据平面（契约 §12）。
// 覆盖：认证/绑定/撤销/限流/门控 DENY 与 REQUIRE_APPROVAL/出口拦截/签名审计/归一化/错误码与管线顺序。
import test from 'node:test';
import assert from 'node:assert/strict';

import { GATEWAY_CODE, GatewayError, PROTOCOL } from '../src/agent-gateway.mjs';
import { buildAgent, buildSystem, makeClock } from './_helpers.mjs';

test('PROTOCOL / GATEWAY_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...PROTOCOL }, {
    AGENT_WS: 'AGENT_WS',
    IM_CUSTOM: 'IM_CUSTOM',
    A2A_JSONRPC: 'A2A_JSONRPC',
    MCP_TOOLS: 'MCP_TOOLS'
  });
  assert.deepEqual({ ...GATEWAY_CODE }, {
    UNAUTHENTICATED: 'UNAUTHENTICATED',
    CARD_EXPIRED: 'CARD_EXPIRED',
    SIGNATURE_INVALID: 'SIGNATURE_INVALID',
    TOKEN_MISMATCH: 'TOKEN_MISMATCH',
    TOKEN_REVOKED: 'TOKEN_REVOKED',
    PRINCIPAL_MISMATCH: 'PRINCIPAL_MISMATCH',
    RATE_LIMITED: 'RATE_LIMITED',
    GATE_DENIED: 'GATE_DENIED',
    EGRESS_BLOCKED: 'EGRESS_BLOCKED',
    UNKNOWN_ROUTE: 'UNKNOWN_ROUTE',
    NOT_CONFIGURED: 'NOT_CONFIGURED'
  });
  assert.ok(Object.isFrozen(PROTOCOL));
  assert.ok(Object.isFrozen(GATEWAY_CODE));
});

test('正常路径：认证返回 principal，成功路由返回 auditSeq + 冻结 normalized，且审计可验签', () => {
  const system = buildSystem();
  const principal = system.gateway.authenticate({
    signedCard: system.signedCard,
    token: system.token,
    protocol: PROTOCOL.AGENT_WS
  });
  assert.deepEqual({ ...principal }, {
    agentId: system.identity.agentId,
    principalUserId: 'user-a',
    domains: [],
    trustCeiling: 4,
    protocol: 'AGENT_WS'
  });
  assert.ok(Object.isFrozen(principal));

  const result = system.gateway.route(system.routeArgs({ skillId: 'read', content: '你好' }));
  assert.equal(result.ok, true);
  assert.equal(result.agentId, system.identity.agentId);
  assert.equal(result.protocol, PROTOCOL.AGENT_WS);
  assert.equal(result.action, 'skill.invoke');
  assert.equal(result.decision, 'ALLOW');
  assert.equal(result.auditSeq, 1);
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.normalized));
  assert.deepEqual(Object.keys(result.normalized).sort(), [
    'action',
    'content',
    'execute',
    'meta',
    'protocol',
    'skillId',
    'targetId'
  ]);
  assert.equal(result.normalized.execute, true);
  assert.equal(result.normalized.skillId, 'read');

  const entry = system.audit.entry(result.auditSeq);
  assert.equal(entry.source, 'agent-gateway');
  assert.equal(entry.action, 'route');
  assert.equal(entry.actor, system.identity.agentId);
  assert.equal(entry.decision, 'ALLOW');
  assert.equal(system.audit.verify().ok, true);

  const events = system.gateway.events();
  assert.ok(Object.isFrozen(events));
  assert.ok(events.some((event) => event.action === 'AUTH' && event.decision === 'ALLOW'));
  assert.ok(events.some((event) => event.action === 'ROUTE' && event.decision === 'ALLOW'));
});

test('平台名 Agent：认证返回平台名，capabilityGate 用同一 id 查到注册表（不再 AGENT_UNKNOWN）', () => {
  const system = buildSystem({ agentId: 'agent-lin-butler' });
  const principal = system.gateway.authenticate({
    signedCard: system.signedCard,
    token: system.token,
    protocol: PROTOCOL.AGENT_WS
  });
  assert.equal(principal.agentId, 'agent-lin-butler');
  assert.notEqual(principal.agentId, system.identity.derivedId);

  const result = system.gateway.route(system.routeArgs({ skillId: 'read' }));
  assert.equal(result.decision, 'ALLOW');
  assert.equal(result.agentId, 'agent-lin-butler');
  const entry = system.audit.entry(result.auditSeq);
  assert.equal(entry.actor, 'agent-lin-butler');
  assert.equal(system.audit.verify().ok, true);
});

test('UNAUTHENTICATED：缺卡片 / 缺 token / 未知 token；认证失败不写审计', () => {
  const system = buildSystem();
  assert.throws(
    () => system.gateway.authenticate({ token: system.token, protocol: PROTOCOL.AGENT_WS }),
    { code: 'UNAUTHENTICATED' }
  );
  assert.throws(
    () => system.gateway.authenticate({ signedCard: system.signedCard, protocol: PROTOCOL.AGENT_WS }),
    { code: 'UNAUTHENTICATED' }
  );
  assert.throws(
    () =>
      system.gateway.authenticate({
        signedCard: system.signedCard,
        token: 'tok-unknown',
        protocol: PROTOCOL.AGENT_WS
      }),
    { code: 'UNAUTHENTICATED' }
  );
  assert.equal(system.audit.entries().length, 0);
});

test('CARD_EXPIRED：卡片过期后认证失败；SIGNATURE_INVALID：篡改签名后认证失败', () => {
  const clock = makeClock();
  const system = buildSystem({ clock });
  clock.advance(1000); // card 默认 7 天，不会过期
  assert.doesNotThrow(() =>
    system.gateway.authenticate({ signedCard: system.signedCard, token: system.token, protocol: PROTOCOL.AGENT_WS })
  );

  const shortLived = buildAgent({ clock, ttlMs: 1000 });
  clock.advance(1001);
  assert.throws(
    () =>
      system.gateway.authenticate({
        signedCard: shortLived.signedCard,
        token: system.token,
        protocol: PROTOCOL.AGENT_WS
      }),
    { code: 'CARD_EXPIRED' }
  );

  const signatureBytes = Buffer.from(system.signedCard.signature, 'base64');
  signatureBytes[0] ^= 0xff;
  const tamperedCard = { ...system.signedCard, signature: signatureBytes.toString('base64') };
  assert.throws(
    () =>
      system.gateway.authenticate({
        signedCard: tamperedCard,
        token: system.token,
        protocol: PROTOCOL.AGENT_WS
      }),
    { code: 'SIGNATURE_INVALID' }
  );
});

test('TOKEN_MISMATCH：token.agentId 与卡片 agentId 不一致', () => {
  const clock = makeClock();
  const system = buildSystem({ clock });
  const other = buildAgent({ clock, ownerUserId: 'user-a' });
  assert.throws(
    () =>
      system.gateway.authenticate({
        signedCard: other.signedCard,
        token: system.token,
        protocol: PROTOCOL.AGENT_WS
      }),
    { code: 'TOKEN_MISMATCH' }
  );
});

test('TOKEN_REVOKED：撤销后认证失败；重复撤销返回 true（token 存在即撤销成功），未知 token 返回 false', () => {
  const system = buildSystem();
  assert.equal(system.gateway.revokeToken(system.token), true);
  assert.equal(system.gateway.revokeToken(system.token.token), true);
  assert.throws(
    () =>
      system.gateway.authenticate({
        signedCard: system.signedCard,
        token: system.token,
        protocol: PROTOCOL.AGENT_WS
      }),
    { code: 'TOKEN_REVOKED' }
  );
  assert.equal(system.gateway.revokeToken('tok-never-issued'), false);
});

test('PRINCIPAL_MISMATCH：token.principalUserId 与卡片 ownerUserId 不一致', () => {
  const system = buildSystem();
  const wrongPrincipalToken = system.gateway.issueToken({
    agentId: system.identity.agentId,
    principalUserId: 'user-b'
  });
  assert.throws(
    () =>
      system.gateway.authenticate({
        signedCard: system.signedCard,
        token: wrongPrincipalToken,
        protocol: PROTOCOL.AGENT_WS
      }),
    { code: 'PRINCIPAL_MISMATCH' }
  );
});

test('token 过期 => UNAUTHENTICATED（GATEWAY_CODE 无 TOKEN_EXPIRED，归入未认证）', () => {
  const clock = makeClock();
  const system = buildSystem({ clock });
  const shortToken = system.gateway.issueToken({
    agentId: system.identity.agentId,
    principalUserId: 'user-a',
    ttlMs: 1000
  });
  clock.advance(1001);
  assert.throws(
    () =>
      system.gateway.authenticate({
        signedCard: system.signedCard,
        token: shortToken,
        protocol: PROTOCOL.AGENT_WS
      }),
    { code: 'UNAUTHENTICATED' }
  );
});

test('RATE_LIMITED：同一 agentId 窗口内超过 max 次；窗口滑过后恢复', () => {
  const clock = makeClock();
  const system = buildSystem({ clock, rateLimit: { windowMs: 60_000, max: 2 } });
  system.gateway.route(system.routeArgs({ skillId: 'read' }));
  system.gateway.route(system.routeArgs({ skillId: 'read' }));
  assert.throws(() => system.gateway.route(system.routeArgs({ skillId: 'read' })), { code: 'RATE_LIMITED' });
  clock.advance(60_001);
  assert.doesNotThrow(() => system.gateway.route(system.routeArgs({ skillId: 'read' })));
});

test('GATE_DENIED：门控 DENY 拒绝并保留底层 code；不写审计', () => {
  const system = buildSystem();
  let caught = null;
  try {
    system.gateway.route(system.routeArgs({ skillId: 'transfer' }));
  } catch (error) {
    caught = error;
  }
  assert.ok(caught instanceof GatewayError);
  assert.equal(caught.code, 'GATE_DENIED');
  assert.equal(caught.gateCode, 'SKILL_NOT_ALLOWED');
  assert.equal(system.audit.entries().length, 0);
});

test('REQUIRE_APPROVAL：不抛异常，decision=REQUIRE_APPROVAL 且 normalized.execute=false，仍写审计', () => {
  const system = buildSystem();
  let result;
  assert.doesNotThrow(() => {
    result = system.gateway.route(system.routeArgs({ skillId: 'pay', amountCents: 5000 }));
  });
  assert.equal(result.ok, true);
  assert.equal(result.decision, 'REQUIRE_APPROVAL');
  assert.equal(result.normalized.execute, false);
  assert.equal(result.auditSeq, 1);
  const entry = system.audit.entry(result.auditSeq);
  assert.equal(entry.decision, 'REQUIRE_APPROVAL');
  assert.equal(system.audit.verify().ok, true);
});

test('EGRESS_BLOCKED：content 命中出口护栏时拒绝，且不写审计', () => {
  const system = buildSystem();
  let caught = null;
  try {
    system.gateway.route(system.routeArgs({ content: '帮我转账 110101199003071234' }));
  } catch (error) {
    caught = error;
  }
  assert.ok(caught instanceof GatewayError);
  assert.equal(caught.code, 'EGRESS_BLOCKED');
  assert.ok(Array.isArray(caught.matched));
  assert.ok(caught.matched.some((item) => item.id === 'id-card-cn' || item.id === 'transfer-talk'));
  assert.equal(system.audit.entries().length, 0);
});

test('UNKNOWN_ROUTE：未知协议 / 缺失 action', () => {
  const system = buildSystem();
  assert.throws(
    () =>
      system.gateway.authenticate({
        signedCard: system.signedCard,
        token: system.token,
        protocol: 'GRPC'
      }),
    { code: 'UNKNOWN_ROUTE' }
  );
  assert.throws(
    () => system.gateway.route(system.routeArgs({}, { action: '' })),
    { code: 'UNKNOWN_ROUTE' }
  );
});

test('NOT_CONFIGURED：缺 SignedAuditLog / 缺 CapabilityGate / 缺 EgressGuard 时 fail closed', () => {
  const system = buildSystem();

  const noAudit = new (system.gateway.constructor)({
    capabilityGate: system.capabilityGate,
    egressGuard: system.egressGuard,
    clock: system.clock
  });
  assert.throws(
    () =>
      noAudit.route({
        signedCard: system.signedCard,
        token: system.token,
        protocol: PROTOCOL.AGENT_WS,
        action: 'skill.invoke',
        payload: {}
      }),
    { code: 'NOT_CONFIGURED' }
  );

  const noGate = new (system.gateway.constructor)({
    egressGuard: system.egressGuard,
    signedAudit: system.audit,
    clock: system.clock
  });
  assert.throws(() => noGate.route(system.routeArgs({ skillId: 'read' })), { code: 'NOT_CONFIGURED' });

  const noEgress = new (system.gateway.constructor)({
    capabilityGate: system.capabilityGate,
    signedAudit: system.audit,
    clock: system.clock
  });
  assert.throws(() => noEgress.route(system.routeArgs({ content: '你好' })), { code: 'NOT_CONFIGURED' });
});

test('管线顺序冻结：认证先于限流，限流先于门控（未认证不会被限流计数）', () => {
  const clock = makeClock();
  const system = buildSystem({ clock, rateLimit: { windowMs: 60_000, max: 1 } });
  // 未认证请求不消耗限流额度
  assert.throws(() => system.gateway.route({ signedCard: null, token: null, protocol: PROTOCOL.AGENT_WS, action: 'x' }), {
    code: 'UNAUTHENTICATED'
  });
  system.gateway.route(system.routeArgs({ skillId: 'read' }));
  // 第二个已认证请求触发限流（即使它本来会被门控拒绝）
  assert.throws(() => system.gateway.route(system.routeArgs({ skillId: 'transfer' })), { code: 'RATE_LIMITED' });
});

test('多次成功路由：auditSeq 单调递增，entries 只追加，verify 始终通过', () => {
  const system = buildSystem();
  const first = system.gateway.route(system.routeArgs({ skillId: 'read' }));
  const second = system.gateway.route(system.routeArgs({ skillId: 'read', content: '普通内容' }));
  assert.equal(second.auditSeq, first.auditSeq + 1);
  const entries = system.audit.entries();
  assert.equal(entries.length, 2);
  assert.deepEqual(entries.map((entry) => entry.seq), [1, 2]);
  assert.ok(Object.isFrozen(entries));
  assert.throws(() => entries.push({}), TypeError);
  assert.equal(system.audit.verify().ok, true);
});

test('events：只追加、可 limit、冻结；GatewayError 是 Error 子类', () => {
  const system = buildSystem();
  system.gateway.route(system.routeArgs({ skillId: 'read' }));
  const all = system.gateway.events();
  const limited = system.gateway.events({ limit: 1 });
  assert.equal(limited.length, 1);
  assert.ok(Object.isFrozen(limited));
  assert.deepEqual(limited[0], all.at(-1));

  try {
    system.gateway.authenticate({ signedCard: null, token: null, protocol: PROTOCOL.AGENT_WS });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof GatewayError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'UNAUTHENTICATED');
  }
});
