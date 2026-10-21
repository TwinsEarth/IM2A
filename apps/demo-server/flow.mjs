/**
 * 模块职责：演示世界（Demo World）—— 把「编排层」与「资金/消息通道」装配成一条可跑通的业务主线
 * 契约来源：contracts/INTERFACES.md v1.0
 *
 * 关键不变量：
 *   1. 陌生人触达必须先经「双方 Agent 结构化协商 → 目标侧 Agent 预审」，才可能进入付费环节；
 *   2. 扣费只发生在目标用户通过审批并查看之后，且账本 exactly-once（重试不二次扣费）；
 *   3. 所有 Agent 外发内容（含 Agent→Agent 协商摘要）必过出口护栏；
 *   4. 用户是最终决策者：Agent 永远只"呈报 + 建议"，钱、承诺、对外动作由人批准。
 *
 * 这条主线就是产品的核心叙事：**让 Agent 团队把行政层做完，人只做决策**。
 */

import {
  AGENT_KIND,
  TRUST_LEVELS,
  TrustMatrix,
  AgentRegistry,
  CapabilityGate,
  DailyBudget,
  ApprovalEngine,
  TICKET_TYPE,
  NegotiationEngine,
  INTENT,
  MemoryStore,
  MEMORY_LAYER,
  EgressGuard
} from '../../services/orchestrator/src/index.mjs';
import { Ledger, PaidMessageService, BILLING } from '../../services/payment/src/index.mjs';
import { BIZ_TYPE, makeEnvelope, SeqAllocator, DedupeStore } from '../../services/messaging/src/index.mjs';

// ───────────────────────────── 角色常量 ─────────────────────────────
export const USERS = Object.freeze({
  LIN: 'user-lin', // 忙碌的创业者：本 demo 的"大老板"
  ZHOU: 'user-zhou', // 陌生人：早期投资人
  MOM: 'user-lin-mom' // 家人：用于验证家庭档权限
});

export const AGENTS = Object.freeze({
  LIN_BUTLER: 'agent-lin-butler', // L0 总管：林总的第一界面
  LIN_FILTER: 'agent-lin-stranger-filter', // L1 子 Agent：陌生人过滤
  LIN_PROCUREMENT: 'agent-lin-procurement', // L1 子 Agent：采购
  LIN_FAMILY: 'agent-lin-family', // L1 子 Agent：家庭事务
  ZHOU_BUTLER: 'agent-zhou-butler' // 对方总管
});

export const USER_PROFILES = Object.freeze({
  [USERS.LIN]: { name: '林总', title: '创业者 / CEO', avatar: '林' },
  [USERS.ZHOU]: { name: '周先生', title: '早期投资人（陌生人）', avatar: '周' },
  [USERS.MOM]: { name: '林母', title: '家人', avatar: '母' }
});

const TRACE_LIMIT = 200;
const AUDIT_LIMIT = 400;

// ───────────────────────────── 世界构造 ─────────────────────────────
export function createWorld({ clock = () => new Date() } = {}) {
  const world = {
    clock,
    trustMatrix: new TrustMatrix({ clock }),
    registry: new AgentRegistry(),
    budget: new DailyBudget({ clock }),
    gate: null,
    approvals: null,
    negotiation: null,
    memory: null,
    egress: null,
    ledger: null,
    paid: null,
    seq: new SeqAllocator({ shards: 64 }),
    dedupe: new DedupeStore({ clock, ttlMs: 24 * 3600e3 }),
    mailboxes: new Map(), // userId -> envelope[]
    audit: [],
    trace: [],
    blocked: [], // 被出口护栏/预审拦下的来意（巡视态用）
    ticketIds: [],
    checks: [],
    ids: {}
  };

  world.gate = new CapabilityGate({
    registry: world.registry,
    trustMatrix: world.trustMatrix,
    budget: world.budget,
    clock,
    onAudit: (event) => push(world.audit, { source: 'capability-gate', ...event }, AUDIT_LIMIT)
  });
  world.approvals = new ApprovalEngine({
    clock,
    onEvent: (event) => push(world.audit, { source: 'approval-engine', ...event }, AUDIT_LIMIT)
  });
  world.negotiation = new NegotiationEngine({ clock, maxRounds: 5, maxTokens: 4000 });
  world.memory = new MemoryStore({ clock });
  world.egress = new EgressGuard({
    clock,
    onAudit: (event) => push(world.audit, { source: 'egress-guard', ...event }, AUDIT_LIMIT)
  });
  world.ledger = new Ledger({ clock });
  world.paid = new PaidMessageService({ ledger: world.ledger, clock, dailySendCap: 3 });

  bootstrapActors(world);
  return world;
}

/** 注册 Agent 团队与信任关系。权限在代码层声明，不靠提示词。 */
function bootstrapActors(world) {
  const { registry, budget, trustMatrix } = world;

  // ── 林总（目标用户）的 Agent 团队 ──
  registry.register({
    agentId: AGENTS.LIN_BUTLER,
    ownerUserId: USERS.LIN,
    kind: AGENT_KIND.BUTLER,
    domain: 'butler',
    allowedSkills: ['calendar.propose_slot', 'mail.read', 'im.reply', 'stranger.screen', 'procurement.purchase', 'fleet.devices', 'fleet.run'],
    destructiveSkills: ['procurement.purchase', 'bank.transfer'],
    trustZoneMin: TRUST_LEVELS.KNOWN, // 总管可对"已知"及以上档位行事
    budgetCentsPerDay: 20000, // 200 元/天
    approvalThresholdCents: 5000, // ≥50 元必须人工审批
    model: 'cloud-large'
  });
  registry.register({
    agentId: AGENTS.LIN_FILTER,
    ownerUserId: USERS.LIN,
    kind: AGENT_KIND.SUBAGENT,
    domain: 'stranger-filter',
    parentId: AGENTS.LIN_BUTLER,
    allowedSkills: ['stranger.screen', 'im.reply'],
    trustZoneMin: TRUST_LEVELS.STRANGER, // 它的工作就是面对陌生人
    budgetCentsPerDay: 0, // 不花钱
    approvalThresholdCents: 0,
    model: 'cloud-large'
  });
  registry.register({
    agentId: AGENTS.LIN_PROCUREMENT,
    ownerUserId: USERS.LIN,
    kind: AGENT_KIND.SUBAGENT,
    domain: 'procurement',
    parentId: AGENTS.LIN_BUTLER,
    allowedSkills: ['procurement.purchase', 'supplier.compare', 'procurement.compare'],
    destructiveSkills: ['procurement.purchase'],
    trustZoneMin: TRUST_LEVELS.TRUSTED, // 采购只对信任档以内的人/商行事
    budgetCentsPerDay: 500000, // ¥5000/天：单笔 ¥2399 在额度内，但超过自动审批阈值 ¥500
    approvalThresholdCents: 50000,
    model: 'cloud-large'
  });
  registry.register({
    agentId: AGENTS.LIN_FAMILY,
    ownerUserId: USERS.LIN,
    kind: AGENT_KIND.SUBAGENT,
    domain: 'family',
    parentId: AGENTS.LIN_BUTLER,
    allowedSkills: ['calendar.propose_slot', 'property.repair', 'utility.pay'],
    trustZoneMin: TRUST_LEVELS.FAMILY,
    budgetCentsPerDay: 30000,
    approvalThresholdCents: 20000,
    model: 'cloud-large'
  });

  // ── 周先生（陌生人）的总管 ──
  registry.register({
    agentId: AGENTS.ZHOU_BUTLER,
    ownerUserId: USERS.ZHOU,
    kind: AGENT_KIND.BUTLER,
    domain: 'butler',
    allowedSkills: ['negotiation.propose', 'mail.send', 'calendar.propose_slot'],
    trustZoneMin: TRUST_LEVELS.STRANGER,
    budgetCentsPerDay: 50000,
    approvalThresholdCents: 30000,
    model: 'cloud-large'
  });

  // 预算额度由集成方显式注入（契约：Gate 不自己推导额度）
  for (const agent of registry.listByOwner(USERS.LIN).concat(registry.listByOwner(USERS.ZHOU))) {
    budget.setCap(agent.agentId, agent.budgetCentsPerDay);
  }

  // ── 信任矩阵初值：家人=家庭档，陌生人=陌生档（默认即是） ──
  trustMatrix.grant({ userId: USERS.LIN, targetId: USERS.MOM, level: TRUST_LEVELS.FAMILY });
  trustMatrix.grant({ userId: USERS.MOM, targetId: USERS.LIN, level: TRUST_LEVELS.FAMILY });
}

// ───────────────────────────── 主场景 ─────────────────────────────
/**
 * 种子场景：一条陌生人付费消息的完整生命周期。
 * 陌生人 → 双方 Agent 协商（4 轮）→ 出口护栏 → 提交来意 → 目标侧预审 →
 * 审批卡片进入人类 IM 通道 → 等待人决策。
 */
export function seedScenario(world) {
  const trace = (act, detail) => push(world.trace, { at: nowIso(world), act, detail }, TRACE_LIMIT);

  // === 0) 出口护栏先拦两条垃圾/越界来意（巡视态展示"今日拦截"） ===
  const spamSamples = [
    {
      content: '您好我是某银行信贷专员，看到您企业信息，可提供低息贷款，请提供身份证号 110101199003074512 与银行卡 6222021234567890123 办理',
      note: '同时命中：身份证号、银行卡号、信贷话术'
    },
    {
      content: '加我微信 vx8888，有内部渠道，稳赚不赔，转账到 6222021234567890123 备注你的姓名',
      note: '命中：银行卡号、转账话术'
    }
  ];
  for (const sample of spamSamples) {
    const verdict = world.egress.inspect({
      content: sample.content,
      fromAgentId: 'agent-unknown-spammer',
      toKind: 'human',
      messageId: null,
      trustZone: TRUST_LEVELS.STRANGER
    });
    if (verdict.verdict !== 'PASS') {
      world.blocked.push({
        at: nowIso(world),
        verdict: verdict.verdict,
        note: sample.note,
        matched: verdict.matched.map((m) => m.id)
      });
      trace('EGRESS_BLOCK', `出口护栏拦截一条来意（${sample.note}）`);
    }
  }

  // === 1) 双方 Agent 结构化协商（不进人类 IM 通道，不打扰人） ===
  const session = world.negotiation.open({
    initiatorAgentId: AGENTS.ZHOU_BUTLER,
    initiatorUserId: USERS.ZHOU,
    responderAgentId: AGENTS.LIN_BUTLER,
    responderUserId: USERS.LIN,
    topic: 'A 轮融资沟通：周先生希望与林总约 20 分钟',
    payload: {
      who: '周先生，早期投资人，经校友会名单获知',
      why: '希望在 A 轮进入，先做一次 20 分钟初步沟通',
      expect: '林总确认一个 20 分钟时段',
      urgency: 'normal'
    }
  });
  const negId = session.id;
  trace('NEGOTIATION_OPEN', '周先生的总管向林总的总管发起事务协商（未通知任何人）');

  const turns = [
    {
      fromAgentId: AGENTS.ZHOU_BUTLER,
      intent: INTENT.PROPOSE,
      payload: {
        propose: '本周五 15:00-15:20 视频沟通',
        reason: '希望尽快建立联系',
        expectedAction: '确认时段'
      },
      tokens: 380
    },
    {
      fromAgentId: AGENTS.LIN_BUTLER,
      intent: INTENT.COUNTER,
      payload: {
        counter: '周五 15:00 林总有家庭事务（家庭档优先级更高），建议 16:00；另请先提供 3 页 BP 摘要',
        reason: '家庭档事务优先，且无背景材料的会面会被拒'
      },
      tokens: 420
    },
    {
      fromAgentId: AGENTS.ZHOU_BUTLER,
      intent: INTENT.COUNTER,
      payload: { provide: 'BP 三页摘要已附（附件 bp-3p.pdf）', accept: '周五 16:00 可行' },
      tokens: 300
    },
    {
      fromAgentId: AGENTS.LIN_BUTLER,
      intent: INTENT.ACCEPT,
      payload: {
        summary: '周五 16:00-16:20 视频 20 分钟，议题：A 轮融资；对方提前 24h 提交 BP 摘要',
        agreed: true
      },
      tokens: 200
    }
  ];
  for (const turn of turns) {
    world.negotiation.turn({ sessionId: negId, ...turn });
  }
  const artifact = world.negotiation.artifact(negId);
  trace(
    'NEGOTIATION_DONE',
    `协商在 ${turns.filter((t) => t.intent === INTENT.COUNTER).length} 轮反提案内收敛，产出方案：${artifact?.summary ?? '—'}`
  );

  // === 2) 出口护栏检查协商摘要（Agent 外发内容同样要过审） ===
  const egressResult = world.egress.inspect({
    content: artifact?.summary ?? '（无摘要）',
    fromAgentId: AGENTS.ZHOU_BUTLER,
    toKind: 'agent',
    messageId: null,
    trustZone: TRUST_LEVELS.STRANGER
  });
  trace('EGRESS_PASS', `协商摘要过审：${egressResult.verdict}`);

  // === 3) 陌生人提交「付费消息意图」（此时一分钱都还没扣） ===
  const summaryText = `周先生（早期投资人）希望就 A 轮融资沟通 20 分钟。双方 Agent 已就时间与材料达成一致：${artifact?.summary ?? ''}`;
  const message = world.paid.submitIntent({
    senderUserId: USERS.ZHOU,
    senderAgentId: AGENTS.ZHOU_BUTLER,
    receiverUserId: USERS.LIN,
    topic: 'A 轮融资沟通（20 分钟）',
    summary: summaryText,
    billingMode: BILLING.VIEW_REPLY_PAY, // 林总"看到且回复"才扣费
    amountCents: 5000, // 50 元
    idempotencyKey: 'intent:zhou-bp-2026-001'
  });
  trace('STRANGER_INTENT', `陌生人来意已提交（计费模式：看到且回复才扣费 ¥50，尚未扣费）`);

  // === 4) 目标侧 Agent 预审（付费之前必须先过预审） ===
  const screenGate = world.gate.evaluate({
    agentId: AGENTS.LIN_FILTER,
    userId: USERS.LIN,
    targetId: USERS.ZHOU,
    skillId: 'stranger.screen',
    amountCents: 0
  });
  const screened = world.paid.screen({ messageId: message.id, verdict: 'PASS' });
  world.memory.write({
    agentId: AGENTS.LIN_FILTER,
    userId: USERS.LIN,
    layer: MEMORY_LAYER.EPISODIC,
    content: `预审通过：${message.topic}，来意与工作/融资相关，命中规则「接受工作与融资类询盘」`,
    tags: ['screen', 'pass', 'stranger']
  });
  trace('SCREENING', `预审 ${screened.state}（门控 ${screenGate.decision}）：来意命中「接受工作/融资类询盘」`);

  // === 5) 生成审批卡片（人机混合的落点：人是最终决策者） ===
  const ticket = world.approvals.create({
    userId: USERS.LIN,
    sourceAgentId: AGENTS.LIN_FILTER,
    ticketType: TICKET_TYPE.NEGOTIATION,
    priority: 'high',
    deadlineMs: 30 * 60e3,
    traceId: 'trace-stranger-001',
    payload: {
      kind: 'STRANGER_INTENT',
      messageId: message.id,
      senderUserId: USERS.ZHOU,
      senderName: USER_PROFILES[USERS.ZHOU].name,
      senderAgentId: AGENTS.ZHOU_BUTLER,
      topic: message.topic,
      summary: summaryText,
      amountCents: message.amountCents,
      billingMode: message.billingMode,
      screening: { verdict: 'PASS', rule: '接受工作与融资类询盘', agentId: AGENTS.LIN_FILTER },
      negotiation: { sessionId: negId, rounds: 2, artifact: artifact?.summary ?? null },
      risk: 'medium',
      decisionHint: '通过后对方将被扣 ¥50 并进入你的会话；你回复后才算完成结算。'
    }
  });
  world.ticketIds.push(ticket.id);
  world.ids.ticketStranger = ticket.id;
  world.ids.messageId = message.id;
  world.ids.negotiationId = negId;

  // === 6) 审批卡片注入人类 IM 通道（bizSeq 保序 + bizId 去重） ===
  const conversationId = `conv:${USERS.LIN}:${USERS.ZHOU}`;
  const seqInfo = world.seq.next({ userId: USERS.LIN, conversationId });
  const envelope = makeEnvelope({
    bizType: BIZ_TYPE.APPROVAL_CARD,
    bizId: `biz-${ticket.id}`,
    bizSeq: seqInfo.seq,
    traceId: ticket.traceId,
    trustZone: TRUST_LEVELS.STRANGER,
    agentContext: { initiatorAgentId: AGENTS.LIN_FILTER, approvalState: 'PENDING' },
    payload: { ticketId: ticket.id, title: '陌生人付费消息待审批', amountCents: message.amountCents }
  });
  deliver(world, USERS.LIN, envelope);
  trace('APPROVAL_CARD', `审批卡片已推送（bizSeq=${envelope.bizSeq}，信任档：陌生）`);

  // === 7) 同时呈报一件采购（展示 REQUIRE_APPROVAL 门控） ===
  const purchaseGate = world.gate.evaluate({
    agentId: AGENTS.LIN_PROCUREMENT,
    userId: USERS.LIN,
    targetId: USERS.MOM,
    skillId: 'procurement.purchase',
    amountCents: 239900,
    destructive: true
  });
  if (purchaseGate.decision === 'REQUIRE_APPROVAL') {
    const purchaseTicket = world.approvals.create({
      userId: USERS.LIN,
      sourceAgentId: AGENTS.LIN_PROCUREMENT,
      ticketType: TICKET_TYPE.SKILL_CALL,
      priority: 'normal',
      deadlineMs: 4 * 3600e3,
      traceId: 'trace-procurement-001',
      payload: {
        kind: 'PROCUREMENT_PURCHASE',
        skillId: 'procurement.purchase',
        gateCode: purchaseGate.code,
        item: '人体工学椅 ×1（已比价 3 家）',
        vendor: '某电商自营',
        amountCents: 239900,
        alternatives: [
          { vendor: 'A 家', amountCents: 239900, note: '次日达、含安装' },
          { vendor: 'B 家', amountCents: 219900, note: '3 日达、不含安装' },
          { vendor: 'C 家', amountCents: 268000, note: '含 5 年保修' }
        ],
        risk: 'low',
        decisionHint: '金额超过该子 Agent 的自动审批阈值（¥500），故上抛给你。'
      }
    });
    world.ticketIds.push(purchaseTicket.id);
    world.ids.ticketProcurement = purchaseTicket.id;
    const purchaseSeq = world.seq.next({ userId: USERS.LIN, conversationId: `conv:${USERS.LIN}:agent-procurement` });
    deliver(
      world,
      USERS.LIN,
      makeEnvelope({
        bizType: BIZ_TYPE.APPROVAL_CARD,
        bizId: `biz-${purchaseTicket.id}`,
        bizSeq: purchaseSeq.seq,
        traceId: purchaseTicket.traceId,
        trustZone: TRUST_LEVELS.SELF,
        agentContext: { initiatorAgentId: AGENTS.LIN_PROCUREMENT, approvalState: 'PENDING' },
        payload: { ticketId: purchaseTicket.id, title: '采购待审批', amountCents: 239900 }
      })
    );
    trace('PROCUREMENT_ESCALATED', `采购子 Agent 触发 ${purchaseGate.code}，金额 ¥2399 上抛审批（超过自动阈值 ¥500）`);
  }

  // === 8) 总管只持有协调摘要，不读子 Agent 原始记忆 ===
  const digest = world.memory.digest({ butlerAgentId: AGENTS.LIN_BUTLER, userId: USERS.LIN });
  world.memory.write({
    agentId: AGENTS.LIN_BUTLER,
    userId: USERS.LIN,
    layer: MEMORY_LAYER.WORKING,
    content: `协调摘要：陌生人过滤 Agent 已预审 1 条融资询盘并通过，等待林总决策；采购 Agent 有 1 件 ¥2399 采购待批。`,
    tags: ['coordination', 'digest']
  });
  trace('MEMORY_DIGEST', `总管拿到协调摘要（${digest.coordination?.length ?? 0} 个子 Agent 状态），不含子 Agent 原始记忆`);

  return {
    negotiationId: negId,
    messageId: message.id,
    strangerTicketId: ticket.id,
    procurementTicketId: world.ids.ticketProcurement ?? null
  };
}

// ───────────────────────────── 人的决策 ─────────────────────────────
/**
 * 用户在审批态做决策。APPROVE/MODIFY 会真正推进业务（扣费、放行、结算）；
 * REJECT 会让对方**不被扣费**地被礼貌拒绝——这是"付费不能替代预审"的产品原则在代码里的体现。
 *
 * `patch`（仅 MODIFY 生效）是审批人的修改意见，会被真正执行：
 *   - STRANGER_INTENT：`{ replyText }` 覆盖 Agent 预拟的回复
 *   - PROCUREMENT_PURCHASE：`{ amountCents }` 覆盖采购金额（议价/改规格）
 * 修改后的实际执行值会写进 trace，保证"Agent 呈报 → 人修改 → 按人改的执行"可追溯。
 */
export function decideTicket(world, ticketId, action, comment = null, patch = null) {
  const ticket = world.approvals.get(ticketId);
  if (!ticket) throw new Error(`审批单不存在: ${ticketId}`);
  const trace = (act, detail) => push(world.trace, { at: nowIso(world), act, detail }, TRACE_LIMIT);
  const kind = ticket.payload?.kind;
  const effectivePatch = action === 'MODIFY' ? { ...(patch ?? {}), modifiedAt: nowIso(world) } : null;

  const decided = world.approvals.decide({
    ticketId,
    actorUserId: ticket.userId,
    action,
    comment,
    patch: effectivePatch
  });

  if (decided.state === 'REJECTED') {
    let message = null;
    if (kind === 'STRANGER_INTENT') {
      message = world.paid.refuse({ messageId: ticket.payload.messageId, reason: 'user_rejected' });
      trace('DECIDE_REJECT', '林总驳回陌生人来意：未扣费，对方 Agent 收到礼貌拒绝');
    } else {
      trace('DECIDE_REJECT', `林总驳回审批单 ${ticketId}`);
    }
    return { ticket: world.approvals.get(ticketId), message };
  }

  // APPROVED / MODIFIED → 进入执行
  world.approvals.beginExecution({ ticketId });
  let result = null;
  let finalMessage = null;

  if (kind === 'STRANGER_INTENT') {
    const messageId = ticket.payload.messageId;
    const senderUserId = ticket.payload.senderUserId;

    // ① 扣费（此时才扣；重试不会二次扣费——账本幂等键 pay:<messageId>）
    const paidResult = world.paid.pay({ messageId, payerUserId: senderUserId });
    trace(
      'PAY',
      paidResult.charged
        ? `扣费成功 ¥${(ticket.payload.amountCents / 100).toFixed(2)}（账本幂等键 pay:${messageId}）`
        : '支付重试：已扣费，本次未重复扣款'
    );

    // ② 查看 → ③ 回复 → ④ 结算
    // 审批人若在 MODIFY 中改了回复口径，以人的修改为准（Agent 只是预拟稿）
    const replyText =
      effectivePatch?.replyText ?? '林总已阅：周五 16:00 可聊 20 分钟，请提前一天发 BP 摘要。';
    if (effectivePatch?.replyText) trace('DECIDE_MODIFY', `林总改写了回复口径：「${replyText}」`);
    world.paid.view({ messageId, viewerUserId: ticket.userId });
    world.paid.reply({ messageId, replyText });
    world.paid.settle({ messageId });
    trace('VIEW_REPLY_SETTLE', '已查看 → 已回复 → 已结算（对方付费生效，接收方入账）');

    // ⑤ 付费且回复后，关系升级为「已知」档（信任是挣来的，不是买来的）
    world.trustMatrix.grant({ userId: ticket.userId, targetId: senderUserId, level: TRUST_LEVELS.KNOWN });
    trace('TRUST_UPGRADE', '周先生的关系档位由「陌生」升级为「已知」（可通过留言，不可查看家庭事务）');

    // ⑥ 记忆与摘要
    world.memory.write({
      agentId: AGENTS.LIN_FILTER,
      userId: ticket.userId,
      layer: MEMORY_LAYER.EPISODIC,
      content: `林总通过了周先生的融资询盘，已扣费并回复；关系升级为 KNOWN`,
      tags: ['decision', 'approve', 'trust-upgrade']
    });

    result = { messageId, charged: paidResult.charged, amountCents: ticket.payload.amountCents };
    finalMessage = world.paid.get(messageId);
  } else if (kind === 'PROCUREMENT_PURCHASE') {
    // 审批人可以改金额（议价/改规格），执行以修改后的为准
    const amountCents = effectivePatch?.amountCents ?? ticket.payload.amountCents;
    if (effectivePatch?.amountCents) {
      trace(
        'DECIDE_MODIFY',
        `林总把采购金额从 ¥${(ticket.payload.amountCents / 100).toFixed(2)} 改为 ¥${(amountCents / 100).toFixed(2)}`
      );
    }
    const spend = world.budget.trySpend(AGENTS.LIN_PROCUREMENT, amountCents);
    trace(
      'PROCUREMENT_EXECUTED',
      spend.ok
        ? `采购已下单 ¥${(amountCents / 100).toFixed(2)}，当日剩余额度 ¥${(spend.remaining / 100).toFixed(2)}`
        : `采购未执行：子 Agent 当日额度不足（剩余 ¥${(spend.remaining / 100).toFixed(2)}）`
    );
    result = { skillId: ticket.payload.skillId, amountCents, spent: spend.ok };
  }

  world.approvals.complete({ ticketId, result });
  trace('TICKET_COMPLETED', `审批单 ${ticketId} 执行完成`);
  return { ticket: world.approvals.get(ticketId), result, message: finalMessage };
}

// ───────────────────────────── 治理有效性自检 ─────────────────────────────
/**
 * 这 12 项是"架构是否真的成立"的可执行证据：每项都在一个**独立小世界**里跑，
 * 不污染演示状态。控制台「巡视态」直接展示这张表——它比任何 PPT 都更有说服力。
 */
export function probeChecks() {
  const checks = [];
  const run = (id, name, expect, fn) => {
    try {
      const actual = fn();
      const ok = expect === actual;
      checks.push({ id, name, expect, actual, ok, detail: ok ? '符合预期' : `不符：期望 ${expect}，实际 ${actual}` });
    } catch (error) {
      checks.push({ id, name, expect, actual: `THROW:${error?.code ?? error?.message ?? error}`, ok: false, detail: String(error?.message ?? error) });
    }
  };

  // 1. 未授权 Skill 必须被代码层拒绝
  run('gate-skill-whitelist', '总管调用未授权 Skill（bank.transfer）被拒', 'SKILL_NOT_ALLOWED', () => {
    const w = createWorld();
    return w.gate.evaluate({
      agentId: AGENTS.LIN_BUTLER,
      userId: USERS.LIN,
      targetId: USERS.MOM,
      skillId: 'bank.transfer',
      amountCents: 0
    }).code;
  });

  // 2. 信任档位方向必须正确（采购 Agent 只能对 TRUSTED 及以上行事）
  run('gate-trust-direction', '采购子 Agent 对陌生人行事被拒（信任档不足）', 'TRUST_TOO_LOW', () => {
    const w = createWorld();
    return w.gate.evaluate({
      agentId: AGENTS.LIN_PROCUREMENT,
      userId: USERS.LIN,
      targetId: USERS.ZHOU, // STRANGER=4 > trustZoneMin=2
      skillId: 'supplier.compare',
      amountCents: 0
    }).code;
  });

  // 3. 金额超阈值 → 必须人工审批（大额支出不能自主执行）
  run('gate-amount-threshold', '采购金额超过自动阈值 → 必须人工审批', 'REQUIRE_APPROVAL', () => {
    const w = createWorld();
    return w.gate.evaluate({
      agentId: AGENTS.LIN_PROCUREMENT,
      userId: USERS.LIN,
      targetId: USERS.MOM,
      skillId: 'procurement.purchase',
      amountCents: 239900
    }).decision;
  });

  // 4. 预算耗尽 → 拒绝（用非破坏性 Skill，避免先命中 DESTRUCTIVE_ACTION）
  run('gate-budget-exceeded', '子 Agent 当日额度耗尽后花钱被拒', 'BUDGET_EXCEEDED', () => {
    const w = createWorld();
    w.budget.setCap(AGENTS.LIN_PROCUREMENT, 10000);
    return w.gate.evaluate({
      agentId: AGENTS.LIN_PROCUREMENT,
      userId: USERS.LIN,
      targetId: USERS.MOM,
      skillId: 'supplier.compare',
      amountCents: 20000
    }).code;
  });

  // 5. 先预审后付费：未预审直接付费必须失败
  run('paid-needs-screening', '未预审直接付费被拒（先预审后付费）', 'NEEDS_SCREENING', () => {
    const w = createWorld();
    const m = w.paid.submitIntent({
      senderUserId: USERS.ZHOU,
      senderAgentId: AGENTS.ZHOU_BUTLER,
      receiverUserId: USERS.LIN,
      topic: '未预审',
      summary: '跳过预审直接发',
      billingMode: BILLING.SEND_PAY,
      amountCents: 5000,
      idempotencyKey: 'probe-needs-screening'
    });
    try {
      w.paid.pay({ messageId: m.id, payerUserId: USERS.ZHOU });
      return 'PAID_BUT_SHOULD_FAIL';
    } catch (error) {
      return error.code ?? 'UNKNOWN';
    }
  });

  // 6. 重复付费（网络重试 5 次）→ 账本只能有一条 CHARGE
  run('paid-exactly-once', '连点 5 次支付，账本只产生 1 条扣费', '1:1', () => {
    const w = createWorld();
    const m = w.paid.submitIntent({
      senderUserId: USERS.ZHOU,
      senderAgentId: AGENTS.ZHOU_BUTLER,
      receiverUserId: USERS.LIN,
      topic: '重复支付',
      summary: '重试测试',
      billingMode: BILLING.SEND_PAY,
      amountCents: 5000,
      idempotencyKey: 'probe-exactly-once'
    });
    w.paid.screen({ messageId: m.id, verdict: 'PASS' });
    let chargedCount = 0;
    for (let i = 0; i < 5; i += 1) {
      const r = w.paid.pay({ messageId: m.id, payerUserId: USERS.ZHOU });
      if (r.charged) chargedCount += 1;
    }
    const charges = w.ledger.entries({ userId: USERS.ZHOU }).filter((e) => e.type === 'CHARGE').length;
    return `${charges}:${chargedCount}`;
  });

  // 7. 预审 BLOCK 后不可付费（付费不能绕过过滤）
  run('paid-blocked-unpayable', '预审 BLOCK 的消息不可付费', 'NOT_PAYABLE', () => {
    const w = createWorld();
    const m = w.paid.submitIntent({
      senderUserId: USERS.ZHOU,
      senderAgentId: AGENTS.ZHOU_BUTLER,
      receiverUserId: USERS.LIN,
      topic: '骚扰',
      summary: '骚扰内容',
      billingMode: BILLING.SEND_PAY,
      amountCents: 5000,
      idempotencyKey: 'probe-blocked'
    });
    w.paid.screen({ messageId: m.id, verdict: 'BLOCK' });
    try {
      w.paid.pay({ messageId: m.id, payerUserId: USERS.ZHOU });
      return 'PAID_BUT_SHOULD_FAIL';
    } catch (error) {
      return error.code ?? 'UNKNOWN';
    }
  });

  // 8. 协商轮次硬上限（防 Agent 无限互喷烧推理）
  run('neg-round-cap', '协商超过 5 轮反提案 → 转人工中断态', 'INPUT_REQUIRED/MAX_ROUNDS', () => {
    const w = createWorld();
    const s = w.negotiation.open({
      initiatorAgentId: AGENTS.ZHOU_BUTLER,
      initiatorUserId: USERS.ZHOU,
      responderAgentId: AGENTS.LIN_BUTLER,
      responderUserId: USERS.LIN,
      topic: '无限拉锯',
      payload: {}
    });
    w.negotiation.turn({ sessionId: s.id, fromAgentId: AGENTS.ZHOU_BUTLER, intent: INTENT.PROPOSE, payload: {} });
    let session = w.negotiation.get(s.id);
    for (let i = 0; i < 8 && session.state === 'WORKING'; i += 1) {
      const r = w.negotiation.turn({
        sessionId: s.id,
        fromAgentId: i % 2 === 0 ? AGENTS.LIN_BUTLER : AGENTS.ZHOU_BUTLER,
        intent: INTENT.COUNTER,
        payload: { round: i + 2 }
      });
      session = r.session;
    }
    return `${session.state}/${session.terminalReason}`;
  });

  // 9. 出口护栏拦截身份证号
  run('egress-idcard', 'Agent 外发内容含身份证号被拦截', 'BLOCK', () => {
    const w = createWorld();
    return w.egress.inspect({
      content: '请把合同寄到张三家，身份证号 110101199003074512，电话 13800000000',
      fromAgentId: AGENTS.LIN_BUTLER,
      toKind: 'agent',
      messageId: null,
      trustZone: TRUST_LEVELS.STRANGER
    }).verdict;
  });

  // 10. 记忆隔离：总管读不到子 Agent 原始记忆
  run('memory-isolation', '总管越权读取子 Agent 原始记忆被拒', 'MEMORY_ISOLATED', () => {
    const w = createWorld();
    w.memory.write({
      agentId: AGENTS.LIN_FILTER,
      userId: USERS.LIN,
      layer: MEMORY_LAYER.EPISODIC,
      content: '子 Agent 的私密执行记录：某份 BP 的具体估值数字'
    });
    try {
      w.memory.read({
        agentId: AGENTS.LIN_FILTER,
        requesterAgentId: AGENTS.LIN_BUTLER,
        userId: USERS.LIN,
        layer: MEMORY_LAYER.EPISODIC
      });
      return 'READ_SUCCEEDED_SHOULD_FAIL';
    } catch (error) {
      return error.code ?? 'UNKNOWN';
    }
  });

  // 11. 审批单非法二次决策
  run('approval-illegal-transition', '审批单已通过后再次通过 → 非法迁移', 'ILLEGAL_TRANSITION', () => {
    const w = createWorld();
    const t = w.approvals.create({
      userId: USERS.LIN,
      sourceAgentId: AGENTS.LIN_FILTER,
      ticketType: TICKET_TYPE.SKILL_CALL,
      payload: { kind: 'PROCUREMENT_PURCHASE' }
    });
    w.approvals.decide({ ticketId: t.id, actorUserId: USERS.LIN, action: 'APPROVE' });
    try {
      w.approvals.decide({ ticketId: t.id, actorUserId: USERS.LIN, action: 'APPROVE' });
      return 'SECOND_APPROVE_SUCCEEDED';
    } catch (error) {
      return error.code ?? 'UNKNOWN';
    }
  });

  // 12. 信任授权过期后自动回落为陌生档
  run('trust-expiry', '信任授权过期后自动回落为陌生档', String(TRUST_LEVELS.STRANGER), () => {
    let t = Date.parse('2026-10-21T12:00:00+08:00');
    const clock = () => new Date(t);
    const w = createWorld({ clock });
    w.trustMatrix.grant({
      userId: USERS.LIN,
      targetId: USERS.ZHOU,
      level: TRUST_LEVELS.TRUSTED,
      expiresAt: new Date(t + 60_000).toISOString()
    });
    const before = w.trustMatrix.levelOf({ userId: USERS.LIN, targetId: USERS.ZHOU });
    t += 120_000; // 时钟推进 2 分钟，授权已过期
    const after = w.trustMatrix.levelOf({ userId: USERS.LIN, targetId: USERS.ZHOU });
    return String(before === TRUST_LEVELS.TRUSTED ? after : `BEFORE_WRONG:${before}`);
  });

  return checks;
}

// ───────────────────────────── 状态快照（供控制台渲染） ─────────────────────────────
export function snapshot(world) {
  // 超时升级必须由外层按 clock 主动驱动（契约：listPending 不自动清理过期单）
  world.approvals.escalateOverdue();

  const tickets = world.ticketIds
    .map((id) => world.approvals.get(id))
    .filter(Boolean)
    .map((ticket) => ({
      id: ticket.id,
      userId: ticket.userId,
      sourceAgentId: ticket.sourceAgentId,
      ticketType: ticket.ticketType,
      state: ticket.state,
      priority: ticket.priority,
      createdAt: ticket.createdAt,
      deadline: ticket.deadline,
      traceId: ticket.traceId,
      payload: ticket.payload,
      patch: ticket.patch ?? null,
      escalationLevel: ticket.escalationLevel ?? 0
    }));

  const pending = tickets.filter((t) => t.state === 'PENDING' || t.state === 'REVIEWING' || t.state === 'EXECUTING');

  return {
    meta: {
      generatedAt: nowIso(world),
      users: Object.entries(USER_PROFILES).map(([userId, profile]) => ({ userId, ...profile })),
      checksPassed: world.checks.filter((c) => c.ok).length,
      checksTotal: world.checks.length
    },
    approvals: tickets,
    pendingCount: pending.length,
    messages: world.paid.list({}).map((m) => ({
      id: m.id,
      senderUserId: m.senderUserId,
      receiverUserId: m.receiverUserId,
      topic: m.topic,
      summary: m.summary,
      state: m.state,
      billingMode: m.billingMode,
      amountCents: m.amountCents,
      createdAt: m.createdAt,
      updatedAt: m.updatedAt ?? null
    })),
    negotiations: world.negotiation.list({}).map((s) => ({
      id: s.id,
      topic: s.topic,
      state: s.state,
      round: s.round,
      tokens: s.tokens,
      initiatorAgentId: s.initiatorAgentId,
      responderAgentId: s.responderAgentId,
      terminalReason: s.terminalReason ?? null,
      artifact: world.negotiation.artifact(s.id),
      turns: (s.turns ?? []).map((t) => ({
        at: t.at,
        fromAgentId: t.fromAgentId,
        intent: t.intent,
        payload: t.payload,
        tokens: t.tokens ?? 0,
        stateAfter: t.stateAfter ?? null
      }))
    })),
    agents: [
      ...world.registry.listByOwner(USERS.LIN),
      ...world.registry.listByOwner(USERS.ZHOU)
    ].map((a) => ({
      agentId: a.agentId,
      ownerUserId: a.ownerUserId,
      kind: a.kind,
      domain: a.domain,
      parentId: a.parentId ?? null,
      allowedSkills: a.allowedSkills,
      trustZoneMin: a.trustZoneMin,
      budgetCentsPerDay: a.budgetCentsPerDay,
      approvalThresholdCents: a.approvalThresholdCents,
      budgetRemainingCents: world.budget.remaining(a.agentId)
    })),
    trust: {
      pairs: [
        { from: USERS.LIN, to: USERS.MOM, level: world.trustMatrix.levelOf({ userId: USERS.LIN, targetId: USERS.MOM }) },
        { from: USERS.LIN, to: USERS.ZHOU, level: world.trustMatrix.levelOf({ userId: USERS.LIN, targetId: USERS.ZHOU }) },
        { from: USERS.MOM, to: USERS.LIN, level: world.trustMatrix.levelOf({ userId: USERS.MOM, targetId: USERS.LIN }) }
      ],
      names: ['SELF', 'FAMILY', 'TRUSTED', 'KNOWN', 'STRANGER']
    },
    ledger: {
      entries: world.ledger.entries({}),
      stats: world.ledger.stats()
    },
    mailboxes: [...world.mailboxes.entries()].map(([userId, envelopes]) => ({ userId, envelopes })),
    blocked: world.blocked,
    trace: world.trace,
    checks: world.checks,
    audit: world.audit.slice(-60)
  };
}

// ───────────────────────────── 小工具 ─────────────────────────────
function push(list, item, limit) {
  list.push(item);
  if (list.length > limit) list.splice(0, list.length - limit);
  return item;
}

function deliver(world, userId, envelope) {
  if (!world.mailboxes.has(userId)) world.mailboxes.set(userId, []);
  const box = world.mailboxes.get(userId);
  const { duplicate } = world.dedupe.checkAndMark(envelope.bizId);
  if (duplicate) return { delivered: false, duplicate: true };
  box.push(envelope);
  return { delivered: true, duplicate: false };
}

function nowIso(world) {
  return world.clock().toISOString();
}
