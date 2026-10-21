// 测试：读侧可见域（契约 §13）。覆盖 SCOPE_EXPIRED / DOMAIN_NOT_GRANTED / TRUST_CEILING_EXCEEDED、STRANGER 裁剪、MEMORY 域永不回原文。
import test from 'node:test';
import assert from 'node:assert/strict';

import { DOMAIN, VISIBILITY_CODE, VisibilityError, VisibilityGate } from '../src/visibility.mjs';
import { TRUST_LEVELS, TrustMatrix } from '../../orchestrator/src/index.mjs';
import { makeClock } from './_helpers.mjs';

function makeGate(clock = makeClock()) {
  const trustMatrix = new TrustMatrix({ clock });
  const gate = new VisibilityGate({ trustMatrix, clock });
  return { clock, trustMatrix, gate };
}

const scopeFor = (gate, overrides = {}) =>
  gate.buildScope({
    principalUserId: 'user-a',
    agentId: 'agent-a',
    domains: [DOMAIN.CONTACT, DOMAIN.GROUP, DOMAIN.TASK, DOMAIN.SCHEDULE, DOMAIN.MEMORY],
    trustCeiling: 4,
    ...overrides
  });

test('DOMAIN / VISIBILITY_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...DOMAIN }, {
    CONTACT: 'contact',
    GROUP: 'group',
    TASK: 'task',
    SCHEDULE: 'schedule',
    MEMORY: 'memory'
  });
  assert.deepEqual({ ...VISIBILITY_CODE }, {
    VISIBILITY_DENIED: 'VISIBILITY_DENIED',
    DOMAIN_NOT_GRANTED: 'DOMAIN_NOT_GRANTED',
    TRUST_CEILING_EXCEEDED: 'TRUST_CEILING_EXCEEDED',
    SCOPE_EXPIRED: 'SCOPE_EXPIRED',
    RESOURCE_NOT_FOUND: 'RESOURCE_NOT_FOUND'
  });
  assert.ok(Object.isFrozen(DOMAIN));
  assert.ok(Object.isFrozen(VISIBILITY_CODE));
});

test('buildScope：冻结对象、domains 去重、TTL 来自注入 clock', () => {
  const { gate, clock } = makeGate();
  const scope = gate.buildScope({
    principalUserId: 'user-a',
    agentId: 'agent-a',
    domains: ['contact', 'contact', 'task', ''],
    trustCeiling: 2,
    ttlMs: 1000
  });
  assert.ok(Object.isFrozen(scope));
  assert.deepEqual([...scope.domains], ['contact', 'task']);
  assert.equal(scope.trustCeiling, 2);
  assert.equal(scope.issuedAt, '2026-10-21T00:00:00.000Z');
  assert.equal(scope.expiresAt, '2026-10-21T00:00:01.000Z');
  assert.equal(clock.iso(), '2026-10-21T00:00:00.000Z');
  assert.throws(() => gate.buildScope({ agentId: 'agent-a' }), { code: 'VISIBILITY_DENIED' });
  assert.throws(() => gate.buildScope({ principalUserId: 'user-a', agentId: 'agent-a', ttlMs: -1 }), {
    code: 'VISIBILITY_DENIED'
  });
});

test('ALLOW：SELF/FAMILY 全字段；TRUSTED 按表允许业务字段', () => {
  const { gate } = makeGate();
  const scope = scopeFor(gate);
  const self = gate.evaluate({ scope, resource: { domain: DOMAIN.CONTACT, trustLevel: TRUST_LEVELS.SELF } });
  assert.equal(self.decision, 'ALLOW');
  assert.equal(self.code, null);
  assert.deepEqual([...self.allowedFields], ['*']);

  const familyProjected = gate.project({
    scope,
    resource: { domain: DOMAIN.CONTACT, trustLevel: TRUST_LEVELS.FAMILY },
    fields: { id: 'u1', displayName: '妈妈', content: '私密' }
  });
  assert.equal(familyProjected.decision, 'ALLOW');
  assert.deepEqual({ ...familyProjected.projected }, { id: 'u1', displayName: '妈妈', content: '私密' });
  assert.deepEqual([...familyProjected.redactedFields], []);

  const trusted = gate.project({
    scope,
    resource: { domain: DOMAIN.CONTACT, trustLevel: TRUST_LEVELS.TRUSTED },
    fields: { id: 'u2', displayName: '朋友', tags: ['x'], detail: {}, endpoint: 'wss://x', secret: '凭据' }
  });
  assert.equal(trusted.decision, 'REDACT');
  assert.deepEqual(Object.keys(trusted.projected).sort(), ['detail', 'displayName', 'endpoint', 'id', 'tags']);
  assert.deepEqual([...trusted.redactedFields], ['secret']);
  assert.deepEqual([...trusted.allowedFields], ['id', 'displayName', 'tags', 'detail', 'endpoint']);
});

test('边界：scope 过期 => DENY/SCOPE_EXPIRED（expiresAt === now 即过期）', () => {
  const { gate, clock } = makeGate();
  const scope = gate.buildScope({
    principalUserId: 'user-a',
    agentId: 'agent-a',
    domains: [DOMAIN.CONTACT],
    ttlMs: 1000
  });
  const resource = { domain: DOMAIN.CONTACT, trustLevel: 1 };
  clock.advance(999);
  assert.equal(gate.evaluate({ scope, resource }).decision, 'ALLOW');
  clock.advance(1);
  const expired = gate.evaluate({ scope, resource });
  assert.equal(expired.decision, 'DENY');
  assert.equal(expired.code, 'SCOPE_EXPIRED');
  const projected = gate.project({ scope, resource, fields: { id: 'u1' } });
  assert.equal(projected.decision, 'DENY');
  assert.deepEqual({ ...projected.projected }, {});
});

test('DENY：DOMAIN_NOT_GRANTED（域不在 scope.domains）', () => {
  const { gate } = makeGate();
  const scope = gate.buildScope({
    principalUserId: 'user-a',
    agentId: 'agent-a',
    domains: [DOMAIN.CONTACT]
  });
  const denied = gate.evaluate({ scope, resource: { domain: DOMAIN.GROUP, trustLevel: 1 } });
  assert.equal(denied.decision, 'DENY');
  assert.equal(denied.code, 'DOMAIN_NOT_GRANTED');
  assert.equal(denied.domain, 'group');
});

test('DENY：TRUST_CEILING_EXCEEDED（resource.trustLevel > scope.trustCeiling）', () => {
  const { gate } = makeGate();
  const scope = gate.buildScope({
    principalUserId: 'user-a',
    agentId: 'agent-a',
    domains: [DOMAIN.CONTACT],
    trustCeiling: 2
  });
  const denied = gate.evaluate({ scope, resource: { domain: DOMAIN.CONTACT, trustLevel: TRUST_LEVELS.KNOWN } });
  assert.equal(denied.decision, 'DENY');
  assert.equal(denied.code, 'TRUST_CEILING_EXCEEDED');
  const allowed = gate.evaluate({ scope, resource: { domain: DOMAIN.CONTACT, trustLevel: TRUST_LEVELS.TRUSTED } });
  assert.equal(allowed.decision, 'ALLOW');
});

test('STRANGER 档：project 后只剩 id/exists，decision=REDACT', () => {
  const { gate } = makeGate();
  const scope = scopeFor(gate);
  const projected = gate.project({
    scope,
    resource: { domain: DOMAIN.CONTACT, trustLevel: TRUST_LEVELS.STRANGER },
    fields: { id: 'u9', displayName: '陌生人', content: '机密内容', exists: true }
  });
  assert.equal(projected.decision, 'REDACT');
  assert.deepEqual({ ...projected.projected }, { id: 'u9', exists: true });
  assert.deepEqual([...projected.redactedFields].sort(), ['content', 'displayName']);
  assert.deepEqual([...projected.allowedFields], ['id', 'exists']);
});

test('记忆域（MEMORY）任何档只回 agentId/lastAt/count/summary，永不回原始内容', () => {
  const { gate } = makeGate();
  const fields = {
    agentId: 'agent-sub',
    lastAt: '2026-10-21T00:00:00.000Z',
    count: 2,
    summary: '2 条记忆',
    content: 'SECRET-RAW-MEMORY',
    tags: ['x']
  };
  for (const trustLevel of [TRUST_LEVELS.SELF, TRUST_LEVELS.FAMILY, TRUST_LEVELS.TRUSTED, TRUST_LEVELS.KNOWN, TRUST_LEVELS.STRANGER]) {
    const scope = scopeFor(gate, { trustCeiling: 4 });
    const projected = gate.project({
      scope,
      resource: { domain: DOMAIN.MEMORY, trustLevel },
      fields
    });
    assert.deepEqual(Object.keys(projected.projected).sort(), ['agentId', 'count', 'lastAt', 'summary']);
    assert.deepEqual([...projected.allowedFields], ['agentId', 'lastAt', 'count', 'summary']);
    assert.equal(projected.decision, 'REDACT');
    assert.ok(projected.redactedFields.includes('content'));
    assert.equal(JSON.stringify(projected.projected).includes('SECRET-RAW-MEMORY'), false);
  }
});

test('KNOWN 档：只允许 id/displayName/tags', () => {
  const { gate } = makeGate();
  const scope = scopeFor(gate);
  const projected = gate.project({
    scope,
    resource: { domain: DOMAIN.CONTACT, trustLevel: TRUST_LEVELS.KNOWN },
    fields: { id: 'u3', displayName: '点头之交', tags: ['a'], detail: '业务细节', endpoint: 'wss://x' }
  });
  assert.equal(projected.decision, 'REDACT');
  assert.deepEqual(Object.keys(projected.projected).sort(), ['displayName', 'id', 'tags']);
  assert.deepEqual([...projected.redactedFields].sort(), ['detail', 'endpoint']);
});

test('VISIBILITY_DENIED：scope 缺失/结构非法；RESOURCE_NOT_FOUND：资源缺失', () => {
  const { gate } = makeGate();
  for (const scope of [null, undefined, {}, { principalUserId: 'user-a' }, { agentId: 'agent-a' }]) {
    const denied = gate.evaluate({ scope, resource: { domain: DOMAIN.CONTACT, trustLevel: 1 } });
    assert.equal(denied.decision, 'DENY');
    assert.equal(denied.code, 'VISIBILITY_DENIED');
  }
  const scope = scopeFor(gate);
  for (const resource of [null, undefined, {}, { trustLevel: 1 }]) {
    const denied = gate.evaluate({ scope, resource });
    assert.equal(denied.decision, 'DENY');
    assert.equal(denied.code, 'RESOURCE_NOT_FOUND');
  }
});

test('evaluate 带 fields / resource.fields 时按裁剪结果给出 REDACT 与 redactedFields', () => {
  const { gate } = makeGate();
  const scope = scopeFor(gate);
  const viaFields = gate.evaluate({
    scope,
    resource: { domain: DOMAIN.CONTACT, trustLevel: TRUST_LEVELS.KNOWN },
    fields: { id: 'u1', displayName: 'x', detail: 'secret' }
  });
  assert.equal(viaFields.decision, 'REDACT');
  assert.deepEqual([...viaFields.redactedFields], ['detail']);

  const viaResourceFields = gate.evaluate({
    scope,
    resource: {
      domain: DOMAIN.CONTACT,
      trustLevel: TRUST_LEVELS.STRANGER,
      fields: ['id', 'displayName']
    }
  });
  assert.equal(viaResourceFields.decision, 'REDACT');
  assert.deepEqual([...viaResourceFields.redactedFields], ['displayName']);
});

test('trustMatrix 兜底：resource 未给 trustLevel 时用 levelOf(principal, resource.id) 计算', () => {
  const { gate, trustMatrix } = makeGate();
  trustMatrix.grant({ userId: 'user-a', targetId: 'user-x', level: TRUST_LEVELS.KNOWN });
  const scope = gate.buildScope({
    principalUserId: 'user-a',
    agentId: 'agent-a',
    domains: [DOMAIN.CONTACT],
    trustCeiling: 2
  });
  const denied = gate.evaluate({ scope, resource: { domain: DOMAIN.CONTACT, id: 'user-x' } });
  assert.equal(denied.code, 'TRUST_CEILING_EXCEEDED');

  const high = gate.buildScope({
    principalUserId: 'user-a',
    agentId: 'agent-a',
    domains: [DOMAIN.CONTACT],
    trustCeiling: 4
  });
  assert.equal(gate.evaluate({ scope: high, resource: { domain: DOMAIN.CONTACT, id: 'user-x' } }).decision, 'ALLOW');
});

test('返回对象与投影结果冻结', () => {
  const { gate } = makeGate();
  const scope = scopeFor(gate);
  const evaluation = gate.evaluate({ scope, resource: { domain: DOMAIN.CONTACT, trustLevel: 1 } });
  const projected = gate.project({ scope, resource: { domain: DOMAIN.CONTACT, trustLevel: 4 }, fields: { id: 'u1', x: 1 } });
  assert.ok(Object.isFrozen(evaluation));
  assert.ok(Object.isFrozen(evaluation.allowedFields));
  assert.ok(Object.isFrozen(projected));
  assert.ok(Object.isFrozen(projected.projected));
  assert.ok(Object.isFrozen(projected.redactedFields));
});

test('VisibilityError 带 code 且是 Error 子类', () => {
  const { gate } = makeGate();
  try {
    gate.buildScope({ principalUserId: '', agentId: 'agent-a' });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof VisibilityError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'VISIBILITY_DENIED');
  }
});
