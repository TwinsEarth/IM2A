// 测试：全链审计追溯（契约 §42）。
// 覆盖：四源合并与时间排序、同刻优先级、完整链 complete/missing、缺环显式、未知 messageId、按账号聚合、只读。
import test from 'node:test';
import assert from 'node:assert/strict';

import { AuditTrace } from '../src/trace.mjs';
import { AuditLog } from '../../audit/src/audit-log.mjs';
import { Ledger } from '../../payment/src/ledger.mjs';
import { DisputeService } from '../../payment/src/dispute.mjs';
import { ModerationService, VERDICT } from '../../moderation/src/moderation.mjs';

const ACCOUNT = 'user-lin';

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

function buildFixture({ audit = undefined } = {}) {
  const clock = makeClock();
  const ledger = new Ledger({ clock });
  const moderation = new ModerationService({
    clock,
    machine: () => ({ verdict: VERDICT.PASS, ruleVersion: 'rule-1', modelVersion: 'model-1', hits: [] })
  });
  const disputes = new DisputeService({ clock, ledger, messageState: () => 'REPLIED' });
  const auditSource = audit === undefined ? new AuditLog({ clock }) : audit;
  const trace = new AuditTrace({ ledger, moderation, disputes, audit: auditSource, clock });
  return { clock, ledger, moderation, disputes, audit: auditSource, trace };
}

/** 造完整链：审核(PASS) → CHARGE → VIEWED → REPLIED → DISPUTE_OPEN → REFUND + DISPUTE_APPROVED */
function buildFullChain(fixture, messageId = 'msg-full') {
  const moderationCase = fixture.moderation.submit({
    messageId,
    senderUserId: ACCOUNT,
    content: '付费消息内容'
  });
  fixture.clock.advance(1000);
  fixture.ledger.post({
    idempotencyKey: `pay:${messageId}`,
    type: 'CHARGE',
    userId: ACCOUNT,
    amountCents: 500,
    ref: messageId
  });
  fixture.clock.advance(1000);
  fixture.audit.append({
    source: 'payment',
    action: 'VIEWED',
    actor: ACCOUNT,
    target: messageId,
    decision: 'VIEWED',
    detail: { messageId }
  });
  fixture.clock.advance(1000);
  fixture.audit.append({
    source: 'payment',
    action: 'REPLIED',
    actor: ACCOUNT,
    target: messageId,
    decision: 'REPLIED',
    detail: { messageId }
  });
  fixture.clock.advance(1000);
  const dispute = fixture.disputes.open({ messageId, userId: ACCOUNT, reason: '内容不符' });
  fixture.clock.advance(1000);
  fixture.disputes.resolve({ disputeId: dispute.id, actorUserId: ACCOUNT, decision: 'APPROVE' });
  return { messageId, moderationCase, dispute };
}

test('完整链：审核→CHARGE→VIEWED→REPLIED→DISPUTE_OPEN→REFUND/DISPUTE_APPROVED，按 at 升序且 complete=true', () => {
  const fixture = buildFixture();
  const { messageId } = buildFullChain(fixture);

  const result = fixture.trace.trace({ messageId });
  assert.equal(result.messageId, messageId);
  assert.equal(result.complete, true);
  assert.deepEqual([...result.missing], []);
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.steps));
  assert.ok(result.steps.every((step) => Object.isFrozen(step)));

  assert.deepEqual(
    result.steps.map((step) => step.type),
    [
      'MODERATION_SUBMITTED',
      'MODERATION_PASS',
      'CHARGE',
      'VIEWED',
      'REPLIED',
      'DISPUTE_OPEN',
      'REFUND',
      'DISPUTE_APPROVED'
    ]
  );
  // 时间严格升序（同一时刻只出现在同源内部）
  const times = result.steps.map((step) => step.at);
  assert.deepEqual(times, [...times].sort());
  // 每条 step 必带 source/type/actor/ref
  for (const step of result.steps) {
    assert.ok(['moderation', 'ledger', 'audit', 'dispute'].includes(step.source));
    assert.equal(typeof step.type, 'string');
    assert.ok('actor' in step);
    assert.ok('ref' in step);
  }
  // 账本金额与退款金额保留
  const charge = result.steps.find((step) => step.type === 'CHARGE');
  assert.equal(charge.amountCents, 500);
  const refund = result.steps.find((step) => step.type === 'REFUND');
  assert.equal(refund.amountCents, 500);
  // 争议时间线 actor 可追溯
  assert.equal(result.steps.find((step) => step.type === 'DISPUTE_OPEN').actor, ACCOUNT);
  assert.equal(result.steps.find((step) => step.type === 'DISPUTE_APPROVED').actor, ACCOUNT);
});

test('同刻优先级：moderation < ledger < reply(audit) < dispute', () => {
  const fixture = buildFixture();
  const messageId = 'msg-same-instant';
  fixture.moderation.submit({ messageId, senderUserId: ACCOUNT, content: '同一时刻' });
  fixture.ledger.post({
    idempotencyKey: `pay:${messageId}`,
    type: 'CHARGE',
    userId: ACCOUNT,
    amountCents: 100,
    ref: messageId
  });
  fixture.audit.append({
    source: 'payment',
    action: 'REPLIED',
    actor: ACCOUNT,
    target: messageId,
    decision: 'REPLIED',
    detail: { messageId }
  });
  fixture.disputes.open({ messageId, userId: ACCOUNT, reason: '同刻争议' });

  const steps = fixture.trace.trace({ messageId }).steps;
  const moderationIndexes = steps.map((step, index) => (step.source === 'moderation' ? index : -1)).filter((index) => index >= 0);
  const ledgerIndex = steps.findIndex((step) => step.source === 'ledger');
  const auditIndex = steps.findIndex((step) => step.source === 'audit');
  const disputeIndex = steps.findIndex((step) => step.source === 'dispute');
  assert.ok(Math.max(...moderationIndexes) < ledgerIndex, 'moderation 必须先于 ledger');
  assert.ok(ledgerIndex < auditIndex, 'ledger 必须先于 reply/audit');
  assert.ok(auditIndex < disputeIndex, 'reply/audit 必须先于 dispute');
  // 所有 step 同一时刻
  assert.equal(new Set(steps.map((step) => step.at)).size, 1);
});

test('缺环显式：只有 CHARGE 没有审核记录 => complete=false 且 missing 明确包含审核', () => {
  const fixture = buildFixture();
  const messageId = 'msg-charge-only';
  fixture.ledger.post({
    idempotencyKey: `pay:${messageId}`,
    type: 'CHARGE',
    userId: ACCOUNT,
    amountCents: 300,
    ref: messageId
  });

  const result = fixture.trace.trace({ messageId });
  assert.equal(result.complete, false);
  assert.deepEqual(
    result.steps.map((step) => step.type),
    ['CHARGE']
  );
  assert.ok(result.missing.length >= 1);
  assert.ok(
    result.missing.some((item) => item.includes('MODERATION') || item.includes('审核')),
    `missing 应明确包含审核：${JSON.stringify(result.missing)}`
  );
  assert.match(result.missing.join(' '), /有扣费无审核/);
});

test('未知 messageId：返回空 steps + 明确的 missing，complete=false', () => {
  const fixture = buildFixture();
  const result = fixture.trace.trace({ messageId: 'msg-never-existed' });
  assert.deepEqual([...result.steps], []);
  assert.equal(result.complete, false);
  assert.equal(result.missing.length, 2);
  assert.ok(result.missing.some((item) => item.includes('MODERATION')));
  assert.ok(result.missing.some((item) => item.includes('CHARGE')));
});

test('追溯是只读的：不触发任何状态迁移（账本/审核/争议条数不变）', () => {
  const fixture = buildFixture();
  const { messageId } = buildFullChain(fixture);
  const before = {
    ledger: fixture.ledger.entries().length,
    moderation: fixture.moderation.list().length,
    disputes: fixture.disputes.list().length
  };
  fixture.trace.trace({ messageId });
  fixture.trace.byAccount({ accountId: ACCOUNT });
  assert.equal(fixture.ledger.entries().length, before.ledger);
  assert.equal(fixture.moderation.list().length, before.moderation);
  assert.equal(fixture.disputes.list().length, before.disputes);
});

test('byAccount：按最近活动倒序聚合该账号相关消息，limit 生效；未知账号空数组', () => {
  const fixture = buildFixture();
  const { messageId: fullMessage } = buildFullChain(fixture);
  const chargeOnly = 'msg-charge-only-2';
  fixture.clock.advance(10_000);
  fixture.ledger.post({
    idempotencyKey: `pay:${chargeOnly}`,
    type: 'CHARGE',
    userId: ACCOUNT,
    amountCents: 200,
    ref: chargeOnly
  });
  fixture.ledger.post({
    idempotencyKey: 'pay:msg-other-account',
    type: 'CHARGE',
    userId: 'user-other',
    amountCents: 999,
    ref: 'msg-other-account'
  });

  const traces = fixture.trace.byAccount({ accountId: ACCOUNT });
  assert.equal(traces.length, 2);
  assert.deepEqual(traces.map((trace) => trace.messageId), [chargeOnly, fullMessage]); // 最近的在前
  assert.ok(Object.isFrozen(traces));
  assert.equal(fixture.trace.byAccount({ accountId: ACCOUNT, limit: 1 }).length, 1);
  assert.deepEqual([...fixture.trace.byAccount({ accountId: 'user-none' })], []);
});

test('审计源兼容：可传 AuditLog.records 数组，detail 提到 messageId 的事件会并入 steps', () => {
  const clock = makeClock();
  const ledger = new Ledger({ clock });
  const moderation = new ModerationService({
    clock,
    machine: () => ({ verdict: VERDICT.PASS, ruleVersion: 'r', modelVersion: 'm', hits: [] })
  });
  const disputes = new DisputeService({ clock, ledger, messageState: () => 'REPLIED' });
  const messageId = 'msg-array-audit';
  moderation.submit({ messageId, senderUserId: ACCOUNT, content: 'x' });
  ledger.post({ idempotencyKey: `pay:${messageId}`, type: 'CHARGE', userId: ACCOUNT, amountCents: 10, ref: messageId });
  const auditRecords = [
    {
      at: '2026-10-21T00:00:05.000Z',
      source: 'payment',
      action: 'REPLIED',
      actor: ACCOUNT,
      target: messageId,
      decision: 'REPLIED',
      detail: { messageId }
    }
  ];
  const trace = new AuditTrace({ ledger, moderation, disputes, audit: auditRecords, clock });
  const result = trace.trace({ messageId });
  const auditStep = result.steps.find((step) => step.source === 'audit');
  assert.ok(auditStep);
  assert.equal(auditStep.type, 'REPLIED');
  assert.equal(result.steps.at(-1).type, 'REPLIED'); // 时间最晚
});

test('入参校验：缺依赖/空 messageId/非法 limit', () => {
  assert.throws(() => new AuditTrace({}), TypeError);
  const fixture = buildFixture();
  assert.throws(() => fixture.trace.trace({}), TypeError);
  assert.throws(() => fixture.trace.byAccount({ accountId: '' }), TypeError);
  assert.throws(() => fixture.trace.byAccount({ accountId: ACCOUNT, limit: 0 }), TypeError);
});
