// 测试文件：§14 频道模型（未读隔离 / CMD / STRANGER_QUEUE / 投递过滤 / 成员与权限边界）/ 运行：node services/messaging/test/channel.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  CHANNEL_CODE,
  CHANNEL_TYPE,
  MEMBER_KIND,
  MESSAGE_KIND,
  ChannelError,
  ChannelRegistry,
} from '../src/channel.mjs';
import * as barrel from '../src/index.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';

function setup() {
  let nowMs = Date.parse(BASE_ISO);
  const clock = () => new Date(nowMs);
  return {
    registry: new ChannelRegistry({ clock }),
    advance(ms) {
      nowMs += ms;
    },
  };
}

function createDirect(registry, channelId = 'ch-direct') {
  return registry.create({ channelId, type: CHANNEL_TYPE.DIRECT, ownerUserId: 'user-a', title: '好友会话' });
}
function joinHuman(registry, channelId, memberId, extra = {}) {
  return registry.join({ channelId, memberId, kind: MEMBER_KIND.HUMAN, ...extra });
}
function joinAgent(registry, channelId, agentId, extra = {}) {
  return registry.join({
    channelId,
    memberId: agentId,
    kind: MEMBER_KIND.AGENT,
    agentId,
    principalUserId: 'user-a',
    ...extra,
  });
}

test('create/get/members：频道视图冻结、成员视图冻结、未知频道 get 返回 undefined', () => {
  const { registry } = setup();
  const channel = createDirect(registry);
  assert.equal(channel.channelId, 'ch-direct');
  assert.equal(channel.type, CHANNEL_TYPE.DIRECT);
  assert.equal(channel.ownerUserId, 'user-a');
  assert.equal(channel.title, '好友会话');
  assert.ok(Object.isFrozen(channel));

  assert.equal(registry.get('ch-direct').channelId, 'ch-direct');
  assert.equal(registry.get('ch-none'), undefined);

  const member = joinHuman(registry, 'ch-direct', 'user-a');
  assert.equal(member.memberId, 'user-a');
  assert.equal(member.kind, MEMBER_KIND.HUMAN);
  assert.equal(member.subscribed, false);
  assert.ok(Object.isFrozen(member));
  const members = registry.members('ch-direct');
  assert.equal(members.length, 1);
  assert.ok(Object.isFrozen(members[0]));
  assert.deepEqual(registry.channelsOf('user-a'), ['ch-direct']);
  assert.deepEqual(registry.channelsOf('user-none'), []);
});

test('边界：CHANNEL_EXISTS / TYPE_INVALID / CHANNEL_UNKNOWN', () => {
  const { registry } = setup();
  createDirect(registry);
  assert.throws(
    () => registry.create({ channelId: 'ch-direct', type: CHANNEL_TYPE.GROUP, ownerUserId: 'user-a' }),
    (error) => error instanceof ChannelError && error.code === 'CHANNEL_EXISTS'
  );
  assert.throws(
    () => registry.create({ channelId: 'ch-bad', type: 'VIDEO_CALL', ownerUserId: 'user-a' }),
    (error) => error instanceof ChannelError && error.code === CHANNEL_CODE.TYPE_INVALID
  );
  const unknownCalls = [
    () => registry.members('ch-none'),
    () => registry.join({ channelId: 'ch-none', memberId: 'user-a', kind: MEMBER_KIND.HUMAN }),
    () => registry.leave({ channelId: 'ch-none', memberId: 'user-a' }),
    () => registry.post({ channelId: 'ch-none', memberId: 'user-a', kind: MESSAGE_KIND.TEXT }),
    () => registry.assertCanPost({ channelId: 'ch-none', memberId: 'user-a', kind: MESSAGE_KIND.TEXT }),
    () => registry.markRead({ memberId: 'user-a', channelId: 'ch-none' }),
    () => registry.messages({ channelId: 'ch-none' }),
  ];
  for (const call of unknownCalls) {
    assert.throws(call, (error) => error instanceof ChannelError && error.code === 'CHANNEL_UNKNOWN');
  }
});

test('成员边界：MEMBER_EXISTS；leave 非成员返回 false；离开后可重新加入', () => {
  const { registry } = setup();
  createDirect(registry);
  joinHuman(registry, 'ch-direct', 'user-b');
  assert.throws(
    () => joinHuman(registry, 'ch-direct', 'user-b'),
    (error) => error instanceof ChannelError && error.code === CHANNEL_CODE.MEMBER_EXISTS
  );
  assert.equal(registry.leave({ channelId: 'ch-direct', memberId: 'user-b' }), true);
  assert.equal(registry.leave({ channelId: 'ch-direct', memberId: 'user-none' }), false);
  assert.deepEqual(registry.members('ch-direct'), []);
  joinHuman(registry, 'ch-direct', 'user-b');
  assert.equal(registry.members('ch-direct').length, 1);
});

test('非成员 post / markRead → NOT_MEMBER', () => {
  const { registry } = setup();
  createDirect(registry);
  joinHuman(registry, 'ch-direct', 'user-a');
  assert.throws(
    () => registry.post({ channelId: 'ch-direct', memberId: 'user-x', kind: MESSAGE_KIND.TEXT }),
    (error) => error instanceof ChannelError && error.code === CHANNEL_CODE.NOT_MEMBER
  );
  assert.throws(
    () => registry.markRead({ memberId: 'user-x', channelId: 'ch-direct' }),
    (error) => error instanceof ChannelError && error.code === CHANNEL_CODE.NOT_MEMBER
  );
  assert.throws(
    () => registry.assertCanPost({ channelId: 'ch-direct', memberId: 'user-x', kind: MESSAGE_KIND.TEXT }),
    { code: 'NOT_MEMBER' }
  );
});

test('DIRECT 未读：其他人类成员 +1、发送者不 +1、markRead 清零，返回值冻结', () => {
  const { registry } = setup();
  createDirect(registry);
  joinHuman(registry, 'ch-direct', 'user-a');
  joinHuman(registry, 'ch-direct', 'user-b');

  const result = registry.post({
    channelId: 'ch-direct',
    memberId: 'user-a',
    kind: MESSAGE_KIND.TEXT,
    payload: { text: '你好' },
  });
  assert.equal(result.stored, true);
  assert.equal(result.unreadDelta, 1);
  assert.deepEqual([...result.deliveredTo], ['user-b']);
  assert.equal(result.message.seq, 1);
  assert.equal(result.message.channelId, 'ch-direct');
  assert.equal(result.message.senderId, 'user-a');
  assert.equal(result.message.senderKind, MEMBER_KIND.HUMAN);
  assert.equal(result.message.stored, true);
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.message));
  assert.ok(Object.isFrozen(result.message.payload));
  assert.ok(Object.isFrozen(result.deliveredTo));

  assert.equal(registry.unreadFor({ memberId: 'user-b' }).total, 1);
  assert.equal(registry.unreadFor({ memberId: 'user-a' }).total, 0, '发送者自己不累计未读');
  assert.deepEqual({ ...registry.unreadFor({ memberId: 'user-b' }).byChannel }, { 'ch-direct': 1 });
  assert.equal(registry.markRead({ memberId: 'user-b', channelId: 'ch-direct' }), 1);
  assert.equal(registry.unreadFor({ memberId: 'user-b' }).total, 0);
  assert.equal(registry.markRead({ memberId: 'user-b', channelId: 'ch-direct' }), 0);
});

test('未读聚合：unreadFor 跨频道汇总 byChannel，markRead 只清指定频道', () => {
  const { registry } = setup();
  createDirect(registry, 'ch-1');
  createDirect(registry, 'ch-2');
  for (const channelId of ['ch-1', 'ch-2']) {
    joinHuman(registry, channelId, 'user-a');
    joinHuman(registry, channelId, 'user-b');
  }
  registry.post({ channelId: 'ch-1', memberId: 'user-a', kind: MESSAGE_KIND.TEXT });
  registry.post({ channelId: 'ch-1', memberId: 'user-a', kind: MESSAGE_KIND.TEXT });
  registry.post({ channelId: 'ch-2', memberId: 'user-a', kind: MESSAGE_KIND.TEXT });

  const unread = registry.unreadFor({ memberId: 'user-b' });
  assert.equal(unread.total, 3);
  assert.deepEqual({ ...unread.byChannel }, { 'ch-1': 2, 'ch-2': 1 });
  assert.equal(registry.markRead({ memberId: 'user-b', channelId: 'ch-1' }), 2);
  const after = registry.unreadFor({ memberId: 'user-b' });
  assert.equal(after.total, 1);
  assert.deepEqual({ ...after.byChannel }, { 'ch-2': 1 });
});

test('未读隔离：AGENT_NEGOTIATION 对人类成员未读恒为 0，人类 POST → POST_DENIED', () => {
  const { registry } = setup();
  registry.create({ channelId: 'neg-1', type: CHANNEL_TYPE.AGENT_NEGOTIATION, ownerUserId: 'user-a', title: '协商' });
  joinHuman(registry, 'neg-1', 'user-a');
  joinAgent(registry, 'neg-1', 'agent-a');
  joinAgent(registry, 'neg-1', 'agent-b');
  assert.equal(registry.setSubscription({ channelId: 'neg-1', memberId: 'agent-a' }), true);
  assert.equal(registry.setSubscription({ channelId: 'neg-1', memberId: 'agent-b', subscribed: true }), true);

  // 协商中间态：Agent 之间多轮，人类未读恒 0（不制造红点）
  const first = registry.post({
    channelId: 'neg-1',
    memberId: 'agent-a',
    kind: MESSAGE_KIND.AGENT_TURN,
    payload: { intent: 'PROPOSE' },
  });
  const second = registry.post({
    channelId: 'neg-1',
    memberId: 'agent-b',
    kind: MESSAGE_KIND.AGENT_TURN,
    payload: { intent: 'COUNTER' },
  });
  assert.equal(first.unreadDelta, 0);
  assert.equal(second.unreadDelta, 0);
  assert.deepEqual([...first.deliveredTo], ['agent-b']);
  assert.deepEqual([...second.deliveredTo], ['agent-a']);
  assert.equal(registry.unreadFor({ memberId: 'user-a' }).total, 0, 'Agent 协商不得制造人类未读');
  assert.deepEqual({ ...registry.unreadFor({ memberId: 'user-a' }).byChannel }, {});

  // 人类不能往协商频道 POST
  assert.throws(
    () => registry.post({ channelId: 'neg-1', memberId: 'user-a', kind: MESSAGE_KIND.TEXT }),
    (error) => error instanceof ChannelError && error.code === CHANNEL_CODE.POST_DENIED
  );
  assert.throws(
    () => registry.assertCanPost({ channelId: 'neg-1', memberId: 'user-a', kind: MESSAGE_KIND.TEXT }),
    { code: 'POST_DENIED' }
  );
  assert.equal(registry.assertCanPost({ channelId: 'neg-1', memberId: 'agent-a', kind: MESSAGE_KIND.AGENT_TURN }), true);
});

test('CMD：stored=false、不计未读、不投递人类、只投递给已订阅 Agent，且不入库', () => {
  const { registry } = setup();
  registry.create({ channelId: 'grp-1', type: CHANNEL_TYPE.GROUP, ownerUserId: 'user-a' });
  joinHuman(registry, 'grp-1', 'user-a');
  joinHuman(registry, 'grp-1', 'user-b');
  joinAgent(registry, 'grp-1', 'agent-a');
  joinAgent(registry, 'grp-1', 'agent-b');
  joinAgent(registry, 'grp-1', 'agent-c'); // 未订阅
  registry.setSubscription({ channelId: 'grp-1', memberId: 'agent-a', subscribed: true });
  registry.setSubscription({ channelId: 'grp-1', memberId: 'agent-b', subscribed: true });

  const result = registry.post({
    channelId: 'grp-1',
    memberId: 'agent-a',
    kind: MESSAGE_KIND.CMD,
    payload: { wake: true },
  });
  assert.equal(result.stored, false);
  assert.equal(result.message.stored, false);
  assert.deepEqual([...result.deliveredTo], ['agent-b'], 'CMD 只投递给已订阅的 Agent 成员');
  assert.equal(result.unreadDelta, 0);
  assert.equal(registry.unreadFor({ memberId: 'user-b' }).total, 0, 'CMD 不计人类未读');
  assert.equal(registry.messages({ channelId: 'grp-1' }).length, 0, 'CMD 不入库');

  // 普通消息才入库并产生未读
  const text = registry.post({ channelId: 'grp-1', memberId: 'user-a', kind: MESSAGE_KIND.TEXT, payload: { t: 1 } });
  assert.equal(text.stored, true);
  assert.equal(registry.messages({ channelId: 'grp-1' }).length, 1);
  assert.deepEqual([...text.deliveredTo], ['user-b', 'agent-a', 'agent-b'], '人类成员 + 已订阅 Agent');
});

test('CMD 边界：显式入库 → CMD_NOT_PERSISTED；未订阅 Agent 发 CMD → AGENT_NOT_SUBSCRIBED', () => {
  const { registry } = setup();
  registry.create({ channelId: 'grp-2', type: CHANNEL_TYPE.GROUP, ownerUserId: 'user-a' });
  joinAgent(registry, 'grp-2', 'agent-a');
  joinAgent(registry, 'grp-2', 'agent-b');
  registry.setSubscription({ channelId: 'grp-2', memberId: 'agent-a', subscribed: true });

  assert.throws(
    () => registry.post({ channelId: 'grp-2', memberId: 'agent-a', kind: MESSAGE_KIND.CMD, persist: true }),
    (error) => error instanceof ChannelError && error.code === CHANNEL_CODE.CMD_NOT_PERSISTED
  );
  assert.throws(
    () => registry.post({ channelId: 'grp-2', memberId: 'agent-b', kind: MESSAGE_KIND.CMD }),
    (error) => error instanceof ChannelError && error.code === CHANNEL_CODE.AGENT_NOT_SUBSCRIBED
  );
  assert.equal(registry.messages({ channelId: 'grp-2' }).length, 0);
});

test('投递过滤：Agent 未订阅不在 deliveredTo，订阅后出现，取消订阅后消失', () => {
  const { registry } = setup();
  createDirect(registry);
  joinHuman(registry, 'ch-direct', 'user-a');
  joinHuman(registry, 'ch-direct', 'user-b');
  joinAgent(registry, 'ch-direct', 'agent-a');

  const before = registry.post({ channelId: 'ch-direct', memberId: 'user-a', kind: MESSAGE_KIND.TEXT });
  assert.deepEqual([...before.deliveredTo], ['user-b'], '未订阅 Agent 不投递');

  registry.setSubscription({ channelId: 'ch-direct', memberId: 'agent-a', subscribed: true });
  assert.equal(registry.isSubscribed({ channelId: 'ch-direct', memberId: 'agent-a' }), true);
  const after = registry.post({ channelId: 'ch-direct', memberId: 'user-a', kind: MESSAGE_KIND.TEXT });
  assert.deepEqual([...after.deliveredTo], ['user-b', 'agent-a']);

  registry.setSubscription({ channelId: 'ch-direct', memberId: 'agent-a', subscribed: false });
  const unsubscribed = registry.post({ channelId: 'ch-direct', memberId: 'user-a', kind: MESSAGE_KIND.TEXT });
  assert.deepEqual([...unsubscribed.deliveredTo], ['user-b']);
  // 未知频道/成员的订阅设置安全返回 false
  assert.equal(registry.setSubscription({ channelId: 'ch-none', memberId: 'agent-a' }), false);
  assert.equal(registry.setSubscription({ channelId: 'ch-direct', memberId: 'agent-none' }), false);
  assert.equal(registry.isSubscribed({ channelId: 'ch-direct', memberId: 'agent-none' }), false);
});

test('STRANGER_QUEUE：陌生人入队不产生对方未读，转审批卡片进 DIRECT 后才产生未读', () => {
  const { registry } = setup();
  registry.create({ channelId: 'queue-user-b', type: CHANNEL_TYPE.STRANGER_QUEUE, ownerUserId: 'user-b' });
  joinHuman(registry, 'queue-user-b', 'user-stranger');
  joinHuman(registry, 'queue-user-b', 'user-b'); // 对方也在队列频道：用于验证「不产生未读」不是靠不在场实现
  joinAgent(registry, 'queue-user-b', 'agent-b', { principalUserId: 'user-b' });
  registry.setSubscription({ channelId: 'queue-user-b', memberId: 'agent-b', subscribed: true });

  const queued = registry.post({
    channelId: 'queue-user-b',
    memberId: 'user-stranger',
    kind: MESSAGE_KIND.INTENT,
    payload: { text: '想认识你' },
  });
  assert.equal(queued.stored, true, '入队消息要存储（等待审批）');
  assert.equal(queued.unreadDelta, 0);
  assert.equal(registry.unreadFor({ memberId: 'user-b' }).total, 0, '陌生入站不得产生对方未读');
  assert.deepEqual([...queued.deliveredTo], ['agent-b'], '队列消息只给已订阅 Agent（供预审）');

  // 预审通过 → 转成审批卡片进入 DIRECT，此时才产生人类未读
  registry.create({ channelId: 'direct-user-b', type: CHANNEL_TYPE.DIRECT, ownerUserId: 'user-b' });
  joinHuman(registry, 'direct-user-b', 'user-b');
  joinAgent(registry, 'direct-user-b', 'agent-b', { principalUserId: 'user-b' });
  const card = registry.post({
    channelId: 'direct-user-b',
    memberId: 'agent-b',
    kind: MESSAGE_KIND.APPROVAL_CARD,
    payload: { from: 'user-stranger' },
  });
  assert.equal(card.unreadDelta, 1);
  assert.deepEqual([...card.deliveredTo], ['user-b']);
  const unread = registry.unreadFor({ memberId: 'user-b' });
  assert.equal(unread.total, 1);
  assert.equal(unread.byChannel['queue-user-b'] ?? 0, 0, '队列频道未读恒 0');
  assert.equal(unread.byChannel['direct-user-b'], 1);
});

test('未读是人类视角：Agent 成员不累计未读，但会出现在投递列表', () => {
  const { registry } = setup();
  createDirect(registry);
  joinHuman(registry, 'ch-direct', 'user-a');
  joinAgent(registry, 'ch-direct', 'agent-a');
  registry.setSubscription({ channelId: 'ch-direct', memberId: 'agent-a', subscribed: true });

  const fromHuman = registry.post({ channelId: 'ch-direct', memberId: 'user-a', kind: MESSAGE_KIND.TEXT });
  assert.deepEqual([...fromHuman.deliveredTo], ['agent-a']);
  assert.equal(registry.unreadFor({ memberId: 'agent-a' }).total, 0, 'Agent 没有未读红点');

  const fromAgent = registry.post({ channelId: 'ch-direct', memberId: 'agent-a', kind: MESSAGE_KIND.TEXT });
  assert.deepEqual([...fromAgent.deliveredTo], ['user-a']);
  assert.equal(registry.unreadFor({ memberId: 'user-a' }).total, 1);
  assert.equal(registry.unreadFor({ memberId: 'agent-a' }).total, 0);
});

test('seq：默认按频道单调递增，显式 seq 生效并同步后续序号', () => {
  const { registry } = setup();
  createDirect(registry);
  joinHuman(registry, 'ch-direct', 'user-a');
  joinHuman(registry, 'ch-direct', 'user-b');
  const post = (seq = null) =>
    registry.post({ channelId: 'ch-direct', memberId: 'user-a', kind: MESSAGE_KIND.TEXT, seq });

  assert.equal(post().message.seq, 1);
  assert.equal(post().message.seq, 2);
  assert.equal(post(42).message.seq, 42);
  assert.equal(post().message.seq, 43, '显式 seq 之后自动序号从 42 继续');
  const late = post(5); // 迟到的显式 seq 不回退计数器
  assert.equal(late.message.seq, 5);
  assert.equal(post().message.seq, 44);
});

test('查询与清理：messages 支持 limit；leave 清理未读；无效消息 kind → TypeError', () => {
  const { registry } = setup();
  createDirect(registry);
  joinHuman(registry, 'ch-direct', 'user-a');
  joinHuman(registry, 'ch-direct', 'user-b');
  for (let i = 0; i < 3; i += 1) {
    registry.post({ channelId: 'ch-direct', memberId: 'user-a', kind: MESSAGE_KIND.TEXT, payload: { i } });
  }
  assert.equal(registry.messages({ channelId: 'ch-direct' }).length, 3);
  assert.equal(registry.messages({ channelId: 'ch-direct', limit: 2 }).length, 2);
  assert.deepEqual(registry.messages({ channelId: 'ch-direct', limit: 2 }).map((m) => m.payload.i), [1, 2]);
  const copy = registry.messages({ channelId: 'ch-direct' });
  copy.length = 0;
  assert.equal(registry.messages({ channelId: 'ch-direct' }).length, 3, 'messages 返回副本');

  assert.equal(registry.unreadFor({ memberId: 'user-b' }).total, 3);
  registry.leave({ channelId: 'ch-direct', memberId: 'user-b' });
  assert.equal(registry.unreadFor({ memberId: 'user-b' }).total, 0, '离开频道清理未读');

  assert.throws(
    () => registry.post({ channelId: 'ch-direct', memberId: 'user-a', kind: 'VIDEO' }),
    TypeError
  );
});

test('barrel：保留 §10 既有导出并追加 §14/§15 导出', () => {
  const names = [
    // §10（必须仍然存在）
    'BIZ_TYPE', 'makeEnvelope', 'validateEnvelope', 'EnvelopeError', 'fnv1a32', 'SeqAllocator', 'DedupeStore',
    // §14
    'CHANNEL_TYPE', 'MEMBER_KIND', 'MESSAGE_KIND', 'CHANNEL_CODE', 'ChannelError', 'ChannelRegistry',
    // §15
    'SESSION_STATE', 'SESSION_CODE', 'SessionError', 'AgentSessionRegistry',
  ];
  for (const name of names) {
    assert.ok(Object.hasOwn(barrel, name), `index.mjs 缺少导出 ${name}`);
    assert.notEqual(barrel[name], undefined);
  }
  assert.equal(barrel.CHANNEL_TYPE.DIRECT, 'DIRECT');
  assert.equal(barrel.MEMBER_KIND.AGENT, 'agent');
  assert.equal(barrel.MESSAGE_KIND.CMD, 'CMD');
  assert.equal(typeof barrel.ChannelRegistry, 'function');
  assert.equal(typeof barrel.AgentSessionRegistry, 'function');
});
