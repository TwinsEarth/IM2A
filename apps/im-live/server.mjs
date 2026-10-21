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
import { TICKET_STATE } from '../../services/orchestrator/src/approval-engine.mjs';

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
export function createApp({ fleet = null, llm = null, skills = null, db = undefined } = {}) {
  const persistence = typeof db === 'string' ? createPersistence({ dbPath: db }) : (db ?? createPersistence());
  const ownsPersistence = !(db !== undefined && typeof db !== 'string');
  const llmClient = llm ?? createLlm();
  const skillExecutor = skills ?? createSkillExecutor({
    relayUrl: FLEET.relayUrl,
    operatorToken: FLEET.operatorToken,
    deviceToken: FLEET.deviceToken,
    dispatchProbe: (machineId, argv) => (fleet !== null && typeof fleet === 'function' ? fleet({ probe: true, machineId, argv }) : dispatchProbe(machineId, argv))
  });
  const holder = { demo: buildDemo(llmClient, skillExecutor, persistence) };
  const fleetOverride = fleet;
  const server = createServer(async (req, res) => {
    try {
      await route(req, res, holder, fleetOverride);
    } catch (error) {
      sendJson(res, 500, { error: 'INTERNAL', message: String(error?.message ?? error) });
    }
  });
  return {
    server,
    holder,
    persistence,
    getState: () => stateFor(holder.demo, USERS.LIN, 'th-lin-agent'),
    closePersistence: () => { if (ownsPersistence) persistence.close(); }
  };
}

export function startServer({ port = 8797, host = '127.0.0.1', fleet = null, llm = null, skills = null, db = undefined } = {}) {
  const app = createApp({ fleet, llm, skills, db });
  return new Promise((resolve, reject) => {
    app.server.once('error', reject);
    app.server.listen(port, host, () => {
      const address = app.server.address();
      resolve({
        ...app,
        host,
        port: address.port,
        url: `http://${host}:${address.port}`,
        close: () => new Promise((done) => app.server.close(() => { app.closePersistence(); done(); }))
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

  if (method === 'GET' && pathname === '/api/state') {
    const user = normalizeUser(searchParams.get('user'));
    const threadId = searchParams.get('thread') ?? defaultThreadFor(user);
    sendJson(res, 200, stateFor(holder.demo, user, threadId));
    return;
  }

  if (method === 'POST' && pathname === '/api/message') {
    const body = await readJson(req);
    const result = await sendMessage(holder.demo, body);
    sendJson(res, result.ok ? 200 : 400, result);
    return;
  }

  const decideMatch = pathname.match(/^\/api\/approvals\/([^/]+)\/decide$/);
  if (method === 'POST' && decideMatch) {
    const body = await readJson(req);
    const result = decide(holder.demo, decodeURIComponent(decideMatch[1]), body);
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

  if (method === 'POST' && pathname === '/api/reset') {
    holder.demo.persistence?.reset();
    holder.demo = buildDemo(holder.demo.llm, holder.demo.skills, holder.demo.persistence);
    sendJson(res, 200, { ok: true, state: stateFor(holder.demo, USERS.LIN, 'th-lin-agent') });
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
    runtime: { version: 'im-live-agent-runtime/1', stats: demo.runtime.stats(), catalog: demo.runtime.catalog() },
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
  const started = await startServer({ port });
  console.log(`  持久化      ${started.persistence.dbPath}`);
  const fleet = await fleetSnapshot();
  console.log(`\n  AgentIM 人机混合 IM 演示已启动`);
  console.log(`  ─────────────────────────────────────────────`);
  console.log(`  控制台      ${started.url}`);
  console.log(`  状态接口    ${started.url}/api/state`);
  console.log(`  fleet       ${fleet.mode}  (${fleet.relayUrl})`);
  console.log(`  store       ${JSON.stringify(started.persistence.stats())}`);
  console.log(`  ─────────────────────────────────────────────`);
  console.log(`  登录身份：林总 / 周先生 / 林母（页面顶部切换）\n`);
}
