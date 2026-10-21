// 测试文件：序号分配 §10（fnv1a32 确定性 / 会话内单调 / 跨会话独立 / 64 分片 / 大量分配无重复）/ 运行：node services/messaging/test/seq.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fnv1a32, SeqAllocator } from '../src/seq.mjs';

test('fnv1a32：空串等于 FNV offset basis，返回值是 uint32 且同输入同输出', () => {
  assert.equal(fnv1a32(''), 0x811c9dc5);
  const samples = ['user-a', 'user-b', 'user-a', '张三', '👨‍👩‍👧', 'a'.repeat(300)];
  for (const sample of samples) {
    const first = fnv1a32(sample);
    const second = fnv1a32(sample);
    assert.equal(first, second, `fnv1a32(${sample}) 必须确定`);
    assert.ok(Number.isInteger(first));
    assert.ok(first >= 0 && first <= 0xffffffff, '必须是 uint32');
  }
  // 不同输入应得到不同哈希（抽样断言，非严格数学保证）
  assert.notEqual(fnv1a32('user-a'), fnv1a32('user-b'));
  assert.notEqual(fnv1a32('alice'), fnv1a32('bob'));
});

test('同会话严格递增：从 1 开始，每次 +1', () => {
  const allocator = new SeqAllocator();
  const seqs = [];
  for (let i = 0; i < 5; i += 1) {
    seqs.push(allocator.next({ userId: 'user-a', conversationId: 'neg-1' }).seq);
  }
  assert.deepEqual(seqs, [1, 2, 3, 4, 5]);
});

test('跨会话互不影响：各自从 1 开始', () => {
  const allocator = new SeqAllocator();
  assert.equal(allocator.next({ userId: 'user-a', conversationId: 'conv-a' }).seq, 1);
  assert.equal(allocator.next({ userId: 'user-a', conversationId: 'conv-a' }).seq, 2);
  assert.equal(allocator.next({ userId: 'user-a', conversationId: 'conv-b' }).seq, 1);
  assert.equal(allocator.next({ userId: 'user-b', conversationId: 'conv-b' }).seq, 2);
  assert.equal(allocator.next({ userId: 'user-a', conversationId: 'conv-a' }).seq, 3);
});

test('shard = fnv1a32(userId) % 64，且恒在 0..63', () => {
  const allocator = new SeqAllocator();
  for (const userId of ['user-a', 'user-b', 'user-c', '张三', 'agent-x']) {
    const { shard } = allocator.next({ userId, conversationId: 'conv-shard' });
    assert.equal(shard, fnv1a32(userId) % 64);
    assert.ok(shard >= 0 && shard <= 63, 'shard 必须落在 0..63');
  }
});

test('自定义分片数：shard 恒在 0..shards-1', () => {
  const allocator = new SeqAllocator({ shards: 7 });
  assert.equal(allocator.stats().shards, 7);
  for (let i = 0; i < 200; i += 1) {
    const { shard } = allocator.next({ userId: `user-${i}`, conversationId: 'conv-7' });
    assert.ok(Number.isInteger(shard) && shard >= 0 && shard < 7);
  }
});

test('大量分配（10000 次）无重复序号，统计正确', () => {
  const allocator = new SeqAllocator();
  const seqs = new Set();
  const shards = new Set();
  for (let i = 0; i < 10000; i += 1) {
    const { seq, shard } = allocator.next({ userId: `user-${i % 100}`, conversationId: 'conv-bulk' });
    assert.ok(!seqs.has(seq), `序号 ${seq} 重复`);
    seqs.add(seq);
    shards.add(shard);
  }
  assert.equal(seqs.size, 10000);
  assert.equal(Math.min(...seqs), 1);
  assert.equal(Math.max(...seqs), 10000);
  assert.equal(allocator.stats().allocated, 10000);
  for (const shard of shards) assert.ok(shard >= 0 && shard <= 63);
  assert.deepEqual({ ...allocator.stats() }, { shards: 64, allocated: 10000 });
});

test('参数校验：缺失 userId / conversationId → TypeError；shards 非法 → RangeError', () => {
  const allocator = new SeqAllocator();
  assert.throws(() => allocator.next({ conversationId: 'conv-1' }), TypeError);
  assert.throws(() => allocator.next({ userId: 'user-a' }), TypeError);
  assert.throws(() => allocator.next({ userId: '', conversationId: 'conv-1' }), TypeError);
  assert.throws(() => new SeqAllocator({ shards: 0 }), RangeError);
  assert.throws(() => new SeqAllocator({ shards: -1 }), RangeError);
  assert.throws(() => new SeqAllocator({ shards: 1.5 }), RangeError);
});
