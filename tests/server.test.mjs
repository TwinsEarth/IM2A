/**
 * 模块职责：IM2A 服务端集成测试 —— 会话/审批/陌生人锁定/Agent Runtime/重置 的端到端行为
 * 运行：node tests/server.test.mjs（或 node scripts/test.mjs）
 * 依赖：仅 Node 内置（fetch + node:sqlite 内存库）；不连网络、不依赖大模型
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { startServer } from '../apps/im2a/server.mjs';
import { USERS } from '../apps/im2a/flow.mjs';

const json = (url, init) =>
  fetch(url, { headers: { 'content-type': 'application/json' }, ...(init ?? {}) }).then(async (response) => ({
    status: response.status,
    body: await response.json().catch(() => ({}))
  }));

test('状态接口：三身份可见会话、2 张待审批、12 项治理自检全绿、Runtime Skill 目录存在', async () => {
  const app = await startServer({ port: 0, db: ':memory:' });
  try {
    const lin = await json(`${app.url}/api/state?user=${USERS.LIN}&thread=th-lin-agent`);
    assert.equal(lin.status, 200);
    assert.equal(lin.body.personas.length, 3);
    assert.ok(lin.body.threads.length >= 3);
    assert.equal(lin.body.tickets.length, 2);
    assert.equal(lin.body.checks.total, 12);
    assert.equal(lin.body.checks.passed, 12);
    assert.equal(lin.body.agents.length, 4);
    assert.equal(lin.body.runtime.catalog.length, 3, '本地 Skill 目录');
    assert.equal(lin.body.auth.enabled, false, '未设置 IM_AUTH 时为演示态');
    assert.equal(lin.body.auditChain.ok, true);
    assert.ok(lin.body.store && lin.body.store.dbPath === ':memory:');

    const zhou = await json(`${app.url}/api/state?user=${USERS.ZHOU}&thread=th-lin-zhou`);
    assert.equal(zhou.body.activeThread.canSend, false, '陌生人未审批前不可直达');
  } finally {
    await app.close();
  }
});

test('熟人聊天：人类消息入会话，Agent 自动起草回复（离线模板）', async () => {
  const app = await startServer({ port: 0, db: ':memory:' });
  try {
    const sent = await json(`${app.url}/api/message`, {
      method: 'POST',
      body: JSON.stringify({ userId: USERS.LIN, threadId: 'th-lin-mom', text: '周六我回来，汤给我留着。' })
    });
    assert.equal(sent.status, 200);
    const messages = sent.body.state.activeThread.messages;
    const human = messages.find((m) => m.text === '周六我回来，汤给我留着。');
    assert.ok(human);
    assert.equal(human.from, USERS.LIN);
    const last = messages.at(-1);
    assert.equal(last.kind, 'agent');
    assert.equal(sent.body.agentReply.ok, true);
    assert.equal(sent.body.agentReply.engine.source, 'template', '无 key 时离线模板');
  } finally {
    await app.close();
  }
});

test('陌生人会话锁定：直接发消息被拒绝，提示先走审批', async () => {
  const app = await startServer({ port: 0, db: ':memory:' });
  try {
    const locked = await json(`${app.url}/api/message`, {
      method: 'POST',
      body: JSON.stringify({ userId: USERS.ZHOU, threadId: 'th-lin-zhou', text: '绕过滤' })
    });
    assert.equal(locked.status, 400);
    assert.equal(locked.body.error, 'LOCKED');

    const empty = await json(`${app.url}/api/message`, {
      method: 'POST',
      body: JSON.stringify({ userId: USERS.LIN, threadId: 'th-lin-mom', text: '   ' })
    });
    assert.equal(empty.status, 400);
    assert.equal(empty.body.error, 'EMPTY_MESSAGE');
  } finally {
    await app.close();
  }
});

test('审批通过陌生人询盘：扣费→查看→回复→结算，关系升级 KNOWN，会话解锁', async () => {
  const app = await startServer({ port: 0, db: ':memory:' });
  try {
    const state = await json(`${app.url}/api/state?user=${USERS.LIN}&thread=th-lin-agent`);
    const ticket = state.body.tickets.find((t) => t.type === 'NEGOTIATION');
    assert.ok(ticket);

    const decided = await json(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST',
      body: JSON.stringify({ action: 'APPROVE', comment: '同意，周五 16:00' })
    });
    assert.equal(decided.status, 200);
    assert.equal(decided.body.state, 'COMPLETED');
    assert.equal(decided.body.result.charged, true);
    assert.equal(decided.body.stateSnapshot.tickets.length, 1);
    const pair = decided.body.stateSnapshot.trust.pairs.find((p) => p.from === USERS.LIN && p.to === USERS.ZHOU);
    assert.equal(pair.level, 3, '信任档位升为已知');
    const thread = decided.body.stateSnapshot.threads.find((t) => t.id === 'th-lin-zhou');
    assert.equal(thread.locked, false);

    const again = await json(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST',
      body: JSON.stringify({ action: 'APPROVE' })
    });
    assert.equal(again.status, 400, '终态审批单不可二次决策');
  } finally {
    await app.close();
  }
});

test('驳回：对方不被扣费、信任不升级、会话仍锁定', async () => {
  const app = await startServer({ port: 0, db: ':memory:' });
  try {
    const state = await json(`${app.url}/api/state?user=${USERS.LIN}&thread=th-lin-agent`);
    const ticket = state.body.tickets.find((t) => t.type === 'NEGOTIATION');
    const decided = await json(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST',
      body: JSON.stringify({ action: 'REJECT', comment: '暂不需要' })
    });
    assert.equal(decided.status, 200);
    const snapshot = decided.body.stateSnapshot;
    const pair = snapshot.trust.pairs.find((p) => p.from === USERS.LIN && p.to === USERS.ZHOU);
    assert.equal(pair.level, 4);
    assert.equal(snapshot.threads.find((t) => t.id === 'th-lin-zhou').locked, true);
  } finally {
    await app.close();
  }
});

test('重置：回到种子场景；未知路由 404；页面可访问', async () => {
  const app = await startServer({ port: 0, db: ':memory:' });
  try {
    const reset = await json(`${app.url}/api/reset`, { method: 'POST' });
    assert.equal(reset.status, 200);
    assert.equal(reset.body.state.tickets.length, 2);
    const missing = await json(`${app.url}/api/nope`);
    assert.equal(missing.status, 404);
    const page = await fetch(`${app.url}/`);
    assert.equal(page.status, 200);
    assert.match(await page.text(), /IM2A/);
  } finally {
    await app.close();
  }
});
