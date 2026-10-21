/**
 * 模块职责：人机混合 IM 实时演示服务 —— 在「demo 治理世界」之上加一层真实可用的聊天产品面
 * 底层架构：DSH + W2M（relay 负责 presence/任务派发，演示服务复用 agent-im 的治理层与付费通道）
 * 契约来源：contracts/INTERFACES.md v1.0；四类治理语义由 flow.mjs / orchestrator / payment 提供，本文件只做组合
 *
 * 设计边界（诚实）：
 *   · 聊天线程与消息是**演示态内存存储**（重启即回到种子场景），不是生产消息库；
 *   · Agent 的「思考」由已冻结的协商/预审/审批模块确定性执行，不调用大模型；
 *   · fleet 面板默认模拟；配置 W2M_OPERATOR_TOKEN / W2M_DEVICE_TOKEN 后显示并操作真实 relay。
 *
 * 路由：
 *   GET  /                               IM 客户端（浏览器）
 *   GET  /api/state?user=&thread=        会话列表 + 当前会话消息 + 审批单 + Agent 团队 + 信任矩阵 + fleet + 自检
 *   POST /api/message                    { userId, threadId, text }
 *   POST /api/approvals/:id/decide       { action: APPROVE|MODIFY|REJECT, comment, patch }
 *   GET  /api/fleet                      真实 relay 健康/在线设备（未配置时为 simulated）
 *   POST /api/fleet/probe                { machineId, argv? } 在真实机器上执行一次（默认 node --version）
 *   POST /api/reset                      回到种子场景
 */
import { createServer } from 'node:http';
import { createServer as createHttpsServer } from 'node:https';
import { readFileSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID } from 'node:crypto';

import {
  USERS,
  AGENTS,
  USER_PROFILES,
  createWorld,
  seedScenario,
  snapshot,
  decideTicket,
  probeChecks
} from '../demo-server/flow.mjs';
import { TRUST_LEVELS } from '../../services/orchestrator/src/trust-zone.mjs';
import { AuditLog } from '../../services/audit/src/audit-log.mjs';
import { createLlm } from './llm.mjs';
import { createSkillExecutor } from './skills.mjs';
import { createAgentRuntime } from './agent-runtime.mjs';
import { createPersistence, defaultDbPath } from './store.mjs';
import { createAuth } from './auth.mjs';
import { TICKET_STATE } from '../../services/orchestrator/src/approval-engine.mjs';
import { createV2Layer, AGENT_PROFILES } from './v2.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const PUBLIC_DIR = path.join(HERE, 'public');
const MAX_BODY = 64 * 1024;

const FLEET = Object.freeze({
  relayUrl: (process.env.W2M_RELAY_URL ?? 'http://202.182.123.154:8787').replace(/\/$/, ''),
  operatorToken: process.env.W2M_OPERATOR_TOKEN ?? null,
  deviceToken: process.env.W2M_DEVICE_TOKEN ?? null
});

const PERSONA_ORDER = Object.freeze([USERS.LIN, USERS.ZHOU, USERS.MOM]);

// ─────────────────────────── 聊天演示世界 ───────────────────────────
function buildDemo(llm, skills, persistence = null) {
  const world = createWorld();
  seedScenario(world);
  world.checks = probeChecks();
  const restoredAudit = persistence === null ? [] : persistence.loadAudit();

  const demo = {
    world,
    authEnabled: false,
    seq: 0,
    threads: [],
    events: [],
    auditLog: new AuditLog({ clock: world.clock, records: restoredAudit }),
    seq: persistence === null ? 0 : persistence.loadSeq(),
    llm,
    skills,
    persistence,
    runtime: createAgentRuntime({ llm, guard: world.egress, registry: world.registry, trustMatrix: world.trustMatrix, skills, capabilityGate: world.gate })
  };
  demo.threads = [
    {
      id: 'th-lin-mom',
        kind: 'family',
        title: '家庭群 · 妈妈',
        members: [USERS.LIN, USERS.MOM],
        messages: [
          makeMessage(demo, USERS.MOM, '周末回来吃饭吗？我炖了汤。', 'text', '2026-10-21T09:40:00.000Z'),
          makeMessage(demo, AGENTS.LIN_FAMILY, '已为你标记：周六 12:00 家庭聚餐（未与公司日程冲突）。', 'agent', '2026-10-21T09:41:00.000Z')
        ]
      },
      {
        id: 'th-lin-zhou',
        kind: 'stranger',
        title: '周先生 · 陌生人询盘',
        members: [USERS.LIN, USERS.ZHOU],
        messages: [
          makeMessage(demo, AGENTS.ZHOU_BUTLER, '我的委托人周先生希望就 A 轮融资交流 20 分钟。已提交来意说明，等待对方 Agent 预审。', 'agent', '2026-10-21T09:30:00.000Z'),
          makeMessage(demo, AGENTS.LIN_FILTER, '预审通过（工作/融资类），已生成审批卡：通过后对方扣费 ¥50 并进入你的会话。', 'agent', '2026-10-21T09:32:00.000Z')
        ]
      },
      {
        id: 'th-lin-agent',
        kind: 'agent',
        title: '我的 Agent 团队',
        members: [USERS.LIN],
        messages: [
          makeMessage(demo, AGENTS.LIN_BUTLER, '今日呈报：2 项待审批（陌生人询盘、人体工学椅采购 ¥2399）。', 'agent', '2026-10-21T09:35:00.000Z')
        ]
      }
  ];
  if (persistence !== null && persistence.hasSeed()) {
    demo.threads = persistence.loadThreads().map((thread) => ({ ...thread, messages: persistence.loadMessages(thread.id) }));
    demo.seq = persistence.loadSeq();
    attachV2(demo);
    return demo;
  }
  // 种子审计：已发生的协商/预审/建单在链上留痕（重启从种子重建，链同样从创世开始）
  recordAudit(demo, { source: 'negotiation-engine', action: 'NEGOTIATION_COMPLETED', actor: AGENTS.ZHOU_BUTLER, traceId: 'trace-stranger-001', detail: { rounds: 2 } });
  recordAudit(demo, { source: 'approval-engine', action: 'TICKET_CREATED', actor: AGENTS.LIN_FILTER, target: String(world.ids.ticketStranger ?? ''), traceId: 'trace-stranger-001', decision: 'PENDING', detail: { channel: 'im' } });
  if (persistence !== null) {
    for (const thread of demo.threads) {
      persistence.saveThread(thread);
      for (const message of thread.messages) persistence.appendMessage(thread.id, message);
    }
    for (const record of demo.auditLog.snapshot()) persistence.appendAudit(record);
    persistence.saveSeq(demo.seq);
  }
  attachV2(demo);
  return demo;
}

/**
 * 挂载 v2 层（身份/网关/可见域/频道/会话/签名审计）。
 * 签名审计链以 v1 哈希链的「链尾」作为迁移锚点：v1 历史不回签，但连续性有证据。
 */
function attachV2(demo) {
  try {
    demo.v2 = createV2Layer({
      world: demo.world,
      legacyHead: demo.auditLog.head(),
      legacyLength: demo.auditLog.size()
    });
  } catch (error) {
    // v2 模块缺失时不让整个 Demo 起不来：显式降级并在状态里标记，绝不假装 v2 可用。
    demo.v2 = null;
    demo.v2Error = String(error?.message ?? error);
  }
  return demo;
}

function recordAudit(demo, entry) {
  const record = demo.auditLog.append(entry);
  if (demo.persistence) demo.persistence.appendAudit(record);
  demo.events.push({ at: record.at, type: 'AUDIT', action: record.action, seq: record.seq });
  return record;
}

function pushMessage(demo, thread, message) {
  thread.messages.push(message);
  if (demo.persistence) demo.persistence.appendMessage(thread.id, message);
  return message;
}

function makeMessage(demo, from, text, kind = 'text', at = null, extra = {}) {
  demo.seq += 1;
  return Object.freeze({
    id: `msg-${demo.seq}`,
    at: at ?? new Date().toISOString(),
    from,
    text,
    kind,
    ...extra
  });
}

function threadOf(demo, threadId) {
  return demo.threads.find((thread) => thread.id === threadId) ?? null;
}

function threadIdFor(a, b) {
  const pair = [a, b].sort().join('|');
  return demoThreadIds[pair] ?? null;
}
const demoThreadIds = Object.freeze({
  [[USERS.LIN, USERS.MOM].sort().join('|')]: 'th-lin-mom',
  [[USERS.LIN, USERS.ZHOU].sort().join('|')]: 'th-lin-zhou'
});

function pendingTickets(demo) {
  return demo.world.approvals
    .listPending(USERS.LIN)
    .map((ticket) => ({
      id: ticket.id,
      state: ticket.state,
      type: ticket.ticketType,
      priority: ticket.priority,
      createdAt: ticket.createdAt,
      deadline: ticket.deadline,
      title: ticket.payload?.title ?? ticket.payload?.kind ?? ticket.ticketType,
      amountCents: ticket.payload?.amountCents ?? null,
      decisionHint: ticket.payload?.decisionHint ?? null,
      payload: ticket.payload
    }))
    .sort((a, b) => String(b.createdAt).localeCompare(String(a.createdAt)));
}

function fleetMode() {
  return FLEET.operatorToken !== null ? 'live' : 'simulated';
}

async function relayGet(pathname, token) {
  const response = await fetch(FLEET.relayUrl + pathname, {
    headers: token ? { authorization: `Bearer ${token}` } : {},
    signal: AbortSignal.timeout(4000)
  });
  const body = await response.json().catch(() => ({}));
  return { status: response.status, body };
}

async function fleetSnapshot() {
  if (FLEET.operatorToken === null) {
    return {
      mode: 'simulated',
      relayUrl: FLEET.relayUrl,
      note: '未配置 W2M_OPERATOR_TOKEN/W2M_DEVICE_TOKEN：fleet 面板为模拟数据。设置环境变量后即接真实 relay。',
      devices: [
        { machine_id: 'sim-macmini', name: 'macmini', platform: 'macos', online: true },
        { machine_id: 'sim-win', name: 'win-desktop', platform: 'windows', online: true },
        { machine_id: 'sim-linux', name: 'jp-shared', platform: 'linux', online: false }
      ]
    };
  }
  try {
    const health = await relayGet('/healthz', null);
    const members = FLEET.deviceToken !== null ? await relayGet('/v1/fleet/members', FLEET.deviceToken) : null;
    const rows = Array.isArray(members?.body?.members) ? members.body.members : [];
    return {
      mode: 'live',
      relayUrl: FLEET.relayUrl,
      relay: { ok: health.status === 200, status: health.status, relayId: health.body?.relay_id ?? null, devices: health.body?.devices ?? null },
      devices: rows.map((member) => ({
        machine_id: member.machine_id ?? null,
        name: member.name ?? member.machine_id ?? 'machine',
        role: member.role ?? null,
        online: member.online === true
      }))
    };
  } catch (error) {
    return { mode: 'live', relayUrl: FLEET.relayUrl, error: String(error?.message ?? error), devices: [] };
  }
}

async function dispatchProbe(machineId, argv) {
  if (FLEET.operatorToken === null) return { ok: false, reason: 'FLEET_NOT_CONFIGURED', detail: '设置 W2M_OPERATOR_TOKEN 后可用真实 fleet' };
  const created = await fetch(FLEET.relayUrl + '/v1/task', {
    method: 'POST',
    headers: { authorization: `Bearer ${FLEET.operatorToken}`, 'content-type': 'application/json' },
    body: JSON.stringify({
      mode: 'broadcast',
      executor_machine_id: machineId,
      command_argv: argv,
      cwd_rel: '.',
      index_total: 1,
      timeout_ms: 60000,
      write: false
    }),
    signal: AbortSignal.timeout(8000)
  }).then(async (response) => ({ status: response.status, body: await response.json().catch(() => ({})) }));
  const taskId = created.body?.task_id ?? null;
  if (taskId === null) return { ok: false, reason: 'DISPATCH_REFUSED', status: created.status, body: created.body };
  if (FLEET.deviceToken === null) return { ok: true, taskId, status: 'dispatched', note: '没有 W2M_DEVICE_TOKEN，无法读取执行输出' };
  for (let attempt = 0; attempt < 12; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 1500));
    const read = await relayGet(`/v1/tasks/${encodeURIComponent(taskId)}`, FLEET.deviceToken);
    const mine = (read.body?.aggregate?.machines ?? []).find((machine) => machine.machine_id === machineId) ?? null;
    const outcome = mine?.outcome ?? null;
    if (outcome !== null && outcome !== 'pending' && outcome !== 'observing') {
      return { ok: true, taskId, status: read.body?.task?.status ?? null, outcome, exitCode: mine?.exit_code ?? null, stdout: mine?.stdout_head ?? null };
    }
  }
  return { ok: true, taskId, status: 'pending', note: '任务已派发，执行结果在轮询窗口内未返回' };
}

// ─────────────────────────── HTTP 服务 ───────────────────────────
export function createApp({ fleet = null, llm = null, skills = null, db = undefined, auth = null } = {}) {
  const authClient = auth ?? createAuth();
  const persistence = typeof db === 'string' ? createPersistence({ dbPath: db }) : (db ?? createPersistence());
  const ownsPersistence = !(db !== undefined && typeof db !== 'string');
  const llmClient = llm ?? createLlm();
  const skillExecutor = skills ?? createSkillExecutor({
    relayUrl: FLEET.relayUrl,
    operatorToken: FLEET.operatorToken,
    deviceToken: FLEET.deviceToken,
    dispatchProbe: (machineId, argv) => (fleet !== null && typeof fleet === 'function' ? fleet({ probe: true, machineId, argv }) : dispatchProbe(machineId, argv))
  });
  const holder = { demo: buildDemo(llmClient, skillExecutor, persistence), auth: authClient };
  const fleetOverride = fleet;
  const handler = async (req, res) => {
    try {
      await route(req, res, holder, fleetOverride);
    } catch (error) {
      sendJson(res, 500, { error: 'INTERNAL', message: String(error?.message ?? error) });
    }
  };
  const server = createServer(handler);
  return {
    server,
    handler,
    holder,
    persistence,
    getState: () => stateFor(holder.demo, USERS.LIN, 'th-lin-agent'),
    closePersistence: () => { if (ownsPersistence) persistence.close(); }
  };
}

/** 读取 TLS 证书/私钥；缺失或不可读时抛具名错误（不静默降级为 http）。 */
export function loadTlsOptions({ certPath, keyPath } = {}) {
  if (typeof certPath !== 'string' || typeof keyPath !== 'string' || certPath === '' || keyPath === '') {
    throw new Error('TLS_CONFIG_MISSING: 需要同时提供 IM_TLS_CERT 与 IM_TLS_KEY（PEM 文件路径）');
  }
  try {
    const cert = readFileSync(certPath, 'utf8');
    const key = readFileSync(keyPath, 'utf8');
    if (!cert.includes('-----BEGIN CERTIFICATE-----') || !key.includes('PRIVATE KEY')) {
      throw new Error('证书或私钥不是 PEM 格式（缺少 BEGIN CERTIFICATE / PRIVATE KEY 标记）');
    }
    return { cert, key };
  } catch (error) {
    throw new Error('TLS_CONFIG_MISSING: 无法读取证书/私钥：' + String(error?.message ?? error));
  }
}

export async function startServer({ port = 8797, host = '127.0.0.1', fleet = null, llm = null, skills = null, db = undefined, auth = null, tls = null } = {}) {
  const app = createApp({ fleet, llm, skills, db, auth });
  const server = tls === null ? app.server : createHttpsServer(loadTlsOptions(tls), app.handler);
  const scheme = tls === null ? 'http' : 'https';
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, host, () => {
      const address = server.address();
      resolve({
        ...app,
        server,
        tls: tls !== null,
        host,
        port: address.port,
        url: `${scheme}://${host}:${address.port}`,
        close: () => new Promise((done) => server.close(() => { app.closePersistence(); done(); }))
      });
    });
  });
}

async function route(req, res, holder, fleetOverride) {
  const url = new URL(req.url ?? '/', `http://${req.headers.host ?? 'localhost'}`);
  const { pathname, searchParams } = url;
  const method = (req.method ?? 'GET').toUpperCase();

  if (method === 'GET' && (pathname === '/' || pathname === '/index.html')) {
    const html = await readFile(path.join(PUBLIC_DIR, 'index.html'), 'utf8');
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
    res.end(html);
    return;
  }

  // ── 鉴权面：登录 / 登出 / 会话查询（公开），其余 API 需要会话 ──
  if (method === 'GET' && pathname === '/api/session') {
    const session = holder.auth.enabled ? holder.auth.parse(req.headers.cookie) : null;
    sendJson(res, 200, { authEnabled: holder.auth.enabled, user: session?.ok === true ? session.user : null, users: holder.auth.usersPublic() });
    return;
  }
  if (method === 'POST' && pathname === '/api/login') {
    if (!holder.auth.enabled) { sendJson(res, 400, { error: 'AUTH_DISABLED', hint: 'IM_AUTH=1 或设置 IM_SESSION_SECRET 后启用登录' }); return; }
    const body = await readJson(req);
    const userId = typeof body?.userId === 'string' ? body.userId : '';
    if (userId === '' || holder.auth.verifyPassword(userId, String(body?.password ?? '')) !== true) {
      sendJson(res, 401, { error: 'WRONG_CREDENTIALS' });
      return;
    }
    const session = holder.auth.issue(userId);
    res.setHeader('set-cookie', holder.auth.cookieFor(session));
    sendJson(res, 200, { ok: true, user: { userId: session.userId, name: session.name }, expiresAt: session.expiresAt });
    return;
  }
  if (method === 'POST' && pathname === '/api/logout') {
    res.setHeader('set-cookie', holder.auth.clearCookie());
    sendJson(res, 200, { ok: true });
    return;
  }

  const session = holder.auth.enabled ? holder.auth.parse(req.headers.cookie) : { ok: true, user: null };
  if (holder.auth.enabled && session.ok !== true) {
    sendJson(res, 401, { error: 'UNAUTHENTICATED', reason: session.reason ?? null });
    return;
  }
  const sessionUserId = holder.auth.enabled && session.ok === true ? session.user.userId : null;

  if (method === 'GET' && pathname === '/api/state') {
    const user = sessionUserId ?? normalizeUser(searchParams.get('user'));
    const threadId = searchParams.get('thread') ?? defaultThreadFor(user);
    const st = stateFor(holder.demo, user, threadId);
    st.auth = { enabled: holder.auth.enabled, user: sessionUserId === null ? null : session.user };
    sendJson(res, 200, st);
    return;
  }

  if (method === 'POST' && pathname === '/api/message') {
    const body = await readJson(req);
    const result = await sendMessage(holder.demo, { ...body, userId: sessionUserId ?? body?.userId });
    sendJson(res, result.ok ? 200 : 400, result);
    return;
  }

  const decideMatch = pathname.match(/^\/api\/approvals\/([^/]+)\/decide$/);
  if (method === 'POST' && decideMatch) {
    const body = await readJson(req);
    const ticketId = decodeURIComponent(decideMatch[1]);
    if (sessionUserId !== null) {
      const owned = holder.demo.world.approvals.listPending(sessionUserId).find((item) => item.id === ticketId) ?? null;
      if (owned === null) { sendJson(res, 403, { error: 'FORBIDDEN', hint: '只能处理自己名下的待审批单' }); return; }
    }
    const result = decide(holder.demo, ticketId, body);
    sendJson(res, result.ok ? 200 : 400, result);
    return;
  }

  if (method === 'GET' && pathname === '/api/fleet') {
    const value = fleetOverride !== null ? fleetOverride() : await fleetSnapshot();
    sendJson(res, 200, value);
    return;
  }

  if (method === 'POST' && pathname === '/api/fleet/probe') {
    const body = await readJson(req);
    const machineId = typeof body.machineId === 'string' ? body.machineId : '';
    const argv = Array.isArray(body.argv) && body.argv.length > 0 ? body.argv.map(String) : ['node', '--version'];
    if (machineId === '') {
      sendJson(res, 400, { ok: false, reason: 'MACHINE_REQUIRED' });
      return;
    }
    const result = fleetOverride !== null ? await fleetOverride({ probe: true, machineId, argv }) : await dispatchProbe(machineId, argv);
    sendJson(res, 200, result);
    return;
  }

  if (method === 'GET' && pathname === '/api/llm') {
    sendJson(res, 200, holder.demo.llm.stats());
    return;
  }

  if (method === 'GET' && pathname === '/api/runtime') {
    sendJson(res, 200, { version: holder.demo.runtime ? 'im-live-agent-runtime/1' : null, stats: holder.demo.runtime?.stats() ?? null, catalog: holder.demo.runtime?.catalog() ?? [], skills: holder.demo.skills?.stats() ?? null });
    return;
  }

  // ═══════════ v2：面向 Agent 的 IM（身份 / 网关 / 可见域 / 频道 / 会话 / 签名审计）═══════════
  if (method === 'GET' && (pathname === '/v2' || pathname === '/api/v2')) {
    const html = await readFile(path.join(PUBLIC_DIR, 'v2.html'), 'utf8');
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
    res.end(html);
    return;
  }

  if (pathname.startsWith('/api/v2') && holder.demo.v2 === null) {
    sendJson(res, 503, { error: 'V2_UNAVAILABLE', message: holder.demo.v2Error ?? 'v2 模块未加载' });
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/agents') {
    const v2 = holder.demo.v2;
    const audit = v2.signedAudit.verify();
    sendJson(res, 200, {
      agents: v2.agentNetwork(),
      gatewayEvents: v2.gateway.events({ limit: 20 }),
      audit: { ok: audit.ok, length: audit.length, chainOk: audit.chain?.ok ?? null, signaturesOk: audit.signatures?.ok ?? null },
      profiles: AGENT_PROFILES
    });
    return;
  }

  const agentActionMatch = pathname.match(/^\/api\/v2\/agents\/([^/]+)\/(connect|disconnect|stream)$/);
  if (agentActionMatch) {
    const agentId = decodeURIComponent(agentActionMatch[1]);
    const action = agentActionMatch[2];

    if (action === 'stream' && method === 'GET') {
      // SSE：Agent 的长连接适配器（生产为 WebSocket/QUIC）。这里证明 transport 抽象可插拔。
      res.writeHead(200, {
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-store',
        connection: 'keep-alive'
      });
      const send = (payload) => {
        if (res.writableEnded === true) throw new Error('SSE 连接已关闭');
        res.write(`data: ${JSON.stringify(payload)}\n\n`);
      };
      send({ event: 'connected', agentId, at: new Date().toISOString() });
      holder.demo.v2.connectAgent({
        agentId,
        subscriptions: [],
        transport: { send, close: () => res.end() }
      });
      const timer = setInterval(() => {
        // 心跳必须主动驱：否则 90s 后 sweepStale 会把在线 Agent 全部置为 OFFLINE
        holder.demo.v2.sessions.heartbeat({ agentId });
        try {
          send({ event: 'heartbeat', at: new Date().toISOString() });
        } catch {
          clearInterval(timer);
        }
      }, 15_000);
      if (typeof timer.unref === 'function') timer.unref();
      req.on('close', () => {
        clearInterval(timer);
        holder.demo.v2.disconnectAgent(agentId);
      });
      return;
    }

    if (action === 'connect' && method === 'POST') {
      const body = await readJson(req);
      const session = holder.demo.v2.connectAgent({
        agentId,
        subscriptions: Array.isArray(body?.subscriptions) ? body.subscriptions.map(String) : []
      });
      sendJson(res, 200, { ok: true, session: { agentId: session.agentId, state: session.state, protocol: session.protocol, subscriptions: session.subscriptions } });
      return;
    }

    if (action === 'disconnect' && method === 'POST') {
      sendJson(res, 200, holder.demo.v2.disconnectAgent(agentId));
      return;
    }
  }

  if (method === 'POST' && pathname === '/api/v2/negotiation/turn') {
    const body = await readJson(req);
    try {
      const result = holder.demo.v2.postAgentTurn({
        channelId: body?.channelId,
        fromAgentId: String(body?.fromAgentId ?? ''),
        intent: String(body?.intent ?? 'PROPOSE'),
        payload: body?.payload ?? {}
      });
      sendJson(res, 200, { ok: true, ...result });
    } catch (error) {
      const code = error?.code ?? 'TURN_FAILED';
      sendJson(res, code === 'GATE_DENIED' || code === 'EGRESS_BLOCKED' ? 403 : 400, { error: code, message: String(error?.message ?? error) });
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/channels') {
    sendJson(res, 200, holder.demo.v2.channelView());
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/visibility') {
    sendJson(res, 200, { matrix: holder.demo.v2.visibilityMatrix() });
    return;
  }

  if (method === 'POST' && pathname === '/api/v2/visibility/probe') {
    const body = await readJson(req);
    try {
      sendJson(res, 200, holder.demo.v2.probeVisibility({ agentId: String(body?.agentId ?? ''), domain: String(body?.domain ?? ''), trustLevel: Number(body?.trustLevel ?? 4), expired: body?.expired === true }));
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'PROBE_FAILED', message: String(error?.message ?? error) });
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/discussions') {
    sendJson(res, 200, holder.demo.v2.discussionView());
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/persona') {
    sendJson(res, 200, holder.demo.v2.personaView());
    return;
  }

  // ── v4：宿主能力（可信上下文 / 旁观静默 / 模型路由 / 轮次内投递）──
  if (method === 'GET' && pathname === '/api/v2/context') {
    try {
      sendJson(res, 200, holder.demo.v2.contextView({ agentId: searchParams.get('agentId') ?? undefined }));
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'CONTEXT_FAILED', message: String(error?.message ?? error) });
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/presence') {
    sendJson(res, 200, holder.demo.v2.presenceView());
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/model-route') {
    sendJson(res, 200, holder.demo.v2.modelRouteView());
    return;
  }

  if (method === 'POST' && pathname === '/api/v2/turn-demo') {
    sendJson(res, 200, holder.demo.v2.midTurnDemo());
    return;
  }

  // ── v5：可信传达（签名信封 / 第三人称转达 / 确认层）──
  if (method === 'GET' && pathname === '/api/v2/envelope') {
    sendJson(res, 200, holder.demo.v2.envelopeView());
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/relay') {
    sendJson(res, 200, holder.demo.v2.relayView());
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/confirmation') {
    sendJson(res, 200, holder.demo.v2.confirmationView());
    return;
  }

  // ── v6：持久邮箱 / 带衰减委托 ──
  if (method === 'GET' && pathname === '/api/v2/mailbox') {
    try {
      sendJson(res, 200, holder.demo.v2.mailboxView());
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'MAILBOX_FAILED', message: String(error?.message ?? error) });
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/delegation') {
    try {
      sendJson(res, 200, holder.demo.v2.delegationView());
    } catch (error) {
      sendJson(res, 400, { error: error?.code ?? 'DELEGATION_FAILED', message: String(error?.message ?? error) });
    }
    return;
  }

  if (method === 'GET' && pathname === '/api/v2/audit') {
    const v2 = holder.demo.v2;
    const verify = v2.signedAudit.verify();
    sendJson(res, 200, {
      verify,
      chain: { legacyHead: null },
      entries: v2.signedAudit.entries().slice(-15).map((entry) => ({
        seq: entry.seq,
        at: entry.at,
        source: entry.source,
        action: entry.action,
        actor: entry.actor,
        decision: entry.decision,
        hash: entry.hash,
        prevHash: entry.prevHash,
        signer: entry.signer ?? null,
        hasSignature: typeof entry.signature === 'string' && entry.signature.length > 0,
        signatureHead: typeof entry.signature === 'string' ? entry.signature.slice(0, 24) : null,
        detail: entry.detail ?? null
      }))
    });
    return;
  }

  if (method === 'POST' && pathname === '/api/v2/audit/tamper') {
    const body = await readJson(req);
    const kind = body?.kind === 'resign' ? 'resign' : 'content';
    sendJson(res, 200, { ok: true, demo: holder.demo.v2.tamperDemo(kind) });
    return;
  }

  if (method === 'POST' && pathname === '/api/reset') {
    holder.demo.persistence?.reset();
    holder.demo = buildDemo(holder.demo.llm, holder.demo.skills, holder.demo.persistence);
    const st = stateFor(holder.demo, sessionUserId ?? USERS.LIN, 'th-lin-agent');
    st.auth = { enabled: holder.auth.enabled, user: sessionUserId === null ? null : session.user };
    sendJson(res, 200, { ok: true, state: st });
    return;
  }

  sendJson(res, 404, { error: 'NOT_FOUND', path: pathname });
}

// ─────────────────────────── 业务动作 ───────────────────────────
function normalizeUser(value) {
  return PERSONA_ORDER.includes(value) ? value : USERS.LIN;
}

function defaultThreadFor(user) {
  if (user === USERS.ZHOU) return 'th-lin-zhou';
  return 'th-lin-agent';
}

function stateFor(demo, user, requestedThread) {
  const world = demo.world;
  const threads = demo.threads
    .filter((thread) => thread.members.includes(user))
    .map((thread) => ({
      id: thread.id,
      title: thread.title,
      kind: thread.kind,
      members: thread.members,
      lastMessage: thread.messages.at(-1) ?? null,
      locked: thread.kind === 'stranger' && world.trustMatrix.levelOf({ userId: user, targetId: peerOf(thread, user) }) > TRUST_LEVELS.KNOWN
    }));
  const activeId = threads.some((thread) => thread.id === requestedThread) ? requestedThread : threads[0]?.id ?? null;
  const active = activeId === null ? null : threadOf(demo, activeId);
  const snap = snapshot(world);
  const integrity = demo.auditLog.verify();
  return {
    me: { userId: user, ...USER_PROFILES[user] },
    personas: PERSONA_ORDER.map((id) => ({ userId: id, ...USER_PROFILES[id] })),
    threads,
    activeThread: active === null ? null : {
      id: active.id,
      title: active.title,
      kind: active.kind,
      members: active.members,
      messages: active.messages.map((message) => ({
        ...message,
        author: USER_PROFILES[message.from] ?? null,
        agent: Object.values(AGENTS).includes(message.from) ? message.from : null,
        ticket: ticketPrefixed(demo, message)
      })),
      canSend: canSend(demo, active, user)
    },
    tickets: pendingTickets(demo),
    agents: snap.agents.filter((agent) => agent.ownerUserId === user),
    trust: snap.trust,
    mailboxes: snap.mailboxes,
    checks: { passed: snap.meta.checksPassed, total: snap.meta.checksTotal, list: snap.checks },
    trace: snap.trace.slice(-20),
    audit: snap.audit.slice(-20),
    auditChain: { ok: integrity.ok, length: integrity.length, head: integrity.head, trimmed: integrity.trimmed, recent: demo.auditLog.records({ limit: 12 }) },
    llm: demo.llm.stats(),
    store: demo.persistence ? demo.persistence.stats() : null,
    auth: { enabled: demo.authEnabled, user: demo.authUser ?? null },
    runtime: { version: 'im-live-agent-runtime/1', stats: demo.runtime.stats(), catalog: demo.runtime.catalog() },
    v2: demo.v2 === null
      ? { available: false, error: demo.v2Error ?? null }
      : {
          available: true,
          agentsOnline: demo.v2.agentNetwork().filter((agent) => agent.state === 'ONLINE').length,
          channels: demo.v2.channelView().channels.length,
          agentPushes: demo.v2.pushLog.length,
          auditOk: demo.v2.signedAudit.verify().ok
        },
    fleet: null, // 由 /api/fleet 单独提供，避免 poll 拖慢状态接口
    serverNow: new Date().toISOString()
  };
}

function ticketPrefixed(demo, message) {
  if (message.kind !== 'card') return null;
  const id = message.ticketId ?? null;
  if (id === null) return null;
  const ticket = demo.world.approvals.listPending(USERS.LIN).find((item) => item.id === id) ?? null;
  return ticket === null ? null : { id: ticket.id, state: ticket.state, payload: ticket.payload };
}

function peerOf(thread, user) {
  return thread.members.find((member) => member !== user) ?? user;
}

function canSend(demo, thread, user) {
  if (thread.kind === 'stranger') {
    const peer = peerOf(thread, user);
    return demo.world.trustMatrix.levelOf({ userId: user, targetId: peer }) <= TRUST_LEVELS.KNOWN;
  }
  return thread.members.includes(user);
}

async function sendMessage(demo, body) {
  const userId = body?.userId;
  const threadId = body?.threadId;
  const text = typeof body?.text === 'string' ? body.text.trim() : '';
  if (!PERSONA_ORDER.includes(userId)) return { ok: false, error: 'UNKNOWN_USER' };
  if (text === '') return { ok: false, error: 'EMPTY_MESSAGE' };
  const thread = threadOf(demo, threadId);
  if (thread === null) return { ok: false, error: 'UNKNOWN_THREAD' };
  if (!thread.members.includes(userId)) return { ok: false, error: 'NOT_A_MEMBER' };
  if (!canSend(demo, thread, userId)) {
    return {
      ok: false,
      error: 'LOCKED',
      hint: '陌生人来意必须先经对方 Agent 预审并审批通过：去「我的 Agent 团队」处理审批卡。'
    };
  }
  const message = makeMessage(demo, userId, text);
  pushMessage(demo, thread, message);
  demo.events.push({ at: message.at, type: 'CHAT_MESSAGE', threadId, messageId: message.id });
  recordAudit(demo, { source: 'messaging', action: 'CHAT_MESSAGE', actor: userId, target: threadId, detail: { messageId: message.id, kind: message.kind } });
  const agentReply = await maybeAgentReply(demo, thread, userId, text);
  return { ok: true, message, agentReply, state: stateFor(demo, userId, threadId) };
}

const AGENT_FOR_THREAD = Object.freeze({ 'th-lin-mom': 'agent-lin-family', 'th-lin-agent': 'agent-lin-butler' });

/** 让 Agent 在人类消息后起草一条回复：模型（或离线模板）→ 出口护栏 → 入会话。 */
async function maybeAgentReply(demo, thread, userId, text) {
  const role = AGENT_FOR_THREAD[thread.id] ?? null;
  if (role === null || thread.kind === 'stranger') return null;
  const pending = demo.world.approvals.listPending(USERS.LIN).length;
  const peer = peerOf(thread, userId);
  const level = demo.world.trustMatrix.levelOf({ userId, targetId: peer });
  const ran = await demo.runtime.runTurn({
    agentId: role,
    ownerUserId: userId,
    peerUserId: peer,
    userText: text,
    context: { userId, senderName: USER_PROFILES[userId]?.name ?? userId, threadTitle: thread.title, pendingCount: pending, trustZone: String(level), recentMessages: thread.messages.slice(-6).map((m) => ({ from: m.from, text: m.text })) }
  });
  for (const call of ran.toolCalls ?? []) {
    recordAudit(demo, { source: 'skill', action: call.allowed ? 'SKILL_CALL' : 'SKILL_DENIED', actor: role, target: thread.id, decision: call.allowed ? 'ALLOW' : 'DENY', detail: { skillId: call.skillId, reason: call.reason ?? null } });
  }
  if (ran.ok !== true) {
    const note = makeMessage(demo, AGENTS.LIN_BUTLER, ran.blocked === true ? 'Agent 草稿被出口护栏拦截（内容不展示），已写入审计链。' : 'Agent 本轮未能生成回复（已记录原因）。', 'agent');
    pushMessage(demo, thread, note);
    recordAudit(demo, { source: 'egress-guard', action: ran.blocked === true ? 'AGENT_DRAFT_BLOCKED' : 'AGENT_FAILED', actor: role, target: thread.id, decision: ran.reason ?? null, detail: { matched: ran.matched ?? null } });
    return { ok: false, blocked: ran.blocked === true, reason: ran.reason ?? null };
  }
  const reply = makeMessage(demo, role, ran.reply, 'agent', null, {
    engine: { source: ran.engine.source, model: ran.engine.model, latencyMs: ran.engine.latencyMs, tokens: ran.engine.tokens, planner: ran.planner?.source ?? null, tools: (ran.toolCalls ?? []).map((c) => ({ skillId: c.skillId, allowed: c.allowed, reason: c.reason ?? null, result: c.result ?? null })) }
  });
  pushMessage(demo, thread, reply);
  recordAudit(demo, { source: 'agent-runtime', action: 'AGENT_REPLY', actor: role, target: thread.id, detail: { engine: ran.engine.source, planner: ran.planner?.source ?? null, tools: (ran.toolCalls ?? []).map((c) => c.skillId) } });
  return { ok: true, messageId: reply.id, engine: reply.engine };
}

function decide(demo, ticketId, body) {
  const action = String(body?.action ?? '').toUpperCase();
  if (!['APPROVE', 'MODIFY', 'REJECT'].includes(action)) return { ok: false, error: 'INVALID_ARGUMENT' };
  try {
    const outcome = decideTicket(demo.world, ticketId, action, body?.comment ?? null, body?.patch ?? null);
    recordAudit(demo, { source: 'approval-engine', action: 'DECIDE_' + action, actor: USERS.LIN, target: ticketId, decision: outcome.ticket?.state ?? null, detail: { result: outcome.result ?? null } });
    appendDecisionMessages(demo, ticketId, action, outcome);
    return { ok: true, ticketId, state: outcome.ticket?.state ?? null, result: outcome.result ?? null, stateSnapshot: stateFor(demo, USERS.LIN, 'th-lin-agent') };
  } catch (error) {
    return { ok: false, error: error?.code ?? 'DECIDE_FAILED', message: String(error?.message ?? error) };
  }
}

function appendDecisionMessages(demo, ticketId, action, outcome) {
  const ticket = outcome.ticket ?? null;
  const payload = ticket?.payload ?? {};
  const kind = payload.kind ?? payload.ticketType ?? 'TICKET';
  const thread =
    kind === 'STRANGER_INTENT'
      ? threadOf(demo, 'th-lin-zhou')
      : threadOf(demo, 'th-lin-agent');
  if (thread === null) return;
  if (action === 'REJECT') {
    pushMessage(demo, thread, {
      ...makeMessage(demo, AGENTS.LIN_BUTLER, `已驳回「${payload.title ?? kind}」：${outcome.message?.state ? `对方消息状态 ${outcome.message.state}` : '执行链已终止'}。`, 'agent'),
      ticketId
    });
    return;
  }
  pushMessage(demo, thread, {
    ...makeMessage(demo, AGENTS.LIN_BUTLER, `审批通过「${payload.title ?? kind}」，执行链已完成：扣费 → 查看 → 回复 → 结算；关系档位已按规则更新。`, 'agent'),
    ticketId
  });
  if (outcome.message !== null && outcome.message !== undefined && outcome.message.state !== undefined) {
    pushMessage(demo, thread, {
      ...makeMessage(demo, USERS.ZHOU, outcome.message.replyText ?? '（已收到林总回复）', 'text'),
      ticketId
    });
  }
}

// ─────────────────────────── 工具 ───────────────────────────
async function readJson(req) {
  const chunks = [];
  let total = 0;
  for await (const chunk of req) {
    total += chunk.length;
    if (total > MAX_BODY) throw new Error('请求体超过 64KB 上限');
    chunks.push(chunk);
  }
  if (chunks.length === 0) return {};
  const text = Buffer.concat(chunks).toString('utf8').trim();
  return text === '' ? {} : JSON.parse(text);
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

const isMain = process.argv[1] && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url));
if (isMain) {
  const port = Number(process.env.PORT ?? process.argv[2] ?? 8797);
  const tls = process.env.IM_TLS_CERT && process.env.IM_TLS_KEY
    ? { certPath: process.env.IM_TLS_CERT, keyPath: process.env.IM_TLS_KEY }
    : null;
  const started = await startServer({ port, tls });
  console.log(`  持久化      ${started.persistence.dbPath}`);
  const fleet = await fleetSnapshot();
  console.log(`\n  AgentIM 人机混合 IM 演示已启动`);
  console.log(`  ─────────────────────────────────────────────`);
  console.log(`  控制台      ${started.url}`);
  console.log(`  鉴权        ${started.holder.auth.enabled ? '已启用（Cookie 会话）' : '未启用（设置 IM_AUTH=1 开启）'}`);
  console.log(`  状态接口    ${started.url}/api/state`);
  console.log(`  fleet       ${fleet.mode}  (${fleet.relayUrl})`);
  console.log(`  store       ${JSON.stringify(started.persistence.stats())}`);
  console.log(`  ─────────────────────────────────────────────`);
  console.log(`  登录身份：林总 / 周先生 / 林母（页面顶部切换）\n`);
}
