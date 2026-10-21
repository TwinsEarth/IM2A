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
import { randomUUID } from 'node:crypto';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { createStore, STORE_KIND } from './store.mjs';
import { createLlm, resolveLlmConfig, LLM_MODE } from './llm.mjs';
import { createRateLimiter, clientKey } from './ratelimit.mjs';
import { AccountService } from '../../services/account/src/account.mjs';
import { createSmsProvider } from '../../services/account/src/sms-provider.mjs';
import { OtpDelivery } from '../../services/account/src/otp-delivery.mjs';
import { Wallet } from '../../services/payment/src/wallet.mjs';
import { DisputeService } from '../../services/payment/src/dispute.mjs';
import { PaymentIntentService } from '../../services/payment/src/channel-adapter.mjs';
import { Reconciler } from '../../services/payment/src/reconcile.mjs';
import { ModerationService } from '../../services/moderation/src/moderation.mjs';
import { Metrics, METRIC, evaluateAlerts } from '../../services/observability/src/metrics.mjs';
import { AuditTrace } from '../../services/observability/src/trace.mjs';
import { CircuitBreaker, ProviderGuard } from '../../services/resilience/src/provider-guard.mjs';
import { BackupService } from '../../services/persistence/src/backup.mjs';
import { createBroadcastBus } from '../../services/messaging/src/broadcast.mjs';
import { VERSION, SCHEMA_VERSION as VERSION_SCHEMA, buildInfo } from './version.mjs';
import { createWorld, decide, previewContext, recordAudit, serialize, snapshot, DomainError, USERS, USER_NAMES } from './domain.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const PUBLIC_DIR = path.join(HERE, 'public');

export const PROFILE = Object.freeze({ DEMO: 'demo', LAN: 'lan', PROD: 'prod' });

/** 两级权限：operator 可写（决策/预览），viewer 只读（看板/账本/审计） */
export const ROLE = Object.freeze({ OPERATOR: 'operator', VIEWER: 'viewer' });

export function resolveRuntime(env = process.env) {
  const profile = String(env.PROFILE ?? PROFILE.DEMO).toLowerCase();
  const adminToken = env.ADMIN_TOKEN ?? null;
  const viewerToken = env.VIEWER_TOKEN ?? null;
  if (!Object.values(PROFILE).includes(profile)) {
    throw new Error(`未知 PROFILE: ${profile}（允许 demo/lan/prod）`);
  }
  if (profile === PROFILE.PROD && (adminToken === null || adminToken.length < 8)) {
    // 生产形态没有管理员令牌 = 运维修界面裸奔；宁可起不来
    throw new Error('PROFILE=prod 必须设置 ADMIN_TOKEN（≥8 字符）');
  }
  if (viewerToken !== null && viewerToken === adminToken) {
    throw new Error('VIEWER_TOKEN 不能与 ADMIN_TOKEN 相同（只读与可写必须是两个凭证）');
  }
  return {
    profile,
    port: Number(env.PORT ?? (profile === PROFILE.DEMO ? 8787 : 8080)),
    host: env.HOST ?? (profile === PROFILE.DEMO ? '127.0.0.1' : '0.0.0.0'),
    adminToken,
    viewerToken,
    trustProxy: String(env.TRUST_PROXY ?? '0') === '1',
    rateLimitPerMin: Number(env.RATE_LIMIT_PER_MIN ?? 120),
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
  holder.limiter = createRateLimiter({ max: rt.rateLimitPerMin });
  holder.buildInfo = buildInfo(env);

  // v0.0.2：短信通道 + OTP 投递（验证码只经 otp.deliver 注入，永不出 API）
  // v0.0.5：主通道 + 熔断 + 显式备用通道（IM2AGENT_SMS_FAULT=1 可演练运营商故障）
  const smsKind = env.IM2AGENT_SMS_KIND ?? (rt.profile === PROFILE.PROD ? 'log' : 'dev');
  const smsPrimaryProvider =
    env.IM2AGENT_SMS_FAULT === '1'
      ? createSmsProvider({ kind: 'vendor', transport: () => { throw new Error('模拟运营商故障'); } })
      : createSmsProvider({ kind: smsKind });
  const smsFallbackProvider = createSmsProvider({ kind: 'log' });
  const smsBreaker = new CircuitBreaker({ name: 'sms', failureThreshold: 3, cooldownMs: 30_000 });
  const smsGuard = new ProviderGuard({
    name: 'sms',
    primary: (args) => smsPrimaryProvider.sendOtp(args),
    fallback: (args) => smsFallbackProvider.sendOtp(args),
    breaker: smsBreaker,
    onAudit: (event) => recordAudit(holder.world, { source: 'provider-guard', action: event.action, actor: 'provider-guard', decision: event.decision, detail: event.detail })
  });
  const guardedSmsProvider = Object.freeze({
    name: 'sms-guard',
    kind: 'guarded',
    info: () => ({ name: 'sms-guard', primary: smsPrimaryProvider.info(), fallback: smsFallbackProvider.info(), breaker: smsBreaker.stats() }),
    sendOtp: (args) => smsGuard.execute(args).value
  });
  const otpDelivery = new OtpDelivery({
    provider: guardedSmsProvider,
    onAudit: (event) => recordAudit(holder.world, { source: 'otp-delivery', action: 'OTP_DELIVERY', actor: null, decision: event?.causeCode ?? event?.type ?? null, detail: event })
  });

  // v0.0.1 产品版：登录/实名/付费准入 + 钱包（金额真相仍在 ledger，钱包是投影 + 策略）
  const accounts = new AccountService({
    clock: () => new Date(),
    otp: { devMode: rt.profile !== PROFILE.PROD, deliver: otpDelivery.deliver }
  });
  const wallet = new Wallet({
    clock: () => new Date(),
    ledger: world.ledger,
    dailySendCapCents: 500_000,
    messageState: (messageId) => holder.world.state.messages.find((m) => m.id === messageId)?.state ?? null,
    accountState: (accountId) => {
      try {
        return accounts.get(accountId)?.state ?? 'ACTIVE';
      } catch {
        return 'ACTIVE';
      }
    }
  });
  holder.accounts = accounts;
  holder.wallet = wallet;
  holder.sms = { provider: guardedSmsProvider, delivery: otpDelivery };
  holder.disputes = new DisputeService({
    clock: () => new Date(),
    ledger: world.ledger,
    messageState: (messageId) => holder.world.state.messages.find((m) => m.id === messageId)?.state ?? null,
    onAudit: (event) => recordAudit(holder.world, { source: 'dispute', action: 'DISPUTE_EVENT', actor: event?.actor ?? null, decision: event?.type ?? null, detail: event })
  });

  // v0.0.3：审核链（dev 机审 = 关键词规则；真实接入时替换 machine 即可，接口不变）
  const moderationMachine = ({ content }) => {
    if (env.IM2AGENT_MACHINE_FAULT === '1') throw new Error('模拟机审服务故障');
    const text = String(content ?? '');
    const ruleVersion = 'rule-v1';
    const modelVersion = 'dev-keyword-v1';
    if (/诈骗|赌博|裸聊|洗钱|验证码给我|汇款到/.test(text)) return { verdict: 'BLOCK', ruleVersion, modelVersion, hits: ['blocked_keyword'] };
    if (/稳赚|内部消息|加我微信|私下转账|高回报/.test(text)) return { verdict: 'FLAG', ruleVersion, modelVersion, hits: ['flagged_keyword'] };
    return { verdict: 'PASS', ruleVersion, modelVersion, hits: [] };
  };
  // v0.0.5：机审也走熔断——OPEN 时 machine 返回 null → 审核链 fail-closed 转人审（绝不 fail-open）
  const machineBreaker = new CircuitBreaker({ name: 'moderation-machine', failureThreshold: 3, cooldownMs: 30_000 });
  const machineGuard = new ProviderGuard({
    name: 'moderation-machine',
    primary: (args) => moderationMachine(args),
    breaker: machineBreaker,
    onAudit: (event) => recordAudit(holder.world, { source: 'provider-guard', action: event.action, actor: 'provider-guard', decision: event.decision, detail: event.detail })
  });
  const guardedMachine = (args) => {
    try {
      return machineGuard.execute(args).value;
    } catch {
      // 熔断打开/主方失败：返回 null，交由 §40 审核链转 HUMAN_PENDING
      return null;
    }
  };
  holder.moderation = new ModerationService({
    clock: () => new Date(),
    machine: guardedMachine,
    onAudit: (event) => recordAudit(holder.world, { source: 'moderation', action: 'MODERATION_EVENT', actor: event?.actor ?? null, decision: event?.to ?? null, detail: event })
  });
  /** messageId -> caseId：同一条消息重试时复用同一审核案（申诉改判后才能投递） */
  holder.moderationCases = new Map();

  // v0.0.4：可观测性（指标/告警/全链追溯）——只读消费者，不改变任何业务状态
  holder.metrics = new Metrics({ clock: () => new Date() });
  holder.trace = new AuditTrace({
    ledger: world.ledger,
    moderation: holder.moderation,
    disputes: holder.disputes,
    audit: world.audit,
    clock: () => new Date()
  });

  // v0.0.5：备份服务（文件级；restore 只返回数据，不自动应用）
  holder.resilience = { smsBreaker, smsGuard, machineBreaker, machineGuard };
  holder.backup = new BackupService({
    store: storeImpl,
    dir: path.join(HERE, '.data', 'backups'),
    keep: 5,
    clock: () => new Date()
  });

  // v0.0.9：支付渠道（mock）+ 日切对账；接微信/支付宝只换 provider
  holder.payments = new PaymentIntentService({
    clock: () => new Date(),
    ledger: world.ledger,
    channels: ['mock'],
    secret: env.IM2AGENT_PAY_SECRET ?? 'dev-pay-secret'
  });
  holder.reconciler = new Reconciler({
    clock: () => new Date(),
    ledger: world.ledger,
    payments: holder.payments
  });

  // v0.1.2：恢复产品域状态（账户/会话/订单）——否则重启后账号与订单全丢
  if (world.extras?.accounts) accounts.importState(world.extras.accounts);
  if (world.extras?.payments) holder.payments.importState(world.extras.payments);

  const persist = () => {
    if (holder.store.kind === STORE_KIND.MEMORY) return;
    holder.store.write(serialize(holder.world, { accounts: accounts.exportState(), payments: holder.payments.exportState() }));
  };
  holder.persist = persist;

  // 多端同步：SSE 推送（客户端也可用 1s 轮询兜底，见 client/README）
  holder.sseClients = new Set();

  // v0.0.8：多节点广播总线（memory 单机 / tcp 多节点；生产替换为 Redis Pub/Sub 时调用方不变）
  const busKind = env.IM2AGENT_BUS ?? 'memory';
  const busPeers = String(env.IM2AGENT_BUS_PEERS ?? '')
    .split(',')
    .map((item) => item.trim())
    .filter((item) => item !== '');
  holder.bus = createBroadcastBus({
    kind: busKind,
    listenPort: Number(env.IM2AGENT_BUS_PORT ?? 0),
    peers: busPeers
  });
  // 所有来源（本节点 + 其它节点）的事件统一从这里投递到本地 SSE 客户端
  holder.bus.subscribe('im2agent.events', (payload) => {
    const line = `data: ${JSON.stringify(payload)}\n\n`;
    for (const client of holder.sseClients) {
      try {
        client.write(line);
      } catch {
        holder.sseClients.delete(client);
      }
    }
  });
  holder.broadcast = (payload) => holder.bus.publish('im2agent.events', payload);

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
  return roleOf(req, holder) !== null;
}

/** 角色判定：operator（可写）> viewer（只读）；未配置任何令牌时仅 demo/lan 放行为 operator */
function roleOf(req, holder) {
  const { adminToken, viewerToken, profile } = holder.rt;
  if (adminToken === null && viewerToken === null) return profile === PROFILE.PROD ? null : ROLE.OPERATOR;
  const header = req.headers['x-admin-token'];
  const provided = Array.isArray(header) ? header[0] : header;
  if (typeof provided !== 'string') return null;
  if (adminToken !== null && provided === adminToken) return ROLE.OPERATOR;
  if (viewerToken !== null && provided === viewerToken) return ROLE.VIEWER;
  return null;
}

/**
 * 访问控制。write=true 要求 operator；write=false 允许 operator 或 viewer。
 * @returns {string|false} 通过时返回角色
 */
function requireAccess(req, res, holder, { write = false } = {}) {
  const role = roleOf(req, holder);
  if (role === null) {
    sendJson(res, 401, { error: 'UNAUTHENTICATED', hint: '请求头 x-admin-token 必须等于 ADMIN_TOKEN（可写）或 VIEWER_TOKEN（只读）' }, holder.rt);
    return false;
  }
  if (write && role !== ROLE.OPERATOR) {
    sendJson(res, 403, { error: 'FORBIDDEN_ROLE', role, hint: 'viewer 只读；写操作需要 ADMIN_TOKEN' }, holder.rt);
    return false;
  }
  return role;
}

async function route(req, res, holder, persist) {
  const url = new URL(req.url ?? '/', `http://${req.headers.host ?? 'localhost'}`);
  const { pathname, searchParams } = url;
  const method = (req.method ?? 'GET').toUpperCase();
  securityHeaders(res, holder.rt.profile);

  // 应用层限流：健康检查放行，其余 /api/* 按客户端 key 计数（反代后需 TRUST_PROXY=1 才采信 XFF）
  // v0.1.3：健康检查与版本探针不消耗限流额度（监控/预检会高频调用，不应把正常运维打成 429）
  if (pathname.startsWith('/api/') && pathname !== '/api/health' && pathname !== '/api/version' && holder.limiter.enabled) {
    const key = clientKey(req, { trustProxy: holder.rt.trustProxy });
    const verdict = holder.limiter.check(key);
    res.setHeader('x-ratelimit-limit', String(holder.limiter.max));
    res.setHeader('x-ratelimit-remaining', String(Math.max(0, Number.isFinite(verdict.remaining) ? verdict.remaining : holder.limiter.max)));
    if (!verdict.ok) {
      res.setHeader('retry-after', String(Math.ceil(verdict.retryAfterMs / 1000)));
      sendJson(res, 429, { error: 'RATE_LIMITED', retryAfterMs: verdict.retryAfterMs, hint: '按 IP 计数；反代后请设 TRUST_PROXY=1 与 RATE_LIMIT_PER_MIN' }, holder.rt);
      return;
    }
  }

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
    sendJson(res, 200, { ok: true, service: 'im2agent', profile: holder.rt.profile, store: holder.store.kind, auditOk: chain.ok, pending: holder.world.state.tickets.filter((t) => t.state === 'PENDING').length, version: VERSION, schemaVersion: VERSION_SCHEMA, generatedAt: new Date().toISOString() }, holder.rt);
    return;
  }

  // v0.1.0：版本契约（公开可读；声明能力，不代表已验证）
  if (method === 'GET' && pathname === '/api/version') {
    sendJson(res, 200, holder.buildInfo, holder.rt);
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
    const role = requireAccess(req, res, holder);
    if (!role) return;
    const { llm, ...safeRt } = holder.rt;
    sendJson(res, 200, {
      ...safeRt,
      role,
      adminToken: holder.rt.adminToken === null ? null : '***',
      viewerToken: holder.rt.viewerToken === null ? null : '***',
      storeInfo: holder.store.info(),
      llm: holder.llm.config(),
      lanUrls: lanUrls(holder.rt.port)
    }, holder.rt);
    return;
  }

  if (method === 'GET' && pathname === '/api/admin/state') {
    const role = requireAccess(req, res, holder);
    if (!role) return;
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
      role,
      profile: holder.rt.profile,
      store: holder.store.info(),
      llm: holder.llm.stats(),
      usernames: USER_NAMES
    }, holder.rt);
    return;
  }

  // 让 LLM 生成审批单的上下文摘要（Go 规范里的 Ticket.Context）—— 写操作，需 operator
  if (method === 'POST' && pathname === '/api/approval/preview') {
    if (!requireAccess(req, res, holder, { write: true })) return;
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
    if (!requireAccess(req, res, holder, { write: true })) return;
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
    if (!requireAccess(req, res, holder)) return;
    sendJson(res, 200, { entries: holder.world.ledger.entries({}), stats: holder.world.ledger.stats() }, holder.rt);
    return;
  }

  if (method === 'GET' && pathname === '/api/audit') {
    if (!requireAccess(req, res, holder)) return;
    sendJson(res, 200, { chain: holder.world.audit.verify(), entries: holder.world.audit.records({ limit: 50 }) }, holder.rt);
    return;
  }

  if (method === 'POST' && pathname === '/api/llm/draft') {
    if (!requireAccess(req, res, holder, { write: true })) return;
    const body = await readJson(req);
    const draft = await holder.llm.draft({ text: String(body.text ?? ''), topic: body.topic ?? null });
    sendJson(res, 200, { draft, stats: holder.llm.stats() }, holder.rt);
    return;
  }

  // ── v0.0.1 产品版：登录 / 实名 / 付费准入 / 钱包 / 退款 ──
  if (method === 'POST' && pathname === '/api/account/otp') {
    const body = await readJson(req);
    try {
      const otp = holder.accounts.requestOtp({ phone: body.phone });
      holder.metrics.inc(METRIC.OTP_SENT);
      sendJson(res, 200, { ok: true, ...otp }, holder.rt);
    } catch (error) {
      holder.metrics.inc(METRIC.OTP_FAILED);
      const code = error?.code ?? 'OTP_FAILED';
      // 投递层失败对用户是"稍后重试"，对运维是告警：429 冷却/额度、502 通道故障
      const status =
        code === 'OTP_COOLDOWN' || code === 'OTP_DAILY_CAP'
          ? 429
          : code === 'OTP_DELIVERY_FAILED' || code === 'PROVIDER_UNAVAILABLE' || String(code).startsWith('SMS_')
            ? 502
            : 400;
      sendJson(res, status, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'POST' && pathname === '/api/account/register') {
    const body = await readJson(req);
    try {
      const result = holder.accounts.register({ phone: body.phone, code: body.code, displayName: body.displayName ?? null });
      persist();
      sendJson(res, 200, { ok: true, account: result.account, session: result.session }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'REGISTER_FAILED';
      sendJson(res, code === 'ALREADY_REGISTERED' ? 409 : 400, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'POST' && pathname === '/api/account/verify') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const body = await readJson(req);
    try {
      const info = holder.accounts.verifyRealName({ accountId: session.session.accountId, realName: body.realName, idNumber: body.idNumber });
      persist();
      sendJson(res, 200, { ok: true, account: holder.accounts.get(session.session.accountId), realName: info }, holder.rt);
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'VERIFY_FAILED', message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'POST' && pathname === '/api/wallet/topup') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const body = await readJson(req);
    try {
      const result = holder.wallet.topUp({
        userId: session.session.accountId,
        amountCents: body.amountCents,
        idempotencyKey: body.idempotencyKey ?? `topup:${randomUUID()}`,
        channel: 'manual'
      });
      persist();
      sendJson(res, 200, { ok: true, ...result, balance: holder.wallet.balance({ userId: session.session.accountId }) }, holder.rt);
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'TOPUP_FAILED', message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/wallet') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const accountId = session.session.accountId;
    sendJson(res, 200, { account: session.account, balance: holder.wallet.balance({ userId: accountId }), summary: holder.wallet.summary({ userId: accountId }), paidAllowed: holder.accounts.canUsePaidMessaging({ accountId }) }, holder.rt);
    return;
  }

  // 付费发送：先过实名准入，再扣费（扣费失败不得产生消息）
  if (method === 'POST' && pathname === '/api/paid/send') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const accountId = session.session.accountId;
    const body = await readJson(req);
    const gate = holder.accounts.canUsePaidMessaging({ accountId });
    if (!gate.ok) {
      sendJson(res, 403, { error: gate.code, hint: '未实名账号不得收发付费消息（结算主体不可追责）' }, holder.rt);
      return;
    }
    const amountCents = Number(body.amountCents ?? 5000);
    const messageId = body.messageId ?? `msg-paid-${randomUUID()}`;
    holder.metrics.inc(METRIC.PAID_SENT);

    // v0.0.3 审核闸门：**扣费之前**必须先过审核；待审/被拒一律不扣费、不产生消息
    let moderationCaseId = holder.moderationCases.get(messageId) ?? null;
    if (moderationCaseId === null) {
      try {
        const created = holder.moderation.submit({
          messageId,
          senderUserId: accountId,
          content: `${body.topic ?? '付费消息'} ${body.summary ?? ''}`.trim() || '（无正文）',
          channel: 'PAID_MESSAGE'
        });
        moderationCaseId = created.caseId;
        holder.moderationCases.set(messageId, moderationCaseId);
        const verdictMetric =
          created.verdict === 'PASS' ? METRIC.MODERATION_PASS : created.verdict === 'FLAG' ? METRIC.MODERATION_FLAG : METRIC.MODERATION_BLOCK;
        holder.metrics.inc(verdictMetric);
      } catch (error) {
        sendJson(res, 400, { error: error?.code ?? 'MODERATION_FAILED', message: String(error?.message ?? error) }, holder.rt);
        return;
      }
    }
    const deliverable = holder.moderation.canDeliver({ caseId: moderationCaseId });
    if (!deliverable.ok) {
      const moderationCase = holder.moderation.get(moderationCaseId);
      persist();
      // HUMAN_PENDING → 202（等审核）；REJECTED/APPEAL_UPHELD → 403（可申诉）
      const status = moderationCase.state === 'HUMAN_PENDING' ? 202 : 403;
      sendJson(res, status, {
        error: moderationCase.state,
        caseId: moderationCaseId,
        verdict: moderationCase.verdict,
        hint: status === 202 ? '已进入人审队列，审核通过前不扣费' : '审核未通过；可申诉，改判后才可投递与扣费'
      }, holder.rt);
      return;
    }

    try {
      const charge = holder.wallet.chargeForMessage({ userId: accountId, messageId, amountCents });
      if (charge.duplicate !== true) holder.metrics.inc(METRIC.CHARGE);
      holder.world.state.messages.push({
        id: messageId,
        senderUserId: accountId,
        receiverUserId: String(body.receiverUserId ?? USERS.ZHOU),
        topic: String(body.topic ?? '付费消息'),
        summary: String(body.summary ?? ''),
        billingMode: 'SEND_PAY',
        amountCents,
        state: 'PAID',
        screening: 'PASS',
        createdAt: new Date().toISOString()
      });
      persist();
      sendJson(res, 200, { ok: true, messageId, charged: charge, balance: holder.wallet.balance({ userId: accountId }) }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'SEND_FAILED';
      sendJson(res, code === 'INSUFFICIENT_FUNDS' ? 402 : 400, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  // 演示用：接收方回复（把消息推进到 REPLIED；真实系统由会话写入驱动）
  if (method === 'POST' && pathname === '/api/paid/reply') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const body = await readJson(req);
    const message = holder.world.state.messages.find((item) => item.id === body.messageId);
    if (!message) {
      sendJson(res, 404, { error: 'NOT_FOUND', messageId: body.messageId ?? null }, holder.rt);
      return;
    }
    message.state = 'REPLIED';
    holder.metrics.inc(METRIC.VIEWED);
    holder.metrics.inc(METRIC.REPLIED);
    persist();
    sendJson(res, 200, { ok: true, messageId: message.id, state: message.state }, holder.rt);
    return;
  }

  if (method === 'POST' && pathname === '/api/paid/refund') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const body = await readJson(req);
    try {
      const result = holder.wallet.refund({ userId: session.session.accountId, messageId: String(body.messageId ?? ''), reason: String(body.reason ?? '用户申请退款') });
      if (result.duplicate !== true) holder.metrics.inc(METRIC.REFUND);
      persist();
      sendJson(res, 200, { ok: true, ...result, balance: holder.wallet.balance({ userId: session.session.accountId }) }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'REFUND_FAILED';
      sendJson(res, code === 'REFUND_WINDOW_CLOSED' ? 409 : 400, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/sms') {
    if (!requireAccess(req, res, holder)) return;
    sendJson(res, 200, { provider: holder.sms.provider.info(), delivery: holder.sms.delivery.stats() }, holder.rt);
    return;
  }

  if (method === 'POST' && pathname === '/api/dispute/open') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const body = await readJson(req);
    try {
      const dispute = holder.disputes.open({
        messageId: String(body.messageId ?? ''),
        userId: session.session.accountId,
        reason: String(body.reason ?? ''),
        evidence: Array.isArray(body.evidence) ? body.evidence : []
      });
      holder.metrics.inc(METRIC.DISPUTE_OPEN);
      persist();
      sendJson(res, 200, { ok: true, dispute }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'DISPUTE_FAILED';
      sendJson(res, code === 'NOT_ELIGIBLE' ? 409 : code === 'DUPLICATE_OPEN' ? 409 : 400, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'POST' && pathname === '/api/dispute/resolve') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const body = await readJson(req);
    try {
      const dispute = holder.disputes.resolve({
        disputeId: String(body.disputeId ?? ''),
        actorUserId: session.session.accountId,
        decision: String(body.decision ?? '').toUpperCase(),
        note: body.note ?? null
      });
      holder.metrics.inc(dispute.state === 'APPROVED' ? METRIC.DISPUTE_APPROVE : METRIC.DISPUTE_REJECT);
      if (dispute.state === 'APPROVED') holder.metrics.inc(METRIC.REFUND);
      persist();
      sendJson(res, 200, { ok: true, dispute, timeline: holder.disputes.timeline({ messageId: dispute.messageId }) }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'RESOLVE_FAILED';
      sendJson(res, code === 'FORBIDDEN' ? 403 : code === 'NOT_FOUND' ? 404 : 409, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/dispute/timeline') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const messageId = url.searchParams.get('messageId') ?? '';
    sendJson(res, 200, { messageId, timeline: holder.disputes.timeline({ messageId }), disputes: holder.disputes.list({}) }, holder.rt);
    return;
  }

  // ── v0.0.3 审核链（机审 → 人审 → 申诉）──
  if (method === 'POST' && pathname === '/api/moderation/submit') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const body = await readJson(req);
    try {
      const moderationCase = holder.moderation.submit({
        messageId: String(body.messageId ?? `msg-manual-${randomUUID()}`),
        senderUserId: session.session.accountId,
        content: String(body.content ?? '')
      });
      holder.moderationCases.set(moderationCase.messageId, moderationCase.caseId);
      persist();
      sendJson(res, 200, { ok: true, case: moderationCase, deliverable: holder.moderation.canDeliver({ caseId: moderationCase.caseId }) }, holder.rt);
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'MODERATION_FAILED', message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'POST' && pathname === '/api/moderation/review') {
    const role = requireAccess(req, res, holder, { write: true });
    if (!role) return;
    const body = await readJson(req);
    try {
      const moderationCase = holder.moderation.review({
        caseId: String(body.caseId ?? ''),
        actorUserId: `operator-${role}`,
        decision: String(body.decision ?? '').toUpperCase(),
        note: body.note ?? null
      });
      holder.metrics.inc(moderationCase.state === 'APPROVED' ? METRIC.MODERATION_APPROVE : METRIC.MODERATION_REJECT);
      persist();
      sendJson(res, 200, { ok: true, case: moderationCase, deliverable: holder.moderation.canDeliver({ caseId: moderationCase.caseId }) }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'REVIEW_FAILED';
      sendJson(res, code === 'NOT_FOUND' ? 404 : 409, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'POST' && pathname === '/api/moderation/appeal') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const body = await readJson(req);
    try {
      const moderationCase = holder.moderation.appeal({
        caseId: String(body.caseId ?? ''),
        userId: session.session.accountId,
        reason: String(body.reason ?? '')
      });
      persist();
      sendJson(res, 200, { ok: true, case: moderationCase }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'APPEAL_FAILED';
      sendJson(res, code === 'NOT_FOUND' ? 404 : code === 'FORBIDDEN' ? 403 : 409, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'POST' && pathname === '/api/moderation/appeal/resolve') {
    const role = requireAccess(req, res, holder, { write: true });
    if (!role) return;
    const body = await readJson(req);
    try {
      const moderationCase = holder.moderation.resolveAppeal({
        caseId: String(body.caseId ?? ''),
        actorUserId: `appeal-reviewer-${role}`,
        decision: String(body.decision ?? '').toUpperCase(),
        note: body.note ?? null
      });
      persist();
      sendJson(res, 200, { ok: true, case: moderationCase, deliverable: holder.moderation.canDeliver({ caseId: moderationCase.caseId }) }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'APPEAL_RESOLVE_FAILED';
      sendJson(res, code === 'NOT_FOUND' ? 404 : 409, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/moderation') {
    if (!requireAccess(req, res, holder)) return;
    sendJson(res, 200, { cases: holder.moderation.list({}), stats: holder.moderation.stats() }, holder.rt);
    return;
  }

  // ── v0.0.4 可观测性：指标 / 告警 / 全链追溯 ──
  if (method === 'GET' && pathname === '/api/ops/metrics') {
    if (!requireAccess(req, res, holder)) return;
    sendJson(res, 200, holder.metrics.snapshot(), holder.rt);
    return;
  }

  if (method === 'GET' && pathname === '/api/ops/alerts') {
    if (!requireAccess(req, res, holder)) return;
    const snapshot = holder.metrics.snapshot();
    const deliveryStats = holder.sms.delivery.stats();
    const otpAttempts = (deliveryStats.sent ?? 0) + (deliveryStats.failed ?? 0);
    const gauges = {
      humanReviewBacklog: holder.moderation.list({ state: 'HUMAN_PENDING' }).length,
      smsFailureRate: otpAttempts === 0 ? 0 : (deliveryStats.failed ?? 0) / otpAttempts,
      reconcileMismatch: 0
    };
    const alerts = evaluateAlerts({ snapshot, gauges });
    sendJson(res, 200, { alerts, gauges, generatedAt: new Date().toISOString() }, holder.rt);
    return;
  }

  if (method === 'GET' && pathname === '/api/ops/trace') {
    if (!requireAccess(req, res, holder)) return;
    const messageId = url.searchParams.get('messageId');
    if (!messageId) {
      sendJson(res, 400, { error: 'MESSAGE_ID_REQUIRED', hint: 'GET /api/ops/trace?messageId=<id>' }, holder.rt);
      return;
    }
    sendJson(res, 200, holder.trace.trace({ messageId }), holder.rt);
    return;
  }

  // ── v0.0.5 内测保障：熔断状态 / 备份与恢复 ──
  if (method === 'GET' && pathname === '/api/ops/resilience') {
    if (!requireAccess(req, res, holder)) return;
    sendJson(res, 200, {
      sms: holder.resilience.smsGuard.stats(),
      moderation: holder.resilience.machineGuard.stats()
    }, holder.rt);
    return;
  }

  if (method === 'POST' && pathname === '/api/ops/backup') {
    if (!requireAccess(req, res, holder, { write: true })) return;
    const body = await readJson(req);
    try {
      // 备份前先把当前状态写入 store（memory 存储下 persist() 会跳过，但备份必须能读到快照）
      holder.store.write(serialize(holder.world, { accounts: holder.accounts.exportState(), payments: holder.payments.exportState() }));
      const created = holder.backup.create({ label: body.label ?? null });
      sendJson(res, 200, { ok: true, backup: created }, holder.rt);
    } catch (error) {
      sendJson(res, 500, { error: error?.code ?? 'BACKUP_FAILED', message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/ops/backup') {
    if (!requireAccess(req, res, holder)) return;
    sendJson(res, 200, { backups: holder.backup.list() }, holder.rt);
    return;
  }

  if (method === 'POST' && pathname === '/api/ops/backup/restore') {
    if (!requireAccess(req, res, holder, { write: true })) return;
    const body = await readJson(req);
    try {
      const restored = holder.backup.restore({ file: String(body.file ?? '') });
      // v0.1.3：验签通过 ≠ 能用。备份 schema 与当前不一致时必须显式拒绝，
      // 否则用户会拿到一份"校验通过但加载即失败"的备份（修 bug：verified-but-unusable）。
      const backupSchema = restored.data?.schemaVersion;
      if (backupSchema !== VERSION_SCHEMA) {
        sendJson(res, 409, {
          error: 'SCHEMA_MISMATCH',
          restored: false,
          backupSchema: backupSchema ?? null,
          currentSchema: VERSION_SCHEMA,
          needsMigration: true,
          hint: `该备份是 schema=${String(backupSchema ?? '缺失')}，当前需要 ${VERSION_SCHEMA}；先对备份文件执行 node scripts/migrate-state.mjs --file <backup.json> 再恢复`
        }, holder.rt);
        return;
      }
      // restore 只返回可验签的数据；**不自动应用**（切换线上状态必须由人决定）
      sendJson(res, 200, {
        ok: true,
        applied: false,
        note: 'restore 仅返回并校验备份数据，未改动当前运行状态；应用需人工确认（见 docs/25 §4）',
        meta: restored.meta,
        summary: {
          agents: Array.isArray(restored.data?.agents) ? restored.data.agents.length : 0,
          tickets: Array.isArray(restored.data?.tickets) ? restored.data.tickets.length : 0,
          messages: Array.isArray(restored.data?.messages) ? restored.data.messages.length : 0,
          audit: Array.isArray(restored.data?.audit) ? restored.data.audit.length : 0
        }
      }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'RESTORE_FAILED';
      sendJson(res, code === 'NOT_FOUND' ? 404 : 409, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  // ── v0.0.7 Web/PWA 客户端静态资源 ──
  if (method === 'GET' && (pathname === '/app' || pathname.startsWith('/app/'))) {
    const relative = pathname === '/app' || pathname === '/app/' ? 'app/index.html' : pathname.slice(1);
    const filePath = path.resolve(PUBLIC_DIR, relative);
    const appRoot = path.resolve(PUBLIC_DIR, 'app');
    // 目录穿越防护：解析后的绝对路径必须仍在 public/app 下
    if (!filePath.startsWith(appRoot + path.sep) && filePath !== path.join(appRoot, 'index.html')) {
      sendJson(res, 404, { error: 'NOT_FOUND' }, holder.rt);
      return;
    }
    try {
      const content = await readFile(filePath);
      const ext = path.extname(filePath).toLowerCase();
      const types = {
        '.html': 'text/html; charset=utf-8',
        '.css': 'text/css; charset=utf-8',
        '.js': 'text/javascript; charset=utf-8',
        '.mjs': 'text/javascript; charset=utf-8',
        '.json': 'application/json; charset=utf-8',
        '.webmanifest': 'application/manifest+json; charset=utf-8',
        '.svg': 'image/svg+xml'
      };
      res.writeHead(200, {
        'content-type': types[ext] ?? 'application/octet-stream',
        'cache-control': ext === '.html' ? 'no-store' : 'public, max-age=300',
        'service-worker-allowed': '/app/'
      });
      res.end(content);
    } catch {
      sendJson(res, 404, { error: 'NOT_FOUND', path: pathname }, holder.rt);
    }
    return;
  }

  // ── v0.0.8 多节点广播状态 ──
  if (method === 'GET' && pathname === '/api/ops/bus') {
    if (!requireAccess(req, res, holder)) return;
    sendJson(res, 200, { bus: holder.bus.stats(), peers: busPeers }, holder.rt);
    return;
  }

  // ── v0.0.9 支付渠道与对账 ──
  if (method === 'POST' && pathname === '/api/pay/intent') {
    const session = requireSession(req, res, holder);
    if (session === null) return;
    const body = await readJson(req);
    try {
      const order = holder.payments.createIntent({
        userId: session.session.accountId,
        amountCents: Number(body.amountCents ?? 0),
        channel: body.channel ?? 'mock',
        orderId: body.orderId ?? null,
        description: body.description ?? null
      });
      persist(); // v0.1.2：下单必须落盘，否则重启丢订单（订单是资金链起点）
      sendJson(res, 200, { ok: true, order }, holder.rt);
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'INTENT_FAILED', message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  // 渠道回调：按**原始字节**验签；验签失败绝不入账
  if (method === 'POST' && pathname === '/api/pay/callback') {
    const raw = await readRawText(req);
    try {
      const result = holder.payments.handleCallback({
        channel: url.searchParams.get('channel') ?? 'mock',
        headers: req.headers,
        body: raw
      });
      persist();
      sendJson(res, 200, { ok: true, ...result }, holder.rt);
    } catch (error) {
      const code = error?.code ?? 'CALLBACK_FAILED';
      const status =
        code === 'SIGNATURE_INVALID' || code === 'TIMESTAMP_EXPIRED'
          ? 401
          : code === 'AMOUNT_MISMATCH' || code === 'CALLBACK_REPLAYED' || code === 'ILLEGAL_STATE'
            ? 409
            : code === 'ORDER_NOT_FOUND'
              ? 404
              : 400;
      sendJson(res, status, { error: code, message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/pay/orders') {
    if (!requireAccess(req, res, holder)) return;
    sendJson(res, 200, { orders: holder.payments.list({}), stats: holder.payments.stats() }, holder.rt);
    return;
  }

  if (method === 'GET' && pathname === '/api/ops/reconcile') {
    if (!requireAccess(req, res, holder)) return;
    const date = url.searchParams.get('date') ?? null;
    try {
      const result = holder.reconciler.run({ date });
      sendJson(res, 200, { ...result, report: holder.reconciler.report({ date }) }, holder.rt);
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'RECONCILE_FAILED', message: String(error?.message ?? error) }, holder.rt);
    }
    return;
  }

  sendJson(res, 404, { error: 'NOT_FOUND', path: pathname }, holder.rt);
}

async function readJson(req) {
  const text = await readRawText(req);
  return text.trim() === '' ? {} : JSON.parse(text);
}

/** 读取原始请求体（支付回调必须按原始字节验签，不能先解析再序列化） */
async function readRawText(req) {
  const chunks = [];
  let total = 0;
  for await (const chunk of req) {
    total += chunk.length;
    if (total > 64 * 1024) throw new Error('请求体超过 64KB');
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString('utf8');
}

/** 从 Authorization: Bearer <token> 取会话令牌（产品版用户态；运维面仍用 x-admin-token） */
function bearerToken(req) {
  const header = req.headers.authorization;
  if (typeof header !== 'string' || !header.startsWith('Bearer ')) return null;
  const token = header.slice(7).trim();
  return token === '' ? null : token;
}

/** @returns {{account, session}|null} 失败时已写响应 */
function requireSession(req, res, holder) {
  const accessToken = bearerToken(req);
  if (accessToken === null) {
    sendJson(res, 401, { error: 'SESSION_INVALID', hint: '需要 Authorization: Bearer <accessToken>' }, holder.rt);
    return null;
  }
  try {
    return holder.accounts.authenticate({ accessToken });
  } catch (error) {
    sendJson(res, 401, { error: error?.code ?? 'SESSION_INVALID', message: String(error?.message ?? error) }, holder.rt);
    return null;
  }
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
    // 总线先于 HTTP 启动：多节点模式必须先监听广播端口，才能接收对端事件
    app.holder.bus
      .start()
      .then(() => {
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
            close: () =>
              new Promise((done) => {
                app.server.close(async () => {
                  try {
                    await app.holder.bus.close();
                  } catch {
                    // 总线关闭失败不应阻塞进程退出
                  }
                  done();
                });
              })
          });
        });
      })
      .catch(reject);
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
