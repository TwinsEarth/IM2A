// 测试文件：§29 Agent 持久邮箱（进程级恢复/幂等/cursor 单调/容量/坏行容忍/多 agent 隔离）/ 运行：node services/messaging/test/agent-mailbox.test.mjs
import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { appendFileSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { AgentMailboxError, AgentMailboxStore, MAILBOX_CODE } from '../src/agent-mailbox.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';
const tempDirs = [];

function makeTempDir() {
  const dir = mkdtempSync(join(tmpdir(), 'agent-mailbox-'));
  tempDirs.push(dir);
  return dir;
}

after(() => {
  for (const dir of tempDirs) rmSync(dir, { recursive: true, force: true });
});

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

function makeStore({ dir = makeTempDir(), capacityPerAgent = 1000 } = {}) {
  const clock = makeClock();
  return { dir, clock, store: new AgentMailboxStore({ dir, clock, capacityPerAgent }) };
}

const msg = (id, extra = {}) => ({ id, text: `message-${id}`, ...extra });

test('deliver/fetch：seq 从 1 递增、limit 分页、nextCursor/pending 正确且返回值冻结', () => {
  const { store } = makeStore();
  const first = store.deliver({ toAgentId: 'agent-a', message: msg('m1') });
  assert.deepEqual({ ...first }, { stored: true, duplicate: false, seq: 1 });
  assert.equal(store.deliver({ toAgentId: 'agent-a', message: msg('m2') }).seq, 2);

  const all = store.fetch({ agentId: 'agent-a' });
  assert.deepEqual(all.messages.map((item) => item.id), ['m1', 'm2']);
  assert.equal(all.nextCursor, 2);
  assert.equal(all.pending, 0);
  assert.ok(Object.isFrozen(all));
  assert.ok(Object.isFrozen(all.messages));
  assert.ok(Object.isFrozen(all.messages[0]));

  const page1 = store.fetch({ agentId: 'agent-a', limit: 1 });
  assert.deepEqual(page1.messages.map((item) => item.id), ['m1']);
  assert.equal(page1.nextCursor, 1);
  assert.equal(page1.pending, 1);

  const page2 = store.fetch({ agentId: 'agent-a', cursor: 1, limit: 10 });
  assert.deepEqual(page2.messages.map((item) => item.id), ['m2']);
  assert.equal(page2.nextCursor, 2);
  assert.equal(page2.pending, 0);

  // 到末尾后再 fetch：空页、pending 0
  const tail = store.fetch({ agentId: 'agent-a', cursor: 2 });
  assert.deepEqual([...tail.messages], []);
  assert.equal(tail.nextCursor, 2);
  assert.equal(tail.pending, 0);

  // 默认 cursor 从已 ack 位移开始（未 ack → 0）
  store.ack({ agentId: 'agent-a', cursor: 1 });
  assert.deepEqual(store.fetch({ agentId: 'agent-a' }).messages.map((item) => item.id), ['m2']);
});

test('进程级恢复：store1 投递 2 条只 ack 1 条 → 同目录新 store2 恢复 pending=1、history 完整、ack 保持', () => {
  const dir = makeTempDir();
  const store1 = new AgentMailboxStore({ dir, clock: makeClock() });
  store1.deliver({ toAgentId: 'agent-a', message: msg('m1') });
  store1.deliver({ toAgentId: 'agent-a', message: msg('m2') });
  store1.ack({ agentId: 'agent-a', cursor: 1 });
  assert.equal(store1.stats('agent-a').pending, 1);

  // 模拟进程崩溃/换机器：同一 dir 新建实例，不共享任何内存
  const store2 = new AgentMailboxStore({ dir, clock: makeClock() });
  const stats = store2.stats('agent-a');
  assert.equal(stats.delivered, 2);
  assert.equal(stats.acked, 1);
  assert.equal(stats.pending, 1);
  assert.ok(stats.sizeBytes > 0);
  assert.deepEqual(store2.history('agent-a').map((item) => item.id), ['m1', 'm2'], 'history 完整');
  assert.deepEqual(
    store2.fetch({ agentId: 'agent-a' }).messages.map((item) => item.id),
    ['m2'],
    '未 ack 的 m2 必须恢复回来'
  );

  // 新实例继续投递，seq 延续；旧实例重新读盘同样看得到
  assert.equal(store2.deliver({ toAgentId: 'agent-a', message: msg('m3') }).seq, 3);
  assert.deepEqual(store1.fetch({ agentId: 'agent-a' }).messages.map((item) => item.id), ['m2', 'm3']);
});

test('幂等：同 message.id 重复投递 → duplicate:true 且 history 只有 1 条（跨实例同样生效）', () => {
  const dir = makeTempDir();
  const store1 = new AgentMailboxStore({ dir, clock: makeClock() });
  const first = store1.deliver({ toAgentId: 'agent-a', message: msg('m1') });
  const retry = store1.deliver({ toAgentId: 'agent-a', message: msg('m1', { text: '重试' }) });
  assert.equal(first.duplicate, false);
  assert.equal(retry.duplicate, true);
  assert.equal(retry.stored, false);
  assert.equal(retry.seq, first.seq, '重复投递返回首次入队 seq');
  assert.equal(store1.history('agent-a').length, 1);

  const store2 = new AgentMailboxStore({ dir, clock: makeClock() });
  assert.equal(store2.deliver({ toAgentId: 'agent-a', message: msg('m1') }).duplicate, true, '跨实例也去重');
  assert.equal(store2.history('agent-a').length, 1);
  // 可选严格模式（加法参数）：rejectDuplicate=true 时重复投递抛 DUPLICATE_MESSAGE
  assert.throws(
    () => store2.deliver({ toAgentId: 'agent-a', message: msg('m1'), rejectDuplicate: true }),
    (error) => error instanceof AgentMailboxError && error.code === MAILBOX_CODE.DUPLICATE_MESSAGE
  );
  assert.equal(store2.history('agent-a').length, 1, '严格模式同样不重复入队');
  // 同一 id 投给另一个 agent 是独立邮箱，不算重复
  assert.equal(store2.deliver({ toAgentId: 'agent-b', message: msg('m1') }).duplicate, false);
});

test('cursor 单调：回退/乱序/负数/非整数 → CURSOR_INVALID；正常推进与幂等 ack 可用', () => {
  const { store } = makeStore();
  store.deliver({ toAgentId: 'agent-a', message: msg('m1') });
  store.deliver({ toAgentId: 'agent-a', message: msg('m2') });
  store.ack({ agentId: 'agent-a', cursor: 1 });

  for (const cursor of [-1, 1.5, '1', 0, 3]) {
    assert.throws(
      () => store.fetch({ agentId: 'agent-a', cursor }),
      (error) => error instanceof AgentMailboxError && error.code === MAILBOX_CODE.CURSOR_INVALID,
      `cursor=${String(cursor)} 应被拒绝`
    );
  }
  assert.deepEqual(store.fetch({ agentId: 'agent-a', cursor: 1 }).messages.map((item) => item.id), ['m2']);

  assert.equal(store.ack({ agentId: 'agent-a', cursor: 2 }).acked, 2);
  assert.equal(store.ack({ agentId: 'agent-a', cursor: 2 }).acked, 2, '同 cursor 重复 ack 幂等');
  for (const cursor of [1, 3, -1, 1.5]) {
    assert.throws(
      () => store.ack({ agentId: 'agent-a', cursor }),
      (error) => error instanceof AgentMailboxError && error.code === MAILBOX_CODE.CURSOR_INVALID
    );
  }
});

test('容量：capacityPerAgent=2 时第 3 条 CAPACITY_EXCEEDED，前 2 条仍可读；ack 释放容量后可继续投递', () => {
  const { store } = makeStore({ capacityPerAgent: 2 });
  store.deliver({ toAgentId: 'agent-a', message: msg('m1') });
  store.deliver({ toAgentId: 'agent-a', message: msg('m2') });
  assert.throws(
    () => store.deliver({ toAgentId: 'agent-a', message: msg('m3') }),
    (error) => error instanceof AgentMailboxError && error.code === MAILBOX_CODE.CAPACITY_EXCEEDED
  );
  // 不静默丢：前 2 条仍完整可读
  assert.deepEqual(store.history('agent-a').map((item) => item.id), ['m1', 'm2']);
  assert.deepEqual(store.fetch({ agentId: 'agent-a' }).messages.map((item) => item.id), ['m1', 'm2']);
  assert.equal(store.stats('agent-a').pending, 2);

  store.ack({ agentId: 'agent-a', cursor: 1 });
  assert.equal(store.deliver({ toAgentId: 'agent-a', message: msg('m3') }).seq, 3, 'ack 释放容量后可继续投递');
  assert.throws(
    () => store.deliver({ toAgentId: 'agent-a', message: msg('m4') }),
    { code: 'CAPACITY_EXCEEDED' }
  );
  // 重复投递不占容量（幂等优先于容量判定）
  assert.equal(store.deliver({ toAgentId: 'agent-a', message: msg('m1') }).duplicate, true);
});

test('文件损坏容忍：手工追加垃圾行/半行后，新实例跳过坏行仍读出合法消息并可继续投递', () => {
  const dir = makeTempDir();
  const store1 = new AgentMailboxStore({ dir, clock: makeClock() });
  store1.deliver({ toAgentId: 'agent-a', message: msg('m1') });
  store1.deliver({ toAgentId: 'agent-a', message: msg('m2') });
  const file = join(dir, 'agent-a.jsonl');
  appendFileSync(file, 'THIS IS NOT JSON\n', 'utf8');
  appendFileSync(file, '{"type":"deliver","seq":99,"message":{', 'utf8'); // 崩溃留下的半行（无换行）

  const store2 = new AgentMailboxStore({ dir, clock: makeClock() });
  assert.deepEqual(store2.history('agent-a').map((item) => item.id), ['m1', 'm2'], '坏行被跳过');
  assert.equal(store2.stats('agent-a').delivered, 2);

  // 半行之后继续写：新记录不能被粘到坏行上（append 前补换行）
  assert.equal(store2.deliver({ toAgentId: 'agent-a', message: msg('m3') }).seq, 3);
  const store3 = new AgentMailboxStore({ dir, clock: makeClock() });
  assert.deepEqual(store3.history('agent-a').map((item) => item.id), ['m1', 'm2', 'm3']);
  store3.ack({ agentId: 'agent-a', cursor: 2 });
  assert.equal(store3.stats('agent-a').pending, 1);
});

test('多 agent 隔离：A 的邮箱不出现 B 的消息，文件各自独立', () => {
  const { store, dir } = makeStore();
  store.deliver({ toAgentId: 'agent-a', message: msg('a-1') });
  store.deliver({ toAgentId: 'agent-b', message: msg('b-1') });
  assert.deepEqual(store.history('agent-a').map((item) => item.id), ['a-1']);
  assert.deepEqual(store.history('agent-b').map((item) => item.id), ['b-1']);
  assert.deepEqual(store.fetch({ agentId: 'agent-a' }).messages.map((item) => item.id), ['a-1']);
  assert.equal(store.stats('agent-b').delivered, 1);
  const fileA = readFileSync(join(dir, 'agent-a.jsonl'), 'utf8');
  assert.ok(fileA.includes('a-1'));
  assert.ok(!fileA.includes('b-1'));
});

test('未知邮箱：fetch/ack/history/stats → MAILBOX_UNKNOWN；deliver 才创建邮箱', () => {
  const { store } = makeStore();
  const unknownCalls = [
    () => store.fetch({ agentId: 'agent-none' }),
    () => store.ack({ agentId: 'agent-none', cursor: 0 }),
    () => store.history('agent-none'),
    () => store.stats('agent-none'),
  ];
  for (const call of unknownCalls) {
    assert.throws(call, (error) => error instanceof AgentMailboxError && error.code === MAILBOX_CODE.MAILBOX_UNKNOWN);
  }
  store.deliver({ toAgentId: 'agent-none', message: msg('m1') });
  assert.equal(store.stats('agent-none').delivered, 1);
});

test('消息非法：缺 message / 无 id / 空 id / 循环引用 → MESSAGE_INVALID', () => {
  const { store } = makeStore();
  const invalidMessages = [undefined, null, 'text', 42, [], {}, { id: '' }, { id: 123 }];
  for (const message of invalidMessages) {
    assert.throws(
      () => store.deliver({ toAgentId: 'agent-a', message }),
      (error) => error instanceof AgentMailboxError && error.code === MAILBOX_CODE.MESSAGE_INVALID,
      `应拒绝 message=${JSON.stringify(message)}`
    );
  }
  const circular = { id: 'm-circular' };
  circular.self = circular;
  assert.throws(() => store.deliver({ toAgentId: 'agent-a', message: circular }), { code: 'MESSAGE_INVALID' });
  assert.throws(() => store.deliver({ toAgentId: 'agent-a' }), { code: 'MESSAGE_INVALID' });
  // 消息非法不得创建文件/入队（邮箱仍不存在）
  assert.throws(() => store.history('agent-a'), { code: 'MAILBOX_UNKNOWN' });
});

test('stats/history：只读冻结，sizeBytes 随写入增长，at 使用注入 clock', () => {
  const { store, dir } = makeStore();
  store.deliver({ toAgentId: 'agent-a', message: msg('m1') });
  const first = store.stats('agent-a');
  assert.deepEqual(Object.keys(first).sort(), ['acked', 'delivered', 'pending', 'sizeBytes']);
  assert.equal(first.delivered, 1);
  assert.equal(first.acked, 0);
  assert.equal(first.pending, 1);
  assert.ok(Object.isFrozen(first));

  store.deliver({ toAgentId: 'agent-a', message: msg('m2') });
  assert.ok(store.stats('agent-a').sizeBytes > first.sizeBytes);
  assert.ok(readFileSync(join(dir, 'agent-a.jsonl'), 'utf8').includes(BASE_ISO));

  const history = store.history('agent-a');
  assert.ok(Object.isFrozen(history));
  assert.ok(Object.isFrozen(history[0]));
  assert.throws(() => history.push('junk'), TypeError);
});

test('参数校验：dir / agentId / limit / capacityPerAgent 非法 → TypeError / RangeError', () => {
  assert.throws(() => new AgentMailboxStore({}), TypeError);
  assert.throws(() => new AgentMailboxStore({ dir: '' }), TypeError);
  assert.throws(() => new AgentMailboxStore({ dir: makeTempDir(), capacityPerAgent: 0 }), RangeError);
  assert.throws(() => new AgentMailboxStore({ dir: makeTempDir(), capacityPerAgent: 1.5 }), RangeError);
  assert.throws(() => new AgentMailboxStore({ dir: makeTempDir(), clock: 'now' }), TypeError);

  const { store } = makeStore();
  assert.throws(() => store.deliver({ toAgentId: '', message: msg('m') }), TypeError);
  assert.throws(() => store.deliver({ toAgentId: '../evil', message: msg('m') }), TypeError);
  assert.throws(() => store.deliver({ toAgentId: 'agent/a', message: msg('m') }), TypeError);
  store.deliver({ toAgentId: 'agent-a', message: msg('m1') });
  assert.throws(() => store.fetch({ agentId: 'agent-a', limit: 0 }), TypeError);
  assert.throws(() => store.fetch({ agentId: 'agent-a', limit: 1.5 }), TypeError);
  assert.throws(() => store.history('agent/a'), TypeError);
});
