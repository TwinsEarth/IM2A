// 测试文件：§50 日切对账（逐笔差异 / missingInLedger / missingInProvider / 只读 / 空日 / Markdown 报告）/ 运行：node services/payment/test/reconcile.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHmac } from 'node:crypto';
import { Ledger } from '../src/ledger.mjs';
import { CHANNEL_KIND, PaymentIntentService } from '../src/channel-adapter.mjs';
import { RECONCILE_CODE, ReconcileError, Reconciler } from '../src/reconcile.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';
const BASE_MS = Date.parse(BASE_ISO);
const BASE_DATE = '2026-10-21';
const SECRET = 'mock-secret-keep-it-safe';

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

/** 接好线的真实栈：ledger + PaymentIntentService + Reconciler（同一 clock） */
function makeStack() {
  const clock = makeClock();
  const ledger = new Ledger({ clock });
  const service = new PaymentIntentService({ clock, ledger, secret: SECRET });
  const reconciler = new Reconciler({ clock, ledger, payments: service });
  return { clock, ledger, service, reconciler };
}

function payOrder(service, order, providerTxnId = `txn-${order.orderId}`) {
  const body = JSON.stringify({ orderId: order.orderId, amountCents: order.amountCents, providerTxnId });
  const signature = createHmac('sha256', SECRET).update(body, 'utf8').digest('hex');
  return service.handleCallback({
    channel: order.channel,
    headers: { 'x-signature': `sha256=${signature}`, 'x-timestamp': String(BASE_MS) },
    body,
  });
}

test('全匹配：已支付订单 vs 账本 DEPOSIT → ok:true、checked/matched 正确、对账只读', () => {
  const { ledger, service, reconciler } = makeStack();
  const first = service.createIntent({ userId: 'user-a', amountCents: 5000, orderId: 'order-a' });
  const second = service.createIntent({ userId: 'user-b', amountCents: 3000, orderId: 'order-b' });
  payOrder(service, first);
  payOrder(service, second);
  const entriesBefore = ledger.entries().length;

  const result = reconciler.run({ date: BASE_DATE });
  assert.equal(result.date, BASE_DATE);
  assert.equal(result.checked, 2);
  assert.equal(result.matched, 2);
  assert.deepEqual([...result.mismatched], []);
  assert.deepEqual([...result.missingInLedger], []);
  assert.deepEqual([...result.missingInProvider], []);
  assert.equal(result.ok, true);
  assert.ok(Object.isFrozen(result));
  assert.equal(ledger.entries().length, entriesBefore, '对账只读：账本条数不变');
  // 默认 date 取注入 clock 当天
  assert.equal(reconciler.run().checked, 2);
});

test('空数据日：checked:0、ok:true（不是错误），report 可归档', () => {
  const { reconciler } = makeStack();
  const result = reconciler.run({ date: '2026-10-20' });
  assert.deepEqual(
    { date: result.date, checked: result.checked, matched: result.matched, ok: result.ok },
    { date: '2026-10-20', checked: 0, matched: 0, ok: true }
  );
  assert.deepEqual([...result.mismatched], []);
  assert.deepEqual([...result.missingInLedger], []);
  const markdown = reconciler.report({ date: '2026-10-20' });
  assert.equal(typeof markdown, 'string');
  assert.match(markdown, /^# 支付日切对账报告 2026-10-20/);
  assert.match(markdown, /PASS/);
  assert.match(markdown, /核对订单数（checked）：0/);
});

test('金额不一致：逐笔列出 mismatched（orderId + 渠道金额 + 账本金额），ok:false', () => {
  const clock = makeClock();
  const ledger = new Ledger({ clock });
  // 账本记 400，渠道订单 500 → 差额
  ledger.post({ idempotencyKey: 'deposit:order-m', type: 'DEPOSIT', userId: 'user-a', amountCents: 400, ref: 'order-m' });
  const payments = { list: () => [{ orderId: 'order-m', userId: 'user-a', amountCents: 500, state: 'PAID' }] };
  const reconciler = new Reconciler({ clock, ledger, payments });

  const before = ledger.entries().length;
  const result = reconciler.run({ date: BASE_DATE });
  assert.equal(result.ok, false);
  assert.equal(result.checked, 1);
  assert.equal(result.matched, 0);
  assert.equal(result.mismatched.length, 1);
  assert.deepEqual({ ...result.mismatched[0] }, {
    orderId: 'order-m',
    providerAmountCents: 500,
    ledgerAmountCents: 400,
    reason: 'AMOUNT_MISMATCH',
  });
  assert.deepEqual([...result.missingInLedger], []);
  assert.equal(ledger.entries().length, before, '对账只读');

  const markdown = reconciler.report({ date: BASE_DATE });
  assert.match(markdown, /FAIL/);
  assert.match(markdown, /order-m/);
  assert.match(markdown, /\| 500 \| 400 \| AMOUNT_MISMATCH \|/);
});

test('账本缺失：渠道已收、账本没记 → missingInLedger 逐笔列出且 ok:false', () => {
  const clock = makeClock();
  const ledger = new Ledger({ clock });
  const payments = {
    list: () => [
      { orderId: 'order-x', userId: 'user-a', amountCents: 700, state: 'PAID' },
      { orderId: 'order-y', userId: 'user-b', amountCents: 900, state: 'REFUNDED' },
    ],
  };
  const reconciler = new Reconciler({ clock, ledger, payments });
  const result = reconciler.run({ date: BASE_DATE });
  assert.equal(result.ok, false);
  assert.equal(result.checked, 2);
  assert.deepEqual([...result.missingInLedger], ['order-x', 'order-y']);
  assert.deepEqual([...result.mismatched], []);
  assert.match(reconciler.report({ date: BASE_DATE }), /账本缺失/);
});

test('渠道缺失：账本有支付入金但订单不存在 → missingInProvider 列出（不改变 ok，按契约）', () => {
  const clock = makeClock();
  const ledger = new Ledger({ clock });
  ledger.post({ idempotencyKey: 'deposit:order-ghost', type: 'DEPOSIT', userId: 'user-a', amountCents: 800, ref: 'order-ghost' });
  const payments = { list: () => [] };
  const reconciler = new Reconciler({ clock, ledger, payments });

  const result = reconciler.run({ date: BASE_DATE });
  assert.equal(result.checked, 0);
  assert.equal(result.ok, true, 'ok 仅由 mismatched/missingInLedger 决定');
  assert.deepEqual([...result.missingInProvider], ['order-ghost']);
});

test('钱包充值等非支付入金不参与渠道对账', () => {
  const { ledger, service, reconciler } = makeStack();
  // §37 钱包充值：DEPOSIT 但没有 deposit:<orderId> 键、ref 也不是订单号
  ledger.post({ idempotencyKey: 'dep-1', type: 'DEPOSIT', userId: 'user-a', amountCents: 10000, ref: 'manual' });
  const order = service.createIntent({ userId: 'user-a', amountCents: 5000, orderId: 'order-paid' });
  payOrder(service, order);

  const result = reconciler.run({ date: BASE_DATE });
  assert.equal(result.checked, 1);
  assert.equal(result.matched, 1);
  assert.equal(result.ok, true);
  assert.deepEqual([...result.missingInProvider], [], '钱包充值不得被误报为渠道缺失');
});

test('跨日过滤：只对 date 当天的订单/入金做对账', () => {
  const { clock, service, reconciler } = makeStack();
  const order = service.createIntent({ userId: 'user-a', amountCents: 5000, orderId: 'order-day1' });
  payOrder(service, order);
  clock.advance(864e5); // 次日
  const result = reconciler.run({ date: '2026-10-22' });
  assert.equal(result.checked, 0);
  assert.equal(result.ok, true);
  assert.deepEqual([...result.missingInLedger], [], '昨天已支付的订单不应在今天报缺失');
});

test('date 参数非法 → INVALID_ARGUMENT（只读、不触碰账本）', () => {
  const { ledger, reconciler } = makeStack();
  const before = ledger.entries().length;
  for (const badDate of ['2026/10/21', '2026-13-01', '2026-02-31', 123, '']) {
    assert.throws(
      () => reconciler.run({ date: badDate }),
      (error) => error instanceof ReconcileError && error.code === RECONCILE_CODE.INVALID_ARGUMENT,
      `应拒绝 date=${String(badDate)}`
    );
  }
  assert.equal(ledger.entries().length, before);
});

test('payments.list 抛错 → ReconcileError(NOT_FOUND)；构造参数校验', () => {
  const clock = makeClock();
  const ledger = new Ledger({ clock });
  const broken = new Reconciler({
    clock,
    ledger,
    payments: {
      list: () => {
        throw new Error('provider down');
      },
    },
  });
  assert.throws(
    () => broken.run(),
    (error) => error instanceof ReconcileError && error.code === RECONCILE_CODE.NOT_FOUND
  );

  assert.throws(() => new Reconciler({}), TypeError);
  assert.throws(() => new Reconciler({ ledger }), TypeError);
  assert.throws(() => new Reconciler({ ledger, payments: {} }), TypeError);
  assert.throws(() => new Reconciler({ ledger: {}, payments: { list: () => [] } }), TypeError);
  assert.throws(() => new Reconciler({ ledger, payments: { list: () => [] }, clock: 'now' }), TypeError);
});

test('report：包含逐笔差异与汇总的 Markdown（可归档），不修改状态', () => {
  const { ledger, service, reconciler } = makeStack();
  const paid = service.createIntent({ userId: 'user-a', amountCents: 5000, orderId: 'order-ok' });
  payOrder(service, paid);
  const entriesBefore = ledger.entries().length;

  const markdown = reconciler.report({ date: BASE_DATE });
  assert.equal(typeof markdown, 'string');
  assert.match(markdown, /^# 支付日切对账报告 /);
  assert.match(markdown, /对账结果：PASS/);
  assert.match(markdown, /核对订单数（checked）：1/);
  assert.match(markdown, /金额匹配（matched）：1/);
  assert.match(markdown, /## 金额不一致/);
  assert.match(markdown, /## 账本缺失/);
  assert.match(markdown, /## 渠道缺失/);
  assert.equal(ledger.entries().length, entriesBefore, 'report 只读');
  assert.equal(service.get('order-ok').state, 'PAID');
});
