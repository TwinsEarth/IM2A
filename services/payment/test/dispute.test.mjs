// 测试文件：§39 退款争议与时间线（NOT_ELIGIBLE 引导自助退 / DUPLICATE_OPEN / APPROVE 账本幂等退款 / REJECT 不动账本 / timeline 举证）/ 运行：node services/payment/test/dispute.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Ledger } from '../src/ledger.mjs';
import { DISPUTE_CODE, DISPUTE_STATE, DisputeError, DisputeService } from '../src/dispute.mjs';

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

function setup({ messageState = () => 'REPLIED', onAudit = () => {} } = {}) {
  const clock = makeClock();
  const ledger = new Ledger({ clock });
  const service = new DisputeService({ clock, ledger, messageState, onAudit });
  return { clock, ledger, service };
}

function seedCharge(ledger, { messageId = 'msg-1', userId = 'user-a', amountCents = 3000 } = {}) {
  return ledger.post({
    idempotencyKey: `pay:${messageId}`,
    type: 'CHARGE',
    userId,
    amountCents,
    ref: messageId,
    meta: { messageId },
  });
}

function openDispute(service, overrides = {}) {
  return service.open({
    messageId: 'msg-1',
    userId: 'user-a',
    reason: '已回复但内容与描述不符',
    evidence: [{ kind: 'text', ref: 't-1' }],
    ...overrides,
  });
}

const refundEntries = (ledger) => ledger.entries().filter((entry) => entry.type === 'REFUND');

test('未回复消息 open → NOT_ELIGIBLE（文案明确引导 §37 自助退款）', () => {
  for (const state of ['INTENT', 'AWAITING_PAYMENT', 'PAID', 'VIEWED', 'REFUNDED', 'EXPIRED', 'REFUSED', null]) {
    const { service, ledger } = setup({ messageState: () => state });
    seedCharge(ledger);
    assert.throws(
      () => openDispute(service),
      (error) => error instanceof DisputeError && error.code === DISPUTE_CODE.NOT_ELIGIBLE,
      `state=${String(state)} 不得开争议`
    );
  }
  // 错误文案必须告诉调用方走自助退款（防止把"已付款未回复"误送人工争议）
  const { service, ledger } = setup({ messageState: () => 'PAID' });
  seedCharge(ledger);
  try {
    openDispute(service);
    assert.fail('应当抛 NOT_ELIGIBLE');
  } catch (error) {
    assert.match(error.message, /§37|自助退款/);
  }
  // 没有扣费流水也不能开（无可退金额）
  const noCharge = setup();
  assert.throws(
    () => openDispute(noCharge.service),
    (error) => error instanceof DisputeError && error.code === DISPUTE_CODE.NOT_ELIGIBLE
  );
});

test('已回复/已结算可开：REPLIED / SETTLED 返回冻结 dispute 且登记 OPEN', () => {
  for (const state of ['REPLIED', 'SETTLED']) {
    const { service, ledger } = setup({ messageState: () => state });
    seedCharge(ledger);
    const dispute = openDispute(service);
    assert.match(dispute.id, /^disp-/);
    assert.equal(dispute.messageId, 'msg-1');
    assert.equal(dispute.userId, 'user-a');
    assert.equal(dispute.state, DISPUTE_STATE.OPEN);
    assert.equal(dispute.reason, '已回复但内容与描述不符');
    assert.deepEqual(dispute.evidence.map((item) => ({ ...item })), [{ kind: 'text', ref: 't-1' }]);
    assert.equal(dispute.createdAt, BASE_ISO);
    assert.equal(dispute.resolvedAt, null);
    assert.equal(dispute.refundEntryId, null);
    assert.ok(Object.isFrozen(dispute));
    assert.ok(Object.isFrozen(dispute.evidence));
    assert.equal(service.get(dispute.id).state, DISPUTE_STATE.OPEN);
  }
});

test('同一消息重复 open（未决）→ DUPLICATE_OPEN', () => {
  const { service, ledger } = setup();
  seedCharge(ledger);
  openDispute(service);
  assert.throws(
    () => openDispute(service),
    (error) => error instanceof DisputeError && error.code === DISPUTE_CODE.DUPLICATE_OPEN
  );
});

test('APPROVE：账本恰好 1 条 refund-dispute:<id> REFUND、金额取原 CHARGE；重复 resolve → ILLEGAL_STATE 且账本不再增加', () => {
  const { service, ledger } = setup();
  seedCharge(ledger, { amountCents: 3000 });
  const dispute = openDispute(service);
  const resolved = service.resolve({ disputeId: dispute.id, actorUserId: 'user-a', decision: 'APPROVE', note: '人工批准' });

  assert.equal(resolved.state, DISPUTE_STATE.APPROVED);
  assert.equal(resolved.decision, 'APPROVE');
  assert.equal(resolved.actorUserId, 'user-a');
  assert.equal(resolved.note, '人工批准');
  assert.equal(resolved.resolvedAt, BASE_ISO);
  assert.equal(resolved.refundAmountCents, 3000);

  const refunds = refundEntries(ledger);
  assert.equal(refunds.length, 1);
  assert.equal(refunds[0].idempotencyKey, `refund-dispute:${dispute.id}`);
  assert.equal(refunds[0].amountCents, 3000, '金额必须等于原 CHARGE');
  assert.equal(refunds[0].ref, 'msg-1');
  assert.equal(refunds[0].userId, 'user-a');
  assert.equal(resolved.refundEntryId, refunds[0].id);

  assert.throws(
    () => service.resolve({ disputeId: dispute.id, actorUserId: 'user-a', decision: 'APPROVE' }),
    (error) => error instanceof DisputeError && error.code === DISPUTE_CODE.ILLEGAL_STATE
  );
  assert.equal(refundEntries(ledger).length, 1, '重复 resolve 不得产生第二条 REFUND');
});

test('REJECT：状态 REJECTED 且不动账本（无 REFUND 条目）', () => {
  const { service, ledger } = setup();
  seedCharge(ledger);
  const dispute = openDispute(service);
  const resolved = service.resolve({ disputeId: dispute.id, actorUserId: 'user-a', decision: 'REJECT', note: '证据不足' });
  assert.equal(resolved.state, DISPUTE_STATE.REJECTED);
  assert.equal(resolved.decision, 'REJECT');
  assert.equal(resolved.refundEntryId, null);
  assert.equal(refundEntries(ledger).length, 0);
  assert.deepEqual(ledger.entries().map((entry) => entry.type), ['CHARGE']);
});

test('幂等重放：同 disputeId 的账本退款键重复触发不产生第二条（含崩溃恢复：账本已写、争议仍 OPEN）', () => {
  // a) 账本层幂等键：再次 post 同一 refund-dispute:<id> → duplicate:true，不新增条目
  const approved = setup();
  seedCharge(approved.ledger);
  const first = openDispute(approved.service);
  approved.service.resolve({ disputeId: first.id, actorUserId: 'user-a', decision: 'APPROVE' });
  const replay = approved.ledger.post({
    idempotencyKey: `refund-dispute:${first.id}`,
    type: 'REFUND',
    userId: 'user-a',
    amountCents: 3000,
    ref: 'msg-1',
  });
  assert.equal(replay.duplicate, true);
  assert.equal(refundEntries(approved.ledger).length, 1);

  // b) 崩溃恢复：退款已落账本但争议状态未更新 → resolve 复用已有退款，不重复记账
  const crashed = setup();
  seedCharge(crashed.ledger);
  const dispute = openDispute(crashed.service);
  crashed.ledger.post({
    idempotencyKey: `refund-dispute:${dispute.id}`,
    type: 'REFUND',
    userId: 'user-a',
    amountCents: 3000,
    ref: 'msg-1',
    meta: { disputeId: dispute.id },
  });
  const recovered = crashed.service.resolve({ disputeId: dispute.id, actorUserId: 'user-a', decision: 'APPROVE' });
  assert.equal(recovered.state, DISPUTE_STATE.APPROVED);
  assert.equal(refundEntries(crashed.ledger).length, 1, '恢复路径不得二次退款');
  assert.equal(recovered.refundEntryId, crashed.ledger.findByIdempotencyKey(`refund-dispute:${dispute.id}`).id);
});

test('防重复退款：该消息已有其它退款流水（如 §37 自助退款）时 APPROVE → ILLEGAL_STATE', () => {
  const { service, ledger } = setup();
  seedCharge(ledger);
  const dispute = openDispute(service);
  ledger.post({
    idempotencyKey: 'refund:msg-1',
    type: 'REFUND',
    userId: 'user-a',
    amountCents: 3000,
    ref: 'msg-1',
  });
  assert.throws(
    () => service.resolve({ disputeId: dispute.id, actorUserId: 'user-a', decision: 'APPROVE' }),
    (error) => error instanceof DisputeError && error.code === DISPUTE_CODE.ILLEGAL_STATE
  );
  assert.equal(refundEntries(ledger).length, 1, '不得产生第二条退款');
});

test('timeline：CHARGE →（争议事件）→ REFUND 时间升序、金额一致，只包含该消息', () => {
  const { service, ledger, clock } = setup();
  seedCharge(ledger, { amountCents: 3000 });
  const dispute = openDispute(service);
  clock.advance(1000);
  service.resolve({ disputeId: dispute.id, actorUserId: 'user-a', decision: 'APPROVE', note: '人工批准' });

  const timeline = service.timeline({ messageId: 'msg-1' });
  const types = timeline.map((event) => event.type);
  assert.ok(types.includes('CHARGE'));
  assert.ok(types.includes('REFUND'));
  assert.ok(types.includes('DISPUTE_OPEN'));
  assert.ok(types.includes('DISPUTE_APPROVED'));
  assert.ok(types.indexOf('CHARGE') < types.indexOf('REFUND'), 'CHARGE 必须排在 REFUND 之前');
  const chargeEvent = timeline.find((event) => event.type === 'CHARGE');
  const refundEvent = timeline.find((event) => event.type === 'REFUND');
  assert.equal(chargeEvent.amountCents, 3000);
  assert.equal(refundEvent.amountCents, 3000, '时间线上退款金额与扣费一致');
  assert.equal(chargeEvent.ref, 'msg-1');
  assert.equal(refundEvent.ref, 'msg-1');
  for (let index = 1; index < timeline.length; index += 1) {
    assert.ok(timeline[index - 1].at <= timeline[index].at, '时间线必须按时间升序');
  }
  assert.ok(Object.isFrozen(timeline));
  assert.ok(Object.isFrozen(timeline[0]));
  // 其它消息不受影响
  assert.deepEqual([...service.timeline({ messageId: 'msg-other' })], []);
});

test('actorUserId 校验：缺失 → TypeError；非属主 → FORBIDDEN；非法 decision → TypeError', () => {
  const { service, ledger } = setup();
  seedCharge(ledger);
  const dispute = openDispute(service);

  assert.throws(
    () => service.resolve({ disputeId: dispute.id, decision: 'APPROVE' }),
    TypeError,
    'actorUserId 缺失按调用方编程错误处理'
  );
  assert.throws(
    () => service.resolve({ disputeId: dispute.id, actorUserId: 'user-b', decision: 'APPROVE' }),
    (error) => error instanceof DisputeError && error.code === DISPUTE_CODE.FORBIDDEN
  );
  for (const decision of ['approve', 'REJECTED', '', null, 123]) {
    assert.throws(
      () => service.resolve({ disputeId: dispute.id, actorUserId: 'user-a', decision }),
      TypeError,
      `非法 decision=${String(decision)} 应拒绝`
    );
  }
  assert.throws(
    () => service.resolve({ disputeId: 'disp-unknown', actorUserId: 'user-a', decision: 'APPROVE' }),
    (error) => error instanceof DisputeError && error.code === DISPUTE_CODE.NOT_FOUND
  );
  // 失败尝试不影响状态：属主仍可正常处理
  assert.equal(service.get(dispute.id).state, DISPUTE_STATE.OPEN);
  assert.equal(
    service.resolve({ disputeId: dispute.id, actorUserId: 'user-a', decision: 'APPROVE' }).state,
    DISPUTE_STATE.APPROVED
  );
});

test('每次状态迁移都写审计（onAudit）：OPEN / REJECT / APPROVE 事件形状一致', () => {
  const events = [];
  const { service, ledger } = setup({ onAudit: (event) => events.push(event) });
  seedCharge(ledger, { amountCents: 3000 });
  const disputed = openDispute(service);
  service.resolve({ disputeId: disputed.id, actorUserId: 'user-a', decision: 'REJECT', note: '证据不足' });
  const second = openDispute(service);
  service.resolve({ disputeId: second.id, actorUserId: 'user-a', decision: 'APPROVE', note: '复审通过' });

  assert.deepEqual(events.map((event) => event.action), ['DISPUTE_OPEN', 'DISPUTE_REJECT', 'DISPUTE_OPEN', 'DISPUTE_APPROVE']);
  for (const event of events) {
    assert.equal(event.actor, 'user-a');
    assert.ok(Object.isFrozen(event));
    assert.ok(Object.isFrozen(event.detail));
    assert.equal(typeof event.at, 'string');
  }
  assert.equal(events[3].decision, DISPUTE_STATE.APPROVED);
  assert.equal(events[3].detail.amountCents, 3000);
  assert.equal(events[3].detail.refundEntryId, service.get(second.id).refundEntryId);
});

test('get / list / stats：按状态过滤、统计退款金额；未知 dispute → NOT_FOUND', () => {
  const { service, ledger } = setup();
  for (const messageId of ['msg-1', 'msg-2', 'msg-3']) seedCharge(ledger, { messageId });
  const first = service.open({ messageId: 'msg-1', userId: 'user-a', reason: 'r1' });
  const second = service.open({ messageId: 'msg-2', userId: 'user-a', reason: 'r2' });
  const third = service.open({ messageId: 'msg-3', userId: 'user-a', reason: 'r3' });
  service.resolve({ disputeId: first.id, actorUserId: 'user-a', decision: 'APPROVE' });
  service.resolve({ disputeId: second.id, actorUserId: 'user-a', decision: 'REJECT' });
  // third 保持 OPEN

  assert.equal(service.list().length, 3);
  assert.equal(service.list({ state: DISPUTE_STATE.OPEN }).length, 1);
  assert.equal(service.list({ state: DISPUTE_STATE.APPROVED }).length, 1);
  assert.equal(service.list({ state: DISPUTE_STATE.REJECTED }).length, 1);
  assert.ok(Object.isFrozen(service.list()));
  assert.deepEqual({ ...service.stats() }, { total: 3, open: 1, approved: 1, rejected: 1, refundedCents: 3000 });
  assert.equal(service.get(third.id).messageId, 'msg-3');
  assert.throws(
    () => service.get('disp-unknown'),
    (error) => error instanceof DisputeError && error.code === DISPUTE_CODE.NOT_FOUND
  );
});

test('状态机边界：REJECTED 后可重新开争议；APPROVED 后不可再开（NOT_ELIGIBLE）', () => {
  const { service, ledger } = setup();
  seedCharge(ledger);
  const first = openDispute(service);
  service.resolve({ disputeId: first.id, actorUserId: 'user-a', decision: 'REJECT' });
  const reopened = openDispute(service); // 拒绝后允许再次申诉
  assert.notEqual(reopened.id, first.id);
  service.resolve({ disputeId: reopened.id, actorUserId: 'user-a', decision: 'APPROVE' });
  assert.throws(
    () => openDispute(service),
    (error) => error instanceof DisputeError && error.code === DISPUTE_CODE.NOT_ELIGIBLE
  );
  assert.equal(refundEntries(ledger).length, 1, '整个申诉历史只有一笔退款');
});

test('不可变：证据是拷贝，修改原始 evidence 不影响争议记录', () => {
  const { service, ledger } = setup();
  seedCharge(ledger);
  const evidence = [{ kind: 'screenshot', ref: 's-1' }];
  const dispute = openDispute(service, { evidence });
  evidence[0].ref = 'changed';
  evidence.push({ kind: 'fake' });
  assert.deepEqual(dispute.evidence.map((item) => ({ ...item })), [{ kind: 'screenshot', ref: 's-1' }]);
  assert.equal(dispute.evidence.length, 1);
});

test('参数校验：构造参数 / open / resolve / timeline 非法输入', () => {
  assert.throws(() => new DisputeService({}), TypeError);
  assert.throws(() => new DisputeService({ ledger: {} }), TypeError);
  assert.throws(() => new DisputeService({ ledger: new Ledger(), messageState: 'x' }), TypeError);
  assert.throws(() => new DisputeService({ ledger: new Ledger(), onAudit: 'x' }), TypeError);
  assert.throws(() => new DisputeService({ ledger: new Ledger(), clock: 'now' }), TypeError);

  const { service, ledger } = setup();
  seedCharge(ledger);
  assert.throws(() => service.open({ userId: 'user-a', reason: 'r' }), TypeError);
  assert.throws(() => service.open({ messageId: 'msg-1', reason: 'r' }), TypeError);
  assert.throws(() => service.open({ messageId: 'msg-1', userId: 'user-a' }), TypeError);
  assert.throws(() => service.open({ messageId: 'msg-1', userId: 'user-a', reason: 'r', evidence: 'x' }), TypeError);
  assert.throws(() => service.timeline({}), TypeError);
  assert.throws(() => service.list({ state: 'NOPE' }), TypeError);
});
