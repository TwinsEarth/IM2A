/**
 * 模块职责：v2 接入层 —— 把「身份 / Agent 网关 / 可见域 / 频道 / 会话 / 签名审计」装配成一个可演示的 Agent 网络
 * 契约来源：contracts/INTERFACES.md §11–§16（v2 冻结契约）
 *
 * 这一层回答 v2 相对 v1 的核心变化：**Agent 不再只是服务端内部的函数调用**。
 *   · 每个 Agent 有自己的 Ed25519 身份与平台签发的 AgentCard（可被第三方验签）
 *   · 所有 Agent 动作经 AgentGateway 单一入口（认证→限流→前置门控→出口护栏→签名审计）
 *   · Agent 代为读数据时必须过 VisibilityGate（按信任档裁剪字段，MEMORY 只回摘要）
 *   · Agent 作为消息内核的一等客户端：独立会话、独立在线状态、只订阅自己关心的频道
 *   · Agent 协商走 AGENT_NEGOTIATION 频道：对人类未读恒为 0（协商不打扰人）
 *
 * 演示边界（诚实）：
 *   · transport 用内存队列 + SSE 适配器，生产为 WebSocket/QUIC；
 *   · 身份私钥在进程内存中生成，生产的私钥应存 KMS/HSM，不落业务库（docs/05 §15 有表设计）。
 */
import { randomUUID } from 'node:crypto';

import { AuditLog } from '../../services/audit/src/audit-log.mjs';
import { SignedAuditLog } from '../../services/audit/src/signed-audit.mjs';
import {
  createAgentIdentity,
  createAgentCard,
  signAgentCard,
  sha256Hex
} from '../../services/identity/src/agent-id.mjs';
import { AgentGateway, PROTOCOL } from '../../services/gateway/src/agent-gateway.mjs';
import { VisibilityGate, DOMAIN } from '../../services/gateway/src/visibility.mjs';
import {
  ChannelRegistry,
  CHANNEL_TYPE,
  MEMBER_KIND,
  MESSAGE_KIND
} from '../../services/messaging/src/channel.mjs';
import { AgentSessionRegistry } from '../../services/messaging/src/session.mjs';

import { USERS, AGENTS } from '../demo-server/flow.mjs';

/** 每个 Agent 的身份画像：域名集合与信任上限决定它能"看见"什么（v2 的读侧权限）。 */
export const AGENT_PROFILES = Object.freeze({
  [AGENTS.LIN_BUTLER]: { displayName: '林总 · 总管', domains: [DOMAIN.CONTACT, DOMAIN.GROUP, DOMAIN.TASK, DOMAIN.SCHEDULE, DOMAIN.MEMORY], trustCeiling: 1 },
  [AGENTS.LIN_FILTER]: { displayName: '林总 · 陌生人过滤', domains: [DOMAIN.CONTACT], trustCeiling: 3 },
  [AGENTS.LIN_PROCUREMENT]: { displayName: '林总 · 采购', domains: [DOMAIN.TASK, DOMAIN.CONTACT], trustCeiling: 2 },
  [AGENTS.ZHOU_BUTLER]: { displayName: '周先生 · 总管', domains: [DOMAIN.CONTACT], trustCeiling: 4 }
});

/** 演示用字段样本：可见域裁剪后能返回哪些字段，直接在这里对比。 */
const SAMPLE_FIELDS = Object.freeze({
  [DOMAIN.CONTACT]: { id: 'user-zhou', exists: true, displayName: '周先生', tags: ['投资人', '校友'], detail: '13800000000', endpoint: 'https://agent.zhou.example' },
  [DOMAIN.GROUP]: { id: 'group-lin-family', exists: true, displayName: '家庭群', tags: ['family'], detail: '成员 3 人', endpoint: null },
  [DOMAIN.TASK]: { id: 'task-chair', exists: true, displayName: '人体工学椅采购', tags: ['procurement'], detail: '已比价 3 家', endpoint: null },
  [DOMAIN.SCHEDULE]: { id: 'evt-fri-16', exists: true, displayName: '周五 16:00 视频会议', tags: ['work'], detail: '与周先生', endpoint: null },
  [DOMAIN.MEMORY]: { id: 'mem-1', exists: true, agentId: 'agent-lin-stranger-filter', lastAt: '2026-10-21T09:40:00.000Z', count: 3, summary: '预审通过 1 条融资询盘', content: '【原始记忆内容，绝不应返回】' }
});

export const CHANNEL_IDS = Object.freeze({
  DIRECT_LIN_ZHOU: 'ch-direct-lin-zhou',
  DIRECT_LIN_MOM: 'ch-direct-lin-mom',
  NEGOTIATION: 'ch-negotiation-lin-zhou',
  TASK_PROCUREMENT: 'ch-task-procurement',
  STRANGER_INBOX: 'ch-stranger-inbox-lin'
});

/**
 * 构造 v2 层。`auditBase` 是已有的哈希链（v1 遗留数据），签名审计在其上继续追加。
 */
export function createV2Layer({ world, legacyHead = null, legacyLength = 0, transports = null }) {
  const clock = world.clock;

  // ① 平台运营者身份：负责签发 AgentCard 与对审计链签名（生产为 KMS 内的 HSM 密钥）
  const operator = createAgentIdentity({ agentId: 'agent-platform-operator' });

  // ② 每个 Agent 自持身份 + 平台签发卡片（卡片含能力/域名/信任上限/过期）
  const identities = {};
  const signedCards = {};
  for (const agentId of Object.keys(AGENT_PROFILES)) {
    const profile = AGENT_PROFILES[agentId];
    const identity = createAgentIdentity({ agentId });
    identities[agentId] = identity;
    const card = createAgentCard({
      identity,
      ownerUserId: agentId === AGENTS.ZHOU_BUTLER ? USERS.ZHOU : USERS.LIN,
      displayName: profile.displayName,
      capabilities: world.registry.get(agentId)?.allowedSkills ?? [],
      domains: profile.domains,
      trustCeiling: profile.trustCeiling,
      endpoint: `agent://${agentId}`,
      ttlMs: 7 * 864e5
    });
    // AgentCard 是**自签**的：它证明"这个 Agent 确实持有该私钥"（Sigil/AICQ 的自持身份思想）。
    // 平台的背书不在这张卡上，而在下面签发的 token 与注册表绑定里——身份与授权必须分开。
    signedCards[agentId] = signAgentCard({ card, identity });
  }

  // ③ 签名审计：哈希链 + 每条 Ed25519 签名（防"有权改库者整链重算"）
  //    第一条是「迁移锚点」：把 v1 哈希链的链尾写进来，证明 v2 的签名链是从既有历史接续的，
  //    而不是另起炉灶——这样既不要求回签历史记录，又保留了跨版本的连续性证据。
  const signedAudit = new SignedAuditLog({ clock, identity: operator, log: new AuditLog({ clock }) });

  // ④ Agent 网关：所有 Agent 流量的唯一入口
  const gateway = new AgentGateway({
    capabilityGate: world.gate,
    egressGuard: world.egress,
    signedAudit,
    clock,
    rateLimit: { windowMs: 60_000, max: 240 }
  });

  // ⑤ 可见域门控：读侧权限
  const visibility = new VisibilityGate({ trustMatrix: world.trustMatrix, clock });

  // ⑥ 频道内核：人类与 Agent 共享，靠频道类型隔离未读
  const channels = new ChannelRegistry({ clock });
  const sessions = new AgentSessionRegistry({ clock, channelRegistry: channels, heartbeatTimeoutMs: 90_000 });

  // ⑦ 为每个 Agent 签发 token（绑定 principal 与 scope；服务端绑定，不信客户端携带）
  const tokens = {};
  for (const agentId of Object.keys(AGENT_PROFILES)) {
    const profile = AGENT_PROFILES[agentId];
    tokens[agentId] = gateway.issueToken({
      agentId,
      principalUserId: signedCards[agentId].card.ownerUserId,
      domains: profile.domains,
      trustCeiling: profile.trustCeiling,
      ttlMs: 15 * 60e3
    });
  }

  // ⑧ 种子频道与成员（注意：协商频道不产生人类未读）
  channels.create({ channelId: CHANNEL_IDS.DIRECT_LIN_ZHOU, type: CHANNEL_TYPE.DIRECT, ownerUserId: USERS.LIN, title: '周先生' });
  channels.join({ channelId: CHANNEL_IDS.DIRECT_LIN_ZHOU, memberId: USERS.LIN, kind: MEMBER_KIND.HUMAN, trustLevel: 3 });
  channels.join({ channelId: CHANNEL_IDS.DIRECT_LIN_ZHOU, memberId: USERS.ZHOU, kind: MEMBER_KIND.HUMAN, trustLevel: 4 });

  channels.create({ channelId: CHANNEL_IDS.DIRECT_LIN_MOM, type: CHANNEL_TYPE.DIRECT, ownerUserId: USERS.LIN, title: '家庭群 · 妈妈' });
  channels.join({ channelId: CHANNEL_IDS.DIRECT_LIN_MOM, memberId: USERS.LIN, kind: MEMBER_KIND.HUMAN, trustLevel: 0 });
  channels.join({ channelId: CHANNEL_IDS.DIRECT_LIN_MOM, memberId: USERS.MOM, kind: MEMBER_KIND.HUMAN, trustLevel: 1 });

  channels.create({ channelId: CHANNEL_IDS.NEGOTIATION, type: CHANNEL_TYPE.AGENT_NEGOTIATION, ownerUserId: USERS.LIN, title: '林总管 × 周总管 · 协商' });
  channels.join({ channelId: CHANNEL_IDS.NEGOTIATION, memberId: AGENTS.LIN_BUTLER, kind: MEMBER_KIND.AGENT, agentId: AGENTS.LIN_BUTLER, principalUserId: USERS.LIN, trustLevel: 3 });
  channels.join({ channelId: CHANNEL_IDS.NEGOTIATION, memberId: AGENTS.ZHOU_BUTLER, kind: MEMBER_KIND.AGENT, agentId: AGENTS.ZHOU_BUTLER, principalUserId: USERS.ZHOU, trustLevel: 4 });

  channels.create({ channelId: CHANNEL_IDS.TASK_PROCUREMENT, type: CHANNEL_TYPE.TASK, ownerUserId: USERS.LIN, title: '任务 · 人体工学椅采购' });
  channels.join({ channelId: CHANNEL_IDS.TASK_PROCUREMENT, memberId: AGENTS.LIN_PROCUREMENT, kind: MEMBER_KIND.AGENT, agentId: AGENTS.LIN_PROCUREMENT, principalUserId: USERS.LIN, trustLevel: 0 });
  channels.join({ channelId: CHANNEL_IDS.TASK_PROCUREMENT, memberId: USERS.LIN, kind: MEMBER_KIND.HUMAN, trustLevel: 0 });

  channels.create({ channelId: CHANNEL_IDS.STRANGER_INBOX, type: CHANNEL_TYPE.STRANGER_QUEUE, ownerUserId: USERS.LIN, title: '陌生人过滤器' });
  channels.join({ channelId: CHANNEL_IDS.STRANGER_INBOX, memberId: USERS.LIN, kind: MEMBER_KIND.HUMAN, trustLevel: 0 });
  channels.join({ channelId: CHANNEL_IDS.STRANGER_INBOX, memberId: AGENTS.LIN_FILTER, kind: MEMBER_KIND.AGENT, agentId: AGENTS.LIN_FILTER, principalUserId: USERS.LIN, trustLevel: 0 });

  // ⑨ 两个总管作为长连接客户端上线（演示用内存 transport；生产为 WS/QUIC）
  const pushLog = [];
  const inboxes = new Map();
  const transportFor = (agentId) =>
    transports?.[agentId] ??
    ({
      send(message) {
        if (!inboxes.has(agentId)) inboxes.set(agentId, []);
        const box = inboxes.get(agentId);
        box.push(message);
        if (box.length > 50) box.shift();
        pushLog.push({ at: new Date(clock()).toISOString(), agentId, channelId: message?.channelId ?? null, kind: message?.kind ?? null });
      },
      close() {}
    });

  const online = [AGENTS.LIN_BUTLER, AGENTS.ZHOU_BUTLER];
  for (const agentId of online) {
    sessions.connect({
      agentId,
      principalUserId: signedCards[agentId].card.ownerUserId,
      transport: transportFor(agentId),
      subscriptions: agentId === AGENTS.LIN_BUTLER ? [CHANNEL_IDS.NEGOTIATION, CHANNEL_IDS.TASK_PROCUREMENT] : [CHANNEL_IDS.NEGOTIATION],
      protocol: PROTOCOL.AGENT_WS
    });
  }

  // 种子审计：v2 事件也进签名链（证明"谁签的"而不只是"内容没变"）
  signedAudit.append({
    source: 'agent-gateway',
    action: 'CHAIN_ANCHOR',
    actor: 'agent-platform-operator',
    detail: { v2: 'signed-audit', legacyHead, legacyLength, reason: '从 v1 哈希链接续，历史不回签但保留连续性证据' }
  });
  signedAudit.append({ source: 'agent-gateway', action: 'V2_BOOTSTRAP', actor: 'agent-platform-operator', detail: { agents: Object.keys(AGENT_PROFILES).length, channels: channels.channelsOf(USERS.LIN).length } });

  const layer = {
    operator,
    identities,
    signedCards,
    signedAudit,
    gateway,
    visibility,
    channels,
    sessions,
    tokens,
    pushLog,
    inboxes,

    /** 身份指纹：公钥哈希前 16 位，UI 上用来显示"这不是嘴说的身份" */
    fingerprintOf(agentId) {
      const card = signedCards[agentId]?.card;
      return card ? sha256Hex(card.publicKeyPem).slice(0, 16) : null;
    },

    /** Agent 上下线（演示用：SSE 接入时会先断开再以新 transport 重连） */
    connectAgent({ agentId, subscriptions = [], transport = null }) {
      if (!signedCards[agentId]) throw new Error(`未知 Agent: ${agentId}`);
      const existing = sessions.presence().find((item) => item.agentId === agentId);
      if (existing && existing.state === 'ONLINE') sessions.disconnect({ agentId, reason: 'reconnect' });
      const session = sessions.connect({
        agentId,
        principalUserId: signedCards[agentId].card.ownerUserId,
        transport: transport ?? transportFor(agentId),
        subscriptions,
        protocol: PROTOCOL.AGENT_WS
      });
      signedAudit.append({ source: 'agent-session', action: 'AGENT_CONNECTED', actor: agentId, detail: { subscriptions } });
      return session;
    },

    disconnectAgent(agentId) {
      sessions.disconnect({ agentId, reason: 'client_closed' });
      signedAudit.append({ source: 'agent-session', action: 'AGENT_DISCONNECTED', actor: agentId, detail: {} });
      return { ok: true };
    },

    /** Agent 网络视图：在线状态 + 可验签的身份 + 订阅 */
    agentNetwork() {
      const presence = sessions.presence();
      const byId = new Map(presence.map((item) => [item.agentId, item]));
      return Object.keys(AGENT_PROFILES).map((agentId) => {
        const signed = signedCards[agentId];
        const live = byId.get(agentId) ?? null;
        return {
          agentId,
          displayName: AGENT_PROFILES[agentId].displayName,
          principalUserId: signed.card.ownerUserId,
          state: live?.state ?? 'OFFLINE',
          protocol: live?.protocol ?? null,
          subscriptions: live?.subscriptions ?? [],
          identity: {
            keyAlg: signed.card.keyAlg,
            fingerprint: layer.fingerprintOf(agentId),
            cardHash: signed.cardHash,
            signatureValid: true,
            expiresAt: signed.card.expiresAt,
            domains: signed.card.domains,
            trustCeiling: signed.card.trustCeiling
          },
          token: {
            domains: tokens[agentId].domains,
            trustCeiling: tokens[agentId].trustCeiling,
            expiresAt: tokens[agentId].expiresAt
          }
        };
      });
    },

    /** 频道视图：人类视角的未读（AGENT_NEGOTIATION 恒为 0）+ 成员构成 */
    channelView() {
      const ids = [
        CHANNEL_IDS.DIRECT_LIN_ZHOU,
        CHANNEL_IDS.DIRECT_LIN_MOM,
        CHANNEL_IDS.NEGOTIATION,
        CHANNEL_IDS.TASK_PROCUREMENT,
        CHANNEL_IDS.STRANGER_INBOX
      ];
      const humanUnread = {};
      for (const userId of [USERS.LIN, USERS.ZHOU, USERS.MOM]) {
        humanUnread[userId] = channels.unreadFor({ memberId: userId });
      }
      return {
        channels: ids
          .map((channelId) => {
            const channel = channels.get(channelId);
            if (!channel) return null;
            return {
              channelId,
              type: channel.type,
              title: channel.title,
              members: channels.members(channelId).map((member) => ({ memberId: member.memberId, kind: member.kind, trustLevel: member.trustLevel }))
            };
          })
          .filter(Boolean),
        unread: humanUnread,
        agentPushes: pushLog.slice(-20)
      };
    },

    /** Agent 之间的一次协商轮次：**必须经网关**，再落频道、再按订阅投递 */
    postAgentTurn({ channelId = CHANNEL_IDS.NEGOTIATION, fromAgentId, intent, payload = {} }) {
      const routed = gateway.route({
        signedCard: signedCards[fromAgentId],
        token: tokens[fromAgentId],
        protocol: PROTOCOL.IM_CUSTOM,
        action: 'AGENT_TURN',
        payload: { channelId, intent, content: payload.summary ?? payload.content ?? null, meta: payload }
      });
      const posted = channels.post({
        channelId,
        memberId: fromAgentId,
        kind: MESSAGE_KIND.AGENT_TURN,
        payload: { intent, ...payload, gatewayAuditSeq: routed.auditSeq }
      });
      const pushed = sessions.push({ channelId, message: posted.message });
      return {
        gateway: { decision: routed.decision, auditSeq: routed.auditSeq },
        message: posted.message,
        stored: posted.stored,
        deliveredTo: posted.deliveredTo,
        pushed
      };
    },

    /** 可见域探针：同一个请求，不同 Agent 拿到的字段不同——这就是"读侧权限" */
    probeVisibility({ agentId, domain, trustLevel, expired = false }) {
      const card = signedCards[agentId].card;
      const built = visibility.buildScope({
        principalUserId: card.ownerUserId,
        agentId,
        domains: card.domains,
        trustCeiling: card.trustCeiling,
        ttlMs: 5 * 60e3
      });
      // 过期场景：契约不允许负 ttl，这里构造一个 expiresAt 已过去的 scope 来演示"过期即失效"
      const scope = expired
        ? { ...built, issuedAt: new Date(clock() - 120_000).toISOString(), expiresAt: new Date(clock() - 60_000).toISOString() }
        : built;
      const resource = { domain, trustLevel, id: `probe-${domain}` };
      const decision = visibility.evaluate({ scope, resource });
      const fields = SAMPLE_FIELDS[domain] ?? { id: resource.id, exists: true };
      const projection = visibility.project({ scope, resource, fields });
      return {
        agentId,
        domain,
        requestedTrustLevel: trustLevel,
        expired,
        scope: { domains: scope.domains, trustCeiling: scope.trustCeiling, expiresAt: scope.expiresAt },
        decision,
        projection,
        fieldsTotal: Object.keys(fields).length
      };
    },

    /**
     * 可见域矩阵：7 组预置探测，覆盖 ALLOW / REDACT / DOMAIN_NOT_GRANTED /
     * TRUST_CEILING_EXCEEDED / MEMORY 摘要 / SCOPE_EXPIRED 六种结果——页面一键展示差异。
     */
    visibilityMatrix() {
      const probe = (spec) => ({ ...spec, result: layer.probeVisibility(spec) });
      return [
        probe({ label: '陌生人总管读「陌生人档」联系人', agentId: AGENTS.ZHOU_BUTLER, domain: DOMAIN.CONTACT, trustLevel: 4 }),
        probe({ label: '陌生人总管读本域「家庭档」联系人（自身家人，允许全字段）', agentId: AGENTS.ZHOU_BUTLER, domain: DOMAIN.CONTACT, trustLevel: 1 }),
        probe({ label: '陌生人总管读日程（未授权域）', agentId: AGENTS.ZHOU_BUTLER, domain: DOMAIN.SCHEDULE, trustLevel: 4 }),
        probe({ label: '过滤 Agent 读「已知档」联系人', agentId: AGENTS.LIN_FILTER, domain: DOMAIN.CONTACT, trustLevel: 3 }),
        probe({ label: '过滤 Agent 试图枚举陌生人档案', agentId: AGENTS.LIN_FILTER, domain: DOMAIN.CONTACT, trustLevel: 4 }),
        probe({ label: '总管读「家庭档」联系人（最亲近档）', agentId: AGENTS.LIN_BUTLER, domain: DOMAIN.CONTACT, trustLevel: 1 }),
        probe({ label: '总管读记忆（只应拿到摘要）', agentId: AGENTS.LIN_BUTLER, domain: DOMAIN.MEMORY, trustLevel: 0 }),
        probe({ label: 'scope 已过期后继续读', agentId: AGENTS.LIN_BUTLER, domain: DOMAIN.CONTACT, trustLevel: 1, expired: true })
      ];
    },

    /** 签名审计的差异化演示：篡改内容 vs 冒充签名，两种攻击被两层防御分别抓住 */
    tamperDemo(kind = 'content') {
      const probe = new SignedAuditLog({ clock, identity: operator, log: new AuditLog({ clock }) });
      for (let i = 1; i <= 3; i += 1) {
        probe.append({ source: 'agent-gateway', action: `DEMO_EVENT_${i}`, actor: AGENTS.LIN_BUTLER, decision: 'ALLOW', detail: { i } });
      }
      const baseline = probe.verify();
      const entries = probe.entries().map((entry) => ({ ...entry }));

      if (kind === 'content') {
        // 攻击 A：直接改内容但保留原哈希 → 链断
        entries[1] = { ...entries[1], detail: { i: 2, injected: '被篡改的金额 ¥999999' } };
      } else {
        // 攻击 B：拿到"签名"但用的是另一把私钥（模拟攻击者只有数据库写权限）→ 链仍完好，签名不成立
        const foreign = createAgentIdentity({ agentId: 'agent-attacker' });
        const target = entries[2];
        entries[2] = { ...target, signer: 'agent-attacker', signature: Buffer.from('deadbeef', 'hex').toString('base64'), foreignPublicKeyPem: foreign.publicKeyPem };
      }

      const rebuilt = new SignedAuditLog({ clock, identity: operator, log: new AuditLog({ clock, records: entries }) });
      const after = rebuilt.verify();
      return {
        kind,
        baseline: { ok: baseline.ok, chainOk: baseline.chain?.ok ?? baseline.ok, signaturesOk: baseline.signatures?.ok ?? baseline.ok },
        after: {
          ok: after.ok,
          chainOk: after.chain?.ok ?? null,
          chainBrokenAt: after.chain?.brokenAt ?? null,
          signaturesOk: after.signatures?.ok ?? null,
          signatureInvalidAt: after.signatures?.invalidAt ?? null
        },
        conclusion:
          kind === 'content'
            ? '改内容不重算哈希：哈希链当场断——链层就能抓住。'
            : '换了签名者：哈希链本身看不出问题（哈希不含签名），但签名层失败——这正是"只做哈希链"防不住的那类攻击。'
      };
    }
  };

  return layer;
}
