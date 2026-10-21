// 测试：陌生 Agent 通信的人类审批挂起（契约 §32）。
// 覆盖：默认 SUSPENDED 且不可投递、只有属主能批、REFUSE 永不投递、过期不可补批、免批路径、事件回放与惰性过期。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  AgentCorrespondence,
  CORRESPONDENCE_CODE,
  CORRESPONDENCE_STATE,
  CorrespondenceError
} from '../src/agent-correspondence.mjs';

const FROM = 'agent-stranger-1';
const TO = 'agent-lin-butler';
const OWNER = 'user-lin';

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

const makeService = (options = {}) => {
  const clock = options.clock ?? makeClock();
  const events = [];
  const service = new AgentCorrespondence({
    clock,
    ...(options.ttlMs ? { ttlMs: options.ttlMs } : {}),
    onAudit: (event) => events.push(event)
  });
  return { clock, service, events };
};

const openInput = (overrides = {}) => ({
  fromAgentId: FROM,
  toAgentId: TO,
  principalUserId: OWNER,
  topic: '帮我约一下周末见面',
  payload: { slot: 'sat-18:00' },
  ...overrides
});

test('CORRESPONDENCE_STATE / CORRESPONDENCE_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...CORRESPONDENCE_STATE }, {
    OPEN: 'OPEN',
    SUSPENDED: 'SUSPENDED',
    APPROVED: 'APPROVED',
    REFUSED: 'REFUSED',
    EXPIRED: 'EXPIRED'
  });
  assert.deepEqual({ ...CORRESPONDENCE_CODE }, {
    NOT_FOUND: 'NOT_FOUND',
    ILLEGAL_STATE: 'ILLEGAL_STATE',
    APPROVAL_REQUIRED: 'APPROVAL_REQUIRED',
    ALREADY_DECIDED: 'ALREADY_DECIDED',
    EXPIRED: 'EXPIRED'
  });
  assert.ok(Object.isFrozen(CORRESPONDENCE_STATE));
  assert.ok(Object.isFrozen(CORRESPONDENCE_CODE));
});

test('陌生来信默认挂起：state=SUSPENDED 且 deliverable=false（挂起中的来意无法进入会话）', () => {
  const { clock, service } = makeService();
  const conversation = service.open(openInput());
  assert.ok(Object.isFrozen(conversation));
  assert.match(conversation.id, /^corr-/);
  assert.equal(conversation.state, CORRESPONDENCE_STATE.SUSPENDED);
  assert.equal(conversation.requiresHumanApproval, true);
  assert.equal(conversation.fromAgentId, FROM);
  assert.equal(conversation.toAgentId, TO);
  assert.equal(conversation.principalUserId, OWNER);
  assert.equal(conversation.createdAt, '2026-10-21T00:00:00.000Z');
  assert.equal(conversation.expiresAt, '2026-10-22T00:00:00.000Z'); // 默认 ttl 24h
  assert.equal(conversation.decidedAt, null);
  assert.ok(Object.isFrozen(conversation.payload));

  const deliverable = service.deliverable({ correspondenceId: conversation.id });
  assert.equal(deliverable.ok, false);
  assert.equal(deliverable.code, 'APPROVAL_REQUIRED');
  assert.ok(Object.isFrozen(deliverable));
  // 没有任何隐式放行：get 仍是 SUSPENDED
  assert.equal(service.get(conversation.id).state, CORRESPONDENCE_STATE.SUSPENDED);
  assert.equal(clock.iso(), '2026-10-21T00:00:00.000Z');
});

test('只有属主能批：非 principalUserId 调 decide 抛错，属主 APPROVE 后 deliverable=ok', () => {
  const { service } = makeService();
  const conversation = service.open(openInput());

  // 实现口径：非属主按 NOT_FOUND 处理（防枚举；契约允许 APPROVAL_REQUIRED 或 NOT_FOUND）
  assert.throws(
    () =>
      service.decide({
        correspondenceId: conversation.id,
        actorUserId: 'user-stranger',
        action: 'APPROVE'
      }),
    { code: 'NOT_FOUND' }
  );
  assert.equal(service.get(conversation.id).state, CORRESPONDENCE_STATE.SUSPENDED);

  const approved = service.decide({
    correspondenceId: conversation.id,
    actorUserId: OWNER,
    action: 'APPROVE',
    comment: '可以约'
  });
  assert.equal(approved.state, CORRESPONDENCE_STATE.APPROVED);
  assert.equal(approved.decidedBy, OWNER);
  assert.equal(approved.comment, '可以约');
  assert.ok(approved.decidedAt !== null);

  const deliverable = service.deliverable({ correspondenceId: conversation.id });
  assert.equal(deliverable.ok, true);
  assert.equal(deliverable.code, null);
});

test('REFUSE => REFUSED，deliverable 永远 false；终态再 decide => ALREADY_DECIDED', () => {
  const { service } = makeService();
  const conversation = service.open(openInput());
  const refused = service.decide({
    correspondenceId: conversation.id,
    actorUserId: OWNER,
    action: 'REFUSE',
    comment: '不约'
  });
  assert.equal(refused.state, CORRESPONDENCE_STATE.REFUSED);
  assert.equal(refused.decidedBy, OWNER);

  const deliverable = service.deliverable({ correspondenceId: conversation.id });
  assert.equal(deliverable.ok, false);
  assert.equal(deliverable.code, 'ALREADY_DECIDED');
  assert.throws(
    () => service.decide({ correspondenceId: conversation.id, actorUserId: OWNER, action: 'APPROVE' }),
    { code: 'ALREADY_DECIDED' }
  );
});

test('已 APPROVED 再 decide => ALREADY_DECIDED；非法 action => ILLEGAL_STATE', () => {
  const { service } = makeService();
  const approvedConversation = service.open(openInput());
  service.decide({ correspondenceId: approvedConversation.id, actorUserId: OWNER, action: 'APPROVE' });
  assert.throws(
    () => service.decide({ correspondenceId: approvedConversation.id, actorUserId: OWNER, action: 'REFUSE' }),
    { code: 'ALREADY_DECIDED' }
  );

  const pending = service.open(openInput({ topic: '另一个来意' }));
  assert.throws(
    () => service.decide({ correspondenceId: pending.id, actorUserId: OWNER, action: 'FORWARD' }),
    { code: 'ILLEGAL_STATE' }
  );
  assert.equal(service.get(pending.id).state, CORRESPONDENCE_STATE.SUSPENDED);
});

test('边界：注入 clock 推过 ttl => EXPIRED；过期后 decide 被拒（不许过期了还能批）', () => {
  const { clock, service } = makeService({ ttlMs: 1000 });
  const first = service.open(openInput());
  clock.advance(1001);
  const expired = service.expire({ correspondenceId: first.id });
  assert.equal(expired.state, CORRESPONDENCE_STATE.EXPIRED);
  assert.throws(
    () => service.decide({ correspondenceId: first.id, actorUserId: OWNER, action: 'APPROVE' }),
    { code: 'EXPIRED' }
  );
  assert.equal(service.deliverable({ correspondenceId: first.id }).code, 'EXPIRED');

  // 不显式调 expire 也不行：decide 惰性过期后拒绝
  const second = service.open(openInput({ topic: '第二条' }));
  clock.advance(1001);
  assert.throws(
    () => service.decide({ correspondenceId: second.id, actorUserId: OWNER, action: 'APPROVE' }),
    { code: 'EXPIRED' }
  );
  assert.equal(service.get(second.id).state, CORRESPONDENCE_STATE.EXPIRED);
});

test('边界：到期时刻即过期（expiresAt === now）；expire 支持显式 nowMs', () => {
  const { clock, service } = makeService({ ttlMs: 1000 });
  const conversation = service.open(openInput());
  clock.advance(1000); // 恰好到期
  assert.equal(service.deliverable({ correspondenceId: conversation.id }).code, 'EXPIRED');

  const other = service.open(openInput({ topic: '显式 nowMs' }));
  // 不推进 clock，直接给 nowMs（到期后的时间）
  const expired = service.expire({ correspondenceId: other.id, nowMs: clock.currentMs() + 1000 });
  assert.equal(expired.state, CORRESPONDENCE_STATE.EXPIRED);
});

test('免批路径：requiresHumanApproval=false => 直接 APPROVED 且可投递（熟人低风险场景）', () => {
  const { service } = makeService();
  const conversation = service.open(openInput({ requiresHumanApproval: false }));
  assert.equal(conversation.state, CORRESPONDENCE_STATE.APPROVED);
  assert.equal(conversation.decidedBy, 'system:auto-approved');
  assert.equal(service.deliverable({ correspondenceId: conversation.id }).ok, true);

  const events = service.events(conversation.id);
  assert.equal(events.length, 1);
  assert.equal(events[0].decision, 'APPROVED');
  assert.equal(events[0].detail.requiresHumanApproval, false);
});

test('事件可回放：每次迁移都有事件，顺序 SUSPENDED -> APPROVED 且带 actorUserId', () => {
  const { service, events } = makeService();
  const conversation = service.open(openInput());
  service.decide({ correspondenceId: conversation.id, actorUserId: OWNER, action: 'APPROVE', comment: 'ok' });

  const trail = service.events(conversation.id);
  assert.equal(trail.length, 2);
  assert.deepEqual(trail.map((event) => event.decision), ['SUSPENDED', 'APPROVED']);
  assert.deepEqual(trail.map((event) => event.action), ['OPEN', 'APPROVE']);
  assert.equal(trail[0].actor, FROM);
  assert.equal(trail[1].actor, OWNER); // 人的裁决可追溯
  assert.equal(trail[1].detail.comment, 'ok');
  assert.ok(Object.isFrozen(trail));
  assert.ok(Object.isFrozen(trail[1]));
  assert.throws(() => trail.push({}), TypeError);

  // onAudit 与 events 同源，便于接 SignedAuditLog
  assert.equal(events.length, 2);
  assert.equal(events[1], trail[1]);
  assert.deepEqual(service.events().length, 2);
  assert.deepEqual([...service.events('corr-unknown')], []);
});

test('get/list：未知 id => NOT_FOUND；list 支持状态过滤、冻结、并按创建时间排序', () => {
  const { clock, service } = makeService();
  assert.throws(() => service.get('corr-unknown'), { code: 'NOT_FOUND' });
  assert.throws(() => service.expire({ correspondenceId: 'corr-unknown' }), { code: 'NOT_FOUND' });

  const first = service.open(openInput({ topic: 't1' }));
  clock.advance(1000); // 让两条来信 createdAt 不同，排序断言才确定
  const second = service.open(openInput({ topic: 't2' }));
  service.decide({ correspondenceId: first.id, actorUserId: OWNER, action: 'APPROVE' });

  const all = service.list();
  assert.deepEqual(all.map((conversation) => conversation.id), [first.id, second.id]);
  assert.ok(Object.isFrozen(all));
  assert.deepEqual(service.list({ state: 'SUSPENDED' }).map((conversation) => conversation.id), [second.id]);
  assert.deepEqual(service.list({ state: 'APPROVED' }).map((conversation) => conversation.id), [first.id]);
  assert.deepEqual([...service.list({ state: 'REFUSED' })], []);
});

test('list 会惰性过期：SUSPENDED 超时后在列表里变成 EXPIRED，并产生 EXPIRE 事件', () => {
  const { clock, service } = makeService({ ttlMs: 1000 });
  const conversation = service.open(openInput());
  clock.advance(1001);
  const listed = service.list();
  assert.equal(listed[0].state, CORRESPONDENCE_STATE.EXPIRED);
  const trail = service.events(conversation.id);
  assert.equal(trail.at(-1).action, 'EXPIRE');
  assert.equal(trail.at(-1).decision, 'EXPIRED');
});

test('expire 对已裁决单幂等：不改状态、不新增事件', () => {
  const { clock, service } = makeService();
  const conversation = service.open(openInput());
  service.decide({ correspondenceId: conversation.id, actorUserId: OWNER, action: 'APPROVE' });
  const before = service.events(conversation.id).length;
  const after = service.expire({ correspondenceId: conversation.id, nowMs: clock.currentMs() + 10 * 864e5 });
  assert.equal(after.state, CORRESPONDENCE_STATE.APPROVED);
  assert.equal(service.events(conversation.id).length, before);
});

test('deliverable：未知 id => NOT_FOUND；REFUSED/EXPIRED 的 code 语义明确', () => {
  const { clock, service } = makeService({ ttlMs: 1000 });
  assert.deepEqual({ ...service.deliverable({ correspondenceId: 'corr-unknown' }) }, {
    ok: false,
    code: 'NOT_FOUND'
  });

  const refused = service.open(openInput({ topic: 'refuse case' }));
  service.decide({ correspondenceId: refused.id, actorUserId: OWNER, action: 'REFUSE' });
  assert.equal(service.deliverable({ correspondenceId: refused.id }).code, 'ALREADY_DECIDED');

  const expired = service.open(openInput({ topic: 'expire case' }));
  clock.advance(1001);
  assert.equal(service.deliverable({ correspondenceId: expired.id }).code, 'EXPIRED');
});

test('open 入参校验：缺必填字段 => TypeError；重复 id => ILLEGAL_STATE；非法 ttl => TypeError', () => {
  const { service } = makeService();
  assert.throws(() => service.open({}), TypeError);
  assert.throws(() => service.open(openInput({ fromAgentId: '' })), TypeError);
  assert.throws(() => service.open(openInput({ topic: '' })), TypeError);
  service.open(openInput({ correspondenceId: 'corr-fixed' }));
  assert.throws(() => service.open(openInput({ correspondenceId: 'corr-fixed' })), { code: 'ILLEGAL_STATE' });
  assert.throws(() => new AgentCorrespondence({ ttlMs: 0 }), TypeError);
});

test('CorrespondenceError 带 code 且是 Error 子类', () => {
  const { service } = makeService();
  try {
    service.get('corr-unknown');
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof CorrespondenceError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'NOT_FOUND');
  }
});
