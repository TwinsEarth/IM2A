/**
 * 模块职责：im2agent 极轻量原型的端到端测试 —— 覆盖 7 个交付形态中可在本机验证的部分
 *   ① demo 单进程 ② 运维界面鉴权 ③ 生产形态拒绝裸奔 ④ 审批闭环与账本幂等
 *   ⑤ LLM 三档降级 ⑥ 局域网形态 ⑦ jsonfile 持久化（重启恢复）
 *
 * 运行：node tests/im2agent.test.mjs（或 node scripts/test.mjs --filter=im2agent）
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import { createApp, startServer, lanUrls, resolveRuntime } from '../apps/im2agent/server.mjs';
import { createLlm, LLM_MODE, templateReply } from '../apps/im2agent/llm.mjs';
import { createStore, STORE_KIND } from '../apps/im2agent/store.mjs';

const json = (url, init = {}) =>
  fetch(url, { headers: { 'content-type': 'application/json', ...(init.headers ?? {}) }, ...init }).then(async (response) => ({
    status: response.status,
    headers: response.headers,
    body: await response.json().catch(() => ({}))
  }));

async function withApp(env, fn) {
  const app = await startServer({ env: { PROFILE: 'demo', PORT: '0', ...env } });
  try {
    return await fn(app);
  } finally {
    await app.close();
  }
}

test('① demo 形态：单进程起闭环，状态页与运维页可访问，默认两张待审批', async () => {
  await withApp({}, async (app) => {
    const base = app.urls[0];
    assert.equal(app.runtime.profile, 'demo');
    const health = await json(`${base}/api/health`);
    assert.equal(health.status, 200);
    assert.equal(health.body.ok, true);
    assert.equal(health.body.service, 'im2agent');
    assert.equal(health.body.pending, 2, '种子场景两张待审批');
    assert.equal(health.body.auditOk, true);

    const page = await fetch(`${base}/`);
    assert.equal(page.status, 200);
    assert.match(await page.text(), /极轻量验证原型/);
    const admin = await fetch(`${base}/admin/`);
    assert.equal(admin.status, 200);
    assert.match(await admin.text(), /运维控制台/);

    const state = await json(`${base}/api/state`);
    assert.equal(state.body.agents.length, 4);
    assert.equal(state.body.pendingTickets, 2);
    assert.equal(state.body.auditChain.ok, true);
  });
});

test('② 运维界面鉴权：无令牌 401，带令牌 200；安全响应头存在', async () => {
  await withApp({ PROFILE: 'lan', ADMIN_TOKEN: 'test-token-123' }, async (app) => {
    const base = app.urls[0];
    const denied = await json(`${base}/api/admin/state`);
    assert.equal(denied.status, 401);
    assert.equal(denied.body.error, 'UNAUTHENTICATED');

    const allowed = await json(`${base}/api/admin/state`, { headers: { 'x-admin-token': 'test-token-123' } });
    assert.equal(allowed.status, 200);
    assert.equal(allowed.body.tickets.length, 2);
    assert.equal(allowed.headers.get('x-frame-options'), 'DENY');
    assert.match(String(allowed.headers.get('content-security-policy')), /default-src 'self'/);
  });
});

test('③ 生产形态：没有 ADMIN_TOKEN 拒绝启动（不裸奔）；有令牌则 HSTS', async () => {
  assert.throws(() => resolveRuntime({ PROFILE: 'prod' }), /ADMIN_TOKEN|prod/);
  assert.throws(() => resolveRuntime({ PROFILE: 'prod', ADMIN_TOKEN: 'short' }), /ADMIN_TOKEN/);

  await withApp({ PROFILE: 'prod', ADMIN_TOKEN: 'prod-token-abcdef' }, async (app) => {
    const response = await fetch(`${app.urls[0]}/api/health`);
    assert.equal(response.status, 200);
    assert.match(String(response.headers.get('strict-transport-security')), /max-age=31536000/);
  });
});

test('④ 审批闭环：通过 → 扣费幂等 → 结算 → 审计链；重复决策 409', async () => {
  await withApp({}, async (app) => {
    const base = app.urls[0];
    const first = await json(`${base}/api/approval/decide`, { method: 'POST', body: JSON.stringify({ ticketId: 'tkt-stranger-1', action: 'APPROVE' }) });
    assert.equal(first.status, 200);
    assert.equal(first.body.ticket.state, 'COMPLETED');
    assert.equal(first.body.message.state, 'SETTLED', '付费消息应走到结算');

    const ledger = await json(`${base}/api/ledger`);
    const charges = ledger.body.entries.filter((entry) => entry.type === 'CHARGE');
    const settles = ledger.body.entries.filter((entry) => entry.type === 'SETTLE');
    assert.equal(charges.length, 1, '只能有一条扣费');
    assert.equal(charges[0].idempotencyKey, 'pay:msg-stranger-1');
    assert.equal(charges[0].amountCents, 5000);
    assert.equal(settles.length, 1, '应有一条结算');

    const audit = await json(`${base}/api/audit`);
    assert.equal(audit.body.chain.ok, true);
    assert.ok(audit.body.entries.some((entry) => entry.action === 'PAID_MESSAGE_SETTLED'));

    const again = await json(`${base}/api/approval/decide`, { method: 'POST', body: JSON.stringify({ ticketId: 'tkt-stranger-1', action: 'APPROVE' }) });
    assert.equal(again.status, 409, '终态不可二次决策');
    assert.equal(again.body.error, 'ILLEGAL_TRANSITION');

    const reject = await json(`${base}/api/approval/decide`, { method: 'POST', body: JSON.stringify({ ticketId: 'tkt-procurement-1', action: 'REJECT' }) });
    assert.equal(reject.status, 200);
    assert.equal(reject.body.ticket.state, 'REJECTED');
  });
});

test('⑤ LLM 三档降级：无 key→template；调用失败→fallback(degraded)；成功→real', async () => {
  // 1) 无 key：确定走模板
  const templateLlm = createLlm({ config: { apiKey: null, baseUrl: 'http://x', model: 'm', timeoutMs: 100, maxTokens: 64, offline: false } });
  const t = await templateLlm.draft({ text: '帮我约个时间' });
  assert.equal(t.tier, LLM_MODE.TEMPLATE);
  assert.equal(templateLlm.stats().templates, 1);
  assert.equal(templateReply({ text: '帮我约个时间' }).length > 0, true);

  // 2) 有 key 但上游失败：降级到 fallback 且标记 degraded
  const failing = createLlm({
    config: { apiKey: 'k', baseUrl: 'http://x', model: 'm', timeoutMs: 100, maxTokens: 64, offline: false },
    fetchImpl: async () => ({ ok: false, status: 500, json: async () => ({}) })
  });
  const f = await failing.draft({ text: '审批一下' });
  assert.equal(f.tier, LLM_MODE.FALLBACK);
  assert.equal(f.degraded, true);
  assert.equal(failing.stats().failures, 1, '失败必须计数，不能假装成功');

  // 3) 上游正常：走 real
  const ok = createLlm({
    config: { apiKey: 'k', baseUrl: 'http://x', model: 'm', timeoutMs: 100, maxTokens: 64, offline: false },
    fetchImpl: async () => ({ ok: true, status: 200, json: async () => ({ choices: [{ message: { content: '已阅，周五 16:00 可以。' } }], usage: { total_tokens: 42 } }) })
  });
  const r = await ok.draft({ text: '审批一下' });
  assert.equal(r.tier, LLM_MODE.REAL);
  assert.match(r.reply, /周五/);
  assert.equal(ok.stats().tokens, 42);
});

test('⑥ 局域网形态：绑 0.0.0.0 且能列出局域网地址', async () => {
  await withApp({ PROFILE: 'lan', PORT: '0', STORE: 'memory' }, async (app) => {
    assert.equal(app.runtime.host, '0.0.0.0');
    assert.ok(Array.isArray(lanUrls(app.port)), 'lanUrls 必须返回数组（无网卡时为空数组也不报错）');
    const health = await json(`${app.urls[0]}/api/health`);
    assert.equal(health.status, 200);
    assert.equal(health.body.profile, 'lan');
  });
});

test('⑦ jsonfile 持久化：重启后审批状态与审计链恢复', async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'im2agent-store-'));
  const file = path.join(dir, 'state.json');
  try {
    const env = { PROFILE: 'lan', PORT: '0', ADMIN_TOKEN: 'tok-123456', STORE: 'jsonfile', STORE_FILE: file };
    const first = await startServer({ env });
    await json(`${first.urls[0]}/api/approval/decide`, {
      method: 'POST',
      headers: { 'x-admin-token': 'tok-123456' },
      body: JSON.stringify({ ticketId: 'tkt-procurement-1', action: 'APPROVE' })
    });
    await first.close();

    const second = await startServer({ env });
    try {
      const state = await json(`${second.urls[0]}/api/admin/state`, { headers: { 'x-admin-token': 'tok-123456' } });
      assert.equal(state.status, 200);
      const ticket = state.body.tickets.find((item) => item.id === 'tkt-procurement-1');
      assert.equal(ticket.state, 'COMPLETED', '重启后已完成的审批不应回到待审批');
      assert.equal(state.body.auditChain.ok, true, '审计链跨重启仍完整');
      assert.ok(state.body.auditChain.length >= 3);
      assert.equal(state.body.store.kind, 'jsonfile');
    } finally {
      await second.close();
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('⑨ Go 规范对齐：/healthz、snake_case 决策、Go 视图、SSE 同步、LLM 生成 Context', async () => {
  await withApp({}, async (app) => {
    const base = app.urls[0];

    // 1) /healthz 纯文本 OK（curl 直接可判读）
    const healthz = await fetch(`${base}/healthz`);
    assert.equal(healthz.status, 200);
    assert.equal((await healthz.text()).trim(), 'OK');
    const healthzJson = await fetch(`${base}/healthz`, { headers: { accept: 'application/json' } });
    assert.equal((await healthzJson.json()).status, 'OK');

    // 2) 规范写法：{ ticket_id, action: "approve" }
    const decided = await json(`${base}/api/approval/decide`, {
      method: 'POST',
      body: JSON.stringify({ ticket_id: 'tkt-procurement-1', action: 'approve' })
    });
    assert.equal(decided.status, 200);
    assert.equal(decided.body.ticket.state, 'COMPLETED');

    // 3) 运维状态同时给出 Go 视图（Ticket/AuditLog/Ledger 的 snake_case 形态）
    const admin = await json(`${base}/api/admin/state`);
    assert.equal(admin.status, 200);
    const view = admin.body.ticketsView.find((ticket) => ticket.id === 'tkt-procurement-1');
    assert.equal(view.status, 'COMPLETED');
    assert.equal(view.agent_id, 'agent-lin-procurement');
    assert.equal(view.price, 239900);
    assert.equal(typeof view.created_at, 'string');
    assert.ok(Array.isArray(admin.body.audits) && admin.body.audits.every((a) => 'target_id' in a && 'time' in a));
    assert.ok(Array.isArray(admin.body.ledgers));
    assert.ok(admin.body.ledgers.every((entry) => 'idempotency_key' in entry));

    // 4) LLM 生成审批上下文（Ticket.Context）：离线时走模板档，页面不报错
    const preview = await json(`${base}/api/approval/preview`, {
      method: 'POST',
      body: JSON.stringify({ ticket_id: 'tkt-stranger-1' })
    });
    assert.equal(preview.status, 200);
    assert.equal(preview.body.tier, 'template');
    assert.ok(String(preview.body.context).length > 10, 'Context 应有可读摘要');
  });
});

test('⑩ 多端同步：SSE 在决策后推送事件（客户端也可退化为 1s 轮询）', async () => {
  await withApp({}, async (app) => {
    const base = app.urls[0];
    const controller = new AbortController();
    const response = await fetch(`${base}/api/events`, { headers: { accept: 'text/event-stream' }, signal: controller.signal });
    assert.equal(response.status, 200);
    assert.match(String(response.headers.get('content-type')), /text\/event-stream/);

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    const readUntil = async (needle, timeoutMs = 3000) => {
      const deadline = Date.now() + timeoutMs;
      while (Date.now() < deadline) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        if (buffer.includes(needle)) return buffer;
      }
      throw new Error(`SSE 未在 ${timeoutMs}ms 内收到 ${needle}：${buffer.slice(0, 200)}`);
    };

    try {
      await readUntil('hello');
      // 另一个"端"做决策 → 本端应收到 decide 事件
      const decided = await json(`${base}/api/approval/decide`, {
        method: 'POST',
        body: JSON.stringify({ ticket_id: 'tkt-procurement-1', action: 'approve' })
      });
      assert.equal(decided.status, 200);
      const text = await readUntil('decide');
      assert.match(text, /"ticketId":"tkt-procurement-1"/);
      assert.match(text, /"state":"COMPLETED"/);
    } finally {
      controller.abort();
    }
  });
});

test('⑫ 两级权限与限流：viewer 只读、operator 可写、越限 429、健康检查豁免', async () => {
  // 只读/可写必须是两个凭证；配成同一个直接拒绝启动
  assert.throws(() => resolveRuntime({ PROFILE: 'lan', ADMIN_TOKEN: 'same-token', VIEWER_TOKEN: 'same-token' }), /VIEWER_TOKEN/);

  await withApp({ PROFILE: 'lan', ADMIN_TOKEN: 'op-token-123', VIEWER_TOKEN: 'view-token-123', RATE_LIMIT_PER_MIN: '0' }, async (app) => {
    const base = app.urls[0];
    const viewer = { 'x-admin-token': 'view-token-123' };
    const operator = { 'x-admin-token': 'op-token-123' };

    const read = await json(`${base}/api/admin/state`, { headers: viewer });
    assert.equal(read.status, 200, 'viewer 可以看板');
    assert.equal(read.body.role, 'viewer');

    const viewerWrite = await json(`${base}/api/approval/decide`, {
      method: 'POST',
      headers: viewer,
      body: JSON.stringify({ ticket_id: 'tkt-procurement-1', action: 'approve' })
    });
    assert.equal(viewerWrite.status, 403, 'viewer 不能审批');
    assert.equal(viewerWrite.body.error, 'FORBIDDEN_ROLE');

    const viewerPreview = await json(`${base}/api/approval/preview`, {
      method: 'POST',
      headers: viewer,
      body: JSON.stringify({ ticket_id: 'tkt-stranger-1' })
    });
    assert.equal(viewerPreview.status, 403, 'viewer 不能触发起草');

    const operatorWrite = await json(`${base}/api/approval/decide`, {
      method: 'POST',
      headers: operator,
      body: JSON.stringify({ ticket_id: 'tkt-procurement-1', action: 'approve' })
    });
    assert.equal(operatorWrite.status, 200, 'operator 可以审批');
  });

  await withApp({ PROFILE: 'demo', RATE_LIMIT_PER_MIN: '3' }, async (app) => {
    const base = app.urls[0];
    const codes = [];
    for (let i = 0; i < 4; i += 1) codes.push((await json(`${base}/api/state`)).status);
    assert.deepEqual(codes.slice(0, 3), [200, 200, 200], '前 3 次放行');
    assert.equal(codes[3], 429, '第 4 次应被限流');
    const limited = await json(`${base}/api/state`);
    assert.equal(limited.body.error, 'RATE_LIMITED');
    const health = await fetch(`${base}/api/health`);
    assert.equal(health.status, 200, '健康检查不受限流影响');
  });
});

test('⑪ 存储契约：postgres 不可用时显式报错（不静默降级为文件）', () => {
  const store = createStore({ kind: STORE_KIND.POSTGRES, dsn: 'postgres://x' });
  assert.throws(() => store.read(), { code: 'STORE_NOT_AVAILABLE' });
  assert.throws(() => store.write({}), { code: 'STORE_NOT_AVAILABLE' });
  assert.equal(store.info().available, false);
  assert.throws(() => createStore({ kind: 'nope' }), { code: 'STORE_INVALID' });
  const memory = createStore({ kind: STORE_KIND.MEMORY });
  memory.write({ a: 1 });
  assert.deepEqual(memory.read(), { a: 1 });
});
