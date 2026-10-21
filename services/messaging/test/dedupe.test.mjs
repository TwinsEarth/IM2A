// 测试文件：bizId 去重 §10（首次 false / 重复 true / TTL 过期可重新标记 / prune）/ 运行：node services/messaging/test/dedupe.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DedupeStore } from '../src/dedupe.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';

function setup({ ttlMs = 1000 } = {}) {
  const state = { nowMs: Date.parse(BASE_ISO) };
  const clock = () => new Date(state.nowMs);
  const store = new DedupeStore({ ttlMs, clock });
  return {
    store,
    state,
    advance(ms) {
      state.nowMs += ms;
    },
    nowIso() {
      return new Date(state.nowMs).toISOString();
    },
  };
}

test('首次 checkAndMark → duplicate:false；重复 → duplicate:true 且 firstSeenAt 不刷新', () => {
  const { store, advance, nowIso } = setup();
  const first = store.checkAndMark('biz-1');
  assert.equal(first.duplicate, false);
  assert.equal(first.firstSeenAt, BASE_ISO);
  assert.ok(Object.isFrozen(first));

  advance(300);
  const second = store.checkAndMark('biz-1');
  assert.equal(second.duplicate, true);
  assert.equal(second.firstSeenAt, BASE_ISO, '首次时间戳不得被重复标记刷新');
  assert.equal(nowIso(), '2026-10-21T10:00:00.300Z');
  assert.equal(store.size(), 1);
});

test('不同 key 互不影响', () => {
  const { store } = setup();
  assert.equal(store.checkAndMark('biz-a').duplicate, false);
  assert.equal(store.checkAndMark('biz-b').duplicate, false);
  assert.equal(store.checkAndMark('biz-a').duplicate, true);
  assert.equal(store.checkAndMark('biz-b').duplicate, true);
  assert.equal(store.size(), 2);
});

test('TTL 过期后（推进 clock）可重新标记为首次，并刷新 firstSeenAt', () => {
  const { store, advance } = setup({ ttlMs: 1000 });
  store.checkAndMark('biz-ttl');
  advance(999);
  assert.equal(store.checkAndMark('biz-ttl').duplicate, true, '999ms 仍在窗口内');

  advance(1); // 累计 1000ms，达到 TTL 边界即视为过期
  const refreshed = store.checkAndMark('biz-ttl');
  assert.equal(refreshed.duplicate, false);
  assert.equal(refreshed.firstSeenAt, '2026-10-21T10:00:01.000Z', '过期重标记必须刷新 firstSeenAt');
  assert.equal(store.size(), 1);

  assert.equal(store.checkAndMark('biz-ttl').duplicate, true, '重标记后重新进入窗口');
});

test('prune：清理过期键并返回清理数量，重复调用幂等', () => {
  const { store, advance } = setup({ ttlMs: 1000 });
  store.checkAndMark('biz-prune-1');
  store.checkAndMark('biz-prune-2');
  advance(500);
  assert.equal(store.prune(), 0, '未过期不清理');
  assert.equal(store.size(), 2);
  advance(500); // 达到 TTL
  assert.equal(store.prune(), 2);
  assert.equal(store.size(), 0);
  assert.equal(store.prune(), 0, '重复 prune 不得重复计数');

  // prune 之后同一 key 可以重新标记
  assert.equal(store.checkAndMark('biz-prune-1').duplicate, false);
});

test('size：统计当前保留键数量（含尚未 prune 的过期项）', () => {
  const { store, advance } = setup({ ttlMs: 1000 });
  store.checkAndMark('k1');
  assert.equal(store.size(), 1);
  advance(1000);
  assert.equal(store.size(), 1, '过期项在 prune 前仍被保留');
  store.prune();
  assert.equal(store.size(), 0);
});

test('参数校验：key 非空字符串；ttlMs 非法 → RangeError', () => {
  const { store } = setup();
  assert.throws(() => store.checkAndMark(''), TypeError);
  assert.throws(() => store.checkAndMark(123), TypeError);
  assert.throws(() => store.checkAndMark(null), TypeError);
  for (const ttlMs of [-1, NaN, Infinity, '1000']) {
    assert.throws(() => new DedupeStore({ ttlMs }), RangeError, `ttlMs=${String(ttlMs)} 应被拒绝`);
  }
});
