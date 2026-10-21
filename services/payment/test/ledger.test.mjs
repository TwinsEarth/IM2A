// 测试文件：不可变账本 §8（exactly-once / 金额边界 / 余额与统计口径）/ 运行：node services/payment/test/ledger.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ENTRY_TYPE, Ledger, LedgerError } from '../src/ledger.mjs';
import * as barrel from '../src/index.mjs';

const FIXED_ISO = '2026-10-21T10:00:00.000Z';

function makeLedger() {
  return new Ledger({ clock: () => new Date(FIXED_ISO) });
}

test('正常入账：CHARGE 条目字段完整、冻结且可按幂等键查到', () => {
  const ledger = makeLedger();
  const { entry, duplicate } = ledger.post({
    idempotencyKey: 'charge:msg-1',
    type: ENTRY_TYPE.CHARGE,
    userId: 'user-a',
    amountCents: 100,
    ref: 'msg-1',
    meta: { scene: 'stranger-message' },
  });
  assert.equal(duplicate, false);
  assert.match(entry.id, /^led-/);
  assert.equal(entry.idempotencyKey, 'charge:msg-1');
  assert.equal(entry.type, 'CHARGE');
  assert.equal(entry.userId, 'user-a');
  assert.equal(entry.amountCents, 100);
  assert.equal(entry.ref, 'msg-1');
  assert.deepEqual(entry.meta, { scene: 'stranger-message' });
  assert.equal(entry.at, FIXED_ISO);
  assert.ok(Object.isFrozen(entry));
  assert.ok(Object.isFrozen(entry.meta));
  assert.equal(ledger.findByIdempotencyKey('charge:msg-1'), entry);
});

test('exactly-once：同一 idempotencyKey 重复 post 只入账一次且 duplicate:true', () => {
  const ledger = makeLedger();
  const first = ledger.post({ idempotencyKey: 'dupe-1', type: 'CHARGE', userId: 'user-a', amountCents: 100 });
  const second = ledger.post({ idempotencyKey: 'dupe-1', type: 'CHARGE', userId: 'user-a', amountCents: 100 });
  assert.equal(first.duplicate, false);
  assert.equal(second.duplicate, true);
  assert.equal(second.entry.id, first.entry.id);
  assert.equal(ledger.entries().length, 1);
  assert.equal(ledger.stats().count, 1);
  assert.equal(ledger.stats().charged, 1);
  assert.equal(ledger.balanceOf('user-a'), 100);
});

test('exactly-once：同 key 但载荷不同仍返回首次结果（重复投递不改变账本）', () => {
  const ledger = makeLedger();
  const first = ledger.post({ idempotencyKey: 'dupe-2', type: 'CHARGE', userId: 'user-a', amountCents: 100 });
  const replay = ledger.post({ idempotencyKey: 'dupe-2', type: 'CHARGE', userId: 'user-x', amountCents: 999 });
  assert.equal(replay.duplicate, true);
  assert.equal(replay.entry.id, first.entry.id);
  assert.equal(replay.entry.amountCents, 100);
  assert.equal(ledger.entries().length, 1);
  assert.equal(ledger.balanceOf('user-a'), 100);
  assert.equal(ledger.balanceOf('user-x'), 0);
});

test('balanceOf：CHARGE 累加、REFUND 扣减、用户隔离、SETTLE 不计入余额', () => {
  const ledger = makeLedger();
  ledger.post({ idempotencyKey: 'c-a-1', type: 'CHARGE', userId: 'user-a', amountCents: 500 });
  ledger.post({ idempotencyKey: 'c-a-2', type: 'CHARGE', userId: 'user-a', amountCents: 300 });
  ledger.post({ idempotencyKey: 'r-a-1', type: 'REFUND', userId: 'user-a', amountCents: 200 });
  ledger.post({ idempotencyKey: 'c-b-1', type: 'CHARGE', userId: 'user-b', amountCents: 100 });
  ledger.post({ idempotencyKey: 's-b-1', type: 'SETTLE', userId: 'user-b', amountCents: 100 });

  assert.equal(ledger.balanceOf('user-a'), 600);
  assert.equal(ledger.balanceOf('user-b'), 100, 'SETTLE 不改变余额口径');
  assert.equal(ledger.balanceOf('user-none'), 0);
});

test('金额边界：0 / 负数 / 小数 / 字符串 / NaN / Infinity / 超安全整数 一律 INVALID_AMOUNT', () => {
  const ledger = makeLedger();
  const badAmounts = [0, -1, -100, 1.5, 0.1, 100.5, '100', '', null, undefined, NaN, Infinity, -Infinity, 2 ** 53];
  for (const amountCents of badAmounts) {
    assert.throws(
      () => ledger.post({ idempotencyKey: `bad-${String(amountCents)}`, type: 'CHARGE', userId: 'user-a', amountCents }),
      (error) => error instanceof LedgerError && error.code === 'INVALID_AMOUNT',
      `应拒绝 amountCents=${String(amountCents)}`
    );
  }
  assert.equal(ledger.entries().length, 0, '非法金额不得留下任何条目');
});

test('type 非法 → INVALID_TYPE', () => {
  const ledger = makeLedger();
  assert.throws(
    () => ledger.post({ idempotencyKey: 'bad-type', type: 'TRANSFER', userId: 'user-a', amountCents: 1 }),
    (error) => error instanceof LedgerError && error.code === 'INVALID_TYPE'
  );
  assert.equal(ledger.entries().length, 0);
});

test('idempotencyKey 缺失 / 空串 / 非字符串 → KEY_REQUIRED', () => {
  const ledger = makeLedger();
  for (const idempotencyKey of [undefined, null, '', 123]) {
    assert.throws(
      () => ledger.post({ idempotencyKey, type: 'CHARGE', userId: 'user-a', amountCents: 1 }),
      (error) => error instanceof LedgerError && error.code === 'KEY_REQUIRED',
      `应拒绝 idempotencyKey=${String(idempotencyKey)}`
    );
  }
});

test('entries 返回副本数组：外部修改不影响账本，且支持 userId 过滤', () => {
  const ledger = makeLedger();
  ledger.post({ idempotencyKey: 'e-1', type: 'CHARGE', userId: 'user-a', amountCents: 100 });
  ledger.post({ idempotencyKey: 'e-2', type: 'REFUND', userId: 'user-a', amountCents: 40 });
  ledger.post({ idempotencyKey: 'e-3', type: 'CHARGE', userId: 'user-b', amountCents: 50 });

  assert.equal(ledger.entries().length, 3);
  const copy = ledger.entries();
  copy.pop();
  copy.push('junk');
  assert.equal(ledger.entries().length, 3, '修改副本不得影响账本');
  assert.equal(ledger.entries({ userId: 'user-a' }).length, 2);
  assert.equal(ledger.entries({ userId: 'user-b' }).length, 1);
  assert.equal(ledger.entries({ userId: 'user-none' }).length, 0);
});

test('stats：只统计条数（count / charged / refunded / settled 均为 number）', () => {
  const ledger = makeLedger();
  ledger.post({ idempotencyKey: 'st-1', type: 'CHARGE', userId: 'user-a', amountCents: 100 });
  ledger.post({ idempotencyKey: 'st-2', type: 'CHARGE', userId: 'user-a', amountCents: 300 });
  ledger.post({ idempotencyKey: 'st-3', type: 'REFUND', userId: 'user-a', amountCents: 100 });
  ledger.post({ idempotencyKey: 'st-4', type: 'SETTLE', userId: 'user-b', amountCents: 200 });
  const stats = ledger.stats();
  assert.deepEqual({ ...stats }, { count: 4, charged: 2, refunded: 1, settled: 1 });
  assert.equal(typeof stats.count, 'number');
  assert.ok(Object.isFrozen(stats));
});

test('snapshot：可 JSON 序列化全量，内容与账本一致', () => {
  const ledger = makeLedger();
  ledger.post({ idempotencyKey: 'snap-1', type: 'CHARGE', userId: 'user-a', amountCents: 100 });
  ledger.post({ idempotencyKey: 'snap-2', type: 'REFUND', userId: 'user-a', amountCents: 40 });
  const snapshot = ledger.snapshot();
  assert.ok(Object.isFrozen(snapshot));
  assert.equal(snapshot.count, 2);
  const parsed = JSON.parse(JSON.stringify(snapshot));
  assert.equal(parsed.count, 2);
  assert.equal(parsed.entries.length, 2);
  assert.equal(parsed.entries[0].idempotencyKey, 'snap-1');
  assert.equal(parsed.entries[1].type, 'REFUND');
});

test('注入 clock：条目时间来自 clock，而非系统时钟', () => {
  const times = ['2026-01-01T00:00:00.000Z', '2026-01-02T00:00:00.000Z'];
  let index = 0;
  const ledger = new Ledger({ clock: () => new Date(times[Math.min(index++, times.length - 1)]) });
  const first = ledger.post({ idempotencyKey: 'clock-1', type: 'CHARGE', userId: 'user-a', amountCents: 100 });
  const second = ledger.post({ idempotencyKey: 'clock-2', type: 'CHARGE', userId: 'user-a', amountCents: 100 });
  assert.equal(first.entry.at, times[0]);
  assert.equal(second.entry.at, times[1]);
});

test('findByIdempotencyKey：未命中返回 undefined', () => {
  const ledger = makeLedger();
  assert.equal(ledger.findByIdempotencyKey('nope'), undefined);
  assert.equal(ledger.findByIdempotencyKey(''), undefined);
});

test('barrel index.mjs 导出 payment 公共 API', () => {
  assert.equal(barrel.ENTRY_TYPE.CHARGE, 'CHARGE');
  assert.equal(barrel.ENTRY_TYPE.REFUND, 'REFUND');
  assert.equal(barrel.ENTRY_TYPE.SETTLE, 'SETTLE');
  assert.equal(typeof barrel.Ledger, 'function');
  assert.equal(typeof barrel.LedgerError, 'function');
  assert.equal(typeof barrel.PaidMessageService, 'function');
  assert.equal(typeof barrel.PaidMessageError, 'function');
  assert.equal(barrel.MSG_STATE.SETTLED, 'SETTLED');
  assert.equal(barrel.BILLING.SEND_PAY, 1);
  assert.equal(barrel.BILLING.VIEW_REPLY_PAY, 2);
});
