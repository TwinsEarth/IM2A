// 测试文件：§37 钱包（充值/扣费幂等、余额不为负、退款窗口防白嫖、日上限、账本单一真相）/ 运行：node services/payment/test/wallet.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Ledger } from '../src/ledger.mjs';
import { WALLET_CODE, Wallet, WalletError } from '../src/wallet.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';

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

function makeWallet(options = {}) {
  const clock = options.clock ?? makeClock();
  const ledger = options.ledger === undefined ? new Ledger({ clock }) : options.ledger;
  const messageState = options.messageState ?? (() => 'PAID');
  const accountState = options.accountState ?? (() => 'ACTIVE');
  const wallet = new Wallet({
    clock,
    ledger,
    dailySendCapCents: options.dailySendCapCents ?? 50000,
    messageState,
    accountState,
  });
  return { clock, ledger, wallet };
}

function entriesOfType(ledger, type) {
  return ledger.entries().filter((entry) => entry.type === type);
}

test('充值 100 元、扣 50 元 → 余额 50 元；再扣 60 元 → INSUFFICIENT_FUNDS 且余额不变（不允许负余额）', () => {
  const { wallet, ledger } = makeWallet();
  const topUp = wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  assert.deepEqual(
    { ...topUp },
    { credited: true, duplicate: false, amountCents: 10000, availableCents: 10000, channel: 'manual' }
  );
  assert.deepEqual({ ...wallet.balance({ userId: 'user-a' }) }, { availableCents: 10000, frozenCents: 0 });

  const charge = wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 5000 });
  assert.deepEqual({ ...charge }, { charged: true, duplicate: false, amountCents: 5000, availableCents: 5000 });
  assert.deepEqual({ ...wallet.balance({ userId: 'user-a' }) }, { availableCents: 5000, frozenCents: 0 });

  assert.throws(
    () => wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-2', amountCents: 6000 }),
    (error) => error instanceof WalletError && error.code === WALLET_CODE.INSUFFICIENT_FUNDS
  );
  assert.deepEqual(
    { ...wallet.balance({ userId: 'user-a' }) },
    { availableCents: 5000, frozenCents: 0 },
    '余额不足时不得扣成负数'
  );
  assert.equal(entriesOfType(ledger, 'CHARGE').length, 1, '余额不足不得写 CHARGE');

  const summary = wallet.summary({ userId: 'user-a' });
  assert.deepEqual(
    { ...summary },
    {
      userId: 'user-a',
      availableCents: 5000,
      frozenCents: 0,
      depositedCents: 10000,
      chargedCents: 5000,
      refundedCents: 0,
      dailyChargedCents: 5000,
      dailySendCapCents: 50000,
    }
  );
  assert.deepEqual(ledger.entries({ userId: 'user-a' }).map((entry) => entry.type), ['DEPOSIT', 'CHARGE']);
  assert.equal(ledger.findByIdempotencyKey('dep-1').type, 'DEPOSIT');
  assert.equal(ledger.findByIdempotencyKey('pay:msg-1').type, 'CHARGE');
});

test('幂等：同 messageId 重复扣费只 1 笔 ledger CHARGE，余额只扣一次', () => {
  const { wallet, ledger } = makeWallet();
  wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  const first = wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 3000 });
  assert.equal(first.charged, true);
  const retry = wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 3000 });
  assert.deepEqual({ ...retry }, { charged: false, duplicate: true, amountCents: 3000, availableCents: 7000 });
  assert.equal(entriesOfType(ledger, 'CHARGE').length, 1);
  assert.deepEqual({ ...wallet.balance({ userId: 'user-a' }) }, { availableCents: 7000, frozenCents: 0 });
  assert.equal(ledger.entries({ userId: 'user-a' }).length, 2);
});

test('未回复可退：state=PAID 时退款成功、余额回补、账本有 REFUND；已回复不可退（REPLIED/SETTLED → REFUND_WINDOW_CLOSED）', () => {
  // PAID 可退
  const paidCase = makeWallet();
  paidCase.wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  paidCase.wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 3000 });
  const refunded = paidCase.wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: '误发' });
  assert.deepEqual(
    { ...refunded },
    { refunded: true, duplicate: false, amountCents: 3000, availableCents: 10000, reason: '误发' }
  );
  assert.equal(entriesOfType(paidCase.ledger, 'REFUND').length, 1);
  assert.equal(paidCase.ledger.findByIdempotencyKey('refund:msg-1').ref, 'msg-1');
  assert.deepEqual({ ...paidCase.wallet.balance({ userId: 'user-a' }) }, { availableCents: 10000, frozenCents: 0 });

  // AWAITING_PAYMENT / VIEWED 也在窗口内
  for (const windowState of ['AWAITING_PAYMENT', 'VIEWED']) {
    const stack = makeWallet({ messageState: () => windowState });
    stack.wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
    stack.wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 3000 });
    assert.equal(
      stack.wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: '窗口内' }).refunded,
      true,
      `${windowState} 应可退`
    );
  }

  // 已回复/已结算不可退
  for (const closedState of ['REPLIED', 'SETTLED']) {
    let state = 'PAID';
    const stack = makeWallet({ messageState: () => state });
    stack.wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
    stack.wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 3000 });
    state = closedState;
    assert.throws(
      () => stack.wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: '太晚了' }),
      (error) => error instanceof WalletError && error.code === WALLET_CODE.REFUND_WINDOW_CLOSED
    );
    assert.equal(entriesOfType(stack.ledger, 'REFUND').length, 0, '窗口关闭不得写 REFUND');
    assert.deepEqual({ ...stack.wallet.balance({ userId: 'user-a' }) }, { availableCents: 7000, frozenCents: 0 });
  }
});

test('重复退款同一 messageId → 幂等，不重复回补（即使状态随后变化也返回 duplicate）', () => {
  let state = 'PAID';
  const { wallet, ledger } = makeWallet({ messageState: () => state });
  wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 3000 });
  assert.equal(wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: '第一次' }).refunded, true);

  state = 'REPLIED'; // 已退过之后状态变化，重试仍必须幂等而不是报窗口关闭
  const retry = wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: '网络重试' });
  assert.deepEqual({ ...retry }, { refunded: false, duplicate: true, amountCents: 3000, availableCents: 10000, reason: '网络重试' });
  assert.equal(entriesOfType(ledger, 'REFUND').length, 1, '不得重复写 REFUND');
  assert.deepEqual({ ...wallet.balance({ userId: 'user-a' }) }, { availableCents: 10000, frozenCents: 0 });
});

test('日发送上限：同一天连续扣到上限 → DAILY_CAP_EXCEEDED（余额不变）；次日 clock 推进后恢复', () => {
  const { wallet, ledger, clock } = makeWallet({ dailySendCapCents: 300 });
  wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  for (const messageId of ['msg-1', 'msg-2', 'msg-3']) {
    assert.equal(wallet.chargeForMessage({ userId: 'user-a', messageId, amountCents: 100 }).charged, true);
  }
  assert.deepEqual({ ...wallet.balance({ userId: 'user-a' }) }, { availableCents: 9700, frozenCents: 0 });
  assert.throws(
    () => wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-4', amountCents: 100 }),
    (error) => error instanceof WalletError && error.code === WALLET_CODE.DAILY_CAP_EXCEEDED
  );
  assert.deepEqual({ ...wallet.balance({ userId: 'user-a' }) }, { availableCents: 9700, frozenCents: 0 });
  assert.equal(entriesOfType(ledger, 'CHARGE').length, 3);

  clock.advance(864e5); // 次日
  assert.equal(wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-4', amountCents: 100 }).charged, true);
  assert.deepEqual({ ...wallet.balance({ userId: 'user-a' }) }, { availableCents: 9600, frozenCents: 0 });
  assert.equal(wallet.summary({ userId: 'user-a' }).dailyChargedCents, 100);
});

test('退款不回滚当日额度（避免"退旧扣新"绕开日上限）', () => {
  const { wallet } = makeWallet({ dailySendCapCents: 300 });
  wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 100 });
  wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-2', amountCents: 100 });
  wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-3', amountCents: 100 });
  wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: '退款' });
  assert.throws(
    () => wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-4', amountCents: 100 }),
    (error) => error instanceof WalletError && error.code === WALLET_CODE.DAILY_CAP_EXCEEDED
  );
});

test('充值幂等：同 idempotencyKey 连充两次 → 只入账一次（重放 duplicate:true）', () => {
  const { wallet, ledger } = makeWallet();
  const first = wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-replay' });
  const retry = wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-replay' });
  assert.equal(first.credited, true);
  assert.deepEqual(
    { ...retry },
    { credited: false, duplicate: true, amountCents: 10000, availableCents: 10000, channel: 'manual' }
  );
  assert.equal(entriesOfType(ledger, 'DEPOSIT').length, 1);
  assert.deepEqual({ ...wallet.balance({ userId: 'user-a' }) }, { availableCents: 10000, frozenCents: 0 });
});

test('金额边界：0 / 负数 / 小数 / 字符串 / NaN / Infinity → INVALID_AMOUNT', () => {
  const { wallet } = makeWallet();
  const badAmounts = [0, -1, 1.5, '100', NaN, Infinity, undefined, 2 ** 53];
  for (const amountCents of badAmounts) {
    assert.throws(
      () => wallet.topUp({ userId: 'user-a', amountCents, idempotencyKey: `dep-${String(amountCents)}` }),
      (error) => error instanceof WalletError && error.code === WALLET_CODE.INVALID_AMOUNT,
      `topUp 应拒绝 ${String(amountCents)}`
    );
    assert.throws(
      () => wallet.chargeForMessage({ userId: 'user-a', messageId: `msg-${String(amountCents)}`, amountCents }),
      (error) => error instanceof WalletError && error.code === WALLET_CODE.INVALID_AMOUNT,
      `charge 应拒绝 ${String(amountCents)}`
    );
  }
});

test('NOT_FOUND：退款未扣过费的消息 / 退别人扣费的消息', () => {
  const { wallet } = makeWallet();
  wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  wallet.topUp({ userId: 'user-b', amountCents: 10000, idempotencyKey: 'dep-2' });
  wallet.chargeForMessage({ userId: 'user-b', messageId: 'msg-other', amountCents: 1000 });
  assert.throws(
    () => wallet.refund({ userId: 'user-a', messageId: 'msg-never-charged', reason: 'x' }),
    (error) => error instanceof WalletError && error.code === WALLET_CODE.NOT_FOUND
  );
  assert.throws(
    () => wallet.refund({ userId: 'user-a', messageId: 'msg-other', reason: 'x' }),
    (error) => error instanceof WalletError && error.code === WALLET_CODE.NOT_FOUND
  );
});

test('SUSPENDED：账号被暂停后充值/扣费/退款全部阻断', () => {
  let account = 'ACTIVE';
  const { wallet } = makeWallet({ accountState: () => account });
  wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 1000 });
  account = 'SUSPENDED';
  for (const call of [
    () => wallet.topUp({ userId: 'user-a', amountCents: 1000, idempotencyKey: 'dep-2' }),
    () => wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-2', amountCents: 1000 }),
    () => wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: 'x' }),
  ]) {
    assert.throws(call, (error) => error instanceof WalletError && error.code === WALLET_CODE.SUSPENDED);
  }
});

test('账本单一真相：§9 直接写账本的 CHARGE 也反映到钱包余额，退款能按 pay:<messageId> 找到扣费', () => {
  const { wallet, ledger } = makeWallet();
  wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  // 模拟 PaidMessageService 直接写账本（不经过钱包）
  ledger.post({
    idempotencyKey: 'pay:msg-ext',
    type: 'CHARGE',
    userId: 'user-a',
    amountCents: 2500,
    ref: 'msg-ext',
    meta: { source: 'paid-message' },
  });
  assert.deepEqual(
    { ...wallet.balance({ userId: 'user-a' }) },
    { availableCents: 7500, frozenCents: 0 },
    '钱包余额从不可变账本投影'
  );
  // 同一幂等键再走钱包扣费 → duplicate，不二次扣费
  const duplicate = wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-ext', amountCents: 2500 });
  assert.equal(duplicate.duplicate, true);
  assert.equal(duplicate.charged, false);
  assert.equal(entriesOfType(ledger, 'CHARGE').length, 1);
  // 退款 fallback 到账本 pay:<messageId> 找到原始扣费
  const refund = wallet.refund({ userId: 'user-a', messageId: 'msg-ext', reason: '误发' });
  assert.equal(refund.refunded, true);
  assert.equal(refund.amountCents, 2500);
  assert.equal(wallet.balance({ userId: 'user-a' }).availableCents, 10000);
});

test('无 ledger（内存投影）模式：充值/扣费/退款/幂等/日上限同样成立', () => {
  const { wallet, clock } = makeWallet({ ledger: null, dailySendCapCents: 300 });
  assert.deepEqual({ ...wallet.balance({ userId: 'user-a' }) }, { availableCents: 0, frozenCents: 0 });
  wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  wallet.topUp({ userId: 'user-a', amountCents: 10000, idempotencyKey: 'dep-1' });
  assert.equal(wallet.balance({ userId: 'user-a' }).availableCents, 10000, '充值幂等');
  wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 100 });
  assert.equal(wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 100 }).duplicate, true);
  assert.equal(wallet.balance({ userId: 'user-a' }).availableCents, 9900);
  wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-2', amountCents: 100 });
  wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-3', amountCents: 100 });
  assert.throws(
    () => wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-4', amountCents: 100 }),
    (error) => error instanceof WalletError && error.code === WALLET_CODE.DAILY_CAP_EXCEEDED
  );
  clock.advance(864e5);
  assert.equal(wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-4', amountCents: 100 }).charged, true);
  assert.equal(wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: '退' }).refunded, true);
  assert.equal(wallet.balance({ userId: 'user-a' }).availableCents, 9700);
  assert.equal(wallet.summary({ userId: 'user-a' }).chargedCents, 400);
});

test('返回值冻结；未知用户余额为 0', () => {
  const { wallet } = makeWallet();
  const topUp = wallet.topUp({ userId: 'user-a', amountCents: 1000, idempotencyKey: 'dep-1' });
  const charge = wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 500 });
  const refund = wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: 'r' });
  for (const value of [topUp, charge, refund, wallet.balance({ userId: 'user-a' }), wallet.summary({ userId: 'user-a' })]) {
    assert.ok(Object.isFrozen(value));
  }
  assert.deepEqual({ ...wallet.balance({ userId: 'user-none' }) }, { availableCents: 0, frozenCents: 0 });
  const emptySummary = wallet.summary({ userId: 'user-none' });
  assert.equal(emptySummary.availableCents, 0);
  assert.equal(emptySummary.depositedCents, 0);
});

test('参数校验：构造参数 / userId / messageId / idempotencyKey / reason 非法 → TypeError/RangeError', () => {
  assert.throws(() => new Wallet({ clock: 'now' }), TypeError);
  assert.throws(() => new Wallet({ ledger: {} }), TypeError);
  assert.throws(() => new Wallet({ dailySendCapCents: -1 }), RangeError);
  assert.throws(() => new Wallet({ messageState: 'x' }), TypeError);
  assert.throws(() => new Wallet({ accountState: 'x' }), TypeError);

  const { wallet } = makeWallet();
  assert.throws(() => wallet.balance({}), TypeError);
  assert.throws(() => wallet.topUp({ userId: 'user-a', amountCents: 100 }), TypeError);
  assert.throws(() => wallet.topUp({ amountCents: 100, idempotencyKey: 'k' }), TypeError);
  assert.throws(() => wallet.chargeForMessage({ userId: 'user-a', amountCents: 100 }), TypeError);
  wallet.topUp({ userId: 'user-a', amountCents: 1000, idempotencyKey: 'dep-1' });
  wallet.chargeForMessage({ userId: 'user-a', messageId: 'msg-1', amountCents: 100 });
  assert.throws(() => wallet.refund({ userId: 'user-a', messageId: 'msg-1' }), TypeError);
  assert.throws(() => wallet.refund({ userId: 'user-a', messageId: 'msg-1', reason: '' }), TypeError);
});
