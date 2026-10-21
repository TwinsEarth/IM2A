// 测试：付费消息审核链（契约 §40）。
// 覆盖：PASS/FLAG/BLOCK 分流、机审不可用 fail-closed、人审/申诉/复核状态机、申诉窗口、自审禁止、canDeliver 唯一判定、审计次数。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  MODERATION_CODE,
  MODERATION_STATE,
  ModerationError,
  ModerationService,
  VERDICT
} from '../src/moderation.mjs';

const SENDER = 'user-lin';
const MOD_A = 'mod-alice';
const MOD_B = 'mod-bob';

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.currentMs = () => currentMs;
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

const machineFor = (verdict) => () => ({
  verdict,
  ruleVersion: 'rule-v1',
  modelVersion: 'model-v1',
  hits: [{ rule: 'spam-pattern', score: 0.9 }]
});

const makeService = (options = {}) => {
  const clock = options.clock ?? makeClock();
  const audited = [];
  const service = new ModerationService({
    clock,
    machine: options.machine ?? machineFor(VERDICT.PASS),
    onAudit: (event) => audited.push(event),
    ...(options.appealWindowMs !== undefined ? { appealWindowMs: options.appealWindowMs } : {})
  });
  return { clock, service, audited };
};

const submit = (service, overrides = {}) =>
  service.submit({
    messageId: 'msg-1',
    senderUserId: SENDER,
    content: '你好，付费消息内容',
    channel: 'PAID_MESSAGE',
    ...overrides
  });

test('MODERATION_STATE / VERDICT / MODERATION_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...MODERATION_STATE }, {
    RECEIVED: 'RECEIVED',
    MACHINE_PASSED: 'MACHINE_PASSED',
    HUMAN_PENDING: 'HUMAN_PENDING',
    APPROVED: 'APPROVED',
    REJECTED: 'REJECTED',
    APPEALED: 'APPEALED',
    APPEAL_GRANTED: 'APPEAL_GRANTED',
    APPEAL_UPHELD: 'APPEAL_UPHELD'
  });
  assert.deepEqual({ ...VERDICT }, { PASS: 'PASS', FLAG: 'FLAG', BLOCK: 'BLOCK' });
  assert.deepEqual({ ...MODERATION_CODE }, {
    NOT_FOUND: 'NOT_FOUND',
    ILLEGAL_STATE: 'ILLEGAL_STATE',
    ALREADY_DECIDED: 'ALREADY_DECIDED',
    FORBIDDEN: 'FORBIDDEN',
    SELF_REVIEW_FORBIDDEN: 'SELF_REVIEW_FORBIDDEN',
    MACHINE_UNAVAILABLE: 'MACHINE_UNAVAILABLE',
    APPEAL_WINDOW_CLOSED: 'APPEAL_WINDOW_CLOSED',
    CONTENT_REQUIRED: 'CONTENT_REQUIRED'
  });
  assert.ok(Object.isFrozen(MODERATION_STATE));
  assert.ok(Object.isFrozen(VERDICT));
  assert.ok(Object.isFrozen(MODERATION_CODE));
});

test('PASS → APPROVED：canDeliver ok，case 上记录 ruleVersion/modelVersion/hits，审计两步迁移', () => {
  const { service, audited } = makeService();
  const moderationCase = submit(service);
  assert.ok(Object.isFrozen(moderationCase));
  assert.match(moderationCase.caseId, /^mod-/);
  assert.equal(moderationCase.state, MODERATION_STATE.APPROVED);
  assert.equal(moderationCase.verdict, VERDICT.PASS);
  assert.equal(moderationCase.ruleVersion, 'rule-v1');
  assert.equal(moderationCase.modelVersion, 'model-v1');
  assert.deepEqual(moderationCase.hits.map((hit) => hit.rule), ['spam-pattern']);
  assert.equal(moderationCase.machineUnavailable, false);

  const deliverable = service.canDeliver({ caseId: moderationCase.caseId });
  assert.deepEqual({ ...deliverable }, { ok: true, code: null });
  assert.ok(Object.isFrozen(deliverable));

  const events = service.events(moderationCase.caseId);
  assert.equal(events.length, 2); // RECEIVED → MACHINE_PASSED → APPROVED
  assert.equal(events[0].from, MODERATION_STATE.RECEIVED);
  assert.equal(events[0].to, MODERATION_STATE.MACHINE_PASSED);
  assert.equal(events[1].from, MODERATION_STATE.MACHINE_PASSED);
  assert.equal(events[1].to, MODERATION_STATE.APPROVED);
  assert.equal(events[1].decision, 'AUTO_APPROVE');
  assert.deepEqual(audited, [...events]);
  for (const event of events) {
    assert.deepEqual(Object.keys(event).sort(), [
      'actor',
      'at',
      'caseId',
      'decision',
      'from',
      'messageId',
      'modelVersion',
      'ruleVersion',
      'to'
    ]);
    assert.equal(event.ruleVersion, 'rule-v1');
    assert.equal(event.modelVersion, 'model-v1');
    assert.equal(event.caseId, moderationCase.caseId);
  }
});

test('FLAG → HUMAN_PENDING：canDeliver 拒绝；人工 APPROVE 后才可投递', () => {
  const { service } = makeService({ machine: machineFor(VERDICT.FLAG) });
  const moderationCase = submit(service);
  assert.equal(moderationCase.state, MODERATION_STATE.HUMAN_PENDING);
  assert.equal(service.canDeliver({ caseId: moderationCase.caseId }).ok, false);

  const reviewed = service.review({ caseId: moderationCase.caseId, actorUserId: MOD_A, decision: 'APPROVE', note: '人工看过' });
  assert.equal(reviewed.state, MODERATION_STATE.APPROVED);
  assert.equal(reviewed.reviewedBy, MOD_A);
  assert.equal(reviewed.reviewNote, '人工看过');
  assert.ok(reviewed.reviewedAt !== null);
  assert.equal(service.canDeliver({ caseId: moderationCase.caseId }).ok, true);
});

test('BLOCK → REJECTED：canDeliver 拒绝；可申诉 → APPEALED → GRANT → APPEAL_GRANTED 且可投递', () => {
  const { service } = makeService({ machine: machineFor(VERDICT.BLOCK) });
  const moderated = submit(service);
  assert.equal(moderated.state, MODERATION_STATE.REJECTED);
  assert.equal(service.canDeliver({ caseId: moderated.caseId }).ok, false);

  const appealed = service.appeal({ caseId: moderated.caseId, userId: SENDER, reason: '误判' });
  assert.equal(appealed.state, MODERATION_STATE.APPEALED);
  assert.equal(appealed.appealReason, '误判');
  assert.equal(service.canDeliver({ caseId: moderated.caseId }).ok, false);

  const granted = service.resolveAppeal({ caseId: moderated.caseId, actorUserId: MOD_A, decision: 'GRANT' });
  assert.equal(granted.state, MODERATION_STATE.APPEAL_GRANTED);
  assert.equal(granted.resolvedBy, MOD_A);
  assert.equal(service.canDeliver({ caseId: moderated.caseId }).ok, true);
});

test('fail-closed：machine 抛错 → HUMAN_PENDING（绝不自动放行），machineUnavailable 记录在 case 上', () => {
  const { service } = makeService({
    machine: () => {
      throw new Error('machine down');
    }
  });
  const moderationCase = submit(service);
  assert.equal(moderationCase.state, MODERATION_STATE.HUMAN_PENDING);
  assert.notEqual(moderationCase.state, MODERATION_STATE.APPROVED);
  assert.equal(moderationCase.machineUnavailable, true);
  assert.equal(moderationCase.ruleVersion, null);
  assert.equal(service.canDeliver({ caseId: moderationCase.caseId }).ok, false);
  const event = service.events(moderationCase.caseId).at(-1);
  assert.equal(event.decision, 'MACHINE_UNAVAILABLE');
  // 人工仍可正常接管
  assert.equal(
    service.review({ caseId: moderationCase.caseId, actorUserId: MOD_A, decision: 'APPROVE' }).state,
    MODERATION_STATE.APPROVED
  );
});

test('fail-closed：machine=null / 返回非法 verdict / 返回 Promise 都转 HUMAN_PENDING', () => {
  for (const machine of [null, () => ({ verdict: 'MAYBE' }), () => Promise.resolve({ verdict: 'PASS' })]) {
    const clock = makeClock();
    const service = new ModerationService({ clock, machine });
    const moderationCase = submit(service);
    assert.equal(moderationCase.state, MODERATION_STATE.HUMAN_PENDING);
    assert.equal(moderationCase.machineUnavailable, true);
  }
});

test('CONTENT_REQUIRED：内容为空/空白/非字符串；缺 messageId/senderUserId => TypeError', () => {
  const { service } = makeService();
  for (const content of ['', '   ', null, undefined, 42]) {
    assert.throws(() => submit(service, { content }), { code: 'CONTENT_REQUIRED' });
  }
  assert.throws(() => submit(service, { messageId: '' }), TypeError);
  assert.throws(() => submit(service, { senderUserId: undefined }), TypeError);
  assert.equal(service.stats().total, 0);
});

test('review 状态机：仅 HUMAN_PENDING；终态再 review => ILLEGAL_STATE；未知 case => NOT_FOUND', () => {
  const { service } = makeService({ machine: machineFor(VERDICT.FLAG) });
  const moderationCase = submit(service);
  service.review({ caseId: moderationCase.caseId, actorUserId: MOD_A, decision: 'APPROVE' });
  assert.throws(
    () => service.review({ caseId: moderationCase.caseId, actorUserId: MOD_A, decision: 'REJECT' }),
    { code: 'ILLEGAL_STATE' }
  );
  assert.throws(() => service.review({ caseId: 'mod-missing', actorUserId: MOD_A, decision: 'APPROVE' }), {
    code: 'NOT_FOUND'
  });

  const pending = submit(service);
  assert.throws(
    () => service.review({ caseId: pending.caseId, actorUserId: MOD_A, decision: 'MAYBE' }),
    { code: 'ILLEGAL_STATE' }
  );
  // 人工拒绝同样开启申诉窗口
  const rejected = service.review({ caseId: pending.caseId, actorUserId: MOD_A, decision: 'REJECT', note: '不行' });
  assert.equal(rejected.state, MODERATION_STATE.REJECTED);
  assert.ok(rejected.appealDeadline !== null);
});

test('appeal 授权：仅 senderUserId（FORBIDDEN）；未知 case NOT_FOUND；非可申诉状态 ILLEGAL_STATE', () => {
  const { service } = makeService({ machine: machineFor(VERDICT.BLOCK) });
  const moderationCase = submit(service);
  assert.throws(() => service.appeal({ caseId: moderationCase.caseId, userId: 'user-other', reason: 'x' }), {
    code: 'FORBIDDEN'
  });
  assert.throws(() => service.appeal({ caseId: 'mod-missing', userId: SENDER, reason: 'x' }), {
    code: 'NOT_FOUND'
  });
  assert.throws(() => service.appeal({ caseId: moderationCase.caseId, userId: SENDER }), TypeError);

  // HUMAN_PENDING 不是可申诉状态
  const flagService = makeService({ machine: machineFor(VERDICT.FLAG) }).service;
  const pending = submit(flagService);
  assert.throws(
    () => flagService.appeal({ caseId: pending.caseId, userId: SENDER, reason: 'x' }),
    { code: 'ILLEGAL_STATE' }
  );
});

test('申诉窗口：窗口内可申诉；恰好在截止时刻可申诉，超过 => APPEAL_WINDOW_CLOSED', () => {
  const clock = makeClock();
  const { service } = makeService({ clock, machine: machineFor(VERDICT.BLOCK), appealWindowMs: 1000 });

  const inside = submit(service, { messageId: 'msg-in' });
  clock.advance(1000); // 恰好到期
  assert.doesNotThrow(() => service.appeal({ caseId: inside.caseId, userId: SENDER, reason: '卡点' }));

  const late = submit(service, { messageId: 'msg-late' });
  clock.advance(1001);
  assert.throws(() => service.appeal({ caseId: late.caseId, userId: SENDER, reason: '晚了' }), {
    code: 'APPEAL_WINDOW_CLOSED'
  });
});

test('APPEAL_UPHELD：canDeliver 拒绝；窗口重置后可再次申诉并最终 GRANT', () => {
  const clock = makeClock();
  const { service } = makeService({ clock, machine: machineFor(VERDICT.FLAG), appealWindowMs: 1000 });
  const moderated = submit(service);
  service.review({ caseId: moderated.caseId, actorUserId: MOD_A, decision: 'REJECT' });
  service.appeal({ caseId: moderated.caseId, userId: SENDER, reason: '第一次申诉' });

  const upheld = service.resolveAppeal({ caseId: moderated.caseId, actorUserId: MOD_B, decision: 'UPHOLD' });
  assert.equal(upheld.state, MODERATION_STATE.APPEAL_UPHELD);
  assert.equal(service.canDeliver({ caseId: moderated.caseId }).ok, false);
  assert.ok(upheld.appealDeadline !== null);

  // 维持原判后窗口重新开启：可再次申诉
  clock.advance(500);
  assert.equal(
    service.appeal({ caseId: moderated.caseId, userId: SENDER, reason: '再申诉' }).state,
    MODERATION_STATE.APPEALED
  );
  // 非发送者仍不能申诉
  assert.throws(() => service.appeal({ caseId: moderated.caseId, userId: MOD_B, reason: 'x' }), {
    code: 'FORBIDDEN'
  });
});

test('自审禁止：原审核人不能复核自己拒绝的申诉（SELF_REVIEW_FORBIDDEN），换人可复核', () => {
  const { service } = makeService({ machine: machineFor(VERDICT.FLAG) });
  const moderated = submit(service);
  service.review({ caseId: moderated.caseId, actorUserId: MOD_A, decision: 'REJECT' });
  service.appeal({ caseId: moderated.caseId, userId: SENDER, reason: '复核' });

  assert.throws(
    () => service.resolveAppeal({ caseId: moderated.caseId, actorUserId: MOD_A, decision: 'GRANT' }),
    { code: 'SELF_REVIEW_FORBIDDEN' }
  );
  assert.equal(service.get(moderated.caseId).state, MODERATION_STATE.APPEALED);

  const granted = service.resolveAppeal({ caseId: moderated.caseId, actorUserId: MOD_B, decision: 'GRANT' });
  assert.equal(granted.state, MODERATION_STATE.APPEAL_GRANTED);
  assert.equal(granted.resolvedBy, MOD_B);
});

test('resolveAppeal 状态机：非 APPEALED => ILLEGAL_STATE；已裁决 => ALREADY_DECIDED；非法动作 ILLEGAL_STATE', () => {
  const { service } = makeService({ machine: machineFor(VERDICT.PASS) });
  const approved = submit(service);
  assert.throws(
    () => service.resolveAppeal({ caseId: approved.caseId, actorUserId: MOD_A, decision: 'GRANT' }),
    { code: 'ILLEGAL_STATE' }
  );

  const blockedService = makeService({ machine: machineFor(VERDICT.BLOCK) }).service;
  const blocked = submit(blockedService, { messageId: 'msg-b' });
  assert.throws(
    () => blockedService.resolveAppeal({ caseId: blocked.caseId, actorUserId: MOD_A, decision: 'MAYBE' }),
    { code: 'ILLEGAL_STATE' }
  );
  blockedService.appeal({ caseId: blocked.caseId, userId: SENDER, reason: 'r' });
  blockedService.resolveAppeal({ caseId: blocked.caseId, actorUserId: MOD_A, decision: 'GRANT' });
  assert.throws(
    () => blockedService.resolveAppeal({ caseId: blocked.caseId, actorUserId: MOD_B, decision: 'GRANT' }),
    { code: 'ALREADY_DECIDED' }
  );
});

test('canDeliver：未知 case NOT_FOUND；HUMAN_PENDING/APPEALED 拒绝；REJECTED 带 FORBIDDEN', () => {
  assert.deepEqual({ ...makeService().service.canDeliver({ caseId: 'mod-missing' }) }, {
    ok: false,
    code: 'NOT_FOUND'
  });
  const service = makeService({ machine: machineFor(VERDICT.FLAG) }).service;
  const pending = submit(service);
  assert.deepEqual({ ...service.canDeliver({ caseId: pending.caseId }) }, { ok: false, code: 'ILLEGAL_STATE' });
  service.review({ caseId: pending.caseId, actorUserId: MOD_A, decision: 'REJECT' });
  assert.deepEqual({ ...service.canDeliver({ caseId: pending.caseId }) }, { ok: false, code: 'FORBIDDEN' });
});

test('审计：事件条数与迁移次数一致、链式 from/to、可按 caseId 过滤、onAudit 同源', () => {
  const { service, audited } = makeService({ machine: machineFor(VERDICT.BLOCK) });
  const moderated = submit(service);
  service.appeal({ caseId: moderated.caseId, userId: SENDER, reason: 'r' });
  service.resolveAppeal({ caseId: moderated.caseId, actorUserId: MOD_A, decision: 'GRANT' });

  const events = service.events(moderated.caseId);
  assert.deepEqual(
    events.map((event) => `${event.from}->${event.to}`),
    ['RECEIVED->REJECTED', 'REJECTED->APPEALED', 'APPEALED->APPEAL_GRANTED']
  );
  assert.equal(events.length, 3); // 正好 3 次状态迁移
  for (let index = 1; index < events.length; index += 1) {
    assert.equal(events[index].from, events[index - 1].to);
  }
  assert.deepEqual(audited, [...events]);
  assert.ok(Object.isFrozen(events));
  assert.throws(() => events.push({}), TypeError);
  assert.equal(service.events().length, 3);
  assert.deepEqual([...service.events('mod-unknown')], []);
});

test('list/stats/get：状态过滤、统计、未知 case NOT_FOUND、非法过滤 ILLEGAL_STATE', () => {
  const { service } = makeService({ machine: machineFor(VERDICT.FLAG) });
  const first = submit(service, { messageId: 'msg-a' });
  service.review({ caseId: first.caseId, actorUserId: MOD_A, decision: 'APPROVE' });
  const second = submit(service, { messageId: 'msg-b' });

  assert.throws(() => service.get('mod-missing'), { code: 'NOT_FOUND' });
  assert.equal(service.list().length, 2);
  assert.deepEqual(service.list({ state: MODERATION_STATE.HUMAN_PENDING }).map((item) => item.caseId), [
    second.caseId
  ]);
  assert.deepEqual([...service.list({ state: MODERATION_STATE.REJECTED })], []);
  assert.throws(() => service.list({ state: 'DONE' }), { code: 'ILLEGAL_STATE' });
  assert.ok(Object.isFrozen(service.list()));

  const stats = service.stats();
  assert.equal(stats.total, 2);
  assert.equal(stats.byState.APPROVED, 1);
  assert.equal(stats.byState.HUMAN_PENDING, 1);
  assert.equal(stats.humanPending, 1);
  assert.equal(stats.approved, 1);
  assert.equal(stats.machineUnavailable, 0);
  assert.ok(Object.isFrozen(stats));
  assert.ok(Object.isFrozen(stats.byState));
});

test('ModerationError 带契约 code 且是 Error 子类', () => {
  const { service } = makeService();
  try {
    service.get('mod-missing');
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof ModerationError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'NOT_FOUND');
  }
});
