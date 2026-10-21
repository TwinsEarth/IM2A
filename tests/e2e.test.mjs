/**
 * 模块职责：端到端集成测试 —— 验证「编排层 + 资金通道 + 消息通道」装配后业务主线成立
 * 契约来源：contracts/INTERFACES.md v1.0
 *
 * 覆盖的核心命题（每一条都是产品原则的可执行断言）：
 *   1. 陌生人触达在人工审批前不扣费、不进入会话；
 *   2. 人通过审批后：扣费 → 查看 → 回复 → 结算，账本恰好一次；
 *   3. 人驳回后：对方不被扣费（付费不能替代预审）；
 *   4. 审批单终态不可二次决策；
 *   5. 12 项治理自检在装配后依然全部成立；
 *   6. HTTP 控制台协议面可用（health / state / decide / reset）。
 *
 * 运行：node tests/e2e.test.mjs
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  USERS,
  AGENTS,
  createWorld,
  seedScenario,
  decideTicket,
  snapshot,
  probeChecks
} from '../apps/demo-server/flow.mjs';
import { startServer } from '../apps/demo-server/server.mjs';

function seededWorld() {
  const world = createWorld();
  seedScenario(world);
  return world;
}

test('种子场景：协商收敛 → 预审通过 → 审批卡片待决策，且此时一分钱未扣', () => {
  const world = seededWorld();
  const state = snapshot(world);

  // 协商：4 轮内收敛并产出方案
  assert.equal(state.negotiations.length, 1);
  const session = state.negotiations[0];
  assert.equal(session.state, 'COMPLETED');
  assert.equal(session.round, 2);
  assert.ok(session.artifact, '协商必须产出方案 artifact');
  assert.equal(session.turns.length, 4);

  // 付费消息：已过预审，停在「待付费」
  const message = state.messages.find((m) => m.id === world.ids.messageId);
  assert.ok(message, '付费消息应存在');
  assert.equal(message.state, 'AWAITING_PAYMENT', '人工审批前必须停在待付费');
  assert.equal(message.billingMode, 2, '应为「看到且回复才扣费」');
  assert.equal(message.amountCents, 5000);

  // 审批卡片：两张待决策（陌生人消息 + 采购）
  assert.equal(state.pendingCount, 2);
  const strangerTicket = state.approvals.find((t) => t.id === world.ids.ticketStranger);
  assert.equal(strangerTicket.state, 'PENDING');
  assert.equal(strangerTicket.priority, 'high');
  assert.equal(strangerTicket.payload.kind, 'STRANGER_INTENT');

  // 账本：没有任何扣费
  assert.equal(state.ledger.entries.length, 0, '审批前不得产生任何账本条目');

  // 出口护栏：拦下两条垃圾/越界来意
  assert.equal(state.blocked.length, 2, '两条样本来意应被拦截');
  assert.ok(state.blocked.every((b) => b.verdict === 'BLOCK' || b.verdict === 'REVIEW'));
});

test('人通过审批：扣费 → 查看 → 回复 → 结算，且账本恰好一次', () => {
  const world = seededWorld();
  const ticketId = world.ids.ticketStranger;

  const outcome = decideTicket(world, ticketId, 'APPROVE');
  assert.equal(outcome.ticket.state, 'COMPLETED');

  const message = world.paid.get(world.ids.messageId);
  assert.equal(message.state, 'SETTLED', '通过后应走完到结算');

  const charges = world.ledger.entries({ userId: USERS.ZHOU }).filter((e) => e.type === 'CHARGE');
  assert.equal(charges.length, 1, '只能有一条扣费');
  assert.equal(charges[0].amountCents, 5000);
  assert.equal(charges[0].idempotencyKey, `pay:${world.ids.messageId}`);

  const settles = world.ledger.entries({}).filter((e) => e.type === 'SETTLE');
  assert.equal(settles.length, 1, '应有一条结算条目');
  assert.equal(settles[0].userId, USERS.LIN, '结算应记在接收方');

  // 信任是挣来的：付费并回复后升级为「已知」档
  const level = world.trustMatrix.levelOf({ userId: USERS.LIN, targetId: USERS.ZHOU });
  assert.equal(level, 3, '关系应升级为 KNOWN(3)');
});

test('人驳回审批：对方不被扣费，消息落 REFUSED', () => {
  const world = seededWorld();
  const outcome = decideTicket(world, world.ids.ticketStranger, 'REJECT');

  assert.equal(outcome.ticket.state, 'REJECTED');
  assert.equal(world.paid.get(world.ids.messageId).state, 'REFUSED');
  assert.equal(world.ledger.entries({}).length, 0, '驳回后账本必须为空（付费不能替代预审）');
  assert.equal(
    world.trustMatrix.levelOf({ userId: USERS.LIN, targetId: USERS.ZHOU }),
    4,
    '被驳回不应升级关系'
  );
});

test('审批单终态不可二次决策（幂等保护）', () => {
  const world = seededWorld();
  decideTicket(world, world.ids.ticketStranger, 'APPROVE');
  assert.throws(() => decideTicket(world, world.ids.ticketStranger, 'APPROVE'), { code: 'ILLEGAL_TRANSITION' });

  // 二次决策不得产生第二条扣费
  const charges = world.ledger.entries({}).filter((e) => e.type === 'CHARGE');
  assert.equal(charges.length, 1);
});

test('采购审批：通过后受子 Agent 当日额度约束', () => {
  const world = seededWorld();
  const before = world.budget.remaining(AGENTS.LIN_PROCUREMENT);
  const outcome = decideTicket(world, world.ids.ticketProcurement, 'APPROVE');

  assert.equal(outcome.ticket.state, 'COMPLETED');
  assert.equal(outcome.result.spent, true);
  assert.equal(world.budget.remaining(AGENTS.LIN_PROCUREMENT), before - 239900);
});

test('修改后执行：MODIFY 的 patch 真正改变执行金额（不是只记录 modifiedAt）', () => {
  const world = seededWorld();
  const before = world.budget.remaining(AGENTS.LIN_PROCUREMENT);
  const outcome = decideTicket(world, world.ids.ticketProcurement, 'MODIFY', '换 B 家，便宜 200', {
    amountCents: 219900
  });

  assert.equal(outcome.ticket.state, 'COMPLETED');
  assert.equal(outcome.ticket.patch.amountCents, 219900, 'patch 必须持久化在审批单上');
  assert.equal(outcome.result.amountCents, 219900, '执行必须用修改后的金额');
  assert.equal(world.budget.remaining(AGENTS.LIN_PROCUREMENT), before - 219900);

  // 修改口径同样适用于陌生人消息的回复：人改过的回复优先于 Agent 预拟稿
  const world2 = seededWorld();
  const stranger = decideTicket(world2, world2.ids.ticketStranger, 'MODIFY', '改个说法', {
    replyText: '林总已阅：本周五 16:00 视频 20 分钟，请提前发 BP。'
  });
  assert.equal(stranger.ticket.state, 'COMPLETED');
  assert.equal(stranger.message.state, 'SETTLED');
  assert.ok(
    world2.trace.some((t) => t.act === 'DECIDE_MODIFY' && String(t.detail).includes('回复口径')),
    '人的修改必须留下可追溯轨迹'
  );
});

test('12 项治理自检在装配后全部成立', () => {
  const checks = probeChecks();
  assert.equal(checks.length, 12, '自检项应为 12 项');
  const failed = checks.filter((c) => !c.ok);
  assert.deepEqual(
    failed.map((c) => `${c.id}: 期望 ${c.expect} 实际 ${c.actual}`),
    [],
    '不允许有失败项'
  );
});

test('HTTP 协议面：health / state / decide / reset', async () => {
  const app = await startServer({ port: 0 });
  try {
    const health = await fetch(`${app.url}/api/health`).then((r) => r.json());
    assert.equal(health.ok, true);
    assert.equal(health.service, 'agent-im-demo');
    assert.equal(health.pendingApprovals, 2);

    const state = await fetch(`${app.url}/api/state`).then((r) => r.json());
    assert.equal(state.pendingCount, 2);
    assert.equal(state.checks.filter((c) => c.ok).length, 12);

    const strangerTicket = state.approvals.find((t) => t.payload?.kind === 'STRANGER_INTENT');
    const decided = await fetch(`${app.url}/api/approvals/${encodeURIComponent(strangerTicket.id)}/decide`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ action: 'APPROVE', comment: '同意，周五 16:00' })
    }).then((r) => r.json());

    assert.equal(decided.ok, true);
    assert.equal(decided.state, 'COMPLETED');
    assert.equal(decided.messageState, 'SETTLED');
    assert.equal(decided.snapshot.pendingCount, 1, '决策后待审批应只剩采购一件');

    // 非法动作
    const bad = await fetch(`${app.url}/api/approvals/x/decide`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ action: 'NOPE' })
    });
    assert.equal(bad.status, 400);

    // 重置回种子场景
    const reset = await fetch(`${app.url}/api/reset`, { method: 'POST' }).then((r) => r.json());
    assert.equal(reset.state.pendingCount, 2);
    assert.equal(reset.state.ledger.entries.length, 0);

    // ── 陌生人付费消息低层协议面：submit → screen → pay → view → reply ──
    const post = (p, body) =>
      fetch(`${app.url}${p}`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(body ?? {})
      });

    const created = await post('/api/intent', {
      topic: 'HTTP 链路验证',
      summary: '通过 REST 提交的来意',
      billingMode: 2,
      amountCents: 2000,
      idempotencyKey: 'http-intent-001'
    }).then((r) => r.json());
    assert.equal(created.ok, true);
    assert.equal(created.message.state, 'INTENT', '提交后必须先落 INTENT，未预审不得付费');

    const screened = await post(`/api/intent/${created.message.id}/screen`, { verdict: 'PASS' }).then((r) => r.json());
    assert.equal(screened.message.state, 'AWAITING_PAYMENT');

    const paidRes = await post(`/api/intent/${created.message.id}/pay`, { payerUserId: USERS.ZHOU }).then((r) => r.json());
    assert.equal(paidRes.charged, true);
    assert.equal(paidRes.message.state, 'PAID');

    const paidAgain = await post(`/api/intent/${created.message.id}/pay`, { payerUserId: USERS.ZHOU }).then((r) => r.json());
    assert.equal(paidAgain.charged, false, '重复支付不得二次扣费');

    const viewed = await post(`/api/intent/${created.message.id}/view`, { viewerUserId: USERS.LIN }).then((r) => r.json());
    assert.equal(viewed.message.state, 'VIEWED');

    const replied = await post(`/api/intent/${created.message.id}/reply`, { replyText: '收到' }).then((r) => r.json());
    assert.equal(replied.message.state, 'REPLIED');

    // 未预审直接付费 → 400 NEEDS_SCREENING
    const raw = await post('/api/intent', {
      topic: '跳过预审',
      summary: '未预审',
      idempotencyKey: 'http-intent-002'
    }).then((r) => r.json());
    const rawPayRes = await post(`/api/intent/${raw.message.id}/pay`, { payerUserId: USERS.ZHOU });
    assert.equal(rawPayRes.status, 400);
    assert.equal((await rawPayRes.json()).error, 'NEEDS_SCREENING');
  } finally {
    await app.close();
  }
});
