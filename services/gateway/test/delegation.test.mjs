// 测试：带衰减的委托（契约 §30）。
// 覆盖四条衰减规则（域名/信任上限/时限/跳数）各自独立用例、撤销级联与 cascade=false 显式选择、effectiveScope 取交集、verify 只返回判定。
import test from 'node:test';
import assert from 'node:assert/strict';

import { DELEGATION_CODE, DelegationError, DelegationService } from '../src/delegation.mjs';

const USER = 'user-lin';
const BUTLER = 'agent-lin-butler';
const CHILD = 'agent-schedule';
const GRANDCHILD = 'agent-calendar';

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.currentMs = () => currentMs;
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

/** 模拟 AgentGateway.issueToken 返回的根 token（注意它的标识字段叫 token 而不是 id） */
function makeRootToken(clock, overrides = {}) {
  return {
    token: 'tok-root',
    agentId: BUTLER,
    principalUserId: USER,
    domains: ['task', 'schedule', 'contact'],
    trustCeiling: 2,
    issuedAt: clock.iso(),
    expiresAt: new Date(clock.currentMs() + 60 * 60e3).toISOString(),
    ...overrides
  };
}

const makeService = (clock = makeClock()) => ({ clock, service: new DelegationService({ clock }) });

test('DELEGATION_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...DELEGATION_CODE }, {
    DELEGATION_DENIED: 'DELEGATION_DENIED',
    SCOPE_ESCALATION: 'SCOPE_ESCALATION',
    EXPIRY_EXCEEDED: 'EXPIRY_EXCEEDED',
    HOP_LIMIT: 'HOP_LIMIT',
    PARENT_REVOKED: 'PARENT_REVOKED',
    TOKEN_INVALID: 'TOKEN_INVALID'
  });
  assert.ok(Object.isFrozen(DELEGATION_CODE));
});

test('正常路径：父 token 为 AgentGateway.issueToken 返回对象，子 token 冻结且父 id 正确', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock);
  const token = service.delegate({
    parentToken: root,
    childAgentId: CHILD,
    principalUserId: USER,
    domains: ['task', 'schedule'],
    trustCeiling: 1,
    ttlMs: 10 * 60e3,
    hopLimit: 1
  });
  assert.ok(Object.isFrozen(token));
  assert.match(token.id, /^del-/);
  assert.equal(token.agentId, CHILD);
  assert.equal(token.principalUserId, USER);
  assert.deepEqual([...token.domains], ['task', 'schedule']);
  assert.equal(token.trustCeiling, 1);
  assert.equal(token.issuedAt, clock.iso());
  assert.equal(token.expiresAt, '2026-10-21T00:10:00.000Z');
  assert.equal(token.parentId, 'tok-root'); // 直接接受网关 token 的 token 字段
  assert.equal(token.hopLimit, 0); // hopLimit=1 => 子 token 只剩 0 跳（叶子）

  const verdict = service.verify({ token, agentId: CHILD });
  assert.equal(verdict.ok, true);
  assert.equal(verdict.code, null);
  assert.deepEqual([...verdict.effectiveScope.domains], ['task', 'schedule']);
  assert.equal(verdict.effectiveScope.trustCeiling, 1);
  assert.equal(verdict.chain.length, 1);
  assert.equal(verdict.chain[0].id, token.id);

  // 也支持用 tokenId 字符串验证
  assert.equal(service.verify({ token: token.id, agentId: CHILD }).ok, true);
});

test('衰减规则①：域名不得扩张（child.domains ⊆ parent.domains）', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock, { domains: ['task'] });
  const base = { parentToken: root, childAgentId: CHILD, principalUserId: USER, ttlMs: 60e3, hopLimit: 1 };
  assert.throws(() => service.delegate({ ...base, domains: ['task', 'schedule'] }), {
    code: 'SCOPE_ESCALATION'
  });
  assert.throws(() => service.delegate({ ...base, domains: ['schedule'] }), { code: 'SCOPE_ESCALATION' });
  assert.doesNotThrow(() => service.delegate({ ...base, domains: ['task'] }));
  assert.doesNotThrow(() => service.delegate({ ...base, domains: [] })); // 空集也是合法收窄
});

test('衰减规则②：信任上限不得放宽（child.trustCeiling <= parent.trustCeiling）', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock, { trustCeiling: 2 });
  const base = { parentToken: root, childAgentId: CHILD, principalUserId: USER, domains: ['task'], ttlMs: 60e3, hopLimit: 1 };
  // 3 比 2 更宽松 => 拒绝
  assert.throws(() => service.delegate({ ...base, trustCeiling: 3 }), { code: 'SCOPE_ESCALATION' });
  // 1 比 2 更严格 => 通过
  const stricter = service.delegate({ ...base, trustCeiling: 1 });
  assert.equal(stricter.trustCeiling, 1);
  assert.equal(service.verify({ token: stricter, agentId: CHILD }).effectiveScope.trustCeiling, 1);
  // 等于父上限也允许
  assert.equal(service.delegate({ ...base, trustCeiling: 2 }).trustCeiling, 2);
});

test('衰减规则③：时限不得长于父（child.expiresAt <= parent.expiresAt）', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock); // 剩余 60 分钟
  const base = { parentToken: root, childAgentId: CHILD, principalUserId: USER, domains: ['task'], trustCeiling: 1, hopLimit: 1 };
  // 子 token 晚于父 => 拒绝
  assert.throws(() => service.delegate({ ...base, ttlMs: 60 * 60e3 + 1 }), { code: 'EXPIRY_EXCEEDED' });
  // 等于父的到期时刻 => 允许
  const equal = service.delegate({ ...base, ttlMs: 60 * 60e3 });
  assert.equal(equal.expiresAt, root.expiresAt);
  // 更短 => 允许
  assert.equal(service.delegate({ ...base, ttlMs: 30 * 60e3 }).expiresAt, '2026-10-21T00:30:00.000Z');
  // 不传 ttlMs => 继承父的剩余时限（不会延长）
  assert.equal(service.delegate({ ...base, ttlMs: undefined }).expiresAt, root.expiresAt);
});

test('衰减规则④：跳数受限（每层 -1；hopLimit=1 的父不能再委托；hopLimit=2 再一层后孙为 0）', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock);
  const base = { parentToken: root, childAgentId: CHILD, principalUserId: USER, domains: ['task'], trustCeiling: 1, ttlMs: 60e3 };

  // hopLimit=1 => 子 token hopLimit=0，只能验证不能继续委托
  const leaf = service.delegate({ ...base, hopLimit: 1 });
  assert.equal(leaf.hopLimit, 0);
  assert.throws(
    () =>
      service.delegate({
        parentToken: leaf,
        childAgentId: GRANDCHILD,
        principalUserId: USER,
        domains: ['task'],
        trustCeiling: 1,
        ttlMs: 60e3,
        hopLimit: 1
      }),
    { code: 'HOP_LIMIT' }
  );

  // hopLimit=2 => 子 token hopLimit=1（可再委托一层），孙 token hopLimit=0（不可再委托）
  const hop2 = service.delegate({ ...base, hopLimit: 2 });
  assert.equal(hop2.hopLimit, 1);
  const grandchild = service.delegate({
    parentToken: hop2,
    childAgentId: GRANDCHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 1,
    ttlMs: 60e3,
    hopLimit: 1
  });
  assert.equal(grandchild.hopLimit, 0);
  assert.throws(
    () =>
      service.delegate({
        parentToken: grandchild,
        childAgentId: 'agent-x',
        principalUserId: USER,
        domains: ['task'],
        trustCeiling: 1,
        ttlMs: 60e3,
        hopLimit: 1
      }),
    { code: 'HOP_LIMIT' }
  );

  // hopLimit 参数必须 >=1
  assert.throws(() => service.delegate({ ...base, hopLimit: 0 }), { code: 'HOP_LIMIT' });
  assert.throws(() => service.delegate({ ...base, hopLimit: -1 }), { code: 'HOP_LIMIT' });
});

test('撤销级联：撤销父 => 后代 verify 返回 PARENT_REVOKED；delegate 也不能从已撤销父继续', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock);
  const child = service.delegate({
    parentToken: root,
    childAgentId: CHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 1,
    ttlMs: 30 * 60e3,
    hopLimit: 2
  });
  const grandchild = service.delegate({
    parentToken: child,
    childAgentId: GRANDCHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 1,
    ttlMs: 10 * 60e3,
    hopLimit: 1
  });
  assert.equal(service.verify({ token: grandchild, agentId: GRANDCHILD }).ok, true);

  const result = service.revoke({ tokenId: child.id }); // cascade 默认 true
  assert.deepEqual([...result.revoked], [child.id, grandchild.id]);

  const childVerdict = service.verify({ token: child, agentId: CHILD });
  assert.equal(childVerdict.ok, false);
  assert.equal(childVerdict.code, 'TOKEN_INVALID'); // 直接撤销自身
  const grandchildVerdict = service.verify({ token: grandchild, agentId: GRANDCHILD });
  assert.equal(grandchildVerdict.ok, false);
  assert.equal(grandchildVerdict.code, 'PARENT_REVOKED'); // 父级撤销级联

  // 已撤销父 token 不能再被委托
  assert.throws(
    () =>
      service.delegate({
        parentToken: child,
        childAgentId: 'agent-x',
        principalUserId: USER,
        domains: ['task'],
        trustCeiling: 1,
        ttlMs: 60e3,
        hopLimit: 1
      }),
    { code: 'PARENT_REVOKED' }
  );
});

test('cascade=false 是显式选择：撤销父后子 token 仍可用（不做级联）', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock);
  const child = service.delegate({
    parentToken: root,
    childAgentId: CHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 1,
    ttlMs: 30 * 60e3,
    hopLimit: 2
  });
  const grandchild = service.delegate({
    parentToken: child,
    childAgentId: GRANDCHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 1,
    ttlMs: 10 * 60e3,
    hopLimit: 1
  });

  const result = service.revoke({ tokenId: child.id, cascade: false });
  assert.deepEqual([...result.revoked], [child.id]);
  assert.equal(service.verify({ token: child, agentId: CHILD }).code, 'TOKEN_INVALID');
  // 显式 cascade=false：后代继续可用（这是治理选择，不是默认行为）
  const grandchildVerdict = service.verify({ token: grandchild, agentId: GRANDCHILD });
  assert.equal(grandchildVerdict.ok, true);
  assert.equal(grandchildVerdict.code, null);
});

test('effectiveScope 是整条链的交集（父顺序保留，不是子集的简单拷贝）', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock, { domains: ['task', 'schedule'], trustCeiling: 2 });
  // 子 token 的 domains 与父等集但顺序不同：交集应保留父的顺序
  const child = service.delegate({
    parentToken: root,
    childAgentId: CHILD,
    principalUserId: USER,
    domains: ['schedule', 'task'],
    trustCeiling: 1,
    ttlMs: 30 * 60e3,
    hopLimit: 3
  });
  const grandchild = service.delegate({
    parentToken: child,
    childAgentId: GRANDCHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 0,
    ttlMs: 10 * 60e3,
    hopLimit: 2
  });

  const verdict = service.verify({ token: grandchild, agentId: GRANDCHILD });
  assert.equal(verdict.ok, true);
  assert.deepEqual([...verdict.effectiveScope.domains], ['task']);
  assert.equal(verdict.effectiveScope.trustCeiling, 0);
  assert.equal(verdict.effectiveScope.expiresAt, grandchild.expiresAt);
  assert.equal(verdict.effectiveScope.hopLimit, grandchild.hopLimit);
  assert.ok(Object.isFrozen(verdict.effectiveScope));
  assert.ok(Object.isFrozen(verdict.effectiveScope.domains));
  assert.deepEqual(verdict.chain.map((token) => token.agentId), [CHILD, GRANDCHILD]);

  // 第一级：交集保留父的 domains 顺序（父 ['task','schedule'] ∩ 子 ['schedule','task']）
  const childVerdict = service.verify({ token: child, agentId: CHILD });
  assert.deepEqual([...childVerdict.effectiveScope.domains], ['task', 'schedule']);
});

test('verify：只返回判定不抛异常；未知/过期/持有者不符 => TOKEN_INVALID', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock);
  const token = service.delegate({
    parentToken: root,
    childAgentId: CHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 1,
    ttlMs: 60e3,
    hopLimit: 1
  });

  assert.doesNotThrow(() => service.verify({ token: 'del-unknown' }));
  assert.deepEqual(
    { ok: service.verify({ token: 'del-unknown' }).ok, code: service.verify({ token: 'del-unknown' }).code },
    { ok: false, code: 'TOKEN_INVALID' }
  );
  assert.equal(service.verify({ token, agentId: 'agent-other' }).code, 'TOKEN_INVALID');
  assert.equal(service.verify({ token: {} }).code, 'TOKEN_INVALID');
  assert.equal(service.verify({}).code, 'TOKEN_INVALID');

  clock.advance(60e3 + 1);
  const expired = service.verify({ token, agentId: CHILD });
  assert.equal(expired.ok, false);
  assert.equal(expired.code, 'TOKEN_INVALID');
  assert.ok(Object.isFrozen(expired));
});

test('delegate 入参校验：父 token 非法/过期 => TOKEN_INVALID；主体与域参数非法 => DELEGATION_DENIED', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock);
  const base = { childAgentId: CHILD, principalUserId: USER, domains: ['task'], trustCeiling: 1, ttlMs: 60e3, hopLimit: 1 };

  assert.throws(() => service.delegate({ ...base, parentToken: null }), { code: 'TOKEN_INVALID' });
  assert.throws(() => service.delegate({ ...base, parentToken: { token: 'tok-x' } }), { code: 'TOKEN_INVALID' });
  assert.throws(
    () => service.delegate({ ...base, parentToken: { ...root, domains: 'task' } }),
    { code: 'TOKEN_INVALID' }
  );
  assert.throws(() => service.delegate({ ...base, parentToken: root, childAgentId: '' }), {
    code: 'DELEGATION_DENIED'
  });
  assert.throws(() => service.delegate({ ...base, parentToken: root, principalUserId: 'user-other' }), {
    code: 'DELEGATION_DENIED'
  });
  assert.throws(() => service.delegate({ ...base, parentToken: root, trustCeiling: 3.5 }), {
    code: 'DELEGATION_DENIED'
  });
  assert.throws(() => service.delegate({ ...base, parentToken: root, domains: 'task' }), {
    code: 'DELEGATION_DENIED'
  });
  assert.throws(() => service.delegate({ ...base, parentToken: root, ttlMs: -1 }), {
    code: 'DELEGATION_DENIED'
  });

  // 父 token 过期后不能再委托
  const expiredRoot = { ...root, expiresAt: new Date(clock.currentMs() - 1).toISOString() };
  assert.throws(() => service.delegate({ ...base, parentToken: expiredRoot }), { code: 'TOKEN_INVALID' });
});

test('chainOf：根在前的委托链；未知 token 返回空数组；冻结', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock);
  const child = service.delegate({
    parentToken: root,
    childAgentId: CHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 1,
    ttlMs: 30 * 60e3,
    hopLimit: 3
  });
  const grandchild = service.delegate({
    parentToken: child,
    childAgentId: GRANDCHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 1,
    ttlMs: 10 * 60e3,
    hopLimit: 2
  });

  assert.deepEqual(service.chainOf(child.id).map((token) => token.agentId), [CHILD]);
  assert.deepEqual(service.chainOf(grandchild.id).map((token) => token.agentId), [CHILD, GRANDCHILD]);
  assert.ok(Object.isFrozen(service.chainOf(grandchild.id)));
  assert.deepEqual([...service.chainOf('del-unknown')], []);
});

test('撤销与重复调用：未知 token 返回空列表；重复撤销幂等返回同一批', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock);
  const child = service.delegate({
    parentToken: root,
    childAgentId: CHILD,
    principalUserId: USER,
    domains: ['task'],
    trustCeiling: 1,
    ttlMs: 60e3,
    hopLimit: 1
  });
  assert.deepEqual([...service.revoke({ tokenId: 'del-unknown' }).revoked], []);
  assert.deepEqual([...service.revoke({ tokenId: child.id }).revoked], [child.id]);
  assert.deepEqual([...service.revoke({ tokenId: child.id }).revoked], [child.id]);
  assert.throws(() => service.revoke({ tokenId: '' }), TypeError);
});

test('DelegationError 带 code 且是 Error 子类', () => {
  const { clock, service } = makeService();
  const root = makeRootToken(clock, { domains: ['task'] });
  try {
    service.delegate({
      parentToken: root,
      childAgentId: CHILD,
      principalUserId: USER,
      domains: ['schedule'],
      trustCeiling: 1,
      ttlMs: 60e3,
      hopLimit: 1
    });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof DelegationError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'SCOPE_ESCALATION');
  }
});
