// 测试文件：§15 Agent 会话层（在线状态 / 订阅投递 / transport 失败 / 心跳超时 / 频道层集成）/ 运行：node services/messaging/test/session.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  SESSION_CODE,
  SESSION_STATE,
  AgentSessionRegistry,
  SessionError,
} from '../src/session.mjs';
import {
  CHANNEL_TYPE,
  MEMBER_KIND,
  MESSAGE_KIND,
  ChannelRegistry,
} from '../src/channel.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';

function makeTransport() {
  const sent = [];
  return {
    sent,
    closed: false,
    send(payload) {
      sent.push(payload);
    },
    close() {
      this.closed = true;
    },
  };
}

function makeFailingTransport() {
  return {
    sent: [],
    closed: false,
    send() {
      throw new Error('sse write failed');
    },
    close() {
      this.closed = true;
    },
  };
}

function setup({ heartbeatTimeoutMs = 90_000, channelRegistry = null } = {}) {
  const state = { nowMs: Date.parse(BASE_ISO) };
  const clock = () => new Date(state.nowMs);
  const sessions = new AgentSessionRegistry({ clock, heartbeatTimeoutMs, channelRegistry });
  return {
    sessions,
    state,
    clock,
    advance(ms) {
      state.nowMs += ms;
    },
  };
}

test('connect：ONLINE 会话视图冻结；heartbeat 用注入 clock 续期 lastSeenAt；presence 字段集符合 §15', () => {
  const { sessions, advance } = setup();
  const session = sessions.connect({
    agentId: 'agent-a',
    principalUserId: 'user-a',
    transport: makeTransport(),
    subscriptions: ['ch-1', 'ch-2'],
  });
  assert.equal(session.state, SESSION_STATE.ONLINE);
  assert.equal(session.protocol, 'AGENT_WS');
  assert.equal(session.principalUserId, 'user-a');
  assert.deepEqual([...session.subscriptions], ['ch-1', 'ch-2']);
  assert.equal(session.lastSeenAt, BASE_ISO);
  assert.ok(Object.isFrozen(session));
  assert.ok(Object.isFrozen(session.subscriptions));

  advance(1500);
  const beat = sessions.heartbeat({ agentId: 'agent-a' });
  assert.equal(beat.lastSeenAt, '2026-10-21T10:00:01.500Z');

  const presence = sessions.presence();
  assert.equal(presence.length, 1);
  assert.deepEqual(Object.keys(presence[0]).sort(), [
    'agentId',
    'lastSeenAt',
    'principalUserId',
    'protocol',
    'state',
    'subscriptions',
  ]);
  assert.ok(Object.isFrozen(presence[0]));
});

test('重复 connect 同一在线 agent → ALREADY_ONLINE；disconnect 后重连成功并换用新 transport', () => {
  const { sessions } = setup();
  const first = makeTransport();
  sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: first, subscriptions: ['ch-1'] });

  assert.throws(
    () => sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: makeTransport() }),
    (error) => error instanceof SessionError && error.code === SESSION_CODE.ALREADY_ONLINE
  );
  // 失败的重复 connect 不得替换原 transport
  assert.deepEqual([...sessions.push({ channelId: 'ch-1', message: { n: 1 } }).delivered], ['agent-a']);
  assert.equal(first.sent.length, 1);

  const disconnected = sessions.disconnect({ agentId: 'agent-a' });
  assert.equal(disconnected.state, SESSION_STATE.OFFLINE);
  assert.equal(first.closed, true);

  const second = makeTransport();
  const reconnected = sessions.connect({
    agentId: 'agent-a',
    principalUserId: 'user-a',
    transport: second,
    subscriptions: ['ch-1'],
  });
  assert.equal(reconnected.state, SESSION_STATE.ONLINE);
  assert.equal(reconnected.lastSeenAt, disconnected.lastSeenAt, 'clock 未推进时 lastSeenAt 不变');
  const result = sessions.push({ channelId: 'ch-1', message: { hello: 1 } });
  assert.deepEqual([...result.delivered], ['agent-a']);
  assert.equal(second.sent.length, 1);
  assert.equal(first.sent.length, 1, '旧 transport 不再收到新消息');
});

test('transport 缺失或不实现 { send, close } → TRANSPORT_REQUIRED', () => {
  const { sessions } = setup();
  const badTransports = [undefined, null, {}, { send() {} }, { close() {} }, { send: 'noop', close() {} }];
  for (const transport of badTransports) {
    assert.throws(
      () => sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport }),
      (error) => error instanceof SessionError && error.code === SESSION_CODE.TRANSPORT_REQUIRED,
      `应拒绝 transport=${JSON.stringify(transport)}`
    );
  }
  assert.equal(sessions.presence().length, 0);
});

test('会话状态错误码：UNKNOWN_AGENT / NOT_ONLINE', () => {
  const { sessions } = setup();
  const unknownCalls = [
    () => sessions.heartbeat({ agentId: 'agent-none' }),
    () => sessions.disconnect({ agentId: 'agent-none' }),
    () => sessions.subscribe({ agentId: 'agent-none', channelId: 'ch-1' }),
    () => sessions.unsubscribe({ agentId: 'agent-none', channelId: 'ch-1' }),
  ];
  for (const call of unknownCalls) {
    assert.throws(call, (error) => error instanceof SessionError && error.code === SESSION_CODE.UNKNOWN_AGENT);
  }

  sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: makeTransport() });
  sessions.disconnect({ agentId: 'agent-a' });
  const offlineCalls = [
    () => sessions.heartbeat({ agentId: 'agent-a' }),
    () => sessions.disconnect({ agentId: 'agent-a' }),
    () => sessions.subscribe({ agentId: 'agent-a', channelId: 'ch-1' }),
    () => sessions.unsubscribe({ agentId: 'agent-a', channelId: 'ch-1' }),
  ];
  for (const call of offlineCalls) {
    assert.throws(call, (error) => error instanceof SessionError && error.code === SESSION_CODE.NOT_ONLINE);
  }
});

test('push 只发「在线 + 已订阅」：在线未订阅、离线已订阅都不投递', () => {
  const { sessions } = setup();
  const transportA = makeTransport();
  sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: transportA, subscriptions: ['ch-1'] });
  const transportB = makeTransport();
  sessions.connect({ agentId: 'agent-b', principalUserId: 'user-b', transport: transportB, subscriptions: ['ch-2'] });
  const transportC = makeTransport();
  sessions.connect({ agentId: 'agent-c', principalUserId: 'user-c', transport: transportC, subscriptions: ['ch-1'] });
  sessions.disconnect({ agentId: 'agent-c' });

  const result = sessions.push({ channelId: 'ch-1', message: { text: 'hi' } });
  assert.deepEqual([...result.delivered], ['agent-a']);
  assert.deepEqual([...result.failed], []);
  assert.equal(transportA.sent.length, 1);
  assert.equal(transportB.sent.length, 0);
  assert.equal(transportC.sent.length, 0);

  // transport.send 收到的载荷形状（SSE 适配器按此序列化）
  assert.equal(transportA.sent[0].channelId, 'ch-1');
  assert.deepEqual(transportA.sent[0].message, { text: 'hi' });
  assert.ok(Object.isFrozen(transportA.sent[0]));
  assert.ok(Object.isFrozen(result.delivered));
});

test('push：transport.send 抛错 → 该 agent 置 OFFLINE、计入 failed，其余 agent 继续收到', () => {
  const { sessions } = setup();
  const good = makeTransport();
  sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: good, subscriptions: ['ch-1'] });
  const bad = makeFailingTransport();
  sessions.connect({ agentId: 'agent-b', principalUserId: 'user-b', transport: bad, subscriptions: ['ch-1'] });

  const result = sessions.push({ channelId: 'ch-1', message: { n: 1 } });
  assert.deepEqual([...result.delivered], ['agent-a']);
  assert.deepEqual([...result.failed], ['agent-b']);
  const badPresence = sessions.presence().find((item) => item.agentId === 'agent-b');
  assert.equal(badPresence.state, SESSION_STATE.OFFLINE);
  assert.equal(bad.closed, true);
  assert.throws(() => sessions.heartbeat({ agentId: 'agent-b' }), { code: 'NOT_ONLINE' });

  const second = sessions.push({ channelId: 'ch-1', message: { n: 2 } });
  assert.deepEqual([...second.delivered], ['agent-a']);
  assert.deepEqual([...second.failed], [], '已离线 agent 不再尝试投递');
});

test('SEND_FAILED：throwOnFailure=true 时 push 抛 SessionError 并携带 delivered/failed', () => {
  const { sessions } = setup();
  sessions.connect({
    agentId: 'agent-a',
    principalUserId: 'user-a',
    transport: makeFailingTransport(),
    subscriptions: ['ch-1'],
  });
  assert.throws(
    () => sessions.push({ channelId: 'ch-1', message: { n: 1 }, throwOnFailure: true }),
    (error) =>
      error instanceof SessionError &&
      error.code === SESSION_CODE.SEND_FAILED &&
      error.failed.includes('agent-a') &&
      Array.isArray(error.delivered)
  );
  assert.equal(sessions.presence()[0].state, SESSION_STATE.OFFLINE);
});

test('心跳超时 sweepStale（注入 clock 推进，不 sleep）：按 lastSeen 判定并置 OFFLINE', () => {
  const { sessions, advance } = setup({ heartbeatTimeoutMs: 90_000 });
  const transportA = makeTransport();
  sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: transportA });
  advance(60_000);
  sessions.connect({ agentId: 'agent-b', principalUserId: 'user-b', transport: makeTransport() });
  advance(30_000); // t+90s：A 恰好到达超时边界

  assert.deepEqual(sessions.sweepStale(), ['agent-a']);
  assert.equal(transportA.closed, true);
  const presence = sessions.presence();
  assert.equal(presence.find((item) => item.agentId === 'agent-a').state, SESSION_STATE.OFFLINE);
  assert.equal(presence.find((item) => item.agentId === 'agent-b').state, SESSION_STATE.ONLINE);

  sessions.heartbeat({ agentId: 'agent-b' });
  advance(89_000);
  assert.deepEqual(sessions.sweepStale(), [], '未到超时不得误杀');
  advance(1_000);
  assert.deepEqual(sessions.sweepStale(), ['agent-b']);
  assert.throws(() => sessions.heartbeat({ agentId: 'agent-b' }), { code: 'NOT_ONLINE' });
});

test('sweepStale(nowMs) 显式时间参数：不推进 clock 也能判定', () => {
  const { sessions } = setup({ heartbeatTimeoutMs: 90_000 });
  sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: makeTransport() });
  const future = Date.parse(BASE_ISO) + 90_000;
  assert.deepEqual(sessions.sweepStale(future), ['agent-a']);
  assert.deepEqual(sessions.sweepStale(future), [], '已离线 agent 不重复返回');
});

test('多 Agent 同频道：两个订阅者都收到，第三个未订阅收不到', () => {
  const { sessions } = setup();
  const transportA = makeTransport();
  const transportB = makeTransport();
  const transportC = makeTransport();
  sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: transportA, subscriptions: ['neg-1'] });
  sessions.connect({ agentId: 'agent-b', principalUserId: 'user-b', transport: transportB, subscriptions: ['neg-1'] });
  sessions.connect({ agentId: 'agent-c', principalUserId: 'user-c', transport: transportC, subscriptions: ['neg-2'] });

  const result = sessions.push({ channelId: 'neg-1', message: { turn: 1 } });
  assert.deepEqual([...result.delivered], ['agent-a', 'agent-b']);
  assert.equal(transportA.sent.length, 1);
  assert.equal(transportB.sent.length, 1);
  assert.equal(transportC.sent.length, 0);
});

test('subscribe/unsubscribe：更新 presence 订阅并即时影响 push', () => {
  const { sessions } = setup();
  const transport = makeTransport();
  sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport });
  assert.deepEqual([...sessions.subscribe({ agentId: 'agent-a', channelId: 'ch-1' })], ['ch-1']);
  assert.deepEqual([...sessions.presence()[0].subscriptions], ['ch-1']);
  assert.deepEqual([...sessions.push({ channelId: 'ch-1', message: { a: 1 } }).delivered], ['agent-a']);
  assert.deepEqual([...sessions.unsubscribe({ agentId: 'agent-a', channelId: 'ch-1' })], []);
  assert.deepEqual([...sessions.push({ channelId: 'ch-1', message: { a: 2 } }).delivered], []);
});

test('与频道层集成：subscribe 传播影响 ChannelRegistry.deliveredTo；disconnect/sweep 后停止投递', () => {
  const state = { nowMs: Date.parse(BASE_ISO) };
  const clock = () => new Date(state.nowMs);
  const channelRegistry = new ChannelRegistry({ clock });
  const sessions = new AgentSessionRegistry({ clock, heartbeatTimeoutMs: 90_000, channelRegistry });
  const advance = (ms) => {
    state.nowMs += ms;
  };

  channelRegistry.create({ channelId: 'ch-dir', type: CHANNEL_TYPE.DIRECT, ownerUserId: 'user-a' });
  channelRegistry.join({ channelId: 'ch-dir', memberId: 'user-a', kind: MEMBER_KIND.HUMAN });
  channelRegistry.join({
    channelId: 'ch-dir',
    memberId: 'agent-a',
    kind: MEMBER_KIND.AGENT,
    agentId: 'agent-a',
    principalUserId: 'user-a',
  });
  channelRegistry.join({
    channelId: 'ch-dir',
    memberId: 'agent-b',
    kind: MEMBER_KIND.AGENT,
    agentId: 'agent-b',
    principalUserId: 'user-a',
  });

  const transportA = makeTransport();
  sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: transportA });
  const transportB = makeTransport();
  sessions.connect({ agentId: 'agent-b', principalUserId: 'user-a', transport: transportB });

  let posted = channelRegistry.post({
    channelId: 'ch-dir',
    memberId: 'user-a',
    kind: MESSAGE_KIND.TEXT,
    payload: { n: 1 },
  });
  assert.deepEqual([...posted.deliveredTo], [], '未订阅时频道投递跳过 Agent');

  assert.deepEqual([...sessions.subscribe({ agentId: 'agent-a', channelId: 'ch-dir' })], ['ch-dir']);
  assert.equal(channelRegistry.isSubscribed({ channelId: 'ch-dir', memberId: 'agent-a' }), true);
  posted = channelRegistry.post({
    channelId: 'ch-dir',
    memberId: 'user-a',
    kind: MESSAGE_KIND.TEXT,
    payload: { n: 2 },
  });
  assert.deepEqual([...posted.deliveredTo], ['agent-a']);

  // 端到端：频道 post → 会话 push
  const pushed = sessions.push({ channelId: 'ch-dir', message: posted.message });
  assert.deepEqual([...pushed.delivered], ['agent-a']);
  assert.equal(transportA.sent.length, 1);

  sessions.disconnect({ agentId: 'agent-a' });
  assert.equal(channelRegistry.isSubscribed({ channelId: 'ch-dir', memberId: 'agent-a' }), false);
  posted = channelRegistry.post({
    channelId: 'ch-dir',
    memberId: 'user-a',
    kind: MESSAGE_KIND.TEXT,
    payload: { n: 3 },
  });
  assert.deepEqual([...posted.deliveredTo], []);

  // 心跳超时同样传播取消订阅
  sessions.subscribe({ agentId: 'agent-b', channelId: 'ch-dir' });
  advance(90_000);
  assert.deepEqual(sessions.sweepStale(), ['agent-b']);
  assert.equal(channelRegistry.isSubscribed({ channelId: 'ch-dir', memberId: 'agent-b' }), false);
  assert.equal(transportB.closed, true);
});

test('connect 的初始 subscriptions 会传播到频道层（无需手动 subscribe）', () => {
  const state = { nowMs: Date.parse(BASE_ISO) };
  const clock = () => new Date(state.nowMs);
  const channelRegistry = new ChannelRegistry({ clock });
  const sessions = new AgentSessionRegistry({ clock, channelRegistry });
  channelRegistry.create({ channelId: 'ch-x', type: CHANNEL_TYPE.GROUP, ownerUserId: 'user-a' });
  channelRegistry.join({ channelId: 'ch-x', memberId: 'user-a', kind: MEMBER_KIND.HUMAN });
  channelRegistry.join({ channelId: 'ch-x', memberId: 'agent-a', kind: MEMBER_KIND.AGENT, agentId: 'agent-a' });

  sessions.connect({
    agentId: 'agent-a',
    principalUserId: 'user-a',
    transport: makeTransport(),
    subscriptions: ['ch-x'],
  });
  const posted = channelRegistry.post({ channelId: 'ch-x', memberId: 'user-a', kind: MESSAGE_KIND.TEXT });
  assert.deepEqual([...posted.deliveredTo], ['agent-a']);
});

test('参数与枚举：非法参数 TypeError / RangeError；SESSION_STATE 四态、SESSION_CODE 五码', () => {
  const { sessions } = setup();
  assert.throws(() => sessions.connect({ principalUserId: 'user-a', transport: makeTransport() }), TypeError);
  assert.throws(() => sessions.connect({ agentId: 'agent-a', transport: makeTransport() }), TypeError);
  assert.throws(
    () => sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: makeTransport(), subscriptions: 'ch-1' }),
    TypeError
  );
  assert.throws(
    () => sessions.connect({ agentId: 'agent-a', principalUserId: 'user-a', transport: makeTransport(), subscriptions: [123] }),
    TypeError
  );
  assert.throws(() => new AgentSessionRegistry({ heartbeatTimeoutMs: 0 }), RangeError);
  assert.throws(() => new AgentSessionRegistry({ clock: 'now' }), TypeError);

  assert.deepEqual({ ...SESSION_STATE }, {
    CONNECTING: 'CONNECTING',
    ONLINE: 'ONLINE',
    OFFLINE: 'OFFLINE',
    REVOKED: 'REVOKED',
  });
  assert.deepEqual({ ...SESSION_CODE }, {
    UNKNOWN_AGENT: 'UNKNOWN_AGENT',
    ALREADY_ONLINE: 'ALREADY_ONLINE',
    NOT_ONLINE: 'NOT_ONLINE',
    TRANSPORT_REQUIRED: 'TRANSPORT_REQUIRED',
    SEND_FAILED: 'SEND_FAILED',
  });
  assert.ok(Object.isFrozen(SESSION_STATE));
  assert.ok(Object.isFrozen(SESSION_CODE));
});
