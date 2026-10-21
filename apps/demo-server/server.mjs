/**
 * 模块职责：演示服务 —— 把 flow.mjs 的业务主线暴露为 HTTP + 静态控制台，验证"协议面"可用
 * 契约来源：contracts/INTERFACES.md v1.0、contracts/openapi/orchestrator.openapi.yaml
 *
 * 路由（与 openapi 契约一致）：
 *   GET  /                              审批态 / 巡视态 控制台
 *   GET  /api/health                    健康检查
 *   GET  /api/state                     全量状态快照（控制台轮询）
 *   POST /api/approvals/:id/decide      人在审批态做决策 { action: 'APPROVE'|'MODIFY'|'REJECT', comment }
 *   POST /api/reset                     重置演示世界（回到种子场景）
 *
 * 说明：这是**演示服务**，不是生产网关。生产网关见 docs/01-总体架构.md 的 L3/L4 分层；
 * 本服务刻意保持零依赖，以便在只有 Node 的环境里也能验证治理逻辑。
 */
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

import { createWorld, seedScenario, decideTicket, snapshot, probeChecks, USERS, AGENTS } from './flow.mjs';

const PUBLIC_DIR = path.join(import.meta.dirname, 'public');
const MAX_BODY_BYTES = 64 * 1024;

export function createApp({ clock } = {}) {
  const holder = { world: buildWorld(clock) };

  const server = createServer(async (req, res) => {
    try {
      await route(req, res, holder);
    } catch (error) {
      sendJson(res, 500, { error: 'INTERNAL', message: String(error?.message ?? error) });
    }
  });

  return { server, holder, getState: () => snapshot(holder.world) };
}

function buildWorld(clock) {
  const world = createWorld(clock ? { clock } : {});
  seedScenario(world);
  world.checks = probeChecks();
  return world;
}

export function startServer({ port = 8787, host = '127.0.0.1', clock } = {}) {
  const app = createApp({ clock });
  return new Promise((resolve, reject) => {
    app.server.once('error', reject);
    app.server.listen(port, host, () => {
      const address = app.server.address();
      resolve({
        ...app,
        host,
        port: address.port,
        url: `http://${host}:${address.port}`,
        close: () => new Promise((done) => app.server.close(done))
      });
    });
  });
}

async function route(req, res, holder) {
  const url = new URL(req.url ?? '/', `http://${req.headers.host ?? 'localhost'}`);
  const { pathname } = url;
  const method = (req.method ?? 'GET').toUpperCase();

  if (method === 'GET' && (pathname === '/' || pathname === '/index.html')) {
    const html = await readFile(path.join(PUBLIC_DIR, 'index.html'), 'utf8');
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
    res.end(html);
    return;
  }

  if (method === 'GET' && pathname === '/api/health') {
    const state = snapshot(holder.world);
    sendJson(res, 200, {
      ok: true,
      service: 'agent-im-demo',
      pendingApprovals: state.pendingCount,
      checks: `${state.meta.checksPassed}/${state.meta.checksTotal}`,
      generatedAt: state.meta.generatedAt
    });
    return;
  }

  if (method === 'GET' && pathname === '/api/state') {
    sendJson(res, 200, snapshot(holder.world));
    return;
  }

  if (method === 'POST' && pathname === '/api/reset') {
    holder.world = buildWorld();
    sendJson(res, 200, { ok: true, state: snapshot(holder.world) });
    return;
  }

  // ── 陌生人付费消息：低层协议面（与 contracts/openapi/orchestrator.openapi.yaml 对齐）──
  // 用于手工/脚本演示"先预审后付费"的完整链路，无需经过控制台。
  if (method === 'POST' && pathname === '/api/intent') {
    const body = await readJson(req);
    try {
      const message = holder.world.paid.submitIntent({
        senderUserId: body.senderUserId ?? USERS.ZHOU,
        senderAgentId: body.senderAgentId ?? AGENTS.ZHOU_BUTLER,
        receiverUserId: body.receiverUserId ?? USERS.LIN,
        topic: body.topic ?? '陌生人询盘',
        summary: body.summary ?? '（未提供摘要）',
        billingMode: Number(body.billingMode ?? 2),
        amountCents: Number(body.amountCents ?? 5000),
        idempotencyKey: body.idempotencyKey ?? `intent:http-${Date.now()}`
      });
      sendJson(res, 201, { ok: true, message });
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'SUBMIT_FAILED', message: String(error?.message ?? error) });
    }
    return;
  }

  const intentMatch = pathname.match(/^\/api\/intent\/([^/]+)\/(screen|pay|view|reply)$/);
  if (method === 'POST' && intentMatch) {
    const messageId = decodeURIComponent(intentMatch[1]);
    const op = intentMatch[2];
    const body = await readJson(req);
    const world = holder.world;
    try {
      if (op === 'screen') {
        const message = world.paid.screen({ messageId, verdict: String(body.verdict ?? 'PASS').toUpperCase() });
        sendJson(res, 200, { ok: true, message });
      } else if (op === 'pay') {
        const outcome = world.paid.pay({ messageId, payerUserId: body.payerUserId ?? USERS.ZHOU });
        sendJson(res, 200, { ok: true, message: outcome.message, charged: outcome.charged });
      } else if (op === 'view') {
        const message = world.paid.view({ messageId, viewerUserId: body.viewerUserId ?? USERS.LIN });
        sendJson(res, 200, { ok: true, message });
      } else {
        const message = world.paid.reply({ messageId, replyText: body.replyText ?? null });
        sendJson(res, 200, { ok: true, message });
      }
    } catch (error) {
      const code = error?.code ?? 'OP_FAILED';
      const status = code === 'NOT_FOUND' ? 404 : code === 'ILLEGAL_STATE' ? 409 : code === 'NOT_PAYABLE' ? 409 : 400;
      sendJson(res, status, { error: code, message: String(error?.message ?? error) });
    }
    return;
  }

  const decideMatch = pathname.match(/^\/api\/approvals\/([^/]+)\/decide$/);
  if (method === 'POST' && decideMatch) {
    const ticketId = decodeURIComponent(decideMatch[1]);
    const body = await readJson(req);
    const action = String(body?.action ?? '').toUpperCase();
    if (!['APPROVE', 'MODIFY', 'REJECT'].includes(action)) {
      sendJson(res, 400, { error: 'INVALID_ARGUMENT', message: 'action 必须是 APPROVE / MODIFY / REJECT' });
      return;
    }
    try {
      const outcome = decideTicket(holder.world, ticketId, action, body?.comment ?? null, body?.patch ?? null);
      sendJson(res, 200, {
        ok: true,
        ticketId,
        state: outcome.ticket?.state ?? null,
        result: outcome.result ?? null,
        messageState: outcome.message?.state ?? null,
        snapshot: snapshot(holder.world)
      });
    } catch (error) {
      const code = error?.code ?? 'DECIDE_FAILED';
      const status = code === 'NOT_FOUND' ? 404 : code === 'ILLEGAL_TRANSITION' ? 409 : 400;
      sendJson(res, status, { error: code, message: String(error?.message ?? error) });
    }
    return;
  }

  sendJson(res, 404, { error: 'NOT_FOUND', path: pathname });
}

async function readJson(req) {
  const chunks = [];
  let total = 0;
  for await (const chunk of req) {
    total += chunk.length;
    if (total > MAX_BODY_BYTES) throw new Error('请求体超过 64KB 上限');
    chunks.push(chunk);
  }
  if (chunks.length === 0) return {};
  const text = Buffer.concat(chunks).toString('utf8').trim();
  if (!text) return {};
  return JSON.parse(text);
}

function sendJson(res, status, payload) {
  const body = JSON.stringify(payload);
  res.writeHead(status, {
    'content-type': 'application/json; charset=utf-8',
    'cache-control': 'no-store',
    'content-length': Buffer.byteLength(body)
  });
  res.end(body);
}

// ── CLI 入口 ──
const isMain = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isMain) {
  const port = Number(process.env.PORT ?? process.argv[2] ?? 8787);
  const started = await startServer({ port });
  const state = started.getState();
  console.log(`\n  AgentIM 演示服务已启动`);
  console.log(`  ─────────────────────────────────────────────`);
  console.log(`  控制台      ${started.url}`);
  console.log(`  状态快照    ${started.url}/api/state`);
  console.log(`  健康检查    ${started.url}/api/health`);
  console.log(`  待审批      ${state.pendingCount} 张卡片`);
  console.log(`  治理自检    ${state.meta.checksPassed}/${state.meta.checksTotal} 通过`);
  console.log(`  ─────────────────────────────────────────────`);
  console.log(`  按 Ctrl+C 停止\n`);
}
