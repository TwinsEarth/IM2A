// 测试：Agent↔Agent 协商引擎（契约 §5）。覆盖 8 态状态机、轮次/Token 硬上限（不抛异常）、artifact、终态拒绝、附加 fail/cancel。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  INTENT,
  NegotiationEngine,
  NegotiationError,
  TASK_STATE
} from '../src/negotiation-engine.mjs';
import { makeClock } from './_helpers.mjs';

function makeEngine(options = {}) {
  return new NegotiationEngine({
    clock: options.clock ?? makeClock(),
    maxRounds: options.maxRounds ?? 5,
    maxTokens: options.maxTokens ?? 4000,
    maxSeconds: options.maxSeconds ?? 300
  });
}

function openSession(engine, overrides = {}) {
  return engine.open({
    initiatorAgentId: 'agent-a',
    initiatorUserId: 'user-a',
    responderAgentId: 'agent-b',
    responderUserId: 'user-b',
    topic: '晚餐地点',
    payload: { proposal: '火锅' },
    ...overrides
  });
}

const fromA = { fromAgentId: 'agent-a' };
const fromB = { fromAgentId: 'agent-b' };

test('TASK_STATE 8 态 / INTENT 6 类常量冻结且与契约一致', () => {
  assert.deepEqual({ ...TASK_STATE }, {
    SUBMITTED: 'SUBMITTED',
    WORKING: 'WORKING',
    INPUT_REQUIRED: 'INPUT_REQUIRED',
    AUTH_REQUIRED: 'AUTH_REQUIRED',
    COMPLETED: 'COMPLETED',
    FAILED: 'FAILED',
    CANCELED: 'CANCELED',
    REJECTED: 'REJECTED'
  });
  assert.deepEqual({ ...INTENT }, {
    PROPOSE: 'PROPOSE',
    COUNTER: 'COUNTER',
    ACCEPT: 'ACCEPT',
    REJECT: 'REJECT',
    REQUEST_INFO: 'REQUEST_INFO',
    REQUEST_AUTH: 'REQUEST_AUTH'
  });
  assert.ok(Object.isFrozen(TASK_STATE));
  assert.ok(Object.isFrozen(INTENT));
});

test('open：返回冻结 session，含集成方消费的扁平参与方字段与初始状态', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  assert.ok(Object.isFrozen(session));
  assert.match(session.id, /^neg-/);
  assert.equal(session.topic, '晚餐地点');
  assert.equal(session.state, 'SUBMITTED');
  assert.equal(session.round, 0);
  assert.equal(session.tokens, 0);
  assert.equal(session.initiatorAgentId, 'agent-a');
  assert.equal(session.responderAgentId, 'agent-b');
  assert.equal(session.initiatorUserId, 'user-a');
  assert.equal(session.responderUserId, 'user-b');
  assert.deepEqual([...session.turns], []);
  assert.equal(session.terminalReason, null);
  assert.equal(session.artifact, null);
  assert.ok(Object.isFrozen(session.turns));
  assert.equal(engine.get(session.id).id, session.id);
});

test('get/artifact：未知 sessionId 抛 NOT_FOUND', () => {
  const engine = makeEngine();
  assert.throws(() => engine.get('neg-missing'), { code: 'NOT_FOUND' });
  assert.throws(() => engine.artifact('neg-missing'), { code: 'NOT_FOUND' });
});

test('WRONG_AGENT：非参与方 Agent 出招被拒绝', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  assert.throws(
    () => engine.turn({ sessionId: session.id, fromAgentId: 'agent-outsider', intent: INTENT.PROPOSE }),
    { code: 'WRONG_AGENT' }
  );
});

test('PROPOSE：仅 SUBMITTED -> WORKING；重复 PROPOSE => ILLEGAL_STATE；turn 字段齐全', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  const { session: working, turn } = engine.turn({
    sessionId: session.id,
    ...fromA,
    intent: INTENT.PROPOSE,
    payload: { proposal: '火锅' },
    tokens: 120
  });
  assert.equal(working.state, TASK_STATE.WORKING);
  assert.equal(working.tokens, 120);
  assert.equal(working.turns.length, 1);
  assert.ok(Object.isFrozen(turn));
  assert.equal(turn.fromAgentId, 'agent-a');
  assert.equal(turn.intent, 'PROPOSE');
  assert.equal(turn.tokens, 120);
  assert.equal(turn.at, '2026-10-21T00:00:00.000Z');
  assert.deepEqual(turn.payload, { proposal: '火锅' });
  assert.throws(
    () => engine.turn({ sessionId: session.id, ...fromB, intent: INTENT.PROPOSE }),
    { code: 'ILLEGAL_STATE' }
  );
});

test('COUNTER：仅 WORKING/INPUT_REQUIRED 合法；round += 1', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  assert.throws(
    () => engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.COUNTER }),
    { code: 'ILLEGAL_STATE' }
  );
  engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.PROPOSE });
  const first = engine.turn({ sessionId: session.id, ...fromB, intent: INTENT.COUNTER, payload: { proposal: '烧烤' } });
  assert.equal(first.session.round, 1);
  assert.equal(first.session.state, TASK_STATE.WORKING);
  const second = engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.COUNTER });
  assert.equal(second.session.round, 2);
});

test('边界：轮次超限不抛异常，置 INPUT_REQUIRED + terminalReason=MAX_ROUNDS', () => {
  const engine = makeEngine({ maxRounds: 2 });
  const session = openSession(engine);
  engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.PROPOSE });
  assert.equal(engine.turn({ sessionId: session.id, ...fromB, intent: INTENT.COUNTER }).session.round, 1);
  assert.equal(engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.COUNTER }).session.round, 2);
  let result;
  assert.doesNotThrow(() => {
    result = engine.turn({ sessionId: session.id, ...fromB, intent: INTENT.COUNTER });
  });
  assert.equal(result.session.round, 3);
  assert.equal(result.session.state, TASK_STATE.INPUT_REQUIRED);
  assert.equal(result.session.terminalReason, 'MAX_ROUNDS');
  // turn 仍然被记录（控制台时间线需要）
  assert.equal(result.session.turns.length, 4);
  assert.equal(result.turn.tokens, 0);
});

test('边界：Token 超限不抛异常，置 INPUT_REQUIRED + terminalReason=TOKEN_BUDGET', () => {
  const engine = makeEngine({ maxTokens: 1000 });
  const session = openSession(engine);
  engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.PROPOSE, tokens: 100 });
  const first = engine.turn({ sessionId: session.id, ...fromB, intent: INTENT.COUNTER, tokens: 600 });
  assert.equal(first.session.state, TASK_STATE.WORKING);
  assert.equal(first.session.tokens, 700);
  let result;
  assert.doesNotThrow(() => {
    result = engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.COUNTER, tokens: 600 });
  });
  assert.equal(result.session.tokens, 1300);
  assert.equal(result.session.state, TASK_STATE.INPUT_REQUIRED);
  assert.equal(result.session.terminalReason, 'TOKEN_BUDGET');
  assert.equal(result.session.round, 2);
});

test('边界：轮次与 Token 同时超限时以 MAX_ROUNDS 优先', () => {
  const engine = makeEngine({ maxRounds: 1, maxTokens: 100 });
  const session = openSession(engine);
  engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.PROPOSE });
  const first = engine.turn({ sessionId: session.id, ...fromB, intent: INTENT.COUNTER, tokens: 500 });
  assert.equal(first.session.state, TASK_STATE.INPUT_REQUIRED);
  assert.equal(first.session.terminalReason, 'TOKEN_BUDGET'); // round=1 未超限，先命中 Token
  const second = engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.COUNTER, tokens: 500 });
  assert.equal(second.session.round, 2);
  assert.equal(second.session.terminalReason, 'MAX_ROUNDS');
});

test('ACCEPT：WORKING -> COMPLETED 并产出 artifact；artifact() 返回同一对象', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.PROPOSE });
  const { session: completed } = engine.turn({
    sessionId: session.id,
    ...fromB,
    intent: INTENT.ACCEPT,
    payload: { summary: '去火锅店' }
  });
  assert.equal(completed.state, TASK_STATE.COMPLETED);
  const artifact = engine.artifact(session.id);
  assert.deepEqual(artifact.type, 'AGREEMENT');
  assert.equal(artifact.summary, '去火锅店');
  assert.deepEqual(artifact.payload, { summary: '去火锅店' });
  assert.equal(artifact.at, '2026-10-21T00:00:00.000Z');
  assert.equal(artifact, completed.artifact);
  assert.ok(Object.isFrozen(artifact));
});

test('ACCEPT：payload 无 summary 时 summary 回落到 topic', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.PROPOSE });
  engine.turn({ sessionId: session.id, ...fromB, intent: INTENT.ACCEPT });
  assert.equal(engine.artifact(session.id).summary, '晚餐地点');
});

test('终态拒绝：COMPLETED/REJECTED 后再收 turn => ILLEGAL_STATE', () => {
  const engine = makeEngine();
  const completedSession = openSession(engine);
  engine.turn({ sessionId: completedSession.id, ...fromA, intent: INTENT.PROPOSE });
  engine.turn({ sessionId: completedSession.id, ...fromB, intent: INTENT.ACCEPT });
  assert.throws(
    () => engine.turn({ sessionId: completedSession.id, ...fromA, intent: INTENT.REQUEST_INFO }),
    { code: 'ILLEGAL_STATE' }
  );

  const rejectedSession = openSession(engine);
  engine.turn({ sessionId: rejectedSession.id, ...fromA, intent: INTENT.REJECT });
  assert.equal(engine.get(rejectedSession.id).state, TASK_STATE.REJECTED);
  assert.throws(
    () => engine.turn({ sessionId: rejectedSession.id, ...fromA, intent: INTENT.PROPOSE }),
    { code: 'ILLEGAL_STATE' }
  );
});

test('REJECT：任意非终态均可拒绝（含 SUBMITTED）', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  const { session: rejected } = engine.turn({ sessionId: session.id, ...fromB, intent: INTENT.REJECT });
  assert.equal(rejected.state, TASK_STATE.REJECTED);
});

test('REQUEST_INFO/REQUEST_AUTH：协议级中断点，可从终态外任意状态进入', () => {
  const engine = makeEngine();
  const infoSession = openSession(engine);
  engine.turn({ sessionId: infoSession.id, ...fromA, intent: INTENT.REQUEST_INFO });
  assert.equal(engine.get(infoSession.id).state, TASK_STATE.INPUT_REQUIRED);
  // INPUT_REQUIRED 下可继续 COUNTER
  const countered = engine.turn({ sessionId: infoSession.id, ...fromB, intent: INTENT.COUNTER });
  assert.equal(countered.session.state, TASK_STATE.WORKING);

  const authSession = openSession(engine);
  engine.turn({ sessionId: authSession.id, ...fromA, intent: INTENT.PROPOSE });
  engine.turn({ sessionId: authSession.id, ...fromB, intent: INTENT.REQUEST_AUTH });
  assert.equal(engine.get(authSession.id).state, TASK_STATE.AUTH_REQUIRED);
  const completed = engine.turn({ sessionId: authSession.id, ...fromA, intent: INTENT.ACCEPT });
  assert.equal(completed.session.state, TASK_STATE.COMPLETED);
});

test('ACCEPT 非法来源状态（SUBMITTED）=> ILLEGAL_STATE', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  assert.throws(
    () => engine.turn({ sessionId: session.id, ...fromA, intent: INTENT.ACCEPT }),
    { code: 'ILLEGAL_STATE' }
  );
});

test('INTENT_INVALID：未知 intent / 缺失 intent', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  assert.throws(
    () => engine.turn({ sessionId: session.id, ...fromA, intent: 'TRADE' }),
    { code: 'INTENT_INVALID' }
  );
  assert.throws(
    () => engine.turn({ sessionId: session.id, ...fromA, intent: undefined }),
    { code: 'INTENT_INVALID' }
  );
});

test('list：按打开顺序；可按 state 过滤；返回冻结数组', () => {
  const engine = makeEngine();
  const first = openSession(engine, { topic: 't1' });
  const second = openSession(engine, { topic: 't2' });
  engine.turn({ sessionId: second.id, ...fromA, intent: INTENT.PROPOSE });
  const all = engine.list();
  assert.deepEqual(all.map((session) => session.id), [first.id, second.id]);
  assert.ok(Object.isFrozen(all));
  assert.deepEqual(engine.list({ state: TASK_STATE.SUBMITTED }).map((session) => session.id), [first.id]);
  assert.deepEqual(engine.list({ state: TASK_STATE.COMPLETED }), []);
});

test('附加方法 fail/cancel：进入 FAILED/CANCELED，terminalReason 取 reason 或默认值；终态拒绝', () => {
  const engine = makeEngine();
  const failedSession = openSession(engine);
  const failed = engine.fail({ sessionId: failedSession.id });
  assert.equal(failed.state, TASK_STATE.FAILED);
  assert.equal(failed.terminalReason, 'FAILED');
  assert.throws(() => engine.fail({ sessionId: failedSession.id }), { code: 'ILLEGAL_STATE' });
  assert.throws(
    () => engine.turn({ sessionId: failedSession.id, ...fromA, intent: INTENT.PROPOSE }),
    { code: 'ILLEGAL_STATE' }
  );

  const canceledSession = openSession(engine);
  const canceled = engine.cancel({ sessionId: canceledSession.id, reason: 'USER_CANCELED' });
  assert.equal(canceled.state, TASK_STATE.CANCELED);
  assert.equal(canceled.terminalReason, 'USER_CANCELED');
  assert.throws(
    () => engine.turn({ sessionId: canceledSession.id, ...fromA, intent: INTENT.PROPOSE }),
    { code: 'ILLEGAL_STATE' }
  );

  const completedSession = openSession(engine);
  engine.turn({ sessionId: completedSession.id, ...fromA, intent: INTENT.PROPOSE });
  engine.turn({ sessionId: completedSession.id, ...fromB, intent: INTENT.ACCEPT });
  assert.throws(() => engine.fail({ sessionId: completedSession.id }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => engine.cancel({ sessionId: completedSession.id }), { code: 'ILLEGAL_STATE' });
});

test('tokens 非法值按 0 计；session 每次状态变更返回新对象（旧对象不变）', () => {
  const engine = makeEngine();
  const session = openSession(engine);
  const { session: afterPropose } = engine.turn({
    sessionId: session.id,
    ...fromA,
    intent: INTENT.PROPOSE,
    tokens: -50
  });
  assert.equal(afterPropose.tokens, 0);
  assert.equal(session.state, TASK_STATE.SUBMITTED);
  assert.notEqual(afterPropose, session);
  const { session: afterCounter } = engine.turn({
    sessionId: session.id,
    ...fromB,
    intent: INTENT.COUNTER,
    tokens: Number.NaN
  });
  assert.equal(afterCounter.tokens, 0);
  assert.equal(afterCounter.turns.length, 2);
});

test('NegotiationError 带 code 且是 Error 子类', () => {
  const engine = makeEngine();
  try {
    engine.get('neg-missing');
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof NegotiationError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'NOT_FOUND');
  }
});
