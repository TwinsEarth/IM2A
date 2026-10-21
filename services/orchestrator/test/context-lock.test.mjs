// 测试：确认锁定的 Context（契约 §34）。
// 覆盖：Agent 只能 propose、属主 confirm 才 LOCKED、LOCKED 值不可覆写（只上抛 CONFLICT）、unlock 必须 reason 且留审计、全部错误码。
import test from 'node:test';
import assert from 'node:assert/strict';

import { CONTEXT_LOCK_CODE, ContextLock, ContextLockError, LOCK_STATE } from '../src/context-lock.mjs';

const OWNER = 'user-lin';
const AGENT = 'agent-lin-butler';
const OTHER_AGENT = 'agent-qiu-butler';

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

const makeLock = (options = {}) => {
  const clock = options.clock ?? makeClock();
  const events = [];
  const lock = new ContextLock({ ownerUserId: OWNER, clock, onAudit: (event) => events.push(event) });
  return { clock, lock, events };
};

test('LOCK_STATE / CONTEXT_LOCK_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...LOCK_STATE }, { PENDING: 'PENDING', LOCKED: 'LOCKED', CONFLICT: 'CONFLICT' });
  assert.deepEqual({ ...CONTEXT_LOCK_CODE }, {
    NOT_FOUND: 'NOT_FOUND',
    ILLEGAL_STATE: 'ILLEGAL_STATE',
    LOCK_IMMUTABLE: 'LOCK_IMMUTABLE',
    CONFLICT_PENDING: 'CONFLICT_PENDING',
    FORBIDDEN: 'FORBIDDEN',
    INVALID_VALUE: 'INVALID_VALUE'
  });
  assert.ok(Object.isFrozen(LOCK_STATE));
  assert.ok(Object.isFrozen(CONTEXT_LOCK_CODE));
});

test('propose：未存在的 key → PENDING；read 返回候选值；重复相同提议幂等', () => {
  const { lock } = makeLock();
  const proposed = lock.propose({
    key: 'relationship.status',
    value: '与 lin 是家人',
    sourceAgentId: AGENT,
    evidence: ['chat-1']
  });
  assert.equal(proposed.state, LOCK_STATE.PENDING);
  assert.ok(Object.isFrozen(proposed));

  const read = lock.read({ key: 'relationship.status' });
  assert.equal(read.state, LOCK_STATE.PENDING);
  assert.equal(read.value, '与 lin 是家人');
  assert.equal(read.lockedBy, null);
  assert.equal(read.lockedAt, null);
  assert.equal(read.candidate, null);
  assert.equal(read.sourceAgentId, AGENT);
  assert.deepEqual([...read.evidence], ['chat-1']);
  assert.ok(Object.isFrozen(read.evidence));

  lock.propose({ key: 'relationship.status', value: '与 lin 是家人', sourceAgentId: OTHER_AGENT });
  assert.deepEqual({ ...lock.stats() }, { pending: 1, locked: 0, conflicts: 0 });

  // 锁前最后一次提议生效
  lock.propose({ key: 'relationship.status', value: '与 lin 是大学同学', sourceAgentId: OTHER_AGENT });
  assert.equal(lock.read({ key: 'relationship.status' }).value, '与 lin 是大学同学');
  assert.equal(lock.stats().pending, 1);
});

test('confirm：只有属主能确认 → LOCKED；非属主（含提议 Agent）一律 FORBIDDEN', () => {
  const { lock } = makeLock();
  lock.propose({ key: 'k', value: 'v1', sourceAgentId: AGENT });

  // 提议的 Agent 不是属主，不能自己确认
  assert.throws(() => lock.confirm({ key: 'k', byUserId: AGENT }), { code: 'FORBIDDEN' });
  assert.throws(() => lock.confirm({ key: 'k', byUserId: 'user-other' }), { code: 'FORBIDDEN' });
  assert.equal(lock.read({ key: 'k' }).state, LOCK_STATE.PENDING);

  const locked = lock.confirm({ key: 'k', byUserId: OWNER });
  assert.equal(locked.state, LOCK_STATE.LOCKED);
  assert.equal(locked.lockedBy, OWNER);
  assert.ok(typeof locked.lockedAt === 'string');
  assert.deepEqual({ ...lock.stats() }, { pending: 0, locked: 1, conflicts: 0 });

  // 已 LOCKED 不可再次确认/覆写
  assert.throws(() => lock.confirm({ key: 'k', byUserId: OWNER }), { code: 'LOCK_IMMUTABLE' });
});

test('LOCKED 后 propose 相同值幂等；propose 不同值 → CONFLICT 且原锁定值不变', () => {
  const { lock } = makeLock();
  lock.propose({ key: 'k', value: 'v1', sourceAgentId: AGENT });
  lock.confirm({ key: 'k', byUserId: OWNER });

  const idempotent = lock.propose({ key: 'k', value: 'v1', sourceAgentId: OTHER_AGENT });
  assert.equal(idempotent.state, LOCK_STATE.LOCKED);
  assert.deepEqual({ ...lock.stats() }, { pending: 0, locked: 1, conflicts: 0 });

  const conflict = lock.propose({ key: 'k', value: 'v2-来自子 Agent', sourceAgentId: OTHER_AGENT, evidence: ['e1'] });
  assert.equal(conflict.state, LOCK_STATE.CONFLICT);
  assert.equal(conflict.value, 'v1'); // 原锁定值不变
  assert.equal(conflict.candidate.value, 'v2-来自子 Agent');
  assert.equal(conflict.candidate.sourceAgentId, OTHER_AGENT);
  assert.ok(Object.isFrozen(conflict.candidate));

  const read = lock.read({ key: 'k' });
  assert.equal(read.state, LOCK_STATE.CONFLICT);
  assert.equal(read.value, 'v1'); // 读到的仍是锁定值
  assert.equal(read.candidate.value, 'v2-来自子 Agent');
  assert.deepEqual({ ...lock.stats() }, { pending: 0, locked: 0, conflicts: 1 });
});

test('CONFLICT 只能由属主 unlock 解决；冲突未决时 confirm => CONFLICT_PENDING', () => {
  const { lock } = makeLock();
  lock.propose({ key: 'k', value: 'v1', sourceAgentId: AGENT });
  lock.confirm({ key: 'k', byUserId: OWNER });
  lock.propose({ key: 'k', value: 'v2', sourceAgentId: OTHER_AGENT });

  assert.throws(() => lock.confirm({ key: 'k', byUserId: OWNER }), { code: 'CONFLICT_PENDING' });
  // 冲突期间再提第三个值：只替换 candidate，锁定值依旧不变
  const updated = lock.propose({ key: 'k', value: 'v3', sourceAgentId: AGENT });
  assert.equal(updated.state, LOCK_STATE.CONFLICT);
  assert.equal(updated.value, 'v1');
  assert.equal(updated.candidate.value, 'v3');
});

test('reject 路径：属主 confirm({decision:"REJECT"}) => 回到未锁定，read => NOT_FOUND', () => {
  const { lock } = makeLock();
  lock.propose({ key: 'k', value: 'v1', sourceAgentId: AGENT });
  const rejected = lock.confirm({ key: 'k', byUserId: OWNER, decision: 'REJECT' });
  assert.equal(rejected.state, null);
  assert.equal(rejected.decision, 'REJECTED');
  assert.throws(() => lock.read({ key: 'k' }), { code: 'NOT_FOUND' });
  assert.deepEqual({ ...lock.stats() }, { pending: 0, locked: 0, conflicts: 0 });
  assert.equal(lock.history({ key: 'k' }).at(-1).decision, 'REJECTED');
});

test('边界：unlock 必须给 reason（否则拒绝），解锁后回到未锁定且留审计', () => {
  const { lock } = makeLock();
  lock.propose({ key: 'k', value: 'v1', sourceAgentId: AGENT });
  lock.confirm({ key: 'k', byUserId: OWNER });

  assert.throws(() => lock.unlock({ key: 'k', byUserId: OWNER }), { code: 'INVALID_VALUE' });
  assert.throws(() => lock.unlock({ key: 'k', byUserId: OWNER, reason: '   ' }), { code: 'INVALID_VALUE' });
  assert.equal(lock.read({ key: 'k' }).state, LOCK_STATE.LOCKED); // 失败不改状态

  const unlocked = lock.unlock({ key: 'k', byUserId: OWNER, reason: '用户改主意了' });
  assert.equal(unlocked.unlocked, true);
  assert.equal(unlocked.state, null);
  assert.throws(() => lock.read({ key: 'k' }), { code: 'NOT_FOUND' });
  assert.deepEqual({ ...lock.stats() }, { pending: 0, locked: 0, conflicts: 0 });

  const unlockEvent = lock.history({ key: 'k' }).find((event) => event.action === 'UNLOCK');
  assert.equal(unlockEvent.actor, OWNER);
  assert.equal(unlockEvent.decision, 'UNLOCKED');
  assert.equal(unlockEvent.detail.reason, '用户改主意了');
  assert.equal(unlockEvent.detail.previousState, LOCK_STATE.LOCKED);
});

test('unlock：非属主 FORBIDDEN；PENDING 状态不可解锁（ILLEGAL_STATE）', () => {
  const { lock } = makeLock();
  lock.propose({ key: 'k', value: 'v1', sourceAgentId: AGENT });
  assert.throws(() => lock.unlock({ key: 'k', byUserId: OWNER, reason: 'r' }), { code: 'ILLEGAL_STATE' });
  lock.confirm({ key: 'k', byUserId: OWNER });
  assert.throws(() => lock.unlock({ key: 'k', byUserId: AGENT, reason: 'r' }), { code: 'FORBIDDEN' });
  assert.equal(lock.read({ key: 'k' }).state, LOCK_STATE.LOCKED);
});

test('冲突的人工解决路径：unlock -> 重新 propose -> confirm 锁定新值', () => {
  const { lock } = makeLock();
  lock.propose({ key: 'k', value: 'v1', sourceAgentId: AGENT });
  lock.confirm({ key: 'k', byUserId: OWNER });
  lock.propose({ key: 'k', value: 'v2', sourceAgentId: OTHER_AGENT });
  lock.unlock({ key: 'k', byUserId: OWNER, reason: '确认 v2 更准确' });
  const pending = lock.propose({ key: 'k', value: 'v2', sourceAgentId: OTHER_AGENT });
  assert.equal(pending.state, LOCK_STATE.PENDING);
  const locked = lock.confirm({ key: 'k', byUserId: OWNER });
  assert.equal(locked.state, LOCK_STATE.LOCKED);
  assert.equal(locked.value, 'v2');
});

test('INVALID_VALUE：key 空 / value undefined / 缺 sourceAgentId / 非法确认动作', () => {
  const { lock } = makeLock();
  assert.throws(() => lock.propose({ key: '', value: 1, sourceAgentId: AGENT }), { code: 'INVALID_VALUE' });
  assert.throws(() => lock.propose({ key: 'k', value: undefined, sourceAgentId: AGENT }), {
    code: 'INVALID_VALUE'
  });
  assert.throws(() => lock.propose({ key: 'k', value: 1 }), { code: 'INVALID_VALUE' });
  lock.propose({ key: 'k', value: 1, sourceAgentId: AGENT });
  assert.throws(() => lock.confirm({ key: 'k', byUserId: OWNER, decision: 'MAYBE' }), {
    code: 'INVALID_VALUE'
  });
});

test('NOT_FOUND：未知 key 的 read/confirm/unlock', () => {
  const { lock } = makeLock();
  assert.throws(() => lock.read({ key: 'missing' }), { code: 'NOT_FOUND' });
  assert.throws(() => lock.confirm({ key: 'missing', byUserId: OWNER }), { code: 'NOT_FOUND' });
  assert.throws(() => lock.unlock({ key: 'missing', byUserId: OWNER, reason: 'r' }), {
    code: 'NOT_FOUND'
  });
});

test('history：只追加、可按 key 过滤、冻结；事件记录 Agent/属主身份', () => {
  const { lock, events } = makeLock();
  lock.propose({ key: 'k1', value: 'v1', sourceAgentId: AGENT });
  lock.confirm({ key: 'k1', byUserId: OWNER });
  lock.propose({ key: 'k1', value: 'v2', sourceAgentId: OTHER_AGENT });
  lock.unlock({ key: 'k1', byUserId: OWNER, reason: '重置' });
  lock.propose({ key: 'k2', value: 'x', sourceAgentId: AGENT });

  const trail = lock.history({ key: 'k1' });
  assert.deepEqual(
    trail.map((event) => `${event.actor}:${event.action}:${event.decision}`),
    [`${AGENT}:PROPOSE:PENDING`, `${OWNER}:CONFIRM:LOCKED`, `${OTHER_AGENT}:PROPOSE:CONFLICT`, `${OWNER}:UNLOCK:UNLOCKED`]
  );
  assert.ok(Object.isFrozen(trail));
  assert.ok(Object.isFrozen(trail[0]));
  assert.throws(() => trail.push({}), TypeError);
  assert.equal(lock.history({ key: 'k1' }).length, 4);
  assert.equal(lock.history().length, 5); // 全局含 k2
  assert.equal(events.length, 5); // onAudit 与 history 同源，便于接 SignedAuditLog
});

test('stats / 值等价判定（对象键序不同视为相同值）', () => {
  const { lock } = makeLock();
  lock.propose({ key: 'k1', value: { b: 2, a: 1 }, sourceAgentId: AGENT });
  lock.confirm({ key: 'k1', byUserId: OWNER });
  // 同一语义不同键序 => 幂等，不产生冲突
  const idempotent = lock.propose({ key: 'k1', value: { a: 1, b: 2 }, sourceAgentId: OTHER_AGENT });
  assert.equal(idempotent.state, LOCK_STATE.LOCKED);
  lock.propose({ key: 'k2', value: 'x', sourceAgentId: AGENT });
  lock.propose({ key: 'k3', value: 'y', sourceAgentId: AGENT });
  lock.confirm({ key: 'k3', byUserId: OWNER });
  lock.propose({ key: 'k3', value: 'z', sourceAgentId: OTHER_AGENT });
  assert.deepEqual({ ...lock.stats() }, { pending: 1, locked: 1, conflicts: 1 });
});

test('ContextLockError 带 code 且是 Error 子类', () => {
  const { lock } = makeLock();
  try {
    lock.read({ key: 'missing' });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof ContextLockError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'NOT_FOUND');
  }
});
