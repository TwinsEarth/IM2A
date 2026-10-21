/**
 * 模块职责：im2agent 原型的极轻量领域层 —— 信任档 / 审批 / 付费消息 / 账本 / 审计链
 * 契约来源：语义对齐 contracts/INTERFACES.md（§1 信任档、§4 审批、§8 账本、§9 付费消息、§16 审计），
 *           但为"单文件、可持久化、可演示"做了极简化实现；生产请用 services/ 下的冻结模块。
 *
 * 状态机（原型版，与冻结契约同名同向）：
 *   审批单：PENDING → APPROVED | REJECTED → EXECUTING → COMPLETED
 *   付费消息：INTENT → AWAITING_PAYMENT → PAID → VIEWED → REPLIED → SETTLED
 * 铁律：①先预审后付费；②扣费幂等键 `pay:<messageId>`；③审计只追加；④人批了才动钱。
 */
import { Ledger } from '../../services/payment/src/ledger.mjs';
import { AuditLog } from '../../services/audit/src/audit-log.mjs';
import { TRUST_LEVELS } from '../../services/orchestrator/src/trust-zone.mjs';

export const USERS = Object.freeze({ LIN: 'user-lin', ZHOU: 'user-zhou', MOM: 'user-lin-mom' });
export const USER_NAMES = Object.freeze({ [USERS.LIN]: '林总', [USERS.ZHOU]: '周先生', [USERS.MOM]: '林母' });

const now = () => new Date().toISOString();

/** 建一个可序列化的世界（jsonfile 存储直接落这个对象；memory 存储进程内保存） */
export function createWorld({ restored = null } = {}) {
  if (restored !== null && typeof restored === 'object') {
    // 从 store 恢复：审计链用持久化的记录重建（链尾/序号延续），其余状态原样接续
    const audit = new AuditLog({ records: restored.audit ?? [] });
    return {
      ledger: new Ledger({}),
      audit,
      state: {
        agents: restored.agents ?? [],
        trust: restored.trust ?? {},
        tickets: restored.tickets ?? [],
        messages: restored.messages ?? [],
        ledger: restored.ledger ?? [],
        audit: (restored.audit ?? []).map((record) => ({ ...record }))
      }
    };
  }

  const world = {
    ledger: new Ledger({}),
    audit: new AuditLog({}),
    state: {
      agents: [
        { agentId: 'agent-lin-butler', ownerUserId: USERS.LIN, domain: 'butler', trustZoneMin: 3, budgetCentsPerDay: 20000 },
        { agentId: 'agent-lin-filter', ownerUserId: USERS.LIN, domain: 'stranger-filter', trustZoneMin: 4, budgetCentsPerDay: 0 },
        { agentId: 'agent-lin-procurement', ownerUserId: USERS.LIN, domain: 'procurement', trustZoneMin: 2, budgetCentsPerDay: 500000 },
        { agentId: 'agent-zhou-butler', ownerUserId: USERS.ZHOU, domain: 'butler', trustZoneMin: 4, budgetCentsPerDay: 50000 }
      ],
      trust: {},
      tickets: [],
      messages: [],
      ledger: [],
      audit: []
    }
  };

  // 种子：陌生人付费消息（先预审，后付费）+ 一件采购
  world.state.messages.push({
    id: 'msg-stranger-1',
    senderUserId: USERS.ZHOU,
    receiverUserId: USERS.LIN,
    topic: 'A 轮融资沟通（20 分钟）',
    summary: '周先生（早期投资人）希望就 A 轮融资沟通 20 分钟，双方 Agent 已就时间达成一致。',
    billingMode: 'VIEW_REPLY_PAY',
    amountCents: 5000,
    state: 'AWAITING_PAYMENT',
    screening: 'PASS',
    createdAt: now()
  });

  world.state.tickets.push({
    id: 'tkt-stranger-1',
    userId: USERS.LIN,
    agentId: 'agent-lin-filter',
    sourceAgentId: 'agent-lin-filter',
    ticketType: 'NEGOTIATION',
    title: '陌生人来意审批',
    context: null,
    price: 5000,
    state: 'PENDING',
    priority: 'high',
    payload: { kind: 'STRANGER_INTENT', messageId: 'msg-stranger-1', senderUserId: USERS.ZHOU, topic: 'A 轮融资沟通（20 分钟）', amountCents: 5000 },
    createdAt: now(),
    resolvedAt: null,
    result: null
  });

  world.state.tickets.push({
    id: 'tkt-procurement-1',
    userId: USERS.LIN,
    agentId: 'agent-lin-procurement',
    sourceAgentId: 'agent-lin-procurement',
    ticketType: 'SKILL_CALL',
    title: '采购需求审批',
    context: null,
    price: 239900,
    state: 'PENDING',
    priority: 'normal',
    payload: { kind: 'PROCUREMENT_PURCHASE', item: '人体工学椅 ×1', amountCents: 239900 },
    createdAt: now(),
    resolvedAt: null,
    result: null
  });

  recordAudit(world, { source: 'negotiation-engine', action: 'NEGOTIATION_COMPLETED', actor: 'agent-zhou-butler', detail: { rounds: 2 } });
  recordAudit(world, { source: 'agent-lin-filter', action: 'SCREENING_PASS', actor: 'agent-lin-filter', target: 'msg-stranger-1', decision: 'PASS' });
  return world;
}

export function recordAudit(world, entry) {
  const record = world.audit.append(entry);
  world.state.audit.push({ ...record });
  return record;
}

/** 序列化：交给 store 持久化（含审计链与账本投影） */
export function serialize(world) {
  return {
    agents: world.state.agents,
    trust: world.state.trust,
    tickets: world.state.tickets,
    messages: world.state.messages,
    ledger: world.ledger.entries({}),
    audit: world.audit.snapshot().map((record) => ({ ...record }))
  };
}

export class DomainError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'DomainError';
    this.code = code;
  }
}

function ticketOf(world, ticketId) {
  const ticket = world.state.tickets.find((item) => item.id === ticketId);
  if (!ticket) throw new DomainError(`审批单不存在: ${ticketId}`, 'NOT_FOUND');
  return ticket;
}

function messageOf(world, messageId) {
  const message = world.state.messages.find((item) => item.id === messageId);
  if (!message) throw new DomainError(`付费消息不存在: ${messageId}`, 'NOT_FOUND');
  return message;
}

/**
 * 人的决策。APPROVE 会真正推进业务：扣费（幂等）→ 查看 → 回复 → 结算 → 关系升级。
 * @param {object} deps { llm } —— 用三档降级的 LLM 起草回复（起草失败不影响治理流程）
 */
export async function decide(world, { ticketId, action, comment = null, llm = null } = {}) {
  const ticket = ticketOf(world, ticketId);
  if (ticket.state !== 'PENDING') throw new DomainError(`审批单已是终态/非待审: ${ticket.state}`, 'ILLEGAL_TRANSITION');
  if (!['APPROVE', 'MODIFY', 'REJECT'].includes(action)) throw new DomainError(`非法动作: ${String(action)}`, 'INVALID_ARGUMENT');

  if (action === 'REJECT') {
    ticket.state = 'REJECTED';
    ticket.resolvedAt = now();
    if (ticket.payload.kind === 'STRANGER_INTENT') {
      messageOf(world, ticket.payload.messageId).state = 'REFUSED';
    }
    recordAudit(world, { source: 'approval-engine', action: 'TICKET_REJECTED', actor: ticket.userId, target: ticket.id, decision: 'REJECTED' });
    return { ticket, message: null };
  }

  ticket.state = 'APPROVED';
  recordAudit(world, { source: 'approval-engine', action: 'TICKET_APPROVED', actor: ticket.userId, target: ticket.id, decision: action, detail: { comment } });

  let message = null;
  if (ticket.payload.kind === 'STRANGER_INTENT') {
    message = messageOf(world, ticket.payload.messageId);
    // ① 扣费（幂等键兜底：重复调用不会二次扣费）
    const charge = world.ledger.post({
      idempotencyKey: `pay:${message.id}`,
      type: 'CHARGE',
      userId: message.senderUserId,
      amountCents: message.amountCents,
      ref: message.id
    });
    message.state = 'PAID';
    // ② 起草回复：模型只起草，内容与动作分离
    let draft = { tier: 'template', reply: '收到，我会尽快回复。', degraded: false };
    if (llm !== null) draft = await llm.draft({ text: message.summary, topic: message.topic });
    message.replyText = draft.reply;
    message.replyTier = draft.tier;
    message.state = 'VIEWED';
    message.state = 'REPLIED';
    // ③ 结算（记录 SETTLE，不改余额口径）
    world.ledger.post({
      idempotencyKey: `settle:${message.id}`,
      type: 'SETTLE',
      userId: message.receiverUserId,
      amountCents: message.amountCents,
      ref: message.id
    });
    message.state = 'SETTLED';
    // ④ 信任升级：付费且回复后，陌生 → 已知
    world.state.trust[`${message.receiverUserId}>${message.senderUserId}`] = TRUST_LEVELS.KNOWN;
    recordAudit(world, {
      source: 'payment',
      action: 'PAID_MESSAGE_SETTLED',
      actor: message.senderUserId,
      target: message.id,
      decision: 'SETTLED',
      detail: { charged: charge.duplicate === false, amountCents: message.amountCents, replyTier: draft.tier }
    });
  }

  ticket.state = 'EXECUTING';
  ticket.state = 'COMPLETED';
  ticket.resolvedAt = now();
  ticket.result = message === null ? { kind: 'SKILL_CALL', amountCents: ticket.payload.amountCents } : { kind: 'PAID_MESSAGE', messageId: message.id };
  recordAudit(world, { source: 'approval-engine', action: 'TICKET_COMPLETED', actor: ticket.userId, target: ticket.id, decision: 'COMPLETED' });
  return { ticket, message };
}

/**
 * 让 LLM 生成审批单的「上下文摘要」（Go 规范里的 Ticket.Context）。
 * 模型只生成**理由/摘要**，不改变任何状态；失败/离线时用确定性模板，不会让页面报错。
 */
export async function previewContext(world, { ticketId, llm }) {
  const ticket = ticketOf(world, ticketId);
  const prompt =
    ticket.payload.kind === 'STRANGER_INTENT'
      ? `陌生人想就「${ticket.payload.topic}」联系我，对方付费 ${ticket.price / 100} 元。用一句话说明我该不该通过、以及通过后会发生什么。`
      : `子 Agent 申请「${ticket.payload.item}」，金额 ${ticket.price / 100} 元。用一句话说明风险与建议。`;
  const draft = llm === null ? { tier: 'template', reply: templateFallback(ticket), degraded: false } : await llm.draft({ text: prompt, topic: ticket.title });
  ticket.context = draft.reply;
  recordAudit(world, { source: 'llm', action: 'CONTEXT_GENERATED', actor: 'system', target: ticket.id, decision: draft.tier, detail: { degraded: draft.degraded === true } });
  return { ticket, draft };
}

function templateFallback(ticket) {
  return ticket.payload.kind === 'STRANGER_INTENT'
    ? `来自陌生人的付费来意（¥${(ticket.price / 100).toFixed(2)}）：通过后会先扣费再进入会话，你回复后对方才完成结算。`
    : `子 Agent 申请 ¥${(ticket.price / 100).toFixed(2)} 的支出，超过自动审批阈值，需要你确认。`;
}

/** 供页面使用的一次性快照（不含内部类实例） */
export function snapshot(world) {
  const liveEntries = world.ledger.entries({});
  return {
    generatedAt: now(),
    agents: world.state.agents,
    trust: world.state.trust,
    tickets: world.state.tickets,
    messages: world.state.messages,
    // 重启后内存账本为空，但持久化投影里有历史 → 展示以"有内容的那份"为准
    ledger: liveEntries.length > 0 ? liveEntries : (world.state.ledger ?? []),
    audit: world.state.audit.slice(-30),
    auditChain: world.audit.verify()
  };
}
