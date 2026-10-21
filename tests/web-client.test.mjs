/**
 * 模块职责：Web/PWA 客户端的验收测试 —— 纯函数逻辑 + 静态资源服务 + 安全边界
 * 契约来源：contracts/INTERFACES.md §47
 *
 * 运行：node tests/web-client.test.mjs
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { formatCents, summarizeTickets, canDecide, moderationQueue, funnelSummary } from '../apps/im2agent/public/app/logic.mjs';
import { startServer } from '../apps/im2agent/server.mjs';

test('logic.mjs：纯函数可被 Node 直接导入（无 DOM/网络）', () => {
  assert.equal(formatCents(5000), '¥50.00');
  assert.equal(formatCents(0), '¥0.00');
  assert.equal(formatCents('bad'), '—');
  assert.equal(formatCents(undefined), '—');

  const summary = summarizeTickets({
    tickets: [
      { state: 'PENDING', priority: 'high' },
      { state: 'PENDING', priority: 'normal' },
      { state: 'COMPLETED' }
    ]
  });
  assert.deepEqual(summary, { pending: 2, pendingHigh: 1, done: 1 });
  assert.deepEqual(summarizeTickets({}), { pending: 0, pendingHigh: 0, done: 0 });
  assert.deepEqual(summarizeTickets(null), { pending: 0, pendingHigh: 0, done: 0 });

  assert.equal(canDecide('operator'), true);
  assert.equal(canDecide('viewer'), false);
  assert.equal(canDecide(undefined), false);

  const queue = moderationQueue({ moderation: [{ caseId: 'c1', state: 'HUMAN_PENDING' }, { caseId: 'c2', state: 'APPROVED' }] });
  assert.equal(queue.length, 1);
  assert.equal(queue[0].caseId, 'c1');
  assert.deepEqual(moderationQueue({}), []);

  assert.deepEqual(funnelSummary({}), []);
  const funnel = funnelSummary({ funnel: { stages: [{ stage: 'PAID_SENT', count: 3, conversionFromPrevious: 1 }, { stage: 'CHARGE', count: 2, conversionFromPrevious: 0.667 }] } });
  assert.equal(funnel.length, 2);
  assert.equal(funnel[1].stage, 'CHARGE');
  assert.ok(Math.abs(funnel[1].rate - 0.667) < 1e-9);
});

async function withApp(fn) {
  const app = await startServer({ env: { PROFILE: 'demo', PORT: '0', STORE: 'memory', RATE_LIMIT_PER_MIN: '0' } });
  try {
    return await fn(app);
  } finally {
    await app.close();
  }
}

test('静态资源：/app/ 壳、manifest、service worker、logic 均可访问且 MIME 正确', async () => {
  await withApp(async (app) => {
    const base = app.urls[0];

    const shell = await fetch(`${base}/app/`);
    assert.equal(shell.status, 200);
    assert.match(String(shell.headers.get('content-type')), /text\/html/);
    const html = await shell.text();
    assert.ok(html.includes('IM2A'), '壳应含品牌');
    assert.ok(html.includes('manifest.webmanifest'), '壳应引用 PWA 清单');
    assert.ok(html.includes('app.js'), '壳应加载客户端脚本');

    const manifest = await fetch(`${base}/app/manifest.webmanifest`);
    assert.equal(manifest.status, 200);
    assert.match(String(manifest.headers.get('content-type')), /manifest\+json/);
    const manifestBody = await manifest.json();
    assert.equal(manifestBody.display, 'standalone');
    assert.ok(Array.isArray(manifestBody.icons) && manifestBody.icons.length >= 1);

    const sw = await fetch(`${base}/app/sw.js`);
    assert.equal(sw.status, 200);
    assert.match(String(sw.headers.get('content-type')), /javascript/);
    assert.match(await sw.text(), /addEventListener/);

    const logic = await fetch(`${base}/app/logic.mjs`);
    assert.equal(logic.status, 200);
    assert.match(String(logic.headers.get('content-type')), /javascript/);

    const appJs = await fetch(`${base}/app/app.js`);
    const appSource = await appJs.text();
    assert.ok(appSource.includes('/api/approval/decide'), '客户端应调用真实审批接口');
    assert.ok(appSource.includes('/api/events'), '客户端应支持 SSE（可用时自动刷新）');
  });
});

test('安全边界：目录穿越被拒；/app 下的路径不能读到服务端源码', async () => {
  await withApp(async (app) => {
    const base = app.urls[0];
    const traversal = await fetch(`${base}/app/../server.mjs`);
    assert.equal(traversal.status, 404, '目录穿越必须 404');
    const traversal2 = await fetch(`${base}/app/%2e%2e/server.mjs`);
    assert.equal(traversal2.status, 404);
    const missing = await fetch(`${base}/app/nope.js`);
    assert.equal(missing.status, 404);
  });
});
