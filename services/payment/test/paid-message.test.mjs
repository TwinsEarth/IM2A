// 测试文件：付费消息 9 态状态机 + 两档计费 + 退款/拒绝/幂等 §9 / 运行：node services/payment/test/paid-message.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Ledger } from '../src/ledger.mjs';
import { BILLING, MSG_STATE, PaidMessageError, PaidMessageService } from '../src/paid-message.mjs';
import * as barrel from '../src/index.mjs';

const BASE_ISO = '2026-10-21T10:00:00.000Z';

function setup(options = {}) {
  const state = { nowMs: Date.parse(BASE_ISO) };
  const clock = () => new Date(state.nowMs);
  const ledger = new Ledger({ clock });
  const service = new PaidMessageService({ ledger, clock, ...options });
  return {
    ledger,
    service,
    clock,
    state,
    advance(ms) {
      state.nowMs += ms;
    },
    advanceDays(days) {
      state.nowMs += days * 864e5;
    },
  };
}

let intentSeq = 0;
function intent(service, overrides = {}) {
  intentSeq += 1;
  return service.submitIntent({
    senderUserId: 'user-a',
    senderAgentId: 'agent-a',
    receiverUserId: 'user-b',
    topic: '你好',
    summary: '在读书会看到你',
    billingMode: BILLING.SEND_PAY,
    amountCents: 100,
    idempotencyKey: `intent-${intentSeq}`,
    ...overrides,
  });
}

/** 造一条「已预审 PASS、待支付」的消息 */
function awaitingPayment(service, overrides = {}) {
  const message = intent(service, overrides);
  return service.screen({ messageId: message.id, verdict: 'PASS' });
}

function entriesOfType(ledger, type) {
  return ledger.entries().filter((entry) => entry.type === type);
}

function paidAndViewed(service) {
  const message = awaitingPayment(service);
  service.pay({ messageId: message.id, payerUserId: 'user-a' });
  service.view({ messageId: message.id, viewerUserId: 'user-b' });
  return message;
}

test('正常路径 SEND_PAY：INTENT→AWAITING_PAYMENT→PAID→VIEWED→REPLIED→SETTLED，CHARGE/SETTLE 各一条', () => {
  const { ledger, service } = setup();
  const created = intent(service);
  assert.equal(created.state, MSG_STATE.INTENT);
  assert.match(created.id, /^msg-/);
  assert.ok(Object.isFrozen(created));
  // 集成字段契约：返回对象必须含以下顶层键
  for (const key of [
    'id', 'senderUserId', 'receiverUserId', 'senderAgentId', 'topic', 'summary',
    'state', 'billingMode', 'amountCents', 'createdAt', 'updatedAt',
  ]) {
    assert.ok(Object.hasOwn(created, key), `message 缺少顶层字段 ${key}`);
  }

  const screened = service.screen({ messageId: created.id, verdict: 'PASS' });
  assert.equal(screened.state, MSG_STATE.AWAITING_PAYMENT);
  assert.equal(screened.screening.verdict, 'PASS');

  const paidResult = service.pay({ messageId: created.id, payerUserId: 'user-a' });
  assert.equal(paidResult.charged, true);
  assert.equal(paidResult.message.state, MSG_STATE.PAID);
  assert.equal(paidResult.message.paidAt, BASE_ISO);
  assert.equal(entriesOfType(ledger, 'CHARGE').length, 1);
  assert.equal(ledger.balanceOf('user-a'), 100);

  const viewed = service.view({ messageId: created.id, viewerUserId: 'user-b' });
  assert.equal(viewed.state, MSG_STATE.VIEWED);
  const replied = service.reply({ messageId: created.id, replyText: '你好呀' });
  assert.equal(replied.state, MSG_STATE.REPLIED);
  assert.equal(replied.replyText, '你好呀');

  const settled = service.settle({ messageId: created.id });
  assert.equal(settled.state, MSG_STATE.SETTLED);
  const settleEntries = entriesOfType(ledger, 'SETTLE');
  assert.equal(settleEntries.length, 1);
  assert.equal(settleEntries[0].userId, 'user-b');
  assert.equal(settleEntries[0].amountCents, 100);
  assert.equal(settleEntries[0].ref, created.id);
  assert.equal(settled.settleEntryId, settleEntries[0].id);
  assert.equal(ledger.balanceOf('user-b'), 0, 'SETTLE 不计入 balanceOf（接收方余额不因结算增加）');
  assert.deepEqual({ ...ledger.stats() }, { count: 2, charged: 1, refunded: 0, settled: 1 });
});

test('铁律1：未 screen 的 pay 抛 NEEDS_SCREENING 且账本零入账、状态不动', () => {
  const { ledger, service } = setup();
  const created = intent(service);
  assert.throws(() => service.pay({ messageId: created.id, payerUserId: 'user-a' }), { code: 'NEEDS_SCREENING' });
  assert.equal(ledger.entries().length, 0);
  assert.equal(service.get(created.id).state, MSG_STATE.INTENT);
});

test('铁律1：预审 BLOCK 后消息落 REFUSED，pay 抛 NOT_PAYABLE 且账本零入账', () => {
  const { ledger, service } = setup();
  const created = intent(service);
  const refused = service.screen({ messageId: created.id, verdict: 'BLOCK' });
  assert.equal(refused.state, MSG_STATE.REFUSED);
  assert.equal(refused.screening.verdict, 'BLOCK');
  // BLOCK 之后 pay 必须显式失败，绝不静默返回、更不得扣费
  assert.throws(() => service.pay({ messageId: created.id, payerUserId: 'user-a' }), { code: 'NOT_PAYABLE' });
  assert.equal(ledger.entries().length, 0);
});

test('铁律2（网络重试连点 5 次）：同一 messageId 账本仅 1 条 CHARGE，其余 charged:false 且 entry 为同一已存在条目', () => {
  const { ledger, service } = setup();
  const message = awaitingPayment(service);
  const results = [];
  for (let i = 0; i < 5; i += 1) {
    results.push(service.pay({ messageId: message.id, payerUserId: 'user-a' }));
  }
  assert.equal(results[0].charged, true, '第 1 次必须真实扣费');
  for (let i = 1; i < 5; i += 1) {
    assert.equal(results[i].charged, false, `第 ${i + 1} 次 pay 不得再次扣费`);
    assert.ok(results[i].entry !== null, `第 ${i + 1} 次 pay 必须返回已存在的 entry（前端会读）`);
    assert.equal(results[i].entry.id, results[0].entry.id);
    assert.equal(results[i].message.state, MSG_STATE.PAID);
  }
  const charges = entriesOfType(ledger, 'CHARGE');
  assert.equal(charges.length, 1, '账本只允许 1 条 CHARGE');
  assert.equal(charges[0].idempotencyKey, `pay:${message.id}`);
  assert.equal(ledger.entries().length, 1);
  assert.equal(ledger.balanceOf('user-a'), 100);
  assert.equal(ledger.stats().charged, 1);
});

test('铁律2（账本兜底）：账本已有 pay:<id> 而消息仍 AWAITING_PAYMENT 时，pay 不二次扣费（charged:false）', () => {
  const { ledger, service } = setup();
  const message = awaitingPayment(service);
  // 模拟「账本已落库、消息状态未更新」的中间窗口（崩溃/重放）
  ledger.post({
    idempotencyKey: `pay:${message.id}`,
    type: 'CHARGE',
    userId: 'user-a',
    amountCents: 100,
    ref: message.id,
    meta: { messageId: message.id },
  });
  const result = service.pay({ messageId: message.id, payerUserId: 'user-a' });
  assert.equal(result.charged, false);
  assert.equal(result.message.state, MSG_STATE.PAID);
  assert.equal(entriesOfType(ledger, 'CHARGE').length, 1);
  assert.equal(ledger.balanceOf('user-a'), 100);
});

test('发送意图幂等：同 idempotencyKey 重试返回同一条消息，且不重复占用每日额度', () => {
  const { service } = setup();
  const first = intent(service, { idempotencyKey: 'retry-intent-1' });
  const again = intent(service, { idempotencyKey: 'retry-intent-1' });
  assert.equal(again.id, first.id);
  assert.equal(service.list().length, 1);
  // 额度只被占用 1 次：再补 2 条才达到 3 条上限
  intent(service);
  intent(service);
  assert.equal(service.list().length, 3);
  assert.throws(() => intent(service), { code: 'DAILY_CAP_EXCEEDED' });
});

test('每日发送上限：dailySendCap=3，第 4 条 DAILY_CAP_EXCEEDED；跨日后额度恢复', () => {
  const { service, advanceDays } = setup({ dailySendCap: 3 });
  intent(service);
  intent(service);
  intent(service);
  assert.throws(() => intent(service), { code: 'DAILY_CAP_EXCEEDED' });
  advanceDays(1);
  const tomorrow = intent(service);
  assert.equal(tomorrow.state, MSG_STATE.INTENT);
});

test('被预审拦截（REFUSED）的消息不占用发送方每日额度', () => {
  const { service } = setup({ dailySendCap: 1 });
  const blocked = intent(service);
  service.screen({ messageId: blocked.id, verdict: 'BLOCK' });
  const next = intent(service); // 若 REFUSED 占额度，这里会 DAILY_CAP_EXCEEDED
  assert.equal(next.state, MSG_STATE.INTENT);
});

test('自己给自己发付费消息 → SELF_MESSAGE 且不落库', () => {
  const { service } = setup();
  assert.throws(() => intent(service, { receiverUserId: 'user-a' }), { code: 'SELF_MESSAGE' });
  assert.equal(service.list().length, 0);
});

test('金额边界：0 / -1 / 1.5 / "100" / NaN / Infinity 在 submitIntent 即被拒绝（INVALID_AMOUNT）', () => {
  const { ledger, service } = setup();
  for (const amountCents of [0, -1, 1.5, '100', NaN, Infinity]) {
    assert.throws(() => intent(service, { amountCents }), { code: 'INVALID_AMOUNT' }, `应拒绝 ${String(amountCents)}`);
  }
  assert.equal(ledger.entries().length, 0);
  assert.equal(service.list().length, 0);
});

test('超过单笔审批上限 approveCapCents → NOT_PAYABLE（等于上限允许）', () => {
  const { service } = setup({ approveCapCents: 500 });
  assert.throws(() => intent(service, { amountCents: 501 }), { code: 'NOT_PAYABLE' });
  const allowed = intent(service, { amountCents: 500 });
  assert.equal(allowed.amountCents, 500);
});

test('PAYER_MISMATCH：payerUserId 必须等于 senderUserId，接收方/第三方付款一律拒绝且零入账', () => {
  const { ledger, service } = setup();
  const message = awaitingPayment(service);
  for (const payerUserId of ['user-b', 'user-x']) {
    assert.throws(
      () => service.pay({ messageId: message.id, payerUserId }),
      (error) => error instanceof PaidMessageError && error.code === 'PAYER_MISMATCH',
      `${payerUserId} 不得替发送方付费`
    );
  }
  assert.throws(() => service.pay({ messageId: message.id }), TypeError);
  assert.equal(ledger.entries().length, 0);
  const result = service.pay({ messageId: message.id, payerUserId: 'user-a' });
  assert.equal(result.charged, true);
  // 已 PAID 后仍不允许错误付款方（重试窗口也不放宽）
  assert.throws(() => service.pay({ messageId: message.id, payerUserId: 'user-b' }), { code: 'PAYER_MISMATCH' });
  assert.equal(service.pay({ messageId: message.id, payerUserId: 'user-a' }).charged, false);
  assert.equal(entriesOfType(ledger, 'CHARGE').length, 1);
});

test('refuse：INTENT|AWAITING_PAYMENT → REFUSED，且不产生任何账本条目；之后 pay 抛 NOT_PAYABLE', () => {
  // INTENT 直接拒绝
  const first = setup();
  const draft = intent(first.service);
  const refusedDraft = first.service.refuse({ messageId: draft.id, reason: '不想接收陌生人消息' });
  assert.equal(refusedDraft.state, MSG_STATE.REFUSED);
  assert.equal(refusedDraft.refuseReason, '不想接收陌生人消息');
  assert.equal(first.ledger.entries().length, 0);
  assert.throws(() => first.service.pay({ messageId: draft.id, payerUserId: 'user-a' }), { code: 'NOT_PAYABLE' });
  assert.throws(() => first.service.refuse({ messageId: draft.id }), { code: 'ILLEGAL_STATE' });

  // 预审 PASS 之后、付款之前驳回
  const second = setup();
  const awaiting = awaitingPayment(second.service);
  const refused = second.service.refuse({ messageId: awaiting.id });
  assert.equal(refused.state, MSG_STATE.REFUSED);
  assert.equal(second.ledger.entries().length, 0);
  assert.throws(() => second.service.pay({ messageId: awaiting.id, payerUserId: 'user-a' }), { code: 'NOT_PAYABLE' });
  assert.equal(second.ledger.entries().length, 0);

  // 非可拒绝状态 → ILLEGAL_STATE
  const third = setup();
  const paid = awaitingPayment(third.service);
  third.service.pay({ messageId: paid.id, payerUserId: 'user-a' });
  assert.throws(() => third.service.refuse({ messageId: paid.id }), { code: 'ILLEGAL_STATE' });
  assert.equal(entriesOfType(third.ledger, 'CHARGE').length, 1);
});

test('合法迁移约束：view 仅 PAID、reply 仅 VIEWED、重复 view/screen 在错误状态下 → ILLEGAL_STATE', () => {
  const { service } = setup();
  const created = intent(service);
  assert.throws(() => service.view({ messageId: created.id, viewerUserId: 'user-b' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => service.reply({ messageId: created.id }), { code: 'ILLEGAL_STATE' });

  const awaiting = service.screen({ messageId: created.id, verdict: 'PASS' });
  assert.equal(awaiting.state, MSG_STATE.AWAITING_PAYMENT);
  assert.throws(() => service.reply({ messageId: created.id }), { code: 'ILLEGAL_STATE' });

  service.pay({ messageId: created.id, payerUserId: 'user-a' });
  assert.throws(() => service.reply({ messageId: created.id }), { code: 'ILLEGAL_STATE' }, '未 view 不能 reply');
  const viewed = service.view({ messageId: created.id, viewerUserId: 'user-b' });
  assert.equal(viewed.state, MSG_STATE.VIEWED);
  assert.throws(() => service.view({ messageId: created.id, viewerUserId: 'user-b' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => service.screen({ messageId: created.id, verdict: 'BLOCK' }), { code: 'ILLEGAL_STATE' });
});

test('退款：仅 PAID|VIEWED 可退，账本记 REFUND 冲平余额；REPLIED 之后退款 → ILLEGAL_STATE', () => {
  // PAID 直接退
  const paidCase = setup();
  const paidMessage = awaitingPayment(paidCase.service);
  paidCase.service.pay({ messageId: paidMessage.id, payerUserId: 'user-a' });
  assert.equal(paidCase.ledger.balanceOf('user-a'), 100);
  const refunded = paidCase.service.refund({ messageId: paidMessage.id, reason: '误发' });
  assert.equal(refunded.state, MSG_STATE.REFUNDED);
  assert.equal(entriesOfType(paidCase.ledger, 'REFUND').length, 1);
  assert.equal(paidCase.ledger.balanceOf('user-a'), 0);
  assert.throws(() => paidCase.service.refund({ messageId: paidMessage.id }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => paidCase.service.pay({ messageId: paidMessage.id, payerUserId: 'user-a' }), { code: 'ILLEGAL_STATE' });

  // VIEWED 可退
  const viewedCase = setup();
  const viewedMessage = paidAndViewed(viewedCase.service);
  assert.equal(viewedCase.service.refund({ messageId: viewedMessage.id }).state, MSG_STATE.REFUNDED);

  // REPLIED 不可退（争议走人工）
  const repliedCase = setup();
  const message = awaitingPayment(repliedCase.service);
  repliedCase.service.pay({ messageId: message.id, payerUserId: 'user-a' });
  repliedCase.service.view({ messageId: message.id, viewerUserId: 'user-b' });
  repliedCase.service.reply({ messageId: message.id, replyText: '收到' });
  assert.throws(() => repliedCase.service.refund({ messageId: message.id }), { code: 'ILLEGAL_STATE' });
  assert.equal(entriesOfType(repliedCase.ledger, 'REFUND').length, 0);
});

test('终态不可再流转：REFUSED / EXPIRED / SETTLED / REFUNDED 上的 pay 等操作 → ILLEGAL_STATE', () => {
  // REFUSED
  const refusedCase = setup();
  const refusedDraft = intent(refusedCase.service);
  refusedCase.service.screen({ messageId: refusedDraft.id, verdict: 'BLOCK' });
  assert.throws(() => refusedCase.service.view({ messageId: refusedDraft.id, viewerUserId: 'user-b' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => refusedCase.service.refund({ messageId: refusedDraft.id }), { code: 'ILLEGAL_STATE' });
  assert.equal(refusedCase.ledger.entries().length, 0);

  // EXPIRED
  const expiredCase = setup();
  const expiredMessage = awaitingPayment(expiredCase.service);
  assert.equal(expiredCase.service.expire({ messageId: expiredMessage.id }).state, MSG_STATE.EXPIRED);
  assert.throws(() => expiredCase.service.pay({ messageId: expiredMessage.id, payerUserId: 'user-a' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => expiredCase.service.screen({ messageId: expiredMessage.id, verdict: 'PASS' }), { code: 'ILLEGAL_STATE' });
  assert.equal(expiredCase.ledger.entries().length, 0);

  // SETTLED
  const settledCase = setup();
  const settledMessage = awaitingPayment(settledCase.service);
  settledCase.service.pay({ messageId: settledMessage.id, payerUserId: 'user-a' });
  settledCase.service.view({ messageId: settledMessage.id, viewerUserId: 'user-b' });
  settledCase.service.reply({ messageId: settledMessage.id });
  settledCase.service.settle({ messageId: settledMessage.id });
  assert.throws(() => settledCase.service.pay({ messageId: settledMessage.id, payerUserId: 'user-a' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => settledCase.service.view({ messageId: settledMessage.id, viewerUserId: 'user-b' }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => settledCase.service.settle({ messageId: settledMessage.id }), { code: 'ILLEGAL_STATE' });
  assert.throws(() => settledCase.service.refund({ messageId: settledMessage.id }), { code: 'ILLEGAL_STATE' });
  assert.equal(entriesOfType(settledCase.ledger, 'SETTLE').length, 1, '重复 settle 不得产生第二条 SETTLE');
  assert.equal(entriesOfType(settledCase.ledger, 'CHARGE').length, 1);

  // REFUNDED
  const refundedCase = setup();
  const refundMessage = awaitingPayment(refundedCase.service);
  refundedCase.service.pay({ messageId: refundMessage.id, payerUserId: 'user-a' });
  refundedCase.service.refund({ messageId: refundMessage.id });
  assert.throws(() => refundedCase.service.pay({ messageId: refundMessage.id, payerUserId: 'user-a' }), { code: 'ILLEGAL_STATE' });
  assert.equal(entriesOfType(refundedCase.ledger, 'CHARGE').length, 1);
  assert.equal(entriesOfType(refundedCase.ledger, 'REFUND').length, 1);
});

test('未知 messageId：get/screen/pay/refuse/view/reply/settle/refund/expire 一律 NOT_FOUND', () => {
  const { service } = setup();
  const unknown = 'msg-unknown';
  assert.throws(() => service.get(unknown), (error) => error instanceof PaidMessageError && error.code === 'NOT_FOUND');
  const operations = [
    () => service.screen({ messageId: unknown, verdict: 'PASS' }),
    () => service.pay({ messageId: unknown, payerUserId: 'user-a' }),
    () => service.refuse({ messageId: unknown }),
    () => service.view({ messageId: unknown, viewerUserId: 'user-b' }),
    () => service.reply({ messageId: unknown }),
    () => service.settle({ messageId: unknown }),
    () => service.refund({ messageId: unknown }),
    () => service.expire({ messageId: unknown }),
  ];
  for (const operation of operations) {
    assert.throws(operation, (error) => error instanceof PaidMessageError && error.code === 'NOT_FOUND');
  }
});

test('两档计费：VIEW_REPLY_PAY 同样在 pay() 扣费一次，meta 记录 billingMode', () => {
  const { ledger, service } = setup();
  const created = intent(service, { billingMode: BILLING.VIEW_REPLY_PAY, amountCents: 250 });
  assert.equal(created.billingMode, BILLING.VIEW_REPLY_PAY);
  service.screen({ messageId: created.id, verdict: 'PASS' });
  const result = service.pay({ messageId: created.id, payerUserId: 'user-a' });
  assert.equal(result.charged, true);
  const charges = entriesOfType(ledger, 'CHARGE');
  assert.equal(charges.length, 1);
  assert.equal(charges[0].meta.billingMode, BILLING.VIEW_REPLY_PAY);
  assert.equal(ledger.balanceOf('user-a'), 250);
});

test('重复 screen PASS 幂等；AWAITING_PAYMENT 上收到 BLOCK 仍可落 REFUSED', () => {
  const { service } = setup();
  const message = awaitingPayment(service);
  assert.equal(service.screen({ messageId: message.id, verdict: 'PASS' }).state, MSG_STATE.AWAITING_PAYMENT);

  const second = awaitingPayment(service);
  const refused = service.screen({ messageId: second.id, verdict: 'BLOCK' });
  assert.equal(refused.state, MSG_STATE.REFUSED);
  assert.throws(() => service.pay({ messageId: second.id, payerUserId: 'user-a' }), { code: 'NOT_PAYABLE' });
});

test('不可变语义：迁移返回新对象，旧对象保持原状态；返回值冻结', () => {
  const { service } = setup();
  const draft = intent(service);
  const awaiting = service.screen({ messageId: draft.id, verdict: 'PASS' });
  assert.equal(draft.state, MSG_STATE.INTENT);
  assert.equal(awaiting.state, MSG_STATE.AWAITING_PAYMENT);
  assert.notEqual(draft, awaiting);
  assert.ok(Object.isFrozen(awaiting));
  assert.ok(Object.isFrozen(awaiting.history));

  const paid = service.pay({ messageId: awaiting.id, payerUserId: 'user-a' });
  assert.equal(awaiting.state, MSG_STATE.AWAITING_PAYMENT);
  assert.equal(paid.message.state, MSG_STATE.PAID);
  assert.ok(Object.isFrozen(paid));
  assert.ok(Object.isFrozen(paid.message));
  assert.ok(Object.isFrozen(paid.entry));
  assert.notEqual(paid.message, awaiting);
});

test('list：不传参返回全部；支持 receiverUserId / state / senderUserId 过滤且返回新数组', () => {
  const { service } = setup({ dailySendCap: 5 });
  const toB = intent(service, { receiverUserId: 'user-b' });
  intent(service, { receiverUserId: 'user-c' });
  const toB2 = intent(service, { receiverUserId: 'user-b' });
  service.screen({ messageId: toB.id, verdict: 'PASS' });
  service.screen({ messageId: toB2.id, verdict: 'BLOCK' });

  assert.equal(service.list().length, 3);
  assert.equal(service.list({ receiverUserId: 'user-b' }).length, 2);
  assert.equal(service.list({ receiverUserId: 'user-none' }).length, 0);
  assert.equal(service.list({ state: MSG_STATE.REFUSED }).length, 1);
  assert.equal(service.list({ state: MSG_STATE.AWAITING_PAYMENT }).length, 1);
  assert.equal(service.list({ senderUserId: 'user-a' }).length, 3);
  assert.equal(service.list({ senderUserId: 'user-x' }).length, 0);
  const copy = service.list();
  copy.push('junk');
  assert.equal(service.list().length, 3);
  for (const message of service.list()) {
    assert.ok(Object.isFrozen(message));
    assert.ok(Object.hasOwn(message, 'senderAgentId'));
    assert.ok(Object.hasOwn(message, 'createdAt'));
    assert.ok(Object.hasOwn(message, 'updatedAt'));
  }
});

test('构造参数校验：缺 ledger → TypeError；dailySendCap / approveCapCents 非法 → RangeError', () => {
  assert.throws(() => new PaidMessageService({}), TypeError);
  assert.throws(() => new PaidMessageService({ ledger: new Ledger(), dailySendCap: -1 }), RangeError);
  assert.throws(() => new PaidMessageService({ ledger: new Ledger(), approveCapCents: 0 }), RangeError);
});

test('barrel index.mjs 导出 payment 公共 API（demo-server 逐字 import 的名字）', () => {
  for (const name of ['Ledger', 'ENTRY_TYPE', 'LedgerError', 'PaidMessageService', 'MSG_STATE', 'BILLING', 'PaidMessageError']) {
    assert.ok(Object.hasOwn(barrel, name), `index.mjs 缺少导出 ${name}`);
    assert.notEqual(barrel[name], undefined);
  }
});
