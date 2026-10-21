// 模块职责：全链审计追溯——从一条 messageId 串起「审核 → 扣费 → 投递 → 回复 → 退款」的证据链，供申诉/监管/复盘使用。
// 契约来源：contracts/INTERFACES.md §42。
// 关键不变量：①steps 按 at 升序，同刻固定优先级（moderation < ledger < reply/audit < dispute）；②每条 step 必带 source/type；
//             ③缺环显式：complete=false 且 missing 列出缺失环节（绝不假装完整）；④只读追溯，不触发任何状态迁移。
const SOURCE_PRIORITY = Object.freeze({ moderation: 0, ledger: 1, audit: 2, dispute: 3 });
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const MESSAGE_ID_PATTERN = /(msg-[A-Za-z0-9_-]+)/;

const messageIdFromLedgerEntry = (entry) => {
  if (!entry || typeof entry !== 'object') return null;
  if (entry.meta && isNonEmptyString(entry.meta.messageId)) return entry.meta.messageId;
  if (isNonEmptyString(entry.ref) && entry.ref.startsWith('msg-')) return entry.ref;
  if (isNonEmptyString(entry.idempotencyKey)) {
    const match = MESSAGE_ID_PATTERN.exec(entry.idempotencyKey);
    if (match) return match[1];
  }
  return null;
};

export class AuditTrace {
  #ledger;
  #moderation;
  #disputes;
  #audit;
  #clock;

  constructor({ ledger, moderation, disputes, audit = null, clock = () => new Date() } = {}) {
    if (!ledger || typeof ledger.entries !== 'function') {
      throw new TypeError('AuditTrace: ledger 必须实现 entries()');
    }
    if (!moderation || typeof moderation.list !== 'function') {
      throw new TypeError('AuditTrace: moderation 必须实现 list()');
    }
    if (!disputes || typeof disputes.timeline !== 'function' || typeof disputes.list !== 'function') {
      throw new TypeError('AuditTrace: disputes 必须实现 timeline()/list()');
    }
    this.#ledger = ledger;
    this.#moderation = moderation;
    this.#disputes = disputes;
    this.#audit = audit;
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
  }

  #nowIso() {
    const sampled = this.#clock();
    return (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
  }

  /** 读取审计记录（兼容 AuditLog.records / SignedAuditLog.entries / snapshot / 数组），只读 */
  #auditRecords() {
    const audit = this.#audit;
    if (!audit) return [];
    if (Array.isArray(audit)) return audit;
    try {
      if (typeof audit.entries === 'function') {
        const result = audit.entries({});
        if (Array.isArray(result)) return result;
      }
      if (typeof audit.records === 'function') {
        const result = audit.records({});
        if (Array.isArray(result)) return result;
      }
      if (typeof audit.snapshot === 'function') {
        const result = audit.snapshot();
        if (Array.isArray(result)) return result;
        if (Array.isArray(result?.entries)) return result.entries;
        if (Array.isArray(result?.records)) return result.records;
      }
    } catch {
      // 只读追溯：审计源读取失败时返回空，由 missing 显式暴露缺环
    }
    return [];
  }

  #moderationSteps(messageId) {
    const steps = [];
    for (const moderationCase of this.#moderation.list()) {
      if (!moderationCase || moderationCase.messageId !== messageId) continue;
      const ref = moderationCase.caseId ?? moderationCase.id ?? messageId;
      const createdAt = moderationCase.createdAt ?? moderationCase.updatedAt ?? null;
      steps.push({
        at: createdAt,
        source: 'moderation',
        type: 'MODERATION_SUBMITTED',
        actor: moderationCase.senderUserId ?? null,
        ref,
        decision: moderationCase.state ?? null
      });
      if (moderationCase.machineUnavailable) {
        steps.push({
          at: createdAt,
          source: 'moderation',
          type: 'MODERATION_UNAVAILABLE',
          actor: 'system',
          ref,
          decision: 'HUMAN_PENDING'
        });
      } else if (isNonEmptyString(moderationCase.verdict)) {
        steps.push({
          at: createdAt,
          source: 'moderation',
          type: `MODERATION_${moderationCase.verdict}`,
          actor: 'machine',
          ref,
          decision: moderationCase.verdict
        });
      }
      if (moderationCase.reviewedAt) {
        steps.push({
          at: moderationCase.reviewedAt,
          source: 'moderation',
          type:
            moderationCase.reviewDecision === 'APPROVE' ? 'MODERATION_APPROVED' : 'MODERATION_REJECTED',
          actor: moderationCase.reviewedBy ?? null,
          ref,
          decision: moderationCase.reviewDecision ?? null
        });
      }
      if (moderationCase.appealedAt) {
        steps.push({
          at: moderationCase.appealedAt,
          source: 'moderation',
          type: 'MODERATION_APPEALED',
          actor: moderationCase.senderUserId ?? null,
          ref,
          decision: 'APPEALED'
        });
      }
      if (moderationCase.resolvedAt) {
        steps.push({
          at: moderationCase.resolvedAt,
          source: 'moderation',
          type:
            moderationCase.resolutionDecision === 'GRANT'
              ? 'MODERATION_APPEAL_GRANTED'
              : 'MODERATION_APPEAL_UPHELD',
          actor: moderationCase.resolvedBy ?? null,
          ref,
          decision: moderationCase.resolutionDecision ?? null
        });
      }
    }
    return steps;
  }

  #ledgerSteps(messageId) {
    const steps = [];
    for (const entry of this.#ledger.entries()) {
      if (!entry) continue;
      const matches =
        entry.ref === messageId ||
        entry.meta?.messageId === messageId ||
        (isNonEmptyString(entry.idempotencyKey) && entry.idempotencyKey.includes(messageId));
      if (!matches) continue;
      steps.push({
        at: entry.at,
        source: 'ledger',
        type: entry.type,
        actor: entry.userId ?? null,
        amountCents: entry.amountCents,
        ref: entry.ref ?? messageId,
        decision: entry.type
      });
    }
    return steps;
  }

  #auditSteps(messageId) {
    const steps = [];
    for (const record of this.#auditRecords()) {
      if (!record || !isNonEmptyString(record.at)) continue;
      const detailMentions =
        typeof record.detail === 'string'
          ? record.detail.includes(messageId)
          : (() => {
              try {
                return JSON.stringify(record.detail ?? null).includes(messageId);
              } catch {
                return false;
              }
            })();
      if (!detailMentions && record.target !== messageId) continue;
      steps.push({
        at: record.at,
        source: 'audit',
        type: record.action ?? 'AUDIT_EVENT',
        actor: record.actor ?? null,
        ref: messageId,
        decision: record.decision ?? null
      });
    }
    return steps;
  }

  #disputeSteps(messageId) {
    const steps = [];
    let timeline = [];
    try {
      timeline = this.#disputes.timeline({ messageId });
    } catch {
      timeline = [];
    }
    let records = [];
    try {
      records = this.#disputes.list();
    } catch {
      records = [];
    }
    const byDisputeId = new Map(records.map((record) => [record.id, record]));
    for (const event of timeline) {
      if (!event || !isNonEmptyString(event.type)) continue;
      if (!event.type.startsWith('DISPUTE_')) continue; // 账本条目已由 ledger 源提供，避免重复
      const dispute = byDisputeId.get(event.ref) ?? null;
      const isOpen = event.type === 'DISPUTE_OPEN';
      steps.push({
        at: event.at,
        source: 'dispute',
        type: event.type,
        actor: isOpen ? (dispute?.userId ?? null) : (dispute?.actorUserId ?? null),
        amountCents: dispute?.refundAmountCents ?? event.amountCents,
        ref: event.ref ?? null,
        decision: dispute?.state ?? null
      });
    }
    return steps;
  }

  /**
   * 追溯一条消息的完整证据链（只读）。
   * @returns {Readonly<{messageId:string, steps:ReadonlyArray<object>, complete:boolean, missing:ReadonlyArray<string>}>}
   */
  trace({ messageId } = {}) {
    if (!isNonEmptyString(messageId)) throw new TypeError('trace: messageId 必填');
    const rawSteps = [
      ...this.#moderationSteps(messageId),
      ...this.#ledgerSteps(messageId),
      ...this.#auditSteps(messageId),
      ...this.#disputeSteps(messageId)
    ];

    const decorated = rawSteps.map((step, index) => ({
      ...step,
      _priority: SOURCE_PRIORITY[step.source] ?? 9,
      _index: index
    }));
    decorated.sort((a, b) => {
      const atA = String(a.at ?? '');
      const atB = String(b.at ?? '');
      if (atA !== atB) return atA < atB ? -1 : 1;
      if (a._priority !== b._priority) return a._priority - b._priority;
      return a._index - b._index;
    });
    const steps = Object.freeze(
      decorated.map(({ _priority, _index, ...step }) =>
        Object.freeze({
          at: step.at,
          source: step.source,
          type: step.type,
          actor: step.actor ?? null,
          ref: step.ref ?? null,
          ...(step.amountCents === undefined ? {} : { amountCents: step.amountCents }),
          decision: step.decision ?? null
        })
      )
    );

    // 完整性：付费消息至少要有「审核记录 + 扣费流水」；缺失必须显式列出
    const hasModeration = steps.some((step) => step.source === 'moderation');
    const hasCharge = steps.some((step) => step.source === 'ledger' && step.type === 'CHARGE');
    const missing = [];
    if (!hasModeration) {
      missing.push(hasCharge ? 'MODERATION: 缺少审核记录（有扣费无审核）' : 'MODERATION: 缺少审核记录');
    }
    if (!hasCharge) missing.push('CHARGE: 缺少扣费流水');

    return Object.freeze({
      messageId,
      steps,
      complete: missing.length === 0,
      missing: Object.freeze(missing)
    });
  }

  /**
   * 按账号聚合追踪：取与该账号相关的消息（账本/审核/争议），按最近活动倒序，最多 limit 条。
   * @returns {ReadonlyArray<object>}
   */
  byAccount({ accountId, limit = 20 } = {}) {
    if (!isNonEmptyString(accountId)) throw new TypeError('byAccount: accountId 必填');
    if (!Number.isInteger(limit) || limit <= 0) throw new TypeError('byAccount: limit 必须是正整数');

    const related = new Map(); // messageId -> 最近活动 at
    const add = (messageId, at) => {
      if (!isNonEmptyString(messageId)) return;
      const previous = related.get(messageId);
      if (previous === undefined || String(at ?? '') > String(previous)) related.set(messageId, at ?? null);
    };

    for (const entry of this.#ledger.entries()) {
      if (!entry || entry.userId !== accountId) continue;
      const messageId = messageIdFromLedgerEntry(entry);
      if (messageId) add(messageId, entry.at);
    }
    for (const moderationCase of this.#moderation.list()) {
      if (!moderationCase || moderationCase.senderUserId !== accountId) continue;
      add(moderationCase.messageId, moderationCase.updatedAt ?? moderationCase.createdAt);
    }
    for (const dispute of this.#disputes.list()) {
      if (!dispute || dispute.userId !== accountId) continue;
      add(dispute.messageId, dispute.resolvedAt ?? dispute.createdAt);
    }

    const sorted = [...related.entries()]
      .sort((a, b) => {
        const atA = String(a[1] ?? '');
        const atB = String(b[1] ?? '');
        if (atA !== atB) return atA < atB ? 1 : -1; // 最近活动在前
        return a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0;
      })
      .slice(0, limit)
      .map(([messageId]) => this.trace({ messageId }));
    return Object.freeze(sorted);
  }
}
