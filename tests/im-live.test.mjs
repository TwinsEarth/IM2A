/**
 * 模块职责：人机混合 IM 演示服务的端到端测试 —— 覆盖「聊天面 + 审批面 + fleet 面」三条用户可见路径
 * 契约来源：apps/im-live/server.mjs 头注释；治理语义复用 flow.mjs（已有独立单测，这里只验证组合后行为）
 *
 * 运行：node tests/im-live.test.mjs（或 node scripts/test.mjs --filter=im-live）
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { startServer } from '../apps/im-live/server.mjs';
import { createLlm, resolveLlmConfig } from '../apps/im-live/llm.mjs';
import { USERS, AGENTS } from '../apps/demo-server/flow.mjs';

const json = (url, init) =>
  fetch(url, { headers: { 'content-type': 'application/json' }, ...(init ?? {}) }).then(async (response) => ({
    status: response.status,
    body: await response.json().catch(() => ({}))
  }));

test('状态接口：三种身份可见会话、2 张待审批、12 项治理自检全绿、fleet 默认模拟', async () => {
  const app = await startServer({ port: 0 });
  try {
    const lin = await json(`${app.url}/api/state?user=${USERS.LIN}&thread=th-lin-agent`);
    assert.equal(lin.status, 200);
    assert.equal(lin.body.personas.length, 3);
    assert.ok(lin.body.threads.length >= 3, '林总至少能看到家庭、陌生人、Agent 三个会话');
    assert.equal(lin.body.tickets.length, 2, '种子场景两张待审批');
    assert.equal(lin.body.checks.total, 12);
    assert.equal(lin.body.checks.passed, 12, '治理自检在演示服务中依然全部成立');
    assert.equal(lin.body.auditChain.ok, true, '审计哈希链完整');
    assert.ok(lin.body.auditChain.length >= 2, '种子场景已有审计记录');
    assert.equal(lin.body.agents.length, 4, '林总团队 4 个 Agent（总管 + 3 子 Agent）');

    const zhou = await json(`${app.url}/api/state?user=${USERS.ZHOU}&thread=th-lin-zhou`);
    assert.equal(zhou.body.activeThread.canSend, false, '陌生人未获审批前不能直接发消息');
    assert.equal(zhou.body.activeThread.kind, 'stranger');

    const fleet = await json(`${app.url}/api/fleet`);
    assert.equal(fleet.body.mode, 'simulated', '未配置 token 时 fleet 为模拟态，保证 demo 可离线运行');
    assert.ok(fleet.body.devices.length > 0);
  } finally {
    await app.close();
  }
});

test('熟人聊天可直接发送；空消息与非成员被拒绝', async () => {
  const app = await startServer({ port: 0 });
  try {
    const sent = await json(`${app.url}/api/message`, {
      method: 'POST',
      body: JSON.stringify({ userId: USERS.LIN, threadId: 'th-lin-mom', text: '周六我回来，汤给我留着。' })
    });
    assert.equal(sent.status, 200);
    assert.equal(sent.body.ok, true);
    const messages = sent.body.state.activeThread.messages;
    const human = messages.find((m) => m.text === '周六我回来，汤给我留着。');
    assert.ok(human, '人类消息已入会话');
    assert.equal(human.from, USERS.LIN);
    assert.equal(messages.at(-1).kind, 'agent', 'Agent 在该会话自动起草一条回复');
    assert.equal(sent.body.agentReply.ok, true);
    assert.equal(sent.body.agentReply.engine.source, 'template', '默认 offline 模式用确定性模板');
    assert.equal(sent.body.state.auditChain.ok, true, '发消息后链仍完整');
    assert.ok(
      sent.body.state.auditChain.recent.some((r) => r.action === 'CHAT_MESSAGE' && r.actor === USERS.LIN),
      '发消息写入审计链'
    );

    const empty = await json(`${app.url}/api/message`, {
      method: 'POST',
      body: JSON.stringify({ userId: USERS.LIN, threadId: 'th-lin-mom', text: '   ' })
    });
    assert.equal(empty.status, 400);
    assert.equal(empty.body.error, 'EMPTY_MESSAGE');

    const outsider = await json(`${app.url}/api/message`, {
      method: 'POST',
      body: JSON.stringify({ userId: USERS.ZHOU, threadId: 'th-lin-mom', text: '我不该在这儿' })
    });
    assert.equal(outsider.status, 400);
    assert.equal(outsider.body.error, 'NOT_A_MEMBER');

    const locked = await json(`${app.url}/api/message`, {
      method: 'POST',
      body: JSON.stringify({ userId: USERS.ZHOU, threadId: 'th-lin-zhou', text: '绕过滤' })
    });
    assert.equal(locked.status, 400);
    assert.equal(locked.body.error, 'LOCKED');
  } finally {
    await app.close();
  }
});

test('审批通过陌生人询盘：扣费 → 查看 → 回复 → 结算，关系升级为「已知」，会话解锁', async () => {
  const app = await startServer({ port: 0 });
  try {
    const state = await json(`${app.url}/api/state?user=${USERS.LIN}&thread=th-lin-agent`);
    const ticket = state.body.tickets.find((t) => t.type === 'NEGOTIATION');
    assert.ok(ticket, '种子场景里应存在陌生人协商审批单');

    const decided = await json(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST',
      body: JSON.stringify({ action: 'APPROVE', comment: '同意，周五 16:00' })
    });
    assert.equal(decided.status, 200);
    assert.equal(decided.body.ok, true);
    assert.equal(decided.body.state, 'COMPLETED');
    assert.equal(decided.body.result.charged, true, '审批通过后对方才被扣费');
    assert.equal(decided.body.stateSnapshot.auditChain.ok, true, '审批后链完整');
    assert.ok(
      decided.body.stateSnapshot.auditChain.recent.some((r) => r.action === 'DECIDE_APPROVE' && r.target === ticket.id),
      '审批决策写入审计链'
    );
    assert.equal(decided.body.stateSnapshot.tickets.length, 1, '审批后只剩采购一张待审批');

    const zhouThread = decided.body.stateSnapshot.threads.find((t) => t.id === 'th-lin-zhou');
    assert.equal(zhouThread.locked, false, '关系升级后陌生人会话解锁');
    const linTrust = decided.body.stateSnapshot.trust.pairs.find((p) => p.from === USERS.LIN && p.to === USERS.ZHOU);
    assert.equal(linTrust.level, 3, '关系档位从 STRANGER(4) 升级为 KNOWN(3)');

    const chat = await json(`${app.url}/api/state?user=${USERS.LIN}&thread=th-lin-zhou`);
    assert.ok(chat.body.activeThread.canSend, '林总现在可以在该会话里回复');
    assert.ok(
      chat.body.activeThread.messages.some((m) => m.kind === 'agent' && m.text.includes('审批通过')),
      '审批结果以 Agent 消息回到会话'
    );

    const again = await json(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST',
      body: JSON.stringify({ action: 'APPROVE' })
    });
    assert.equal(again.status, 400, '终态审批单不可二次决策');
  } finally {
    await app.close();
  }
});

test('采购审批：通过后执行在预算内、额度扣减；非法动作被拒绝', async () => {
  const app = await startServer({ port: 0 });
  try {
    const state = await json(`${app.url}/api/state?user=${USERS.LIN}&thread=th-lin-agent`);
    const ticket = state.body.tickets.find((t) => t.payload?.kind === 'PROCUREMENT_PURCHASE');
    assert.ok(ticket);

    const bad = await json(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST',
      body: JSON.stringify({ action: 'NOPE' })
    });
    assert.equal(bad.status, 400);

    const decided = await json(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST',
      body: JSON.stringify({ action: 'APPROVE', comment: '买' })
    });
    assert.equal(decided.status, 200);
    assert.equal(decided.body.ok, true);
    assert.equal(decided.body.state, 'COMPLETED');
    assert.equal(decided.body.result.amountCents, 239900);
    assert.equal(decided.body.result.spent, true);
    const agentThread = decided.body.stateSnapshot.threads.find((t) => t.id === 'th-lin-agent');
    assert.ok(agentThread.lastMessage.text.includes('审批通过'), '采购结果回到 Agent 会话');
  } finally {
    await app.close();
  }
});

test('驳回：对方不被扣费、无权进入会话，执行链终止', async () => {
  const app = await startServer({ port: 0 });
  try {
    const state = await json(`${app.url}/api/state?user=${USERS.LIN}&thread=th-lin-agent`);
    const ticket = state.body.tickets.find((t) => t.type === 'NEGOTIATION');
    const decided = await json(`${app.url}/api/approvals/${encodeURIComponent(ticket.id)}/decide`, {
      method: 'POST',
      body: JSON.stringify({ action: 'REJECT', comment: '暂不需要' })
    });
    assert.equal(decided.status, 200);
    assert.equal(decided.body.ok, true);
    const snapshot = decided.body.stateSnapshot;
    assert.equal(snapshot.tickets.length, 1);
    const pair = snapshot.trust.pairs.find((p) => p.from === USERS.LIN && p.to === USERS.ZHOU);
    assert.equal(pair.level, 4, '驳回不升级信任档位');
    const zhouThread = snapshot.threads.find((t) => t.id === 'th-lin-zhou');
    assert.equal(zhouThread.locked, true, '驳回后陌生人会话仍锁定');
  } finally {
    await app.close();
  }
});

test('Agent 接入真实模型协议：注入 LLM stub 后回复来自 llm，状态与 /api/llm 如实反映', async () => {
  const requests = [];
  const stubFetch = async (url, init) => {
    requests.push({ url, body: JSON.parse(init.body) });
    return {
      status: 200,
      async json() {
        return {
          model: 'deepseek-v4.1-flash',
          choices: [{ message: { content: '收到，已记入家庭日程（真实模型草稿）。' } }],
          usage: { prompt_tokens: 30, completion_tokens: 12, total_tokens: 42 }
        };
      }
    };
  };
  const llm = createLlm({ config: resolveLlmConfig({ DEEPSEEK_API_KEY: 'test-key' }), fetchImpl: stubFetch });
  const app = await startServer({ port: 0, llm });
  try {
    const sent = await json(`${app.url}/api/message`, {
      method: 'POST',
      body: JSON.stringify({ userId: USERS.LIN, threadId: 'th-lin-mom', text: '帮我把周六的饭局记一下' })
    });
    assert.equal(sent.status, 200);
    assert.equal(sent.body.agentReply.ok, true);
    assert.equal(sent.body.agentReply.engine.source, 'llm');
    assert.equal(sent.body.agentReply.engine.model, 'deepseek-v4.1-flash');
    const last = sent.body.state.activeThread.messages.at(-1);
    assert.equal(last.kind, 'agent');
    assert.match(last.text, /真实模型草稿/);
    assert.equal(last.engine.source, 'llm');
    assert.equal(requests.length, 1, '一次人类消息触发一次模型调用');
    assert.equal(requests[0].body.model, 'deepseek-v4.1-flash');
    assert.equal(requests[0].url.endsWith('/chat/completions'), true);
    assert.equal(sent.body.state.llm.mode, 'live');
    assert.equal(sent.body.state.llm.calls, 1);
    assert.equal(sent.body.state.llm.totalTokens, 42);

    const status = await json(`${app.url}/api/llm`);
    assert.equal(status.status, 200);
    assert.equal(status.body.mode, 'live');
    assert.equal(status.body.model, 'deepseek-v4.1-flash');
    assert.equal(status.body.calls, 1);
  } finally {
    await app.close();
  }
});

test('fleet 面：模拟态下 probe 明确拒绝；重置回到种子场景；未知路由 404', async () => {
  const app = await startServer({ port: 0 });
  try {
    const probe = await json(`${app.url}/api/fleet/probe`, {
      method: 'POST',
      body: JSON.stringify({ machineId: 'sim-macmini' })
    });
    assert.equal(probe.status, 200);
    assert.equal(probe.body.ok, false);
    assert.equal(probe.body.reason, 'FLEET_NOT_CONFIGURED', '未配置 token 时不假装执行成功');

    const noMachine = await json(`${app.url}/api/fleet/probe`, { method: 'POST', body: JSON.stringify({}) });
    assert.equal(noMachine.status, 400);

    const reset = await json(`${app.url}/api/reset`, { method: 'POST' });
    assert.equal(reset.status, 200);
    assert.equal(reset.body.state.tickets.length, 2);

    const missing = await json(`${app.url}/api/nope`);
    assert.equal(missing.status, 404);
  } finally {
    await app.close();
  }
});

test('身份兜底：未知用户回落到林总，页面与静态资源可访问', async () => {
  const app = await startServer({ port: 0 });
  try {
    const state = await json(`${app.url}/api/state?user=nobody`);
    assert.equal(state.status, 200);
    assert.equal(state.body.me.userId, USERS.LIN);
    const page = await fetch(`${app.url}/`);
    assert.equal(page.status, 200);
    const html = await page.text();
    assert.ok(html.includes('AgentIM'), '页面是 IM 演示控制台');
    assert.ok(html.includes('/api/state'), '前端通过状态接口取数');
  } finally {
    await app.close();
  }
});

export const __agents = AGENTS;
