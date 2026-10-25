// SPDX-License-Identifier: MIT
//
// 三态 UI 视图模型：审批态 / 巡视态 / 会话态。
//
// 语义源：IM2A Flutter 客户端的三态导航与三个 screen
//   - client/app/lib/main.dart（三态导航）
//   - client/app/lib/screens/approval_screen.dart（审批态：待办 + SSE 卡片流）
//   - client/app/lib/screens/overview_screen.dart（巡视态：Agent 日志/审计/协商时间线）
//   - client/app/lib/screens/conversation_screen.dart（会话态：人类消息/Agent 摘要/审批卡片分流）
//
// 本模块输出**纯视图模型**（无网络/无存储），供 OpenClaw 频道壳或其它前端复用。

export type UiState = "approval" | "overview" | "conversation";

export const UI_STATES: readonly UiState[] = ["approval", "overview", "conversation"];

export const UI_STATE_LABELS: Readonly<Record<UiState, string>> = {
  approval: "审批态",
  overview: "巡视态",
  conversation: "会话态",
};

export type UiEventType =
  | "pendingApproval"
  | "approvalsResolved"
  | "openConversation"
  | "openOverview";

export interface UiEvent {
  type: UiEventType;
}

/** 合法三态迁移表；未列出的组合一律非法（显式抛错，不静默停留）。 */
export const UI_TRANSITIONS: Readonly<Record<UiState, Readonly<Record<string, UiState>>>> = {
  approval: {
    approvalsResolved: "overview",
    openConversation: "conversation",
    openOverview: "overview",
  },
  overview: {
    pendingApproval: "approval",
    openConversation: "conversation",
  },
  conversation: {
    pendingApproval: "approval",
    openOverview: "overview",
  },
};

export class UiTransitionError extends Error {
  readonly from: UiState;
  readonly event: string;
  constructor(from: UiState, event: string) {
    super(`illegal ui transition: ${from} -> ${event}`);
    this.name = "UiTransitionError";
    this.from = from;
    this.event = event;
  }
}

/** 初始三态（默认审批态；可由 channel config.defaultUiState 覆盖）。 */
export function initialUiState(defaultState: string = "approval"): UiState {
  return isUiState(defaultState) ? defaultState : "approval";
}

export function isUiState(value: string): value is UiState {
  return (UI_STATES as readonly string[]).includes(value);
}

/** 执行一次三态迁移；非法迁移抛 UiTransitionError（可判定、可测试）。 */
export function switchState(current: UiState, event: UiEvent): UiState {
  const row = UI_TRANSITIONS[current];
  const next = row?.[event?.type];
  if (!next) {
    throw new UiTransitionError(current, event?.type ?? "<missing>");
  }
  return next;
}

// ---- 审批态 ----

export type TicketState = "PENDING" | "APPROVED" | "REJECTED" | "EXPIRED";

export interface ApprovalTicket {
  ticketId: string;
  title?: string;
  summary?: string;
  agentId?: string;
  state: TicketState;
  deadline?: string;
}

export type ApprovalAction = "approve" | "modify" | "reject";

export interface ApprovalCardModel {
  id: string;
  title: string;
  summary: string;
  agent: string;
  state: TicketState;
  pending: boolean;
  actions: readonly ApprovalAction[];
  deadline?: string;
}

export interface ApprovalView {
  state: "approval";
  cards: readonly ApprovalCardModel[];
  pendingCount: number;
  resolvedCount: number;
}

/** 审批态：待办卡片流（PENDING 给 approve/modify/reject 三个动作；终态只读）。 */
export function buildApprovalView(tickets: readonly ApprovalTicket[]): ApprovalView {
  const cards = tickets.map((t) => {
    const pending = t.state === "PENDING";
    return {
      id: t.ticketId,
      title: t.title ?? "待审批",
      summary: t.summary ?? "",
      agent: t.agentId ?? "agent",
      state: t.state,
      pending,
      actions: pending ? (["approve", "modify", "reject"] as const) : ([] as const),
      deadline: t.deadline,
    };
  });
  return {
    state: "approval",
    cards,
    pendingCount: cards.filter((c) => c.pending).length,
    resolvedCount: cards.filter((c) => !c.pending).length,
  };
}

// ---- 巡视态 ----

export interface OverviewLogEntry {
  at: string;
  agent: string;
  action: string;
  detail?: string;
}

export interface NegotiationEntry {
  id: string;
  state: string;
  round: number;
}

export interface OverviewTimelineItem {
  kind: "log" | "negotiation";
  at?: string;
  text: string;
}

export interface OverviewView {
  state: "overview";
  timeline: readonly OverviewTimelineItem[];
  logCount: number;
  negotiationCount: number;
}

/** 巡视态：Agent 日志 + 协商时间线（只读巡视，不产生副作用）。 */
export function buildOverviewView(
  logs: readonly OverviewLogEntry[],
  negotiations: readonly NegotiationEntry[] = [],
): OverviewView {
  const timeline: OverviewTimelineItem[] = [
    ...logs.map((l) => ({
      kind: "log" as const,
      at: l.at,
      text: `${l.agent} ${l.action}${l.detail ? `: ${l.detail}` : ""}`,
    })),
    ...negotiations.map((n) => ({
      kind: "negotiation" as const,
      text: `negotiation ${n.id} ${n.state} round=${n.round}`,
    })),
  ];
  return {
    state: "overview",
    timeline,
    logCount: logs.length,
    negotiationCount: negotiations.length,
  };
}

// ---- 会话态 ----
// 与 Flutter ChatMsg.kind 对齐：0=人类消息 1=Agent 摘要 2=审批卡片。

export type ConversationKind = 0 | 1 | 2;

export interface ConversationMessage {
  id: string;
  from: string;
  text: string;
  kind: ConversationKind;
  mine?: boolean;
}

export interface ConversationView {
  state: "conversation";
  messages: readonly ConversationMessage[];
  humanCount: number;
  agentSummaryCount: number;
  approvalCardCount: number;
}

/** 会话态：人类消息 / Agent 摘要 / 审批卡片物理分流（不合并正文）。 */
export function buildConversationView(messages: readonly ConversationMessage[]): ConversationView {
  for (const m of messages) {
    if (m.kind !== 0 && m.kind !== 1 && m.kind !== 2) {
      throw new Error(`invalid conversation message kind: ${String(m.kind)}`);
    }
  }
  return {
    state: "conversation",
    messages,
    humanCount: messages.filter((m) => m.kind === 0).length,
    agentSummaryCount: messages.filter((m) => m.kind === 1).length,
    approvalCardCount: messages.filter((m) => m.kind === 2).length,
  };
}

export type UiViewModel = ApprovalView | OverviewView | ConversationView;
