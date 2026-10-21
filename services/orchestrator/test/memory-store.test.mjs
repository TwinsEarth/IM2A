// 测试：三层记忆 + 按 Agent 隔离（契约 §6）。覆盖 MEMORY_ISOLATED / LAYER_INVALID / NOT_FOUND、digest 不泄露 content、purge。
import test from 'node:test';
import assert from 'node:assert/strict';

import { MEMORY_LAYER, MemoryIsolationError, MemoryStore } from '../src/memory-store.mjs';
import { makeClock } from './_helpers.mjs';

function setup() {
  const clock = makeClock();
  const store = new MemoryStore({ clock });
  return { clock, store };
}

const BUTLER = 'agent-butler';
const SUB = 'agent-sub';

test('MEMORY_LAYER 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...MEMORY_LAYER }, { WORKING: 'working', EPISODIC: 'episodic', SEMANTIC: 'semantic' });
  assert.ok(Object.isFrozen(MEMORY_LAYER));
});

test('write：返回冻结 record，含 id/agentId/userId/layer/content/tags/at/expiresAt', () => {
  const { store } = setup();
  const record = store.write({
    agentId: BUTLER,
    userId: 'user-a',
    layer: MEMORY_LAYER.WORKING,
    content: '用户喜欢火锅',
    tags: ['food', 'food', '', 42]
  });
  assert.ok(Object.isFrozen(record));
  assert.match(record.id, /^mem-/);
  assert.equal(record.agentId, BUTLER);
  assert.equal(record.userId, 'user-a');
  assert.equal(record.layer, 'working');
  assert.equal(record.content, '用户喜欢火锅');
  assert.deepEqual([...record.tags], ['food']);
  assert.equal(record.at, '2026-10-21T00:00:00.000Z');
  assert.equal(record.expiresAt, '2026-10-21T01:00:00.000Z'); // working 默认 1h
  assert.ok(Object.isFrozen(record.tags));
});

test('write：尊重各层默认 TTL，可选 ttlMs 覆盖，<=0 表示永不过期', () => {
  const { store } = setup();
  const episodic = store.write({
    agentId: BUTLER,
    userId: 'user-a',
    layer: MEMORY_LAYER.EPISODIC,
    content: 'x'
  });
  assert.equal(episodic.expiresAt, '2026-10-28T00:00:00.000Z');
  const semantic = store.write({
    agentId: BUTLER,
    userId: 'user-a',
    layer: MEMORY_LAYER.SEMANTIC,
    content: 'x'
  });
  assert.equal(semantic.expiresAt, '2027-01-19T00:00:00.000Z');
  const custom = store.write({
    agentId: BUTLER,
    userId: 'user-a',
    layer: MEMORY_LAYER.WORKING,
    content: 'x',
    ttlMs: 5000
  });
  assert.equal(custom.expiresAt, '2026-10-21T00:00:05.000Z');
  const forever = store.write({
    agentId: BUTLER,
    userId: 'user-a',
    layer: MEMORY_LAYER.WORKING,
    content: 'x',
    ttlMs: 0
  });
  assert.equal(forever.expiresAt, null);
});

test('read：按 agent/layer/user 过滤，按时间倒序，limit 生效，返回冻结数组', () => {
  const { store, clock } = setup();
  const first = store.write({ agentId: BUTLER, userId: 'user-a', layer: 'working', content: 'a' });
  clock.advance(1000);
  const second = store.write({ agentId: BUTLER, userId: 'user-a', layer: 'working', content: 'b' });
  clock.advance(1000);
  store.write({ agentId: BUTLER, userId: 'user-b', layer: 'working', content: 'c' });
  store.write({ agentId: BUTLER, userId: 'user-a', layer: 'episodic', content: 'd' });

  const records = store.read({ agentId: BUTLER, userId: 'user-a', layer: 'working' });
  assert.deepEqual(records.map((record) => record.content), ['b', 'a']);
  assert.ok(Object.isFrozen(records));
  assert.equal(store.read({ agentId: BUTLER, userId: 'user-a', layer: 'working', limit: 1 }).length, 1);
  assert.deepEqual(store.read({ agentId: BUTLER, userId: 'user-a', layer: 'working', limit: 0 }), []);
  assert.deepEqual(
    store.read({ agentId: BUTLER, layer: 'semantic' }).map((record) => record.content),
    []
  );
  assert.equal(store.read({ agentId: BUTLER, layer: 'episodic' })[0].id, store.read({ agentId: BUTLER, layer: 'episodic' })[0].id);
  assert.ok(first.id !== second.id);
});

test('MEMORY_ISOLATED：butler 越权读子 Agent 原始记忆（read 与 search 都拦截）', () => {
  const { store } = setup();
  store.write({ agentId: SUB, userId: 'user-a', layer: 'working', content: '子 Agent 的私密原始记忆' });
  assert.throws(
    () => store.read({ agentId: SUB, requesterAgentId: BUTLER, userId: 'user-a', layer: 'working' }),
    { code: 'MEMORY_ISOLATED' }
  );
  assert.throws(
    () =>
      store.search({
        agentId: SUB,
        requesterAgentId: BUTLER,
        userId: 'user-a',
        layer: 'working',
        query: '私密'
      }),
    { code: 'MEMORY_ISOLATED' }
  );
  // 子 Agent 自己可以读
  assert.equal(
    store.read({ agentId: SUB, requesterAgentId: SUB, userId: 'user-a', layer: 'working' }).length,
    1
  );
});

test('LAYER_INVALID：write/read/search 的 layer 必须属于三层之一', () => {
  const { store } = setup();
  assert.throws(
    () => store.write({ agentId: BUTLER, userId: 'user-a', layer: 'dream', content: 'x' }),
    { code: 'LAYER_INVALID' }
  );
  assert.throws(() => store.read({ agentId: BUTLER, userId: 'user-a', layer: 'dream' }), {
    code: 'LAYER_INVALID'
  });
  assert.throws(
    () => store.search({ agentId: BUTLER, userId: 'user-a', layer: 'dream', query: 'x' }),
    { code: 'LAYER_INVALID' }
  );
  assert.throws(() => store.read({ agentId: BUTLER, userId: 'user-a', layer: undefined }), {
    code: 'LAYER_INVALID'
  });
});

test('NOT_FOUND：write 缺 agentId/userId；purge 缺 userId（防止误删全库）', () => {
  const { store } = setup();
  assert.throws(
    () => store.write({ userId: 'user-a', layer: 'working', content: 'x' }),
    { code: 'NOT_FOUND' }
  );
  assert.throws(
    () => store.write({ agentId: BUTLER, layer: 'working', content: 'x' }),
    { code: 'NOT_FOUND' }
  );
  assert.throws(() => store.purge({}), { code: 'NOT_FOUND' });
  assert.throws(() => store.purge({ userId: '   ' }), { code: 'NOT_FOUND' });
});

test('边界：working 记忆 TTL 到期后 read/search 均不可见', () => {
  const { store, clock } = setup();
  store.write({ agentId: BUTLER, userId: 'user-a', layer: 'working', content: 'expiring hello' });
  clock.advance(60 * 60e3 - 1);
  assert.equal(store.read({ agentId: BUTLER, userId: 'user-a', layer: 'working' }).length, 1);
  clock.advance(1); // 恰到期
  assert.deepEqual(store.read({ agentId: BUTLER, userId: 'user-a', layer: 'working' }), []);
  assert.deepEqual(
    store.search({ agentId: BUTLER, userId: 'user-a', layer: 'working', query: 'hello' }),
    []
  );
});

test('search：确定性词频打分，命中数/长度决定排序；大小写无关；不命中不返回', () => {
  const { store } = setup();
  store.write({ agentId: BUTLER, userId: 'user-a', layer: 'working', content: 'Hello hello world' });
  store.write({ agentId: BUTLER, userId: 'user-a', layer: 'working', content: 'world only' });
  store.write({ agentId: BUTLER, userId: 'user-a', layer: 'working', content: 'unrelated' });

  const forHello = store.search({ agentId: BUTLER, userId: 'user-a', layer: 'working', query: 'HELLO' });
  assert.equal(forHello.length, 1);
  assert.ok(forHello[0].score > 0);
  assert.ok(Object.isFrozen(forHello[0]));
  assert.ok(Object.isFrozen(forHello[0].record));

  const forWorld = store.search({ agentId: BUTLER, userId: 'user-a', layer: 'working', query: 'world' });
  assert.equal(forWorld.length, 2);
  assert.ok(forWorld[0].score >= forWorld[1].score);
  // 更短内容同样命中数 => 分数更高（1+ln(len) 分母更小）
  assert.equal(forWorld[0].record.content, 'world only');

  assert.deepEqual(
    store.search({ agentId: BUTLER, userId: 'user-a', layer: 'working', query: 'nomatch' }),
    []
  );
  assert.deepEqual(store.search({ agentId: BUTLER, userId: 'user-a', layer: 'working', query: '' }), []);
  assert.equal(
    store.search({ agentId: BUTLER, userId: 'user-a', layer: 'working', query: 'world', limit: 1 }).length,
    1
  );
  // 确定性：同样查询结果一致
  assert.deepEqual(
    store.search({ agentId: BUTLER, userId: 'user-a', layer: 'working', query: 'world' }).map(
      (item) => item.record.id
    ),
    forWorld.map((item) => item.record.id)
  );
});

test('digest：只返回协调摘要，绝不含 content；按 Agent 聚合 count/lastAt/summary', () => {
  const { store, clock } = setup();
  store.write({
    agentId: SUB,
    userId: 'user-a',
    layer: 'working',
    content: 'SECRET-PLAN-子Agent原始内容',
    tags: ['x']
  });
  clock.advance(5000);
  store.write({
    agentId: SUB,
    userId: 'user-a',
    layer: 'episodic',
    content: 'SECRET-PLAN-第二条',
    tags: []
  });
  store.write({ agentId: 'agent-sibling', userId: 'user-a', layer: 'working', content: 'sibling' });
  store.write({ agentId: 'agent-other-user', userId: 'user-b', layer: 'working', content: 'other user' });
  store.write({ agentId: BUTLER, userId: 'user-a', layer: 'working', content: 'butler 自己的记忆' });

  const digest = store.digest({ butlerAgentId: BUTLER, userId: 'user-a' });
  assert.equal(digest.contentExposed, false);
  assert.ok(Object.isFrozen(digest));
  assert.ok(Object.isFrozen(digest.coordination));
  assert.deepEqual(
    digest.coordination.map((item) => item.agentId),
    ['agent-sibling', SUB]
  );
  const subDigest = digest.coordination.find((item) => item.agentId === SUB);
  assert.equal(subDigest.count, 2);
  assert.equal(subDigest.lastAt, '2026-10-21T00:00:05.000Z');
  assert.match(subDigest.summary, /2 条记忆/);
  assert.match(subDigest.summary, /working/);
  assert.match(subDigest.summary, /episodic/);
  // 关键隔离断言：digest 序列化后不得出现任何原始内容
  const serialized = JSON.stringify(digest);
  assert.equal(serialized.includes('SECRET-PLAN'), false);
  assert.equal(serialized.includes('butler 自己的记忆'), false);
  assert.equal(JSON.stringify(store.digest({ butlerAgentId: BUTLER, userId: 'nobody' })), JSON.stringify({ coordination: [], contentExposed: false }));
});

test('purge：只删目标用户记忆，返回 deleted/byAgent/tombstones（tombstone 不含 content）', () => {
  const { store } = setup();
  store.write({ agentId: BUTLER, userId: 'user-a', layer: 'working', content: 'a-butler' });
  store.write({ agentId: SUB, userId: 'user-a', layer: 'working', content: 'a-sub' });
  store.write({ agentId: SUB, userId: 'user-b', layer: 'working', content: 'b-sub' });

  const result = store.purge({ userId: 'user-a' });
  assert.equal(result.deleted, 2);
  assert.deepEqual({ ...result.byAgent }, { [BUTLER]: 1, [SUB]: 1 });
  assert.equal(result.tombstones.length, 2);
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.tombstones));
  for (const tombstone of result.tombstones) {
    assert.deepEqual(Object.keys(tombstone).sort(), ['agentId', 'at', 'id', 'layer']);
    assert.equal('content' in tombstone, false);
  }
  assert.deepEqual(store.read({ agentId: SUB, userId: 'user-a', layer: 'working' }), []);
  assert.equal(store.read({ agentId: SUB, userId: 'user-b', layer: 'working' }).length, 1);
  // 幂等：再次 purge 无副作用
  const again = store.purge({ userId: 'user-a' });
  assert.equal(again.deleted, 0);
  assert.deepEqual({ ...again.byAgent }, {});
  assert.deepEqual([...again.tombstones], []);
});

test('MemoryIsolationError 带 code 且是 Error 子类', () => {
  const { store } = setup();
  try {
    store.read({ agentId: SUB, requesterAgentId: BUTLER, userId: 'user-a', layer: 'working' });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof MemoryIsolationError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'MEMORY_ISOLATED');
  }
});
