// SPDX-License-Identifier: MIT
//
// 付费注意力状态机：IM2A 核心语义的 TypeScript 实现（纯内存、可注入时钟、零网络）。
//
// 语义源（以 core 为准）：
//   - internal/api/paid_flow.go（预检顺序、billingMode 1|2、reject/expire/open 行为、
//     blacklist/pricing/auto-reject 阈值）
//   - internal/ledger/ledger.go（账本 PENDING→CHARGED→REFUNDED；message_id 幂等）
//
// 与 core handler 的两处已知差异（不是规则变更，是"库 vs HTTP handler"的边界）：
//   1) 重复 reject：core handler 会因 ledger 已 REFUNDED 报错；本状态机把同一 messageId 的
//      相同操作视为**幂等重放**（返回首次结果 + idempotent:true），符合"同一 message_id 不二次扣费"；
//   2) autoAcceptBelowCents 在 core v2.3.25 的 send 路径中**未被使用**（只存偏好）；本实现同样不启用。
// 两类差异在 README「与 core 的边界」中明示，若 core 语义变更需同步 bump 版本。

export type BillingMode = 1 | 2;

/** 付费消息状态（paid_flow.go 的 paidMessage.status）。 */
export type PaidMessageStatus = "charged" | "delivered" | "rejected" | "expired";

/** 发送前置决策态（paid_flow.go 的 PaidSendResponse.status，不是消息状态）。 */
export type DecisionStatus = "blocked" | "below_price" | "auto_rejected";

/** 账本状态（ledger.Status：pending→charged→refunded）。 */
export type LedgerStatus = "pending" | "charged" | "refunded";

/** 注意力级别 0..3（低/中/高/紧急；core 的 AttentionLevel 语义）。 */
export type AttentionLevel = 0 | 1 | 2 | 3;

export const ATTENTION_LEVEL_NAMES = ["low", "medium", "high", "urgent"] as const;

export interface ReceiverPolicy {
  /** 接收方最低定价（分）；0/缺省 = 未设置。 */
  minPriceCents?: number;
  /** 出价 ≥ 此值自动拒收（防高价骚扰）；0/缺省 = 不启用。 */
  autoRejectAboveCents?: number;
  /** 接收方黑名单（senderId 字符串形式）。 */
  blockedSenders?: readonly string[];
  /** core 的 PG blacklist 强制拦截结果（由调用方查库后传入；缺省 false）。 */
  senderBlacklisted?: boolean;
}

export interface SendInput {
  messageId: string;
  senderId: string | number;
  receiverId: string | number;
  content: string;
  billingMode: BillingMode;
  amountCents: number;
  attentionLevel?: AttentionLevel;
  /** compliance 预审是否通过；缺省 true（调用方必须先做预审，付费永远在预审之后）。 */
  compliancePassed?: boolean;
}

export interface SendAccepted {
  status: PaidMessageStatus;
  messageId: string;
  ledgerStatus: LedgerStatus;
  idempotent?: boolean;
}

export interface SendRejected {
  status: DecisionStatus;
  messageId: "";
  reason: string;
  idempotent?: boolean;
}

export type SendResult = SendAccepted | SendRejected;

export interface OpenResult {
  messageId: string;
  status: "opened" | "already_charged" | "charged";
  charged: boolean;
  idempotent?: boolean;
}

export interface RejectResult {
  messageId: string;
  status: "rejected";
  refunded: boolean;
  idempotent?: boolean;
}

export interface ExpireResult {
  expiredCount: number;
  messageIds: string[];
}

export interface PaidEvent {
  type: "paid_message" | "ledger" | "decision";
  messageId: string;
  status?: string;
  ledgerStatus?: LedgerStatus;
  reason?: string;
  atUnix: number;
}

export type PaidAttentionErrorCode = "NOT_FOUND" | "ILLEGAL_TRANSITION" | "INVALID_INPUT";

export class PaidAttentionError extends Error {
  readonly code: PaidAttentionErrorCode;
  constructor(code: PaidAttentionErrorCode, message: string) {
    super(message);
    this.name = "PaidAttentionError";
    this.code = code;
  }
}

interface PaidMessageRecord {
  messageId: string;
  senderId: string;
  receiverId: string;
  billingMode: BillingMode;
  amountCents: number;
  status: PaidMessageStatus;
  ledgerStatus: LedgerStatus;
  attentionLevel: AttentionLevel;
  createdAtUnix: number;
  refunded: boolean;
}

/**
 * 合法状态迁移表（供文档/测试枚举；`null` = 该事件在语义上不改变消息状态）。
 * 事件：send(1|2) / open / reject / expire。
 */
export const PAID_TRANSITIONS: Readonly<Record<string, Readonly<Record<string, string | null>>>> = {
  "": { send_mode1: "charged", send_mode2: "delivered" },
  charged: { open: null, reject: "rejected", expire: null },
  delivered: { open: null, reject: "rejected", expire: "expired" },
  rejected: { reject: "rejected" },
  expired: {},
};

export interface PaidAttentionOptions {
  /** 当前时间（秒）；缺省 Date.now()/1000。测试注入确定性时钟。 */
  now?: () => number;
  /** 过期默认 age（秒）；与 core 一致 86400。 */
  defaultMaxAgeSec?: number;
}

/**
 * 付费注意力状态机：过程内幂等、事件可观测。
 * 每个 messageId 的 send/open/reject/expire 操作以 `op:messageId` 为幂等键重放，
 * 同一操作重复执行不会二次扣费/退款；不同非法操作返回 PaidAttentionError。
 */
export class PaidAttentionMachine {
  private readonly now: () => number;
  private readonly defaultMaxAgeSec: number;
  private readonly messages = new Map<string, PaidMessageRecord>();
  private readonly replay = new Map<string, unknown>();
  private readonly eventLog: PaidEvent[] = [];

  constructor(options: PaidAttentionOptions = {}) {
    this.now = options.now ?? (() => Math.floor(Date.now() / 1000));
    this.defaultMaxAgeSec = options.defaultMaxAgeSec ?? 86400;
  }

  get events(): readonly PaidEvent[] {
    return this.eventLog;
  }

  get(messageId: string): Readonly<PaidMessageRecord> | undefined {
    return this.messages.get(messageId);
  }

  send(input: SendInput, policy: ReceiverPolicy = {}): SendResult {
    this.validateSend(input);
    const key = `send:${input.messageId}`;
    const replayed = this.replay.get(key);
    if (replayed !== undefined) {
      return { ...(replayed as SendResult), idempotent: true };
    }
    const result = this.doSend(input, policy);
    this.replay.set(key, result);
    return result;
  }

  open(messageId: string): OpenResult {
    const key = `open:${messageId}`;
    const replayed = this.replay.get(key);
    if (replayed !== undefined) {
      return { ...(replayed as OpenResult), idempotent: true };
    }
    const rec = this.requireMessage(messageId);
    if (rec.status === "rejected" || rec.status === "expired") {
      throw new PaidAttentionError("ILLEGAL_TRANSITION", `cannot open message in status ${rec.status}`);
    }
    let result: OpenResult;
    if (rec.billingMode === 1) {
      // 发送即扣费：打开不重复扣。
      result = { messageId, status: "opened", charged: false };
    } else if (rec.ledgerStatus === "charged") {
      result = { messageId, status: "already_charged", charged: true };
    } else {
      // billingMode=2：接收方打开才扣费；消息状态在 core 中保持 delivered。
      this.setLedger(rec, "charged");
      this.emit({ type: "ledger", messageId, ledgerStatus: "charged" });
      result = { messageId, status: "charged", charged: true };
    }
    this.replay.set(key, result);
    return result;
  }

  reject(messageId: string, reason = ""): RejectResult {
    const key = `reject:${messageId}`;
    const replayed = this.replay.get(key);
    if (replayed !== undefined) {
      return { ...(replayed as RejectResult), idempotent: true };
    }
    const rec = this.requireMessage(messageId);
    if (rec.status === "expired") {
      throw new PaidAttentionError("ILLEGAL_TRANSITION", "cannot reject an expired message");
    }
    let refunded = false;
    if (rec.ledgerStatus === "charged") {
      this.setLedger(rec, "refunded");
      rec.refunded = true;
      refunded = true;
    }
    const from = rec.status;
    rec.status = "rejected";
    this.emit({ type: "paid_message", messageId, status: "rejected", reason });
    if (from !== "rejected") {
      this.emit({ type: "ledger", messageId, ledgerStatus: rec.ledgerStatus });
    }
    const result: RejectResult = { messageId, status: "rejected", refunded };
    this.replay.set(key, result);
    return result;
  }

  expire(nowUnix?: number, maxAgeSec?: number): ExpireResult {
    const now = nowUnix ?? this.now();
    const maxAge = maxAgeSec ?? this.defaultMaxAgeSec;
    if (maxAge <= 0) {
      throw new PaidAttentionError("INVALID_INPUT", "maxAgeSec must be positive");
    }
    const ids: string[] = [];
    for (const rec of this.messages.values()) {
      // core 只清理 paidMessage.status === "delivered" 的消息（mode1 的 charged 不清理）。
      if (rec.status !== "delivered") continue;
      if (now - rec.createdAtUnix < maxAge) continue;
      if (rec.ledgerStatus === "charged") {
        this.setLedger(rec, "refunded");
        rec.refunded = true;
        this.emit({ type: "ledger", messageId: rec.messageId, ledgerStatus: "refunded" });
      }
      rec.status = "expired";
      ids.push(rec.messageId);
      this.emit({ type: "paid_message", messageId: rec.messageId, status: "expired" });
    }
    return { expiredCount: ids.length, messageIds: ids };
  }

  private validateSend(input: SendInput): void {
    if (!input || typeof input.messageId !== "string" || input.messageId.length === 0) {
      throw new PaidAttentionError("INVALID_INPUT", "messageId required");
    }
    if (input.billingMode !== 1 && input.billingMode !== 2) {
      throw new PaidAttentionError("INVALID_INPUT", "billingMode must be 1 or 2");
    }
    if (typeof input.amountCents !== "number" || !Number.isFinite(input.amountCents) || input.amountCents <= 0) {
      throw new PaidAttentionError("INVALID_INPUT", "amountCents must be positive");
    }
    const level = input.attentionLevel ?? 0;
    if (!Number.isInteger(level) || level < 0 || level > 3) {
      throw new PaidAttentionError("INVALID_INPUT", "attentionLevel must be integer 0..3");
    }
  }

  private doSend(input: SendInput, policy: ReceiverPolicy): SendResult {
    const senderId = String(input.senderId);

    // 1) compliance 预审（不可妥协原则：付费永远在预审之后）。
    if (input.compliancePassed === false) {
      return this.decision("blocked", "compliance pre-check rejected");
    }
    // 2) 接收方定价。
    const minPrice = policy.minPriceCents ?? 0;
    if (minPrice > 0 && input.amountCents < minPrice) {
      return this.decision("below_price", `receiver requires at least ${minPrice} cents`);
    }
    // 3) 黑名单（内存 blocklist + core 的 PG blacklist 结果）。
    const blocked = policy.senderBlacklisted === true || (policy.blockedSenders ?? []).includes(senderId);
    if (blocked) {
      return this.decision("blocked", "sender is blocked by receiver");
    }
    // 4) 自动拒收阈值（防高价骚扰；严格 >=）。
    const autoRejectAbove = policy.autoRejectAboveCents ?? 0;
    if (autoRejectAbove > 0 && input.amountCents >= autoRejectAbove) {
      return this.decision("auto_rejected", "above receiver's auto-reject threshold");
    }

    const attentionLevel = (input.attentionLevel ?? 0) as AttentionLevel;
    const ledgerStatus: LedgerStatus = input.billingMode === 1 ? "charged" : "pending";
    const status: PaidMessageStatus = input.billingMode === 1 ? "charged" : "delivered";
    const rec: PaidMessageRecord = {
      messageId: input.messageId,
      senderId,
      receiverId: String(input.receiverId),
      billingMode: input.billingMode,
      amountCents: input.amountCents,
      status,
      ledgerStatus,
      attentionLevel,
      createdAtUnix: this.now(),
      refunded: false,
    };
    this.messages.set(input.messageId, rec);
    this.emit({ type: "decision", messageId: input.messageId, status, reason: "paid_message_sent" });
    this.emit({ type: "ledger", messageId: input.messageId, ledgerStatus });
    return { status, messageId: input.messageId, ledgerStatus };
  }

  private decision(status: DecisionStatus, reason: string): SendRejected {
    this.emit({ type: "decision", messageId: "", status, reason });
    return { status, messageId: "", reason };
  }

  private requireMessage(messageId: string): PaidMessageRecord {
    const rec = this.messages.get(messageId);
    if (!rec) {
      throw new PaidAttentionError("NOT_FOUND", `message not found: ${messageId}`);
    }
    return rec;
  }

  private setLedger(rec: PaidMessageRecord, status: LedgerStatus): void {
    rec.ledgerStatus = status;
  }

  private emit(event: Omit<PaidEvent, "atUnix">): void {
    this.eventLog.push({ ...event, atUnix: this.now() });
  }
}
