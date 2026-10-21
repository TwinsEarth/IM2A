// 测试文件：§25 轮次内投递（mid-turn delivery：MID_TURN/QUEUED/dropped 审计/容量）/ 运行：node services/messaging/test/turn-inbox.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { INBOX_CODE, TURN_STATE, TurnInbox, TurnInboxError } from '../src/turn-inbox.mjs';

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

function makeInbox(options = {}) {
  const clock = makeClock();
  return { clock, inbox: new TurnInbox({ clock, ...options }) };
}

function msg(id) {
  return { id, text: `message-${id}` };
}

test('轮次内投递：beginTurn → inject(MID_TURN) → drain 立即消费（不必等到 endTurn）', () => {
  const { inbox } = makeInbox();
  const turn = inbox.beginTurn({ agentId: 'agent-a' });
  assert.equal(turn.state, TURN_STATE.RUNNING);
  assert.match(turn.id, /^turn-/);
  assert.equal(turn.startedAt, BASE_ISO);
  assert.ok(Object.isFrozen(turn));

  const injected = inbox.inject({ agentId: 'agent-a', message: msg('m1') });
  assert.deepEqual({ ...injected }, { delivered: 'MID_TURN', pending: 1 });

  // 这是整个机制的意义：同一轮次的 drain 立刻拿到，协商提案不等审批卡片
  const drained = inbox.drain({ agentId: 'agent-a' });
  assert.deepEqual(drained.consumed.map((item) => item.id), ['m1']);
  assert.equal(drained.pending, 0);
  assert.deepEqual(inbox.drain({ agentId: 'agent-a' }).consumed, []);

  const ended = inbox.endTurn({ agentId: 'agent-a', outcome: 'done' });
  assert.deepEqual({ ...ended }, { injected: 1, consumed: 1, dropped: 0, state: TURN_STATE.CLOSED });
});

test('无活动轮次注入 → QUEUED，等下一轮 drain 才拿到', () => {
  const { inbox } = makeInbox();
  const queued = inbox.inject({ agentId: 'agent-a', message: msg('q1') });
  assert.deepEqual({ ...queued }, { delivered: 'QUEUED', pending: 1 });

  const idleStatus = inbox.status({ agentId: 'agent-a' });
  assert.equal(idleStatus.state, TURN_STATE.IDLE);
  assert.equal(idleStatus.pending, 1);
  // 无活动轮次时不能 drain
  assert.throws(
    () => inbox.drain({ agentId: 'agent-a' }),
    (error) => error instanceof TurnInboxError && error.code === INBOX_CODE.NO_ACTIVE_TURN
  );

  inbox.beginTurn({ agentId: 'agent-a' });
  const drained = inbox.drain({ agentId: 'agent-a' });
  assert.deepEqual(drained.consumed.map((item) => item.id), ['q1']);
  assert.equal(inbox.status({ agentId: 'agent-a' }).pending, 0);
});

test('drain 按到达顺序消费：先 QUEUED 后 MID_TURN', () => {
  const { inbox } = makeInbox();
  inbox.inject({ agentId: 'agent-a', message: msg('q1') });
  inbox.beginTurn({ agentId: 'agent-a' });
  inbox.inject({ agentId: 'agent-a', message: msg('m2') });
  inbox.inject({ agentId: 'agent-a', message: msg('m3') });
  const drained = inbox.drain({ agentId: 'agent-a' });
  assert.deepEqual(drained.consumed.map((item) => item.id), ['q1', 'm2', 'm3']);
});

test('endTurn 未消费 → 计入 dropped，status/审计可查（不静默丢失）', () => {
  const { inbox } = makeInbox();
  inbox.beginTurn({ agentId: 'agent-a' });
  inbox.inject({ agentId: 'agent-a', message: msg('m1') });
  inbox.inject({ agentId: 'agent-a', message: msg('m2') });
  const ended = inbox.endTurn({ agentId: 'agent-a' });
  assert.deepEqual({ ...ended }, { injected: 2, consumed: 0, dropped: 2, state: TURN_STATE.CLOSED });

  const status = inbox.status({ agentId: 'agent-a' });
  assert.equal(status.state, TURN_STATE.IDLE);
  assert.equal(status.pending, 0);
  assert.equal(status.turns.length, 1);
  const turn = status.turns[0];
  assert.equal(turn.state, TURN_STATE.CLOSED);
  assert.equal(turn.endedAt, BASE_ISO);
  assert.equal(turn.dropped, 2);
  assert.deepEqual(turn.droppedMessages.map((item) => item.id), ['m1', 'm2'], '未消费消息必须可见');
  assert.ok(Object.isFrozen(turn.droppedMessages));

  const audit = inbox.audit({ agentId: 'agent-a' });
  const drops = audit.filter((event) => event.action === 'DROP');
  assert.equal(drops.length, 2, '每条被丢弃的消息都要有审计');
  assert.ok(drops.every((event) => event.detail.reason === 'endTurn_unconsumed'));
  assert.ok(audit.some((event) => event.action === 'END_TURN' && event.detail.dropped === 2));
});

test('容量超限 → CAPACITY_EXCEEDED（QUEUED 与 MID_TURN 共用容量）', () => {
  const { inbox } = makeInbox({ capacity: 2 });
  inbox.inject({ agentId: 'agent-a', message: msg('q1') });
  inbox.inject({ agentId: 'agent-a', message: msg('q2') });
  assert.throws(
    () => inbox.inject({ agentId: 'agent-a', message: msg('q3') }),
    (error) => error instanceof TurnInboxError && error.code === INBOX_CODE.CAPACITY_EXCEEDED
  );

  inbox.beginTurn({ agentId: 'agent-a' });
  inbox.drain({ agentId: 'agent-a' });
  inbox.inject({ agentId: 'agent-a', message: msg('m1') });
  inbox.inject({ agentId: 'agent-a', message: msg('m2') });
  assert.throws(
    () => inbox.inject({ agentId: 'agent-a', message: msg('m3') }),
    (error) => error instanceof TurnInboxError && error.code === INBOX_CODE.CAPACITY_EXCEEDED
  );
});

test('错误码：TURN_NOT_FOUND / AGENT_MISMATCH / TURN_CLOSED / NO_ACTIVE_TURN', () => {
  const { inbox } = makeInbox();
  assert.throws(
    () => inbox.drain({ agentId: 'agent-a' }),
    (error) => error instanceof TurnInboxError && error.code === INBOX_CODE.NO_ACTIVE_TURN
  );
  assert.throws(
    () => inbox.endTurn({ agentId: 'agent-a' }),
    (error) => error instanceof TurnInboxError && error.code === INBOX_CODE.NO_ACTIVE_TURN
  );
  for (const call of [
    () => inbox.inject({ agentId: 'agent-a', message: msg('m'), turnId: 'turn-none' }),
    () => inbox.drain({ agentId: 'agent-a', turnId: 'turn-none' }),
    () => inbox.endTurn({ agentId: 'agent-a', turnId: 'turn-none' }),
  ]) {
    assert.throws(call, (error) => error instanceof TurnInboxError && error.code === INBOX_CODE.TURN_NOT_FOUND);
  }

  const turn = inbox.beginTurn({ agentId: 'agent-a' });
  for (const call of [
    () => inbox.inject({ agentId: 'agent-b', message: msg('m2'), turnId: turn.id }),
    () => inbox.drain({ agentId: 'agent-b', turnId: turn.id }),
    () => inbox.endTurn({ agentId: 'agent-b', turnId: turn.id }),
    () => inbox.beginTurn({ agentId: 'agent-b', turnId: turn.id }),
  ]) {
    assert.throws(call, (error) => error instanceof TurnInboxError && error.code === INBOX_CODE.AGENT_MISMATCH);
  }

  inbox.endTurn({ agentId: 'agent-a', turnId: turn.id });
  for (const call of [
    () => inbox.inject({ agentId: 'agent-a', message: msg('m3'), turnId: turn.id }),
    () => inbox.drain({ agentId: 'agent-a', turnId: turn.id }),
    () => inbox.endTurn({ agentId: 'agent-a', turnId: turn.id }),
    () => inbox.beginTurn({ agentId: 'agent-a', turnId: turn.id }),
  ]) {
    assert.throws(call, (error) => error instanceof TurnInboxError && error.code === INBOX_CODE.TURN_CLOSED);
  }
});

test('beginTurn：显式 turnId 幂等重试；新轮开启时旧轮自动关闭且未消费消息进 dropped', () => {
  const { inbox } = makeInbox();
  const first = inbox.beginTurn({ agentId: 'agent-a', turnId: 'turn-fixed-1' });
  assert.equal(first.id, 'turn-fixed-1');
  const retry = inbox.beginTurn({ agentId: 'agent-a', turnId: 'turn-fixed-1' });
  assert.equal(retry.id, first.id, '同 turnId 重试幂等');
  assert.equal(retry.startedAt, first.startedAt);
  assert.equal(inbox.status({ agentId: 'agent-a' }).turns.length, 1);

  inbox.inject({ agentId: 'agent-a', message: msg('m1') });
  const second = inbox.beginTurn({ agentId: 'agent-a' }); // 未 endTurn 直接开新轮
  assert.notEqual(second.id, first.id);
  const turns = inbox.status({ agentId: 'agent-a' }).turns;
  assert.equal(turns.length, 2);
  assert.equal(turns[0].state, TURN_STATE.CLOSED);
  assert.equal(turns[0].dropped, 1);
  assert.deepEqual(turns[0].droppedMessages.map((item) => item.id), ['m1']);
  assert.equal(turns[1].state, TURN_STATE.RUNNING);
  const audit = inbox.audit({ agentId: 'agent-a' });
  assert.ok(
    audit.some((event) => event.action === 'DROP' && event.detail.reason === 'superseded_by_beginTurn'),
    '自动关闭旧轮也必须留审计'
  );
});

test('多 Agent 收件箱隔离：轮次、pending、审计互不影响', () => {
  const { inbox } = makeInbox();
  inbox.beginTurn({ agentId: 'agent-a' });
  inbox.beginTurn({ agentId: 'agent-b' });
  inbox.inject({ agentId: 'agent-a', message: msg('a1') });
  inbox.inject({ agentId: 'agent-b', message: msg('b1') });

  assert.deepEqual(inbox.drain({ agentId: 'agent-a' }).consumed.map((item) => item.id), ['a1']);
  assert.equal(inbox.status({ agentId: 'agent-b' }).pending, 1);
  assert.deepEqual(inbox.drain({ agentId: 'agent-b' }).consumed.map((item) => item.id), ['b1']);
  assert.equal(inbox.audit({ agentId: 'agent-a' }).some((event) => event.actor !== 'agent-a'), false);
});

test('status/audit：未知 agent 返回 IDLE 空态与空审计', () => {
  const { inbox } = makeInbox();
  assert.deepEqual(
    { ...inbox.status({ agentId: 'agent-none' }) },
    { state: TURN_STATE.IDLE, pending: 0, turns: [] }
  );
  assert.deepEqual(inbox.audit({ agentId: 'agent-none' }), []);
});

test('返回值冻结、审计只追加', () => {
  const { inbox } = makeInbox();
  const turn = inbox.beginTurn({ agentId: 'agent-a' });
  const injected = inbox.inject({ agentId: 'agent-a', message: msg('m1') });
  const drained = inbox.drain({ agentId: 'agent-a' });
  const ended = inbox.endTurn({ agentId: 'agent-a' });
  assert.ok(Object.isFrozen(turn));
  assert.ok(Object.isFrozen(injected));
  assert.ok(Object.isFrozen(drained));
  assert.ok(Object.isFrozen(drained.consumed));
  assert.ok(Object.isFrozen(ended));
  const audit = inbox.audit({ agentId: 'agent-a' });
  assert.ok(Object.isFrozen(audit));
  assert.ok(Object.isFrozen(audit[0]));
  const size = audit.length;
  inbox.inject({ agentId: 'agent-a', message: msg('queued') });
  assert.ok(inbox.audit({ agentId: 'agent-a' }).length > size, '审计只追加');
});

test('参数校验：capacity / agentId / message / turnId 非法 → TypeError/RangeError', () => {
  assert.throws(() => new TurnInbox({ capacity: 0 }), RangeError);
  assert.throws(() => new TurnInbox({ capacity: 1.5 }), RangeError);
  const { inbox } = makeInbox();
  assert.throws(() => inbox.beginTurn({}), TypeError);
  assert.throws(() => inbox.beginTurn({ agentId: '' }), TypeError);
  assert.throws(() => inbox.inject({ agentId: 'agent-a' }), TypeError);
  assert.throws(() => inbox.inject({ agentId: 'agent-a', message: null }), TypeError);
  assert.throws(() => inbox.inject({ agentId: 'agent-a', message: msg('m'), turnId: '' }), TypeError);
  assert.throws(() => inbox.drain({}), TypeError);
  assert.throws(() => inbox.endTurn({ agentId: 'agent-a', turnId: 123 }), TypeError);
  assert.throws(() => inbox.status({}), TypeError);
  assert.throws(() => inbox.audit({}), TypeError);
});
