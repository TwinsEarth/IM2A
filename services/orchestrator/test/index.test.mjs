// 测试：barrel 汇总导出（契约 §1–§7 的集成入口）。
// 集成方 demo-server/flow.mjs 逐字 import 下列命名导出，缺一个就会直接报错，因此这里逐一锁定。
import test from 'node:test';
import assert from 'node:assert/strict';

import * as api from '../src/index.mjs';
import { TrustMatrix as TrustMatrixSource } from '../src/trust-zone.mjs';
import { EgressGuard as EgressGuardSource } from '../src/egress-guard.mjs';
import { makeClock } from './_helpers.mjs';

const REQUIRED_EXPORTS = [
  'AGENT_KIND',
  'TRUST_LEVELS',
  'TrustMatrix',
  'AgentRegistry',
  'CapabilityGate',
  'DailyBudget',
  'ApprovalEngine',
  'TICKET_TYPE',
  'TICKET_STATE',
  'NegotiationEngine',
  'INTENT',
  'TASK_STATE',
  'MemoryStore',
  'MEMORY_LAYER',
  'EgressGuard'
];

test('index.mjs 导出集成方硬性要求的全部命名导出', () => {
  for (const name of REQUIRED_EXPORTS) {
    assert.ok(name in api, `缺少命名导出: ${name}`);
    assert.notEqual(api[name], undefined, `命名导出为 undefined: ${name}`);
  }
});

test('index.mjs 同时导出错误类/决策常量/默认规则等契约符号', () => {
  for (const name of [
    'TrustZoneError',
    'AgentRegistryError',
    'GateDeniedError',
    'ApprovalStateError',
    'NegotiationError',
    'MemoryIsolationError',
    'EgressBlockedError',
    'DECISION',
    'GATE_CODE',
    'TRUST_NAMES',
    'VERDICT',
    'DEFAULT_RULES'
  ]) {
    assert.ok(name in api, `缺少命名导出: ${name}`);
  }
});

test('barrel 导出与源模块是同一引用（无重复定义/无包装）', () => {
  assert.equal(api.TrustMatrix, TrustMatrixSource);
  assert.equal(api.EgressGuard, EgressGuardSource);
});

test('常量经 barrel 导出后数值与契约一致', () => {
  assert.deepEqual({ ...api.TRUST_LEVELS }, { SELF: 0, FAMILY: 1, TRUSTED: 2, KNOWN: 3, STRANGER: 4 });
  assert.deepEqual({ ...api.TASK_STATE }, {
    SUBMITTED: 'SUBMITTED',
    WORKING: 'WORKING',
    INPUT_REQUIRED: 'INPUT_REQUIRED',
    AUTH_REQUIRED: 'AUTH_REQUIRED',
    COMPLETED: 'COMPLETED',
    FAILED: 'FAILED',
    CANCELED: 'CANCELED',
    REJECTED: 'REJECTED'
  });
  assert.deepEqual({ ...api.TICKET_STATE }, {
    PENDING: 'PENDING',
    REVIEWING: 'REVIEWING',
    APPROVED: 'APPROVED',
    REJECTED: 'REJECTED',
    MODIFIED: 'MODIFIED',
    EXECUTING: 'EXECUTING',
    COMPLETED: 'COMPLETED',
    FAILED: 'FAILED',
    EXPIRED: 'EXPIRED'
  });
});

test('错误类均为 Error 子类且带 code', () => {
  const cases = [
    [new api.TrustZoneError('x', 'CODE_X'), 'TrustZoneError', 'CODE_X'],
    [new api.AgentRegistryError('x', 'CODE_X'), 'AgentRegistryError', 'CODE_X'],
    [new api.GateDeniedError('x', { code: 'CODE_X' }), 'GateDeniedError', 'CODE_X'],
    [new api.ApprovalStateError('x', 'CODE_X'), 'ApprovalStateError', 'CODE_X'],
    [new api.NegotiationError('x', 'CODE_X'), 'NegotiationError', 'CODE_X'],
    [new api.MemoryIsolationError('x', 'CODE_X'), 'MemoryIsolationError', 'CODE_X'],
    [new api.EgressBlockedError('x'), 'EgressBlockedError', 'EGRESS_BLOCKED']
  ];
  for (const [error, name, code] of cases) {
    assert.ok(error instanceof Error, `${name} 必须是 Error 子类`);
    assert.equal(error.name, name);
    assert.equal(error.code, code);
  }
});

test('端到端集成：门控→审批→执行→协商→记忆隔离→出口护栏，字段与集成方消费口径一致', () => {
  const clock = makeClock();

  // 1) 配置：注册表 + 信任矩阵 + 日预算 + 门控
  const registry = new api.AgentRegistry();
  registry.register({
    agentId: 'agent-butler',
    ownerUserId: 'user-a',
    kind: api.AGENT_KIND.BUTLER,
    allowedSkills: ['pay', 'chat'],
    trustZoneMin: 4,
    budgetCentsPerDay: 10000,
    approvalThresholdCents: 5000
  });
  registry.register({
    agentId: 'agent-sub',
    ownerUserId: 'user-a',
    kind: api.AGENT_KIND.SUBAGENT,
    parentId: 'agent-butler',
    allowedSkills: ['chat'],
    trustZoneMin: 2
  });
  const trustMatrix = new api.TrustMatrix({ clock });
  trustMatrix.grant({ userId: 'user-a', targetId: 'user-b', level: api.TRUST_LEVELS.TRUSTED });
  const budget = new api.DailyBudget({ clock });
  budget.setCap('agent-butler', 10000);
  const gate = new api.CapabilityGate({ registry, trustMatrix, budget, clock });

  const allow = gate.assertAllowed({
    agentId: 'agent-butler',
    userId: 'user-a',
    targetId: 'user-b',
    skillId: 'pay',
    amountCents: 100
  });
  assert.equal(allow.decision, 'ALLOW');

  const needApproval = gate.evaluate({
    agentId: 'agent-butler',
    userId: 'user-a',
    targetId: 'user-b',
    skillId: 'pay',
    amountCents: 5000
  });
  assert.equal(needApproval.decision, 'REQUIRE_APPROVAL');
  assert.equal(needApproval.code, 'AMOUNT_THRESHOLD');

  // 2) 审批：创建 -> 人工决策 -> 执行 -> 完成；字段对齐控制台
  const approvals = new api.ApprovalEngine({ clock });
  const ticket = approvals.create({
    userId: 'user-a',
    sourceAgentId: 'agent-butler',
    ticketType: api.TICKET_TYPE.PAYMENT,
    payload: { amountCents: 5000, targetUserId: 'user-b' },
    priority: 'high',
    deadlineMs: 10 * 60e3,
    traceId: 'trace-1'
  });
  for (const field of [
    'id',
    'userId',
    'sourceAgentId',
    'ticketType',
    'state',
    'payload',
    'priority',
    'deadline',
    'createdAt',
    'traceId',
    'patch'
  ]) {
    assert.ok(field in ticket, `审批单缺少字段 ${field}`);
  }
  assert.equal(approvals.listPending('user-a')[0].id, ticket.id);
  approvals.beginReview({ ticketId: ticket.id, actorUserId: 'user-a' });
  approvals.decide({ ticketId: ticket.id, actorUserId: 'user-a', action: 'APPROVE', comment: 'ok' });
  approvals.beginExecution({ ticketId: ticket.id });
  assert.equal(approvals.complete({ ticketId: ticket.id, result: { ok: true } }).state, 'COMPLETED');
  assert.equal(approvals.escalateOverdue().length, 0);
  assert.ok(approvals.events(ticket.id).some((event) => event.action === 'COMPLETE'));

  // 3) 协商：8 态机、artifact、turn 字段（控制台时间线逐条渲染）
  const negotiations = new api.NegotiationEngine({ clock });
  const session = negotiations.open({
    initiatorAgentId: 'agent-butler',
    initiatorUserId: 'user-a',
    responderAgentId: 'agent-sub',
    responderUserId: 'user-a',
    topic: '是否发送付费消息'
  });
  for (const field of [
    'id',
    'topic',
    'state',
    'round',
    'tokens',
    'initiatorAgentId',
    'responderAgentId',
    'initiatorUserId',
    'responderUserId',
    'turns',
    'terminalReason',
    'artifact'
  ]) {
    assert.ok(field in session, `协商 session 缺少字段 ${field}`);
  }
  negotiations.turn({ sessionId: session.id, fromAgentId: 'agent-butler', intent: api.INTENT.PROPOSE });
  negotiations.turn({
    sessionId: session.id,
    fromAgentId: 'agent-sub',
    intent: api.INTENT.COUNTER,
    tokens: 120
  });
  const accepted = negotiations.turn({
    sessionId: session.id,
    fromAgentId: 'agent-butler',
    intent: api.INTENT.ACCEPT,
    payload: { summary: '同意发送' }
  });
  assert.equal(accepted.session.state, 'COMPLETED');
  for (const field of ['at', 'fromAgentId', 'intent', 'payload', 'tokens']) {
    assert.ok(field in accepted.turn, `turn 缺少字段 ${field}`);
  }
  const artifact = negotiations.artifact(session.id);
  assert.deepEqual(Object.keys(artifact).sort(), ['at', 'payload', 'summary', 'type']);
  assert.equal(artifact.type, 'AGREEMENT');

  // 4) 记忆：butler 只能拿 digest，读子 Agent 原始记忆被隔离
  const memory = new api.MemoryStore({ clock });
  memory.write({
    agentId: 'agent-sub',
    userId: 'user-a',
    layer: api.MEMORY_LAYER.WORKING,
    content: '子 Agent 的原始私密记忆'
  });
  assert.throws(
    () =>
      memory.read({
        agentId: 'agent-sub',
        requesterAgentId: 'agent-butler',
        userId: 'user-a',
        layer: api.MEMORY_LAYER.WORKING
      }),
    { code: 'MEMORY_ISOLATED' }
  );
  const digest = memory.digest({ butlerAgentId: 'agent-butler', userId: 'user-a' });
  assert.equal(digest.contentExposed, false);
  assert.equal(digest.coordination.length, 1);
  assert.equal(JSON.stringify(digest).includes('原始私密记忆'), false);

  // 5) 出口护栏：PII 拦截，普通内容放行且审计绑定 messageId
  const egress = new api.EgressGuard({ clock });
  const blocked = egress.inspect({
    content: '身份证 110101199003071234',
    fromAgentId: 'agent-butler',
    toKind: 'agent',
    messageId: 'msg-1'
  });
  assert.equal(blocked.verdict, 'BLOCK');
  assert.ok(blocked.matched.every((item) => typeof item.id === 'string'));
  assert.equal(blocked.audit.target, 'msg-1');
  assert.throws(
    () => egress.assertPass({ content: '转账给我', fromAgentId: 'agent-butler' }),
    { code: 'EGRESS_BLOCKED' }
  );
  assert.equal(
    egress.inspect({ content: '你好', fromAgentId: 'agent-butler', toKind: 'user' }).verdict,
    'PASS'
  );
});
