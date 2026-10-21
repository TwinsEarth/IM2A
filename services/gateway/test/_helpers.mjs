// 测试辅助：可推进假时钟 + 一套接好线的 v2 数据平面（identity/card/token/gate/egress/signed-audit）。
import { AuditLog } from '../../audit/src/audit-log.mjs';
import { SignedAuditLog } from '../../audit/src/signed-audit.mjs';
import { AgentGateway, PROTOCOL } from '../src/agent-gateway.mjs';
import { createAgentCard, createAgentIdentity, signAgentCard } from '../../identity/src/agent-id.mjs';
import {
  AgentRegistry,
  CapabilityGate,
  DailyBudget,
  EgressGuard,
  TrustMatrix
} from '../../orchestrator/src/index.mjs';

export function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.iso = () => new Date(currentMs).toISOString();
  clock.currentMs = () => currentMs;
  return clock;
}

/** 造一个独立身份 + 已签名卡片（可指定平台名 / ttl / owner） */
export function buildAgent({
  clock = makeClock(),
  ownerUserId = 'user-a',
  ttlMs = 7 * 864e5,
  displayName = '小助手',
  agentId = null
} = {}) {
  const identity = createAgentIdentity({ clock, agentId });
  const card = createAgentCard({ identity, ownerUserId, displayName, ttlMs, clock });
  const signedCard = signAgentCard({ card, identity });
  return { identity, card, signedCard };
}

/**
 * 接好线的网关系统：
 *  - 主 Agent 的 registry 能力：read / pay / delete-file（破坏性）
 *  - 预算 10000 分、审批阈值 5000 分
 *  - SignedAuditLog 作为唯一审计写入点
 */
export function buildSystem({ rateLimit = { windowMs: 60_000, max: 120 }, clock = makeClock(), agentId = null } = {}) {
  const { identity, card, signedCard } = buildAgent({ clock, ownerUserId: 'user-a', agentId });
  const audit = new SignedAuditLog({ clock, identity });
  const registry = new AgentRegistry();
  registry.register({
    agentId: identity.agentId,
    ownerUserId: 'user-a',
    kind: 'butler',
    allowedSkills: ['read', 'pay', 'delete-file'],
    destructiveSkills: ['delete-file'],
    trustZoneMin: 4,
    budgetCentsPerDay: 10000,
    approvalThresholdCents: 5000
  });
  const trustMatrix = new TrustMatrix({ clock });
  const budget = new DailyBudget({ clock });
  budget.setCap(identity.agentId, 10000);
  const capabilityGate = new CapabilityGate({ registry, trustMatrix, budget, clock });
  const egressGuard = new EgressGuard({ clock });
  const gateway = new AgentGateway({ capabilityGate, egressGuard, signedAudit: audit, clock, rateLimit });
  const token = gateway.issueToken({ agentId: identity.agentId, principalUserId: 'user-a' });
  const routeArgs = (payload = {}, overrides = {}) => ({
    signedCard,
    token,
    protocol: PROTOCOL.AGENT_WS,
    action: 'skill.invoke',
    payload,
    ...overrides
  });
  return {
    clock,
    identity,
    card,
    signedCard,
    token,
    audit,
    registry,
    trustMatrix,
    budget,
    capabilityGate,
    egressGuard,
    gateway,
    routeArgs,
    AuditLog
  };
}
