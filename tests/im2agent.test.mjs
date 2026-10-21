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
  // 默认强制 memory 存储：否则 PROFILE=lan/prod 会落到共享的 .data/im2agent.json，
  // 造成"上一个用例/上一次运行的决策"污染下一个用例（曾因此出现 tkt 已 COMPLETED 的假失败）。
  const app = await startServer({ env: { PROFILE: 'demo', PORT: '0', STORE: 'memory', ...env } });
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

test('⑬ v0.0.1 产品版：未实名拦截 → 实名 → 充值 → 扣费 → 退款窗口', async () => {
  await withApp({ RATE_LIMIT_PER_MIN: '0' }, async (app) => {
    const base = app.urls[0];
    const post = (pathname, body, token = null) =>
      json(`${base}${pathname}`, {
        method: 'POST',
        headers: token === null ? {} : { authorization: `Bearer ${token}` },
        body: JSON.stringify(body)
      });

    // 1) 手机 OTP 登录（dev 模式返回固定码）
    const otp = await post('/api/account/otp', { phone: '13800000001' });
    assert.equal(otp.status, 200);
    assert.ok(otp.body.devCode, 'dev 模式应返回验证码（生产不下发）');
    const registered = await post('/api/account/register', { phone: '13800000001', code: otp.body.devCode, displayName: '内测用户' });
    assert.equal(registered.status, 200);
    const token = registered.body.session.accessToken;
    assert.ok(String(token).startsWith('sess-'));
    assert.equal(registered.body.account.state, 'UNVERIFIED');

    // 2) 未实名：不得收发付费消息
    const blocked = await post('/api/paid/send', { amountCents: 5000 }, token);
    assert.equal(blocked.status, 403);
    assert.equal(blocked.body.error, 'REALNAME_REQUIRED');

    // 3) 实名：只回 hash/掩码，明文不得回传
    const verified = await post('/api/account/verify', { realName: '张三', idNumber: '110101199001011234' }, token);
    assert.equal(verified.status, 200);
    assert.equal(verified.body.account.state, 'VERIFIED');
    assert.ok(!JSON.stringify(verified.body).includes('110101199001011234'), '身份证明文不得回传');

    // 4) 充值 100 元 → 扣 50 元
    const topup = await post('/api/wallet/topup', { amountCents: 10000, idempotencyKey: 'topup-1' }, token);
    assert.equal(topup.status, 200);
    assert.equal(topup.body.balance.availableCents, 10000);
    const sent = await post('/api/paid/send', { amountCents: 5000, messageId: 'msg-paid-test-1' }, token);
    assert.equal(sent.status, 200);
    assert.equal(sent.body.balance.availableCents, 5000);

    // 5) 未回复可退（白嫖防线：消息还是 PAID）
    const refunded = await post('/api/paid/refund', { messageId: 'msg-paid-test-1', reason: '不需要了' }, token);
    assert.equal(refunded.status, 200);
    assert.equal(refunded.body.balance.availableCents, 10000);

    // 6) 已回复不可退
    const second = await post('/api/paid/send', { amountCents: 5000, messageId: 'msg-paid-test-2' }, token);
    assert.equal(second.status, 200);
    assert.equal((await post('/api/paid/reply', { messageId: 'msg-paid-test-2' }, token)).status, 200);
    const late = await post('/api/paid/refund', { messageId: 'msg-paid-test-2', reason: '再退一次' }, token);
    assert.equal(late.status, 409);
    assert.equal(late.body.error, 'REFUND_WINDOW_CLOSED');

    // 7) 余额不足不得产生消息（余额 5000 分，尝试扣 6000 分）
    const poor = await post('/api/paid/send', { amountCents: 6000, messageId: 'msg-paid-test-3' }, token);
    assert.equal(poor.status, 402);
    assert.equal(poor.body.error, 'INSUFFICIENT_FUNDS');
  });
});

test('⑭ v0.0.2：短信通道冷却 + 已回复只能走人工争议（账本幂等退款）', async () => {
  await withApp({ RATE_LIMIT_PER_MIN: '0', IM2AGENT_SMS_KIND: 'dev' }, async (app) => {
    const base = app.urls[0];
    const post = (pathname, body, token = null) =>
      json(`${base}${pathname}`, {
        method: 'POST',
        headers: token === null ? {} : { authorization: `Bearer ${token}` },
        body: JSON.stringify(body)
      });

    // ① 短信通道：首条成功（dev 记录不外发），60s 冷却内重发 → 429 OTP_COOLDOWN
    const firstOtp = await post('/api/account/otp', { phone: '13800000002' });
    assert.equal(firstOtp.status, 200);
    assert.ok(firstOtp.body.devCode, 'dev 通道便于内测自测');
    const secondOtp = await post('/api/account/otp', { phone: '13800000002' });
    assert.equal(secondOtp.status, 429);
    assert.equal(secondOtp.body.error, 'OTP_COOLDOWN');
    const sms = await json(`${base}/api/sms`);
    assert.equal(sms.body.provider.name, 'sms-guard', '短信应经熔断守卫');
    assert.equal(sms.body.provider.primary.kind, 'dev', '主通道是 dev 提供方');
    assert.equal(sms.body.provider.breaker.state, 'CLOSED');
    assert.equal(sms.body.delivery.sent, 1, '冷却命中不应重发');
    assert.equal(sms.body.delivery.cooldownHits, 1);

    // ② 完成准入：注册 → 实名 → 充值
    const registered = await post('/api/account/register', { phone: '13800000002', code: firstOtp.body.devCode });
    assert.equal(registered.status, 200);
    const token = registered.body.session.accessToken;
    assert.equal((await post('/api/account/verify', { realName: '李四', idNumber: '110101199002022345' }, token)).status, 200);
    assert.equal((await post('/api/wallet/topup', { amountCents: 10000, idempotencyKey: 'topup-d' }, token)).status, 200);

    // ③ 发一条付费消息并让对方回复 → 自助退款窗口关闭
    assert.equal((await post('/api/paid/send', { amountCents: 5000, messageId: 'msg-d-1' }, token)).status, 200);
    assert.equal((await post('/api/paid/reply', { messageId: 'msg-d-1' }, token)).status, 200);

    // ④ 未回复消息不能开争议（该走 §37 自助退款）
    assert.equal((await post('/api/paid/send', { amountCents: 1000, messageId: 'msg-d-2' }, token)).status, 200);
    const ineligible = await post('/api/dispute/open', { messageId: 'msg-d-2', reason: '想退' }, token);
    assert.equal(ineligible.status, 409);
    assert.equal(ineligible.body.error, 'NOT_ELIGIBLE');

    // ⑤ 已回复 → 开争议 → 人工批准 → 账本恰一条幂等退款
    const opened = await post('/api/dispute/open', { messageId: 'msg-d-1', reason: '服务未按约定提供', evidence: ['chat:msg-d-1'] }, token);
    assert.equal(opened.status, 200);
    assert.equal(opened.body.dispute.state, 'OPEN');
    const approved = await post('/api/dispute/resolve', { disputeId: opened.body.dispute.id, decision: 'APPROVE', note: '同意退款' }, token);
    assert.equal(approved.status, 200);
    assert.equal(approved.body.dispute.state, 'APPROVED');
    const types = approved.body.timeline.map((entry) => entry.type);
    assert.ok(types.includes('CHARGE') && types.includes('REFUND'), `时间线应含 CHARGE→REFUND：${types.join('→')}`);
    assert.ok(types.indexOf('CHARGE') < types.lastIndexOf('REFUND'), '退款必须在扣费之后');

    const ledger = await json(`${base}/api/ledger`);
    const refunds = ledger.body.entries.filter((entry) => entry.type === 'REFUND');
    assert.equal(refunds.length, 1, '一条消息最多一笔退款');
    assert.match(String(refunds[0].idempotencyKey), /^refund-dispute:/);

    const replay = await post('/api/dispute/resolve', { disputeId: opened.body.dispute.id, decision: 'APPROVE' }, token);
    assert.equal(replay.status, 409, '终态不可二次裁决');
    const ledger2 = await json(`${base}/api/ledger`);
    assert.equal(ledger2.body.entries.filter((entry) => entry.type === 'REFUND').length, 1, '重放不得增加退款');
  });
});

test('⑮ v0.0.3 审核链：待审不扣费 / 拒绝可申诉改判 / 审核是唯一前置', async () => {
  await withApp({ RATE_LIMIT_PER_MIN: '0', IM2AGENT_SMS_KIND: 'dev' }, async (app) => {
    const base = app.urls[0];
    const post = (pathname, body, token = null, headers = {}) =>
      json(`${base}${pathname}`, {
        method: 'POST',
        headers: { ...(token === null ? {} : { authorization: `Bearer ${token}` }), ...headers },
        body: JSON.stringify(body)
      });
    const getJson = (pathname, headers = {}) => json(`${base}${pathname}`, { headers });

    // 准入：注册 → 实名 → 充值 100 元
    const otp = await post('/api/account/otp', { phone: '13800000003' });
    const registered = await post('/api/account/register', { phone: '13800000003', code: otp.body.devCode });
    const token = registered.body.session.accessToken;
    await post('/api/account/verify', { realName: '王五', idNumber: '110101199003033456' }, token);
    await post('/api/wallet/topup', { amountCents: 10000, idempotencyKey: 'topup-mod' }, token);

    // ① 机审 FLAG → 202 待审，且**不扣费**
    const flagged = await post('/api/paid/send', { amountCents: 5000, messageId: 'mod-1', topic: '稳赚项目', summary: '内部消息，加我微信' }, token);
    assert.equal(flagged.status, 202);
    assert.equal(flagged.body.error, 'HUMAN_PENDING');
    assert.ok(flagged.body.caseId);
    let wallet = await getJson('/api/wallet', { authorization: `Bearer ${token}` });
    assert.equal(wallet.body.balance.availableCents, 10000, '待审不得扣费');

    // ② 人审通过后重发同一 messageId → 放行并扣费
    const reviewed = await post('/api/moderation/review', { caseId: flagged.body.caseId, decision: 'APPROVE', note: '业务沟通，放行' });
    assert.equal(reviewed.status, 200);
    assert.equal(reviewed.body.case.state, 'APPROVED');
    const sent = await post('/api/paid/send', { amountCents: 5000, messageId: 'mod-1', topic: '稳赚项目', summary: '内部消息，加我微信' }, token);
    assert.equal(sent.status, 200, '审核通过后应放行');
    wallet = await getJson('/api/wallet', { authorization: `Bearer ${token}` });
    assert.equal(wallet.body.balance.availableCents, 5000, '放行时才扣费');

    // ③ 机审 BLOCK → 403 拒绝，且不扣费
    const blocked = await post('/api/paid/send', { amountCents: 1000, messageId: 'mod-2', topic: '诈骗汇款', summary: '汇款到指定账户' }, token);
    assert.equal(blocked.status, 403);
    assert.equal(blocked.body.error, 'REJECTED');
    assert.equal(blocked.body.verdict, 'BLOCK');
    wallet = await getJson('/api/wallet', { authorization: `Bearer ${token}` });
    assert.equal(wallet.body.balance.availableCents, 5000, '被拒不得扣费');

    // ④ 申诉 → 改判 GRANT → 再投递成功并扣费
    const appealed = await post('/api/moderation/appeal', { caseId: blocked.body.caseId, reason: '误判：正常业务沟通' }, token);
    assert.equal(appealed.status, 200);
    assert.equal(appealed.body.case.state, 'APPEALED');
    const granted = await post('/api/moderation/appeal/resolve', { caseId: blocked.body.caseId, decision: 'GRANT', note: '人工复核通过' });
    assert.equal(granted.status, 200);
    assert.equal(granted.body.case.state, 'APPEAL_GRANTED');
    const resent = await post('/api/paid/send', { amountCents: 1000, messageId: 'mod-2', topic: '诈骗汇款', summary: '汇款到指定账户' }, token);
    assert.equal(resent.status, 200, '改判后应可投递');

    // ⑤ 审计：审核事件带规则/模型版本；机审异常 fail-closed 由单测覆盖（此处验证统计口径）
    const moderation = await getJson('/api/moderation');
    assert.ok(moderation.body.stats.total >= 2, '统计应包含两条审核案');
    assert.equal(moderation.body.stats.byState.APPROVED, 1);
    assert.equal(moderation.body.stats.byState.APPEAL_GRANTED, 1);
    const audit = await getJson('/api/audit');
    assert.ok(audit.body.entries.some((entry) => entry.source === 'moderation'));
  });
});

test('⑯ v0.0.4 可观测：漏斗指标 / 告警 / 全链追溯', async () => {
  await withApp({ RATE_LIMIT_PER_MIN: '0', IM2AGENT_SMS_KIND: 'dev' }, async (app) => {
    const base = app.urls[0];
    const post = (pathname, body, token = null) =>
      json(`${base}${pathname}`, {
        method: 'POST',
        headers: { ...(token === null ? {} : { authorization: `Bearer ${token}` }) },
        body: JSON.stringify(body)
      });
    const getJson = (pathname) => json(`${base}${pathname}`);

    const otp = await post('/api/account/otp', { phone: '13800000005' });
    const registered = await post('/api/account/register', { phone: '13800000005', code: otp.body.devCode });
    const token = registered.body.session.accessToken;
    await post('/api/account/verify', { realName: '赵六', idNumber: '110101199004044567' }, token);
    await post('/api/wallet/topup', { amountCents: 10000, idempotencyKey: 'topup-obs' }, token);

    // 走一条完整链：FLAG → 人审通过 → 扣费 → 回复 → 争议 → 退款
    const flagged = await post('/api/paid/send', { amountCents: 5000, messageId: 'obs-1', topic: '稳赚项目', summary: '内部消息' }, token);
    assert.equal(flagged.status, 202);
    await post('/api/moderation/review', { caseId: flagged.body.caseId, decision: 'APPROVE' });
    assert.equal((await post('/api/paid/send', { amountCents: 5000, messageId: 'obs-1', topic: '稳赚项目', summary: '内部消息' }, token)).status, 200);
    assert.equal((await post('/api/paid/reply', { messageId: 'obs-1' }, token)).status, 200);
    const opened = await post('/api/dispute/open', { messageId: 'obs-1', reason: '服务未提供' }, token);
    assert.equal(opened.status, 200);
    const resolved = await post('/api/dispute/resolve', { disputeId: opened.body.dispute.id, decision: 'APPROVE' }, token);
    assert.equal(resolved.status, 200);

    // ① 指标：计数与漏斗（转化率必须是有限数，不是 NaN）
    const metrics = await getJson('/api/ops/metrics');
    assert.equal(metrics.status, 200);
    const counters = metrics.body.counters;
    assert.ok(counters.PAID_SENT >= 2, '两次发送尝试都计入');
    assert.ok(counters.MODERATION_FLAG >= 1);
    assert.ok(counters.MODERATION_APPROVE >= 1);
    assert.ok(counters.CHARGE >= 1);
    assert.ok(counters.REPLIED >= 1);
    assert.ok(counters.REFUND >= 1);
    for (const [stage, rate] of Object.entries(metrics.body.funnel.conversionRates)) {
      assert.ok(Number.isFinite(rate), `漏斗 ${stage} 转化率必须是有限数（不得 NaN）`);
    }

    // ② 追溯：一条消息串起审核与资金链，且顺序正确
    const trace = await getJson('/api/ops/trace?messageId=obs-1');
    assert.equal(trace.status, 200);
    const sources = new Set(trace.body.steps.map((step) => step.source));
    assert.ok(sources.has('moderation'), '追溯应包含审核来源');
    assert.ok(sources.has('ledger'), '追溯应包含账本来源');
    assert.equal(trace.body.complete, true, '完整链应 complete=true');
    const chargeIndex = trace.body.steps.findIndex((step) => step.type === 'CHARGE');
    const refundIndex = trace.body.steps.findIndex((step) => step.type === 'REFUND');
    assert.ok(chargeIndex >= 0 && refundIndex > chargeIndex, '退款必须晚于扣费');

    // ③ 告警：制造 21 条待审积压 → 应触发 human-review-backlog
    for (let index = 0; index < 21; index += 1) {
      const caseRes = await post('/api/moderation/submit', { messageId: `obs-backlog-${index}`, content: '内部消息，加我微信' }, token);
      assert.equal(caseRes.status, 200);
    }
    const alerts = await getJson('/api/ops/alerts');
    assert.equal(alerts.status, 200);
    assert.ok(alerts.body.gauges.humanReviewBacklog >= 21, '积压应被计数');
    assert.ok(alerts.body.alerts.some((alert) => alert.id === 'human-review-backlog'), '积压越线必须告警');
  });
});

test('⑰ v0.0.5 内测保障：短信熔断降级 / 机审熔断 fail-closed / 备份恢复不自动应用', async () => {
  await withApp({ RATE_LIMIT_PER_MIN: '0', IM2AGENT_SMS_FAULT: '1', IM2AGENT_MACHINE_FAULT: '1', IM2AGENT_SMS_KIND: 'dev' }, async (app) => {
    const base = app.urls[0];
    const post = (pathname, body, token = null) =>
      json(`${base}${pathname}`, {
        method: 'POST',
        headers: { ...(token === null ? {} : { authorization: `Bearer ${token}` }) },
        body: JSON.stringify(body)
      });
    const getJson = (pathname) => json(`${base}${pathname}`);

    // ① 短信主通道故障 → 熔断 + 显式降级（每次仍 200，但必须标记 usedFallback）
    const codes = [];
    for (const phone of ['13800000011', '13800000012', '13800000013']) {
      const otp = await post('/api/account/otp', { phone });
      assert.equal(otp.status, 200, '主通道故障时应降级而不是整体失败');
      codes.push(otp.body.devCode);
    }
    let resilience = await getJson('/api/ops/resilience');
    assert.equal(resilience.body.sms.breaker.state, 'OPEN', '连续失败应打开熔断');
    assert.ok(resilience.body.sms.fallbackUsed >= 3, '必须记录降级次数');
    assert.ok(resilience.body.sms.lastCauseCode, '降级必须带原因码');

    // ② OPEN 后不再调用主方（快速失败，避免雪崩）
    const primaryCallsBefore = resilience.body.sms.primaryCalls;
    const afterOpen = await post('/api/account/otp', { phone: '13800000014' });
    assert.equal(afterOpen.status, 200, '熔断开放时仍应走备用通道');
    resilience = await getJson('/api/ops/resilience');
    assert.equal(resilience.body.sms.primaryCalls, primaryCallsBefore, 'OPEN 后不得再调用主方');

    // ③ 机审熔断 → 必须 fail-closed 转人审（绝不自动放行）
    const registered = await post('/api/account/register', { phone: '13800000011', code: codes[0] });
    const token = registered.body.session.accessToken;
    const firstSubmit = await post('/api/moderation/submit', { messageId: 'resil-1', content: '普通业务沟通' }, token);
    assert.equal(firstSubmit.status, 200);
    assert.equal(firstSubmit.body.case.state, 'HUMAN_PENDING', '机审故障必须转人审');
    assert.equal(firstSubmit.body.case.machineUnavailable, true);
    assert.equal(firstSubmit.body.deliverable.ok, false);
    for (let index = 2; index <= 3; index += 1) {
      await post('/api/moderation/submit', { messageId: `resil-${index}`, content: '普通业务沟通' }, token);
    }
    resilience = await getJson('/api/ops/resilience');
    assert.equal(resilience.body.moderation.breaker.state, 'OPEN');

    // ④ 备份：可创建、可列出、可验签恢复，但**不自动应用**
    const created = await post('/api/ops/backup', { label: '内测演练' });
    assert.equal(created.status, 200);
    assert.ok(String(created.body.backup.sha256).length === 64, '备份必须带 sha256');
    const listed = await getJson('/api/ops/backup');
    assert.ok(listed.body.backups.length >= 1);
    const restored = await post('/api/ops/backup/restore', { file: created.body.backup.file });
    assert.equal(restored.status, 200);
    assert.equal(restored.body.applied, false, 'restore 不得自动应用');
    assert.ok(restored.body.summary.audit >= 1, '恢复数据应包含审计记录');
  });
});

test('⑱ v0.0.9 支付渠道：验签入账 / 幂等 / 篡改拒绝 / 对账', async () => {
  await withApp({ RATE_LIMIT_PER_MIN: '0', IM2AGENT_PAY_SECRET: 'test-pay-secret' }, async (app) => {
    const base = app.urls[0];
    const post = (pathname, body, token = null, extra = {}) =>
      json(`${base}${pathname}`, {
        method: 'POST',
        headers: { ...(token === null ? {} : { authorization: `Bearer ${token}` }), ...extra },
        body: typeof body === 'string' ? body : JSON.stringify(body)
      });

    // 起一个实名账户
    const otp = await post('/api/account/otp', { phone: '13800000021' });
    const registered = await post('/api/account/register', { phone: '13800000021', code: otp.body.devCode });
    const token = registered.body.session.accessToken;
    await post('/api/account/verify', { realName: '钱七', idNumber: '110101199005055678' }, token);

    // ① 下单：INTENT，不产生账本流水
    const intent = await post('/api/pay/intent', { amountCents: 5000, orderId: 'ord-1', description: '内测充值' }, token);
    assert.equal(intent.status, 200);
    assert.equal(intent.body.order.state, 'INTENT');
    let ledger = await json(`${base}/api/ledger`);
    assert.equal(ledger.body.entries.filter((entry) => entry.type === 'DEPOSIT').length, 0, '下单不得入账');

    // ② 篡改 body（签名不匹配）→ 401，且不入账
    const { createHmac } = await import('node:crypto');
    const sign = (body) => `sha256=${createHmac('sha256', 'test-pay-secret').update(body).digest('hex')}`;
    const payload = JSON.stringify({ orderId: 'ord-1', amountCents: 5000, providerTxnId: 'txn-1' });
    const tampered = payload.replace('5000', '5001');
    const bad = await post('/api/pay/callback', tampered, null, {
      'x-signature': sign(payload),
      'x-timestamp': String(Date.now()),
      'content-type': 'application/json'
    });
    assert.equal(bad.status, 401);
    assert.equal(bad.body.error, 'SIGNATURE_INVALID');
    ledger = await json(`${base}/api/ledger`);
    assert.equal(ledger.body.entries.filter((entry) => entry.type === 'DEPOSIT').length, 0, '验签失败绝不入账');

    // ③ 正确签名 → PAID + 一条 DEPOSIT；余额到位
    const ok = await post('/api/pay/callback', payload, null, {
      'x-signature': sign(payload),
      'x-timestamp': String(Date.now()),
      'content-type': 'application/json'
    });
    assert.equal(ok.status, 200);
    assert.equal(ok.body.order.state, 'PAID');
    ledger = await json(`${base}/api/ledger`);
    assert.equal(ledger.body.entries.filter((entry) => entry.type === 'DEPOSIT').length, 1);
    let wallet = await json(`${base}/api/wallet`, { headers: { authorization: `Bearer ${token}` } });
    assert.equal(wallet.body.balance.availableCents, 5000);

    // ④ 重放同一 providerTxnId → 幂等，不入账第二次
    const replay = await post('/api/pay/callback', payload, null, {
      'x-signature': sign(payload),
      'x-timestamp': String(Date.now()),
      'content-type': 'application/json'
    });
    assert.equal(replay.status, 200);
    assert.equal(replay.body.duplicate, true);
    ledger = await json(`${base}/api/ledger`);
    assert.equal(ledger.body.entries.filter((entry) => entry.type === 'DEPOSIT').length, 1, '重放不得重复入账');

    // ⑤ 对账：全匹配
    const reconcile = await json(`${base}/api/ops/reconcile`);
    assert.equal(reconcile.status, 200);
    assert.equal(reconcile.body.ok, true);
    assert.equal(reconcile.body.mismatched.length, 0);
    assert.equal(reconcile.body.missingInLedger.length, 0);
  });
});

test('⑲ v0.1.0 版本契约与数据 schema 闸门', async () => {
  // 纯逻辑：serialize 带 schemaVersion；不匹配必须显式失败
  const { serialize, createWorld, SCHEMA_VERSION, DomainError } = await import('../apps/im2agent/domain.mjs');
  const world = createWorld({});
  const snapshot = serialize(world);
  assert.equal(snapshot.schemaVersion, SCHEMA_VERSION);
  assert.throws(
    () => createWorld({ restored: { ...snapshot, schemaVersion: SCHEMA_VERSION - 1 } }),
    (error) => error instanceof DomainError && error.code === 'SCHEMA_MISMATCH'
  );
  assert.throws(() => createWorld({ restored: { ...snapshot, schemaVersion: undefined } }), /SCHEMA_MISMATCH|schema/);

  await withApp({ RATE_LIMIT_PER_MIN: '0' }, async (app) => {
    const version = await json(`${app.urls[0]}/api/version`);
    assert.equal(version.status, 200);
    assert.equal(version.body.version, '0.1.0');
    assert.equal(version.body.contractRevision, 'R21');
    assert.equal(version.body.schemaVersion, SCHEMA_VERSION);
    assert.ok(version.body.capabilities >= 10, '能力清单应列出对外能力数');

    const health = await json(`${app.urls[0]}/api/health`);
    assert.equal(health.body.version, '0.1.0');
    assert.equal(health.body.schemaVersion, SCHEMA_VERSION);
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
