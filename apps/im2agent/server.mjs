/**
 * 模块职责：im2agent 极轻量验证原型的**单进程服务** —— Demo / 局域网 / 公网生产 三种形态同一份代码
 * 契约来源：apps/im2agent/README.md；治理语义对齐 contracts/INTERFACES.md（此处为极简实现）
 *
 * 形态由环境变量决定（不靠改代码）：
 *   PROFILE=demo（默认）  只绑 127.0.0.1，不要求 ADMIN_TOKEN，内存存储 —— `node server.mjs` 即跑
 *   PROFILE=lan           绑 0.0.0.0，打印局域网地址，可选 jsonfile 落盘 —— 办公室/家庭可用
 *   PROFILE=prod          必须设置 ADMIN_TOKEN 否则拒绝启动；强制安全响应头；支持反代 HTTPS（Caddy）
 *   存储：STORE=memory|jsonfile|postgres（postgres 在本机无驱动，显式报错不静默降级）
 *
 * 页面：
 *   /            极简状态页
 *   /admin/      网页运维界面（Agent 注册表 / 审批流 / 付费账本 / 审计流 / LLM 状态）
 * API：
 *   GET  /api/health              健康检查（无鉴权）
 *   GET  /api/state               对外状态（无鉴权，不含敏感细节）
 *   GET  /api/llm                 LLM 引擎状态（无鉴权，只读）
 *   GET  /api/admin/state         运维全量状态（需 x-admin-token）
 *   POST /api/approval/decide     { ticketId, action, comment }（需 token）
 *   GET  /api/ledger              账本（需 token）
 *   GET  /api/audit               审计链 + 验链结果（需 token）
 *   POST /api/llm/draft           { text } 试用三档降级（需 token）
 *   GET  /api/config              运行配置（需 token，token 本身打码）
 */
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { createStore, STORE_KIND } from './store.mjs';
import { createLlm, resolveLlmConfig, LLM_MODE } from './llm.mjs';
import { createWorld, decide, previewContext, serialize, snapshot, DomainError, USERS, USER_NAMES } from './domain.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const PUBLIC_DIR = path.join(HERE, 'public');

export const PROFILE = Object.freeze({ DEMO: 'demo', LAN: 'lan', PROD: 'prod' });

export function resolveRuntime(env = process.env) {
  const profile = String(env.PROFILE ?? PROFILE.DEMO).toLowerCase();
  const adminToken = env.ADMIN_TOKEN ?? null;
  if (!Object.values(PROFILE).includes(profile)) {
    throw new Error(`未知 PROFILE: ${profile}（允许 demo/lan/prod）`);
  }
  if (profile === PROFILE.PROD && (adminToken === null || adminToken.length < 8)) {
    // 生产形态没有管理员令牌 = 运维修界面裸奔；宁可起不来
    throw new Error('PROFILE=prod 必须设置 ADMIN_TOKEN（≥8 字符）');
  }
  return {
    profile,
    port: Number(env.PORT ?? (profile === PROFILE.DEMO ? 8787 : 8080)),
    host: env.HOST ?? (profile === PROFILE.DEMO ? '127.0.0.1' : '0.0.0.0'),
    adminToken,
    storeKind: env.STORE ?? (profile === PROFILE.DEMO ? STORE_KIND.MEMORY : STORE_KIND.JSONFILE),
    storeFile: env.STORE_FILE ?? path.join(HERE, '.data', 'im2agent.json'),
    llm: resolveLlmConfig(env)
  };
}

export function createApp({ env = process.env, runtime = null, llm = null, store = null } = {}) {
  const rt = runtime ?? resolveRuntime(env);
  const storeImpl = store ?? createStore({ kind: rt.storeKind, file: rt.storeFile, dsn: env.POSTGRES_URL ?? null });
  const llmImpl = llm ?? createLlm({ config: rt.llm });
  const restored = storeImpl.read();
  const world = createWorld({ restored });
  const holder = { world, rt, store: storeImpl, llm: llmImpl };

  const persist = () => {
    if (holder.store.kind === STORE_KIND.MEMORY) return;
    holder.store.write(serialize(holder.world));
  };

  // 多端同步：SSE 推送（客户端也可用 1s 轮询兜底，见 client/README）
  holder.sseClients = new Set();
  holder.broadcast = (payload) => {
    const line = `data: ${JSON.stringify(payload)}\n\n`;
    for (const client of holder.sseClients) {
      try {
        client.write(line);
      } catch {
        holder.sseClients.delete(client);
      }
    }
  };

  const server = createServer(async (req, res) => {
    try {
      await route(req, res, holder, persist);
    } catch (error) {
      const code = error?.code ?? 'INTERNAL';
      const status = code === 'NOT_FOUND' ? 404 : code === 'ILLEGAL_TRANSITION' || code === 'INVALID_ARGUMENT' ? 409 : 400;
      sendJson(res, status, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
  });
  return { server, holder, persist };
}

function securityHeaders(res, profile) {
  res.setHeader('x-content-type-options', 'nosniff');
  res.setHeader('x-frame-options', 'DENY');
  res.setHeader('referrer-policy', 'no-referrer');
  res.setHeader('content-security-policy', "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'");
  if (profile === PROFILE.PROD) res.setHeader('strict-transport-security', 'max-age=31536000; includeSubDomains');
}

function authorized(req, holder) {
  const token = holder.rt.adminToken;
  if (token === null) return holder.rt.profile !== PROFILE.PROD; // demo/lan 未设令牌时放行（生产起不来）
  const header = req.headers['x-admin-token'];
  const provided = Array.isArray(header) ? header[0] : header;
  return typeof provided === 'string' && provided === token;
}

function requireAdmin(req, res, holder) {
  if (authorized(req, holder)) return true;
  sendJson(res, 401, { error: 'UNAUTHENTICATED', hint: '请求头 x-admin-token 必须等于服务端 ADMIN_TOKEN' }, holder.rt);
  return false;
}

async function route(req, res, holder, persist) {
  const url = new URL(req.url ?? '/', `http://${req.headers.host ?? 'localhost'}`);
  const { pathname, searchParams } = url;
  const method = (req.method ?? 'GET').toUpperCase();
  securityHeaders(res, holder.rt.profile);

  if (method === 'GET' && (pathname === '/' || pathname === '/index.html')) {
    const html = await readFile(path.join(PUBLIC_DIR, 'index.html'), 'utf8');
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
    res.end(html);
    return;
  }
  if (method === 'GET' && (pathname === '/admin' || pathname === '/admin/' || pathname === '/admin/index.html')) {
    const html = await readFile(path.join(PUBLIC_DIR, 'admin.html'), 'utf8');
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
    res.end(html);
    return;
  }

  if (method === 'GET' && pathname === '/api/health') {
    const chain = holder.world.audit.verify();
    sendJson(res, 200, { ok: true, service: 'im2agent', profile: holder.rt.profile, store: holder.store.kind, auditOk: chain.ok, pending: holder.world.state.tickets.filter((t) => t.state === 'PENDING').length, generatedAt: new Date().toISOString() }, holder.rt);
    return;
  }

  // 规范要求：GET /healthz 返回 OK（curl 可直接判读；带 Accept: application/json 时回 JSON）
  if (method === 'GET' && pathname === '/healthz') {
    const chain = holder.world.audit.verify();
    const wantsJson = String(req.headers.accept ?? '').includes('application/json');
    if (wantsJson) {
      sendJson(res, 200, { status: 'OK', service: 'im2agent', auditOk: chain.ok, pending: holder.world.state.tickets.filter((t) => t.state === 'PENDING').length }, holder.rt);
      return;
    }
    res.writeHead(200, { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store' });
    res.end('OK');
    return;
  }

  // 多端同步（SSE）：审批/预览/决策都会广播；客户端也可退化为 1s 轮询
  if (method === 'GET' && pathname === '/api/events') {
    res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-store', connection: 'keep-alive' });
    res.write(`data: ${JSON.stringify({ type: 'hello', at: new Date().toISOString() })}\n\n`);
    holder.sseClients.add(res);
    const timer = setInterval(() => {
      try {
        res.write(': ping\n\n');
      } catch {
        clearInterval(timer);
        holder.sseClients.delete(res);
      }
    }, 15_000);
    if (typeof timer.unref === 'function') timer.unref();
    req.on('close', () => {
      clearInterval(timer);
      holder.sseClients.delete(res);
    });
    return;
  }

  if (method === 'GET' && pathname === '/api/state') {
    const snap = snapshot(holder.world);
    sendJson(res, 200, {
      profile: holder.rt.profile,
      agents: snap.agents,
      pendingTickets: snap.tickets.filter((t) => t.state === 'PENDING').length,
      messages: snap.messages.map((m) => ({ id: m.id, topic: m.topic, state: m.state, amountCents: m.amountCents })),
      auditChain: snap.auditChain,
      llm: holder.llm.stats()
    }, holder.rt);
    return;
  }

  if (method === 'GET' && pathname === '/api/llm') {
    sendJson(res, 200, { stats: holder.llm.stats(), config: holder.llm.config() }, holder.rt);
    return;
  }

  if (method === 'GET' && pathname === '/api/config') {
    if (!requireAdmin(req, res, holder)) return;
    const { llm, ...safeRt } = holder.rt;
    sendJson(res, 200, {
      ...safeRt,
      adminToken: holder.rt.adminToken === null ? null : '***',
      storeInfo: holder.store.info(),
      llm: holder.llm.config(),
      lanUrls: lanUrls(holder.rt.port)
    }, holder.rt);
    return;
  }

  if (method === 'GET' && pathname === '/api/admin/state') {
    if (!requireAdmin(req, res, holder)) return;
    const snap = snapshot(holder.world);
    // 同时给出 Go 规范视图（Ticket/AuditLog/Ledger 的 snake_case 形态），便于 Flutter/Go 客户端直接消费
    const ticketsView = snap.tickets.map((ticket) => ({
      id: ticket.id,
      agent_id: ticket.agentId ?? ticket.sourceAgentId ?? null,
      title: ticket.title ?? ticket.ticketType,
      context: ticket.context ?? null,
      price: ticket.price ?? ticket.payload?.amountCents ?? 0,
      status: ticket.state,
      created_at: ticket.createdAt
    }));
    const auditsView = snap.audit.map((record) => ({
      time: record.at,
      actor: record.actor ?? null,
      action: record.action,
      target_id: record.target ?? null,
      details: record.detail === null || record.detail === undefined ? null : JSON.stringify(record.detail)
    }));
    const ledgersView = snap.ledger.map((entry) => ({
      time: entry.at,
      user_id: entry.userId,
      type: entry.type,
      price: entry.amountCents,
      ref: entry.ref ?? null,
      idempotency_key: entry.idempotencyKey
    }));
    sendJson(res, 200, {
      ...snap,
      ticketsView,
      audits: auditsView,
      ledgers: ledgersView,
      profile: holder.rt.profile,
      store: holder.store.info(),
      llm: holder.llm.stats(),
      usernames: USER_NAMES
    }, holder.rt);
    return;
  }

  // 让 LLM 生成审批单的上下文摘要（Go 规范里的 Ticket.Context）
  if (method === 'POST' && pathname === '/api/approval/preview') {
    if (!requireAdmin(req, res, holder)) return;
    const body = await readJson(req);
    try {
      const result = await previewContext(holder.world, { ticketId: String(body.ticket_id ?? body.ticketId ?? ''), llm: holder.llm });
      persist();
      holder.broadcast({ type: 'preview', at: new Date().toISOString(), ticketId: result.ticket.id, tier: result.draft.tier });
      sendJson(res, 200, { ok: true, ticket_id: result.ticket.id, context: result.ticket.context, tier: result.draft.tier, degraded: result.draft.degraded === true }, holder.rt);
    } catch (error) {
      const code = error instanceof DomainError ? error.code : (error?.code ?? 'PREVIEW_FAILED');
      sendJson(res, code === 'NOT_FOUND' ? 404 : 400, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'POST' && pathname === '/api/approval/decide') {
    if (!requireAdmin(req, res, holder)) return;
    const body = await readJson(req);
    // 兼容两种写法：{ticketId, action:"APPROVE"} 与规范写法 {ticket_id, action|decision:"approve"}
    const ticketId = String(body.ticket_id ?? body.ticketId ?? '');
    const action = String(body.action ?? body.decision ?? '').toUpperCase();
    try {
      const outcome = await decide(holder.world, { ticketId, action, comment: body.comment ?? null, llm: holder.llm });
      persist();
      holder.broadcast({ type: 'decide', at: new Date().toISOString(), ticketId, state: outcome.ticket.state });
      sendJson(res, 200, {
        ok: true,
        ticket: outcome.ticket,
        message: outcome.message,
        state: snapshot(holder.world)
      }, holder.rt);
    } catch (error) {
      const code = error instanceof DomainError ? error.code : (error?.code ?? 'DECIDE_FAILED');
      const status = code === 'NOT_FOUND' ? 404 : code === 'ILLEGAL_TRANSITION' ? 409 : 400;
      sendJson(res, status, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/ledger') {
    if (!requireAdmin(req, res, holder)) return;
    sendJson(res, 200, { entries: holder.world.ledger.entries({}), stats: holder.world.ledger.stats() }, holder.rt);
    return;
  }

  if (method === 'GET' && pathname === '/api/audit') {
    if (!requireAdmin(req, res, holder)) return;
    sendJson(res, 200, { chain: holder.world.audit.verify(), entries: holder.world.audit.records({ limit: 50 }) }, holder.rt);
    return;
  }

  if (method === 'POST' && pathname === '/api/llm/draft') {
    if (!requireAdmin(req, res, holder)) return;
    const body = await readJson(req);
    const draft = await holder.llm.draft({ text: String(body.text ?? ''), topic: body.topic ?? null });
    sendJson(res, 200, { draft, stats: holder.llm.stats() }, holder.rt);
    return;
  }

  sendJson(res, 404, { error: 'NOT_FOUND', path: pathname }, holder.rt);
}

async function readJson(req) {
  const chunks = [];
  let total = 0;
  for await (const chunk of req) {
    total += chunk.length;
    if (total > 64 * 1024) throw new Error('请求体超过 64KB');
    chunks.push(chunk);
  }
  if (chunks.length === 0) return {};
  const text = Buffer.concat(chunks).toString('utf8').trim();
  return text === '' ? {} : JSON.parse(text);
}

function sendJson(res, status, payload, rt) {
  if (rt) securityHeaders(res, rt.profile);
  const body = JSON.stringify(payload);
  res.writeHead(status, { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store', 'content-length': Buffer.byteLength(body) });
  res.end(body);
}

/** 局域网形态：列出本机所有非回环 IPv4，方便手机/平板直接连 */
export function lanUrls(port) {
  const urls = [];
  const interfaces = os.networkInterfaces();
  for (const [name, addresses] of Object.entries(interfaces)) {
    for (const address of addresses ?? []) {
      if (address.family === 'IPv4' && address.internal === false) urls.push(`http://${address.address}:${port}（${name}）`);
    }
  }
  return urls;
}

export function startServer({ env = process.env, ...rest } = {}) {
  const app = createApp({ env, ...rest });
  const { rt } = app.holder;
  return new Promise((resolve, reject) => {
    app.server.once('error', reject);
    app.server.listen(rt.port, rt.host, () => {
      const address = app.server.address();
      const urls = [`http://${address.address}:${address.port}`];
      if (rt.host === '0.0.0.0') urls.push(...lanUrls(address.port));
      resolve({
        ...app,
        runtime: rt,
        port: address.port,
        host: rt.host,
        urls,
        close: () => new Promise((done) => app.server.close(() => done()))
      });
    });
  });
}

// ── CLI 入口：单进程、零依赖起闭环 ──────────────────────────────────────────
const isMain = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isMain) {
  try {
    const started = await startServer({});
    const { rt } = started;
    console.log('\n  im2agent 已启动');
    console.log('  ─────────────────────────────────────────────');
    console.log(`  形态        ${rt.profile}`);
    console.log(`  存储        ${started.holder.store.kind}${rt.storeKind === 'jsonfile' ? `（${rt.storeFile}）` : ''}`);
    console.log(`  LLM         ${started.holder.llm.stats().mode}（${started.holder.llm.config().model}）`);
    console.log(`  状态页      ${started.urls[0]}/`);
    console.log(`  运维界面    ${started.urls[0]}/admin/${rt.adminToken ? `?token=${rt.adminToken}` : ''}`);
    if (started.urls.length > 1) {
      console.log('  局域网地址：');
      for (const url of started.urls.slice(1)) console.log(`              ${url}`);
    }
    if (rt.adminToken === null) console.log('  ⚠ 未设置 ADMIN_TOKEN：仅适合 demo/lan，公网部署必须设置');
    console.log('  ─────────────────────────────────────────────');
    console.log('  Ctrl+C 停止\n');
  } catch (error) {
    console.error(`启动失败：${String(error?.message ?? error)}`);
    process.exitCode = 1;
  }
}

export { LLM_MODE, USERS };
