// 测试文件：§49 支付渠道抽象（意图 / HMAC 回调验签 / 时间窗 / 幂等入账 / 退款）/ 运行：node services/payment/test/payment-channel.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHmac } from 'node:crypto';
import { Ledger } from '../src/ledger.mjs';
import {
  CHANNEL_KIND,
  PAYMENT_CODE,
  PAYMENT_STATE,
  PaymentChannelError,
  PaymentIntentService,
} from '../src/channel-adapter.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';
const BASE_MS = Date.parse(BASE_ISO);
const SECRET = 'mock-secret-keep-it-safe';
const WINDOW_MS = 5 * 60e3;

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

function makeService({ channels = [CHANNEL_KIND.MOCK], secret = SECRET, timestampWindowMs = WINDOW_MS } = {}) {
  const clock = makeClock();
  const ledger = new Ledger({ clock });
  const service = new PaymentIntentService({ clock, ledger, channels, secret, timestampWindowMs });
  return { clock, ledger, service };
}

/** 渠道回调：对原始 body 字节做 HMAC-SHA256，产出 {headers, body} */
function signedCallback(body, { ts = BASE_MS, secret = SECRET } = {}) {
  const rawBody = typeof body === 'string' ? body : JSON.stringify(body);
  const signature = createHmac('sha256', secret).update(rawBody, 'utf8').digest('hex');
  return { body: rawBody, headers: { 'x-signature': `sha256=${signature}`, 'x-timestamp': String(ts) } };
}

function callbackBody({ orderId = 'order-1', amountCents = 5000, providerTxnId = 'txn-1', status = null } = {}) {
  const body = { orderId, amountCents, providerTxnId };
  if (status !== null) body.status = status;
  return body;
}

function createOrder(service, overrides = {}) {
  return service.createIntent({
    userId: 'user-a',
    amountCents: 5000,
    channel: CHANNEL_KIND.MOCK,
    orderId: 'order-1',
    description: '充值',
    ...overrides,
  });
}

function pay(service, order, { providerTxnId = 'txn-1', ts = BASE_MS } = {}) {
  const body = callbackBody({ orderId: order.orderId, amountCents: order.amountCents, providerTxnId });
  return service.handleCallback({ channel: order.channel, ...signedCallback(body, { ts }) });
}

test('createIntent：冻结订单 + payUrl 占位；同 orderId → ORDER_EXISTS；未启用渠道 → CHANNEL_UNSUPPORTED', () => {
  const { service } = makeService();
  const order = createOrder(service);
  assert.equal(order.orderId, 'order-1');
  assert.equal(order.userId, 'user-a');
  assert.equal(order.amountCents, 5000);
  assert.equal(order.channel, CHANNEL_KIND.MOCK);
  assert.equal(order.state, PAYMENT_STATE.INTENT);
  assert.equal(order.payUrl, 'mock://pay/order-1');
  assert.equal(order.createdAt, BASE_ISO);
  assert.ok(Object.isFrozen(order));

  assert.throws(
    () => createOrder(service),
    (error) => error instanceof PaymentChannelError && error.code === PAYMENT_CODE.ORDER_EXISTS
  );
  // 不传 orderId 自动生成
  assert.match(service.createIntent({ userId: 'user-b', amountCents: 100 }).orderId, /^order-/);
  // 未启用渠道绝不静默走 mock
  assert.throws(
    () => service.createIntent({ userId: 'user-a', amountCents: 100, channel: CHANNEL_KIND.WECHAT }),
    (error) => error instanceof PaymentChannelError && error.code === PAYMENT_CODE.CHANNEL_UNSUPPORTED
  );
  assert.throws(() => service.createIntent({ userId: 'user-a', amountCents: 0 }), { code: 'INVALID_AMOUNT' });
});

test('正确签名回调 → PAID + 账本恰好 1 条 DEPOSIT（幂等键 deposit:<orderId>）', () => {
  const { service, ledger } = makeService();
  const order = createOrder(service);
  assert.equal(ledger.entries().length, 0);

  const result = pay(service, order);
  assert.equal(result.ok, true);
  assert.equal(result.duplicate, false);
  assert.equal(result.order.state, PAYMENT_STATE.PAID);
  assert.equal(result.order.paidAt, BASE_ISO);
  assert.equal(result.ledgerEntry.type, 'DEPOSIT');
  assert.equal(result.ledgerEntry.userId, 'user-a');
  assert.equal(result.ledgerEntry.amountCents, 5000);
  assert.equal(result.ledgerEntry.idempotencyKey, 'deposit:order-1');
  assert.equal(ledger.entries().filter((entry) => entry.type === 'DEPOSIT').length, 1);
  assert.equal(service.get('order-1').state, PAYMENT_STATE.PAID);
});

test('验签失败（签名错/缺头/篡改 body/格式非法）→ SIGNATURE_INVALID 且账本条数不变、状态不变', () => {
  const { service, ledger } = makeService();
  const order = createOrder(service);
  const body = callbackBody();

  // 缺 x-signature
  assert.throws(
    () => service.handleCallback({ channel: 'mock', headers: { 'x-timestamp': String(BASE_MS) }, body: JSON.stringify(body) }),
    (error) => error.code === PAYMENT_CODE.SIGNATURE_INVALID
  );
  // 错误 secret
  const wrongSecret = signedCallback(body, { secret: 'wrong-secret' });
  assert.throws(
    () => service.handleCallback({ channel: 'mock', ...wrongSecret }),
    (error) => error.code === PAYMENT_CODE.SIGNATURE_INVALID
  );
  // 签名格式非法
  assert.throws(
    () => service.handleCallback({ channel: 'mock', headers: { 'x-signature': 'sha256=abc', 'x-timestamp': String(BASE_MS) }, body: JSON.stringify(body) }),
    (error) => error.code === PAYMENT_CODE.SIGNATURE_INVALID
  );
  // 签名后篡改原始 body
  const good = signedCallback(body);
  assert.throws(
    () => service.handleCallback({ channel: 'mock', headers: good.headers, body: good.body.replace('5000', '5001') }),
    (error) => error.code === PAYMENT_CODE.SIGNATURE_INVALID
  );

  assert.equal(ledger.entries().length, 0, '验签失败不得写任何账本流水');
  assert.equal(service.get('order-1').state, PAYMENT_STATE.INTENT);
});

test('时间窗：超窗/未来时间 → TIMESTAMP_EXPIRED；恰好窗口边界可通过（毫秒与秒两种时间戳）', () => {
  const { service, ledger } = makeService();
  const order = createOrder(service);
  const body = callbackBody();
  assert.throws(
    () => service.handleCallback({ channel: 'mock', ...signedCallback(body, { ts: BASE_MS - WINDOW_MS - 1 }) }),
    (error) => error.code === PAYMENT_CODE.TIMESTAMP_EXPIRED
  );
  assert.throws(
    () => service.handleCallback({ channel: 'mock', ...signedCallback(body, { ts: BASE_MS + WINDOW_MS + 1 }) }),
    (error) => error.code === PAYMENT_CODE.TIMESTAMP_EXPIRED
  );
  assert.equal(ledger.entries().length, 0);

  // 恰好边界（|now-ts| === window）允许；时间戳用秒也支持
  const edge = service.handleCallback({
    channel: 'mock',
    ...signedCallback(body, { ts: (BASE_MS - WINDOW_MS) / 1000 }),
  });
  assert.equal(edge.order.state, PAYMENT_STATE.PAID);
  assert.equal(ledger.entries().length, 1);
});

test('金额与订单不一致 → AMOUNT_MISMATCH，状态仍 INTENT、不入账', () => {
  const { service, ledger } = makeService();
  const order = createOrder(service);
  const body = callbackBody({ amountCents: 4999, providerTxnId: 'txn-x' });
  assert.throws(
    () => service.handleCallback({ channel: 'mock', ...signedCallback(body) }),
    (error) => error.code === PAYMENT_CODE.AMOUNT_MISMATCH
  );
  assert.equal(ledger.entries().length, 0);
  assert.equal(service.get('order-1').state, PAYMENT_STATE.INTENT);
});

test('同一 providerTxnId 回调两次 → 第二次 duplicate:true，账本仍 1 条；不同 txnId 复用订单 → CALLBACK_REPLAYED', () => {
  const { service, ledger } = makeService();
  const order = createOrder(service);
  const first = pay(service, order, { providerTxnId: 'txn-dupe' });
  assert.equal(first.duplicate, false);
  const replay = pay(service, order, { providerTxnId: 'txn-dupe' });
  assert.equal(replay.ok, true);
  assert.equal(replay.duplicate, true);
  assert.equal(replay.order.state, PAYMENT_STATE.PAID);
  assert.equal(replay.ledgerEntry.id, first.ledgerEntry.id);
  assert.equal(ledger.entries().filter((entry) => entry.type === 'DEPOSIT').length, 1);

  const otherBody = callbackBody({ orderId: 'order-1', amountCents: 5000, providerTxnId: 'txn-other' });
  assert.throws(
    () => service.handleCallback({ channel: 'mock', ...signedCallback(otherBody) }),
    (error) => error.code === PAYMENT_CODE.CALLBACK_REPLAYED
  );
});

test('未知订单 → ORDER_NOT_FOUND；未启用渠道回调 → CHANNEL_UNSUPPORTED（不碰账本）', () => {
  const { service, ledger } = makeService();
  const unknownBody = callbackBody({ orderId: 'order-unknown' });
  assert.throws(
    () => service.handleCallback({ channel: 'mock', ...signedCallback(unknownBody) }),
    (error) => error.code === PAYMENT_CODE.ORDER_NOT_FOUND
  );
  const order = createOrder(service);
  const body = callbackBody({ orderId: order.orderId });
  assert.throws(
    () => service.handleCallback({ channel: CHANNEL_KIND.WECHAT, ...signedCallback(body) }),
    (error) => error.code === PAYMENT_CODE.CHANNEL_UNSUPPORTED
  );
  assert.equal(ledger.entries().length, 0);
});

test('refund：仅 PAID 可退、写 REFUND 幂等；二次退款 duplicate、账本仍 1 条；未支付订单 → ILLEGAL_STATE', () => {
  const { service, ledger } = makeService();
  const order = createOrder(service);
  assert.throws(
    () => service.refund({ orderId: 'order-1' }),
    (error) => error.code === PAYMENT_CODE.ILLEGAL_STATE
  );

  pay(service, order);
  const refund = service.refund({ orderId: 'order-1', reason: '用户取消' });
  assert.equal(refund.ok, true);
  assert.equal(refund.duplicate, false);
  assert.equal(refund.order.state, PAYMENT_STATE.REFUNDED);
  assert.equal(refund.ledgerEntry.type, 'REFUND');
  assert.equal(refund.ledgerEntry.idempotencyKey, 'refund:order-1');
  assert.equal(refund.ledgerEntry.amountCents, 5000);
  assert.equal(ledger.entries().filter((entry) => entry.type === 'REFUND').length, 1);

  const again = service.refund({ orderId: 'order-1' });
  assert.equal(again.duplicate, true);
  assert.equal(again.ledgerEntry.id, refund.ledgerEntry.id);
  assert.equal(ledger.entries().filter((entry) => entry.type === 'REFUND').length, 1);

  assert.throws(
    () => service.refund({ orderId: 'order-missing' }),
    (error) => error.code === PAYMENT_CODE.ORDER_NOT_FOUND
  );
  // 已退款订单不能再收到支付回调
  assert.throws(() => pay(service, order), (error) => error.code === PAYMENT_CODE.ILLEGAL_STATE);
});

test('PENDING 回调只改状态不入账，随后 SUCCESS 才入账一次', () => {
  const { service, ledger } = makeService();
  const order = createOrder(service);
  const pendingBody = callbackBody({ orderId: 'order-1', amountCents: 5000, providerTxnId: 'txn-p', status: 'PENDING' });
  const pending = service.handleCallback({ channel: 'mock', ...signedCallback(pendingBody) });
  assert.equal(pending.order.state, PAYMENT_STATE.PENDING);
  assert.equal(ledger.entries().length, 0);

  const success = pay(service, order, { providerTxnId: 'txn-p' });
  assert.equal(success.order.state, PAYMENT_STATE.PAID);
  assert.equal(ledger.entries().filter((entry) => entry.type === 'DEPOSIT').length, 1);
});

test('未配置 secret 时 fail-closed：任何回调都 SIGNATURE_INVALID，不写账本', () => {
  const { service, ledger } = makeService({ secret: null });
  const order = createOrder(service);
  const body = callbackBody({ orderId: order.orderId });
  const signed = signedCallback(body, { secret: 'whatever' });
  assert.throws(
    () => service.handleCallback({ channel: 'mock', ...signed }),
    (error) => error.code === PAYMENT_CODE.SIGNATURE_INVALID
  );
  assert.equal(ledger.entries().length, 0);
});

test('get/list/stats：按状态与用户过滤、统计金额；未知订单 ORDER_NOT_FOUND；返回冻结', () => {
  const { service } = makeService();
  const first = createOrder(service, { orderId: 'order-1', userId: 'user-a' });
  service.createIntent({ userId: 'user-b', amountCents: 2000, orderId: 'order-2' });
  pay(service, first);

  assert.equal(service.list().length, 2);
  assert.equal(service.list({ state: PAYMENT_STATE.PAID }).length, 1);
  assert.equal(service.list({ state: PAYMENT_STATE.INTENT }).length, 1);
  assert.equal(service.list({ userId: 'user-b' }).length, 1);
  assert.ok(Object.isFrozen(service.list()));
  assert.ok(Object.isFrozen(service.list()[0]));

  const stats = service.stats();
  assert.equal(stats.total, 2);
  assert.equal(stats.paid, 1);
  assert.equal(stats.intent, 1);
  assert.equal(stats.paidAmountCents, 5000);
  assert.ok(Object.isFrozen(stats));

  assert.throws(
    () => service.get('order-missing'),
    (error) => error.code === PAYMENT_CODE.ORDER_NOT_FOUND
  );
  assert.throws(() => service.list({ state: 'NOPE' }), TypeError);
});

test('构造参数校验：ledger/channels/secret/window 非法 → TypeError/RangeError', () => {
  assert.throws(() => new PaymentIntentService({}), TypeError);
  assert.throws(() => new PaymentIntentService({ ledger: {} }), TypeError);
  assert.throws(() => new PaymentIntentService({ ledger: new Ledger(), channels: ['paypal'] }), TypeError);
  assert.throws(() => new PaymentIntentService({ ledger: new Ledger(), secret: '' }), TypeError);
  assert.throws(() => new PaymentIntentService({ ledger: new Ledger(), timestampWindowMs: 0 }), RangeError);
  assert.throws(() => new PaymentIntentService({ ledger: new Ledger(), clock: 'now' }), TypeError);
});
