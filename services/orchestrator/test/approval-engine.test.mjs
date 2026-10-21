// 测试：审批引擎（契约 §4）。覆盖 9 态状态机、非法迁移、过期、超时升级幂等、事件溯源、优先级排序。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  ApprovalEngine,
  ApprovalStateError,
  TICKET_STATE,
  TICKET_TYPE
} from '../src/approval-engine.mjs';
import { makeClock } from './_helpers.mjs';

function makeEngine(options = {}) {
  const clock = options.clock ?? makeClock();
  const events = [];
  const engine = new ApprovalEngine({
    clock,
    escalation: options.escalation ?? { afterMs: [10 * 60e3, 20 * 60e3], channels: ['watch', 'sms'] },
    onEvent: (event) => events.push(event)
  });
  return { clock, engine, events };
}

function createTicket(engine, overrides = {}) {
  return engine.create({
    userId: 'user-a',
    sourceAgentId: 'agent-b',
    ticketType: TICKET_TYPE.PAYMENT,
    payload: { amountCents: 5000 },
    ...overrides
  });
}

test('TICKET_STATE 9 态 / TICKET_TYPE 4 类常量冻结且与契约一致', () => {
  assert.deepEqual({ ...TICKET_STATE }, {
    PENDING: 'PENDING',
    REVIEWING: 'REVIEWING',
    APPROVED: 'APPROVED',
    REJECTED: 'REJECTED',
    MODIFIED: 'MODIFIED',
    EXECUTING: 'EXECUTING',
    COMPLETED: 'COMPLETED',
    FAILED: 'FAILED',
    EXPIRED: 'EXPIRED'
  });
  assert.deepEqual({ ...TICKET_TYPE }, {
    NEGOTIATION: 'NEGOTIATION',
    PAYMENT: 'PAYMENT',
    SKILL_CALL: 'SKILL_CALL',
    EXTERNAL_ACTION: 'EXTERNAL_ACTION'
  });
  assert.ok(Object.isFrozen(TICKET_STATE));
  assert.ok(Object.isFrozen(TICKET_TYPE));
});

test('create：返回冻结 ticket，字段/默认值/ID 前缀/截止时间均符合契约', () => {
  const { engine, events } = makeEngine();
  const ticket = createTicket(engine, { deadlineMs: 5 * 60e3, traceId: 'trace-1' });
  assert.ok(Object.isFrozen(ticket));
  assert.match(ticket.id, /^tkt-/);
  assert.equal(ticket.userId, 'user-a');
  assert.equal(ticket.sourceAgentId, 'agent-b');
  assert.equal(ticket.ticketType, 'PAYMENT');
  assert.equal(ticket.state, 'PENDING');
  assert.deepEqual(ticket.payload, { amountCents: 5000 });
  assert.ok(Object.isFrozen(ticket.payload));
  assert.equal(ticket.createdAt, '2026-10-21T00:00:00.000Z');
  assert.equal(ticket.deadline, '2026-10-21T00:05:00.000Z');
  assert.equal(ticket.priority, 'normal');
  assert.equal(ticket.traceId, 'trace-1');
  assert.equal(ticket.patch, null);
  assert.equal(events.length, 1);
  assert.equal(events[0].action, 'CREATE');
});

test('create 默认截止时间 = 30 分钟', () => {
  const { engine } = makeEngine();
  const ticket = createTicket(engine);
  assert.equal(ticket.deadline, '2026-10-21T00:30:00.000Z');
});

test('get：未知 ticketId 抛 NOT_FOUND', () => {
  const { engine } = makeEngine();
  assert.throws(() => engine.get('tkt-missing'), { code: 'NOT_FOUND' });
});

test('listPending：按 priority(urgent>high>normal>low) 再按 deadline 升序，且只含本人待办', () => {
  const { engine } = makeEngine();
  const urgent = createTicket(engine, { priority: 'urgent', deadlineMs: 60 * 60e3 });
  const high = createTicket(engine, { priority: 'high', deadlineMs: 60 * 60e3 });
  const normalLate = createTicket(engine, { priority: 'normal', deadlineMs: 60 * 60e3 });
  const normalEarly = createTicket(engine, { priority: 'normal', deadlineMs: 5 * 60e3 });
  const low = createTicket(engine, { priority: 'low' });
  createTicket(engine, { userId: 'user-b', priority: 'urgent' });
  const approved = createTicket(engine, { priority: 'urgent' });
  engine.decide({ ticketId: approved.id, actorUserId: 'user-a', action: 'APPROVE' });

  const pending = engine.listPending('user-a');
  assert.deepEqual(
    pending.map((ticket) => ticket.id),
    [urgent.id, high.id, normalEarly.id, normalLate.id, low.id]
  );
  assert.ok(Object.isFrozen(pending));
  assert.deepEqual(engine.listPending('user-none'), []);
});

test('decide APPROVE：本人可决策；非本人 FORBIDDEN；未知单 NOT_FOUND', () => {
  const { engine } = makeEngine();
  const ticket = createTicket(engine);
  assert.throws(
    () => engine.decide({ ticketId: ticket.id, actorUserId: 'user-b', action: 'APPROVE' }),
    { code: 'FORBIDDEN' }
  );
  assert.throws(
    () => engine.decide({ ticketId: 'tkt-missing', actorUserId: 'user-a', action: 'APPROVE' }),
    { code: 'NOT_FOUND' }
  );
  const approved = engine.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'APPROVE' });
  assert.equal(approved.state, TICKET_STATE.APPROVED);
  assert.ok(Object.isFrozen(approved));
  assert.equal(engine.get(ticket.id).state, TICKET_STATE.APPROVED);
  // 原 ticket 对象不被原地修改
  assert.equal(ticket.state, TICKET_STATE.PENDING);
});

test('decide MODIFY 写入 patch；decide REJECT 进入 REJECTED', () => {
  const { engine } = makeEngine();
  const modifyTicket = createTicket(engine);
  const modified = engine.decide({
    ticketId: modifyTicket.id,
    actorUserId: 'user-a',
    action: 'MODIFY',
    patch: { amountCents: 1000 },
    comment: '金额改小'
  });
  assert.equal(modified.state, TICKET_STATE.MODIFIED);
  assert.deepEqual(modified.patch, { amountCents: 1000 });
  assert.ok(Object.isFrozen(modified.patch));

  const rejectTicket = createTicket(engine);
  const rejected = engine.decide({ ticketId: rejectTicket.id, actorUserId: 'user-a', action: 'REJECT' });
  assert.equal(rejected.state, TICKET_STATE.REJECTED);
});

test('decide：不支持的 action => ILLEGAL_TRANSITION；已决策后再 decide => ILLEGAL_TRANSITION', () => {
  const { engine } = makeEngine();
  const ticket = createTicket(engine);
  assert.throws(
    () => engine.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'TRANSFER' }),
    { code: 'ILLEGAL_TRANSITION' }
  );
  engine.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'APPROVE' });
  assert.throws(
    () => engine.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'APPROVE' }),
    { code: 'ILLEGAL_TRANSITION' }
  );
});

test('beginReview：仅 PENDING -> REVIEWING；重复/非 PENDING => ILLEGAL_TRANSITION；非本人 FORBIDDEN', () => {
  const { engine } = makeEngine();
  const ticket = createTicket(engine);
  assert.throws(
    () => engine.beginReview({ ticketId: ticket.id, actorUserId: 'user-b' }),
    { code: 'FORBIDDEN' }
  );
  const reviewing = engine.beginReview({ ticketId: ticket.id, actorUserId: 'user-a' });
  assert.equal(reviewing.state, TICKET_STATE.REVIEWING);
  assert.throws(
    () => engine.beginReview({ ticketId: ticket.id, actorUserId: 'user-a' }),
    { code: 'ILLEGAL_TRANSITION' }
  );
  // REVIEWING 下仍可正常决策（白名单 REVIEWING -> APPROVED）
  const approved = engine.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'APPROVE' });
  assert.equal(approved.state, TICKET_STATE.APPROVED);
  assert.throws(
    () => engine.beginReview({ ticketId: ticket.id, actorUserId: 'user-a' }),
    { code: 'ILLEGAL_TRANSITION' }
  );
});

test('执行链：APPROVED/MODIFIED -> EXECUTING -> COMPLETED/FAILED；跳步一律 ILLEGAL_TRANSITION', () => {
  const { engine } = makeEngine();
  const ticket = createTicket(engine);
  assert.throws(() => engine.beginExecution({ ticketId: ticket.id }), { code: 'ILLEGAL_TRANSITION' });
  engine.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'APPROVE' });
  assert.throws(() => engine.complete({ ticketId: ticket.id }), { code: 'ILLEGAL_TRANSITION' });
  engine.beginExecution({ ticketId: ticket.id });
  assert.equal(engine.get(ticket.id).state, TICKET_STATE.EXECUTING);
  const completed = engine.complete({ ticketId: ticket.id, result: { ok: true } });
  assert.equal(completed.state, TICKET_STATE.COMPLETED);
  assert.throws(() => engine.fail({ ticketId: ticket.id, error: new Error('x') }), {
    code: 'ILLEGAL_TRANSITION'
  });

  const modifiedTicket = createTicket(engine);
  engine.decide({
    ticketId: modifiedTicket.id,
    actorUserId: 'user-a',
    action: 'MODIFY',
    patch: { amountCents: 1 }
  });
  engine.beginExecution({ ticketId: modifiedTicket.id });
  const failed = engine.fail({ ticketId: modifiedTicket.id, error: new Error('boom') });
  assert.equal(failed.state, TICKET_STATE.FAILED);
});

test('边界：审批单过期后 decide 抛 EXPIRED_TICKET，且单据被置为 EXPIRED（仅一次审计）', () => {
  const { engine, clock, events } = makeEngine();
  const ticket = createTicket(engine, { deadlineMs: 1000 });
  clock.advance(999);
  assert.equal(engine.get(ticket.id).state, TICKET_STATE.PENDING);
  clock.advance(2); // 越过 deadline
  assert.throws(
    () => engine.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'APPROVE' }),
    { code: 'EXPIRED_TICKET' }
  );
  assert.equal(engine.get(ticket.id).state, TICKET_STATE.EXPIRED);
  assert.equal(
    events.filter((event) => event.action === 'EXPIRE' && event.target === ticket.id).length,
    1
  );
  // 重复 decide 仍抛 EXPIRED_TICKET（不再产生重复 EXPIRE 事件）
  assert.throws(
    () => engine.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'APPROVE' }),
    { code: 'EXPIRED_TICKET' }
  );
  assert.equal(
    events.filter((event) => event.action === 'EXPIRE' && event.target === ticket.id).length,
    1
  );
});

test('escalateOverdue：按 afterMs 逐级升级；重复调用不重复升级；返回 {ticketId,level,channel,at}', () => {
  const { engine, clock } = makeEngine();
  const ticket = createTicket(engine, { deadlineMs: 60 * 60e3 });

  assert.deepEqual(engine.escalateOverdue(), []);
  clock.advance(9 * 60e3);
  assert.deepEqual(engine.escalateOverdue(), []);

  clock.advance(2 * 60e3); // 11 分钟
  const level1 = engine.escalateOverdue();
  assert.equal(level1.length, 1);
  assert.equal(level1[0].ticketId, ticket.id);
  assert.equal(level1[0].level, 1);
  assert.equal(level1[0].channel, 'watch');
  assert.equal(level1[0].at, '2026-10-21T00:11:00.000Z');

  assert.deepEqual(engine.escalateOverdue(), []);
  assert.deepEqual(engine.escalateOverdue((clock.currentMs() + 60e3)), []);

  clock.advance(10 * 60e3); // 21 分钟
  const level2 = engine.escalateOverdue();
  assert.equal(level2.length, 1);
  assert.equal(level2[0].level, 2);
  assert.equal(level2[0].channel, 'sms');

  clock.advance(5 * 60 * 60e3);
  assert.deepEqual(engine.escalateOverdue(), []); // 已到最高级，不再升级
  const escalatedEvents = engine.events(ticket.id).filter((event) => event.action === 'ESCALATE');
  assert.equal(escalatedEvents.length, 2);
  assert.equal(escalatedEvents[0].detail.escalationLevel, 1);
  assert.equal(escalatedEvents[1].detail.escalationLevel, 2);
});

test('escalateOverdue 支持显式 nowMs 且不升级已决策/已过期单据', () => {
  const { engine, clock } = makeEngine();
  const pending = createTicket(engine, { deadlineMs: 60 * 60e3 });
  const approved = createTicket(engine, { deadlineMs: 60 * 60e3 });
  engine.decide({ ticketId: approved.id, actorUserId: 'user-a', action: 'APPROVE' });

  const future = clock.currentMs() + 25 * 60e3;
  const escalated = engine.escalateOverdue(future);
  assert.deepEqual([...new Set(escalated.map((entry) => entry.ticketId))], [pending.id]);
  assert.equal(escalated.at(-1).level, 2);
});

test('events：只追加、可按 ticketId 过滤、冻结；快照不被后续事件改写', () => {
  const { engine, events } = makeEngine();
  const ticket = createTicket(engine);
  engine.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'APPROVE' });
  engine.beginExecution({ ticketId: ticket.id });
  engine.complete({ ticketId: ticket.id, result: { ok: true } });

  const global = engine.events();
  assert.ok(Object.isFrozen(global));
  assert.throws(() => global.push({}), TypeError);
  assert.deepEqual(
    global.map((event) => event.action),
    ['CREATE', 'DECIDE', 'BEGIN_EXECUTION', 'COMPLETE']
  );
  const scoped = engine.events(ticket.id);
  assert.equal(scoped.length, 4);
  const snapshot = engine.events(ticket.id);
  const otherTicket = createTicket(engine);
  engine.decide({ ticketId: otherTicket.id, actorUserId: 'user-a', action: 'APPROVE' });
  engine.beginExecution({ ticketId: otherTicket.id });
  engine.fail({ ticketId: otherTicket.id, error: 'x' });
  assert.equal(snapshot.length, 4);
  assert.equal(engine.events(ticket.id).length, 4);
  assert.equal(events.length, engine.events().length);
});

test('ApprovalStateError 带 code 且是 Error 子类', () => {
  const { engine } = makeEngine();
  try {
    engine.decide({ ticketId: 'tkt-missing', actorUserId: 'user-a', action: 'APPROVE' });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof ApprovalStateError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'NOT_FOUND');
  }
});
