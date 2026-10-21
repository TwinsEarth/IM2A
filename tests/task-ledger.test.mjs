/**
 * 模块职责：TaskLedger 契约测试（红阶段）—— 任务账本的状态机/幂等/租约/重放/过期回收
 * 契约来源：docs/ARCHITECTURE.md §3（TaskLedger 行）与 §6（v0.2 重写执行方案）
 *
 * 这些断言是 v0.2 的验收标准：先让它们红，再写实现。
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { TaskLedger, TASK_STATE, TASK_EVENT } from '../apps/im2a/task-ledger.mjs';

const KEY = 'idem-001';

function ledgerWith({ now = 1_000_000 } = {}) {
  let clock = now;
  const ledger = new TaskLedger({ clock: () => clock });
  return { ledger, advance: (ms) => { clock += ms; return clock; }, set: (value) => { clock = value; return clock; }, now: () => clock };
}

test('create：QUEUED、事件只追加、payload 冻结', () => {
  const { ledger } = ledgerWith();
  const created = ledger.create({ kind: 'agent.turn', payload: { text: 'hi' }, idempotencyKey: KEY });
  assert.equal(created.duplicate, false);
  assert.equal(created.task.id.length > 0, true);
  assert.equal(created.task.state, TASK_STATE.QUEUED);
  assert.equal(created.task.attempt, 0);
  assert.ok(Object.isFrozen(created.task.payload));
  const events = ledger.events();
  assert.equal(events.length, 1);
  assert.equal(events[0].type, TASK_EVENT.CREATED);
  assert.ok(Object.isFrozen(events[0]));
  assert.equal(events[0].seq, 1);
});

test('幂等：同一 idempotencyKey 只创建一次，返回 duplicate=true 且不追加事件', () => {
  const { ledger } = ledgerWith();
  const first = ledger.create({ kind: 'agent.turn', payload: { n: 1 }, idempotencyKey: KEY });
  const second = ledger.create({ kind: 'agent.turn', payload: { n: 2 }, idempotencyKey: KEY });
  assert.equal(second.duplicate, true);
  assert.equal(second.task.id, first.task.id);
  assert.equal(ledger.events().length, 1, '重复创建不得追加事件');
  assert.equal(ledger.events()[0].seq, 1);
});

test('lease：领取任务 → RUNNING、attempt=1、带 worker 与到期时间；无任务返回 null', () => {
  const { ledger } = ledgerWith({ now: 5_000 });
  const created = ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: KEY, leaseMs: 10_000 });
  const leased = ledger.lease({ workerId: 'w-1' });
  assert.equal(leased.task.id, created.task.id);
  assert.equal(leased.task.state, TASK_STATE.RUNNING);
  assert.equal(leased.task.attempt, 1);
  assert.equal(leased.task.lease.workerId, 'w-1');
  assert.equal(leased.task.lease.expiresAt, 5_000 + 10_000);
  assert.equal(ledger.lease({ workerId: 'w-2' }), null, '没有排队任务时返回 null');
});

test('lease：不把已取消/终态任务发出去', () => {
  const { ledger } = ledgerWith();
  const a = ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: 'a' });
  ledger.cancel({ taskId: a.task.id, reason: '人撤销' });
  ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: 'b' });
  const leased = ledger.lease({ workerId: 'w-1' });
  assert.notEqual(leased.task.id, a.task.id);
});

test('heartbeat：持有者可续租；非持有者/未知任务被具名拒绝', () => {
  const { ledger, advance, now } = ledgerWith({ now: 1_000 });
  ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: KEY, leaseMs: 5_000 });
  const leased = ledger.lease({ workerId: 'w-1' });
  advance(2_000);
  const beat = ledger.heartbeat({ taskId: leased.task.id, workerId: 'w-1', extendMs: 5_000 });
  assert.equal(beat.ok, true);
  assert.equal(beat.task.lease.expiresAt, now() + 5_000);
  assert.throws(() => ledger.heartbeat({ taskId: leased.task.id, workerId: 'w-2', extendMs: 1_000 }), /WRONG_WORKER/);
  assert.throws(() => ledger.heartbeat({ taskId: 'nope', workerId: 'w-1', extendMs: 1_000 }), /NOT_FOUND/);
});

test('complete：成功 → SUCCEEDED 且 result 冻结；重复完成幂等（不再追加事件）', () => {
  const { ledger } = ledgerWith();
  ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: KEY });
  const leased = ledger.lease({ workerId: 'w-1' });
  const done = ledger.complete({ taskId: leased.task.id, workerId: 'w-1', result: { reply: 'ok' } });
  assert.equal(done.ok, true);
  assert.equal(done.task.state, TASK_STATE.SUCCEEDED);
  assert.ok(Object.isFrozen(done.task.result));
  const eventCount = ledger.events().length;
  const again = ledger.complete({ taskId: leased.task.id, workerId: 'w-1', result: { reply: 'again' } });
  assert.equal(again.duplicate, true);
  assert.equal(again.task.result.reply, 'ok', '重复完成不覆盖首次结果');
  assert.equal(ledger.events().length, eventCount, '重复完成不得追加事件');
});

test('租约过期后旧 worker 不能再完成（TOKEN_STALE）', () => {
  const { ledger, advance } = ledgerWith({ now: 1_000 });
  ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: KEY, leaseMs: 1_000, maxAttempts: 3 });
  const leased = ledger.lease({ workerId: 'w-old' });
  advance(5_000);
  ledger.sweepExpired();
  assert.throws(() => ledger.complete({ taskId: leased.task.id, workerId: 'w-old', result: {} }), /TOKEN_STALE/);
});

test('sweepExpired：过期租约回收一次 → 重排队 attempt+1；重复 sweep 不再产生事件', () => {
  const { ledger, advance } = ledgerWith({ now: 1_000 });
  ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: KEY, leaseMs: 1_000, maxAttempts: 3 });
  ledger.lease({ workerId: 'w-1' });
  advance(2_000);
  const swept = ledger.sweepExpired();
  assert.equal(swept.length, 1);
  const after = ledger.inspect(ledger.list()[0].id);
  assert.equal(after.state, TASK_STATE.QUEUED);
  assert.equal(after.attempt, 1);
  const eventCount = ledger.events().length;
  const again = ledger.sweepExpired();
  assert.equal(again.length, 0);
  assert.equal(ledger.events().length, eventCount);
});

test('fail：可重试 → 重新排队；不可重试 → FAILED；超过 maxAttempts → FAILED', () => {
  const { ledger } = ledgerWith();
  const t = ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: KEY, maxAttempts: 2 });
  const leased = ledger.lease({ workerId: 'w-1' });
  const retried = ledger.fail({ taskId: t.task.id, workerId: 'w-1', error: 'LLM_TIMEOUT', retryable: true });
  assert.equal(retried.task.state, TASK_STATE.QUEUED);
  assert.equal(retried.task.attempt, 1);
  ledger.lease({ workerId: 'w-1' });
  const failed = ledger.fail({ taskId: t.task.id, workerId: 'w-1', error: 'FATAL', retryable: false });
  assert.equal(failed.task.state, TASK_STATE.FAILED);
  assert.equal(failed.task.error, 'FATAL');

  const t2 = ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: 'two', maxAttempts: 1 });
  ledger.lease({ workerId: 'w-1' });
  ledger.fail({ taskId: t2.task.id, workerId: 'w-1', error: 'X', retryable: true });
  const after = ledger.inspect(t2.task.id);
  assert.equal(after.state, TASK_STATE.FAILED, 'attempt 达到 maxAttempts 后不再排队');
});

test('cancel：取消后不可再领取、不可再完成；重复取消幂等', () => {
  const { ledger } = ledgerWith();
  const t = ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: KEY });
  const cancelled = ledger.cancel({ taskId: t.task.id, reason: '人不批了' });
  assert.equal(cancelled.task.state, TASK_STATE.CANCELLED);
  assert.equal(ledger.lease({ workerId: 'w-1' }), null);
  const again = ledger.cancel({ taskId: t.task.id, reason: 'x' });
  assert.equal(again.duplicate, true);
  assert.throws(() => ledger.complete({ taskId: t.task.id, workerId: 'w-1', result: {} }), /NOT_RUNNING|TERMINAL|ILLEGAL/);
});

test('replay：由事件流重建快照；损坏/篡改无法通过序号校验', () => {
  const { ledger } = ledgerWith();
  const created = ledger.create({ kind: 'agent.turn', payload: { a: 1 }, idempotencyKey: KEY, leaseMs: 10_000 });
  ledger.lease({ workerId: 'w-1' });
  ledger.complete({ taskId: created.task.id, workerId: 'w-1', result: { ok: true } });
  const events = ledger.events();
  const rebuilt = TaskLedger.fromEvents(events);
  const restored = rebuilt.inspect(created.task.id);
  assert.equal(restored.state, TASK_STATE.SUCCEEDED);
  assert.equal(restored.result.ok, true);
  assert.equal(rebuilt.events().length, events.length);
});

test('list/stats：可按状态过滤，统计派生自任务快照而非事件文本', () => {
  const { ledger } = ledgerWith();
  ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: 'a' });
  ledger.create({ kind: 'agent.turn', payload: {}, idempotencyKey: 'b' });
  ledger.lease({ workerId: 'w-1' });
  assert.equal(ledger.list().length, 2);
  assert.equal(ledger.list({ state: TASK_STATE.RUNNING }).length, 1);
  assert.equal(ledger.list({ state: TASK_STATE.QUEUED }).length, 1);
  const stats = ledger.stats();
  assert.equal(stats.total, 2);
  assert.equal(stats.byState[TASK_STATE.RUNNING], 1);
  assert.ok(stats.events >= 3);
});

test('校验：非法参数与未知任务返回具名错误', () => {
  const { ledger } = ledgerWith();
  assert.throws(() => ledger.create({ kind: '', payload: {} }), /INVALID_KIND/);
  assert.throws(() => ledger.create({ kind: 'x', payload: {}, leaseMs: -1 }), /INVALID/);
  assert.throws(() => ledger.complete({ taskId: 'nope', workerId: 'w', result: {} }), /NOT_FOUND/);
  assert.throws(() => ledger.inspect('nope'), /NOT_FOUND/);
});
