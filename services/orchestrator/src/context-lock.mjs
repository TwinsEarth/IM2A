// 模块职责：确认锁定的 Context（MyContext 式）——Agent 只能 propose，关键结论的写入权在用户；用户确认后永久锁定，任何子 Agent 不得覆写。
// 契约来源：contracts/INTERFACES.md §34。
// 关键不变量：①只有 ownerUserId 能 confirm/unlock（否则 FORBIDDEN）；②LOCKED 的值不可被任何 propose 覆写（不同值只能上抛 CONFLICT）；
//             ③冲突不自动合并、必须上抛到人；④unlock 是唯一解锁路径且必须给 reason 并留审计。
import { canonicalize } from '../../identity/src/agent-id.mjs';

export const LOCK_STATE = Object.freeze({ PENDING: 'PENDING', LOCKED: 'LOCKED', CONFLICT: 'CONFLICT' });

export const CONTEXT_LOCK_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  ILLEGAL_STATE: 'ILLEGAL_STATE',
  LOCK_IMMUTABLE: 'LOCK_IMMUTABLE',
  CONFLICT_PENDING: 'CONFLICT_PENDING',
  FORBIDDEN: 'FORBIDDEN',
  INVALID_VALUE: 'INVALID_VALUE'
});

const CODE = CONTEXT_LOCK_CODE;
const LOCK_STATES = new Set(Object.values(LOCK_STATE));
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

const freezeEvidence = (value) => Object.freeze(Array.isArray(value) ? [...value] : []);

/** 值等价：用与审计链一致的稳定序列化，保证对象键序差异不算不同值 */
const sameValue = (a, b) => canonicalize(a) === canonicalize(b);

export class ContextLockError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'ContextLockError';
    this.code = code;
  }
}

export class ContextLock {
  #ownerUserId;
  #clock;
  #onAudit;
  /** @type {Map<string, object>} key -> frozen internal entry（PENDING/LOCKED/CONFLICT） */
  #entries;
  /** @type {ReadonlyArray<object>} 只追加审计事件 */
  #events;

  constructor({ ownerUserId, clock = () => new Date(), onAudit = () => {} } = {}) {
    if (!isNonEmptyString(ownerUserId)) throw new TypeError('ContextLock: ownerUserId 必填');
    this.#ownerUserId = ownerUserId;
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#onAudit = typeof onAudit === 'function' ? onAudit : () => {};
    this.#entries = new Map();
    this.#events = [];
  }

  #nowIso() {
    const sampled = this.#clock();
    return (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
  }

  #emit({ actor, action, target, decision, detail = {} }) {
    const event = Object.freeze({
      at: this.#nowIso(),
      actor: actor ?? null,
      action,
      target,
      decision,
      detail: Object.freeze({ ...detail })
    });
    this.#events.push(event);
    this.#onAudit(event);
    return event;
  }

  #require(key) {
    const entry = this.#entries.get(key);
    if (!entry) throw new ContextLockError(`Context key 不存在: ${String(key)}`, CODE.NOT_FOUND);
    return entry;
  }

  #assertOwner(byUserId, action) {
    if (!isNonEmptyString(byUserId) || byUserId !== this.#ownerUserId) {
      throw new ContextLockError(`${action} 只有属主 ${this.#ownerUserId} 可以执行（FORBIDDEN）`, CODE.FORBIDDEN);
    }
  }

  #assertKeyValue({ key, value }) {
    if (!isNonEmptyString(key)) {
      throw new ContextLockError('key 必须是非空字符串', CODE.INVALID_VALUE);
    }
    if (value === undefined) {
      throw new ContextLockError('value 不能是 undefined', CODE.INVALID_VALUE);
    }
  }

  /** 对外只读视图（read/confirm/propose 统一返回口径） */
  #view(entry) {
    if (entry.state === LOCK_STATE.PENDING) {
      return Object.freeze({
        key: entry.key,
        state: LOCK_STATE.PENDING,
        value: entry.value,
        lockedBy: null,
        lockedAt: null,
        candidate: null,
        sourceAgentId: entry.sourceAgentId,
        evidence: entry.evidence
      });
    }
    if (entry.state === LOCK_STATE.LOCKED) {
      return Object.freeze({
        key: entry.key,
        state: LOCK_STATE.LOCKED,
        value: entry.value,
        lockedBy: entry.lockedBy,
        lockedAt: entry.lockedAt,
        candidate: null
      });
    }
    return Object.freeze({
      key: entry.key,
      state: LOCK_STATE.CONFLICT,
      value: entry.value, // 始终是原锁定值（CONFLICT 不得改写它）
      lockedBy: entry.lockedBy,
      lockedAt: entry.lockedAt,
      candidate: Object.freeze({
        value: entry.candidate.value,
        sourceAgentId: entry.candidate.sourceAgentId,
        evidence: entry.candidate.evidence,
        proposedAt: entry.candidate.proposedAt
      })
    });
  }

  /**
   * Agent 提议（无写入权）。
   * 未存在 → PENDING；已 LOCKED 且值相同 → 幂等 LOCKED；已 LOCKED 且值不同 → CONFLICT（原值不变）。
   */
  propose({ key, value, sourceAgentId, evidence = [] } = {}) {
    this.#assertKeyValue({ key, value });
    if (!isNonEmptyString(sourceAgentId)) {
      throw new ContextLockError('sourceAgentId 必填（必须能追溯是哪个 Agent 提的）', CODE.INVALID_VALUE);
    }
    const nowIso = this.#nowIso();
    const existing = this.#entries.get(key) ?? null;

    if (!existing) {
      const entry = Object.freeze({
        key,
        state: LOCK_STATE.PENDING,
        value,
        sourceAgentId,
        evidence: freezeEvidence(evidence),
        proposedAt: nowIso,
        updatedAt: nowIso
      });
      this.#entries.set(key, entry);
      this.#emit({ actor: sourceAgentId, action: 'PROPOSE', target: key, decision: LOCK_STATE.PENDING, detail: { changed: true } });
      return this.#view(entry);
    }

    if (existing.state === LOCK_STATE.LOCKED) {
      if (sameValue(value, existing.value)) {
        // 幂等：与锁定值相同，不改变任何状态
        this.#emit({
          actor: sourceAgentId,
          action: 'PROPOSE',
          target: key,
          decision: LOCK_STATE.LOCKED,
          detail: { changed: false, idempotent: true }
        });
        return this.#view(existing);
      }
      // 与锁定值不同：只记录冲突，绝不覆写原值
      const conflict = Object.freeze({
        ...existing,
        state: LOCK_STATE.CONFLICT,
        updatedAt: nowIso,
        candidate: Object.freeze({
          value,
          sourceAgentId,
          evidence: freezeEvidence(evidence),
          proposedAt: nowIso
        })
      });
      this.#entries.set(key, conflict);
      this.#emit({
        actor: sourceAgentId,
        action: 'PROPOSE',
        target: key,
        decision: LOCK_STATE.CONFLICT,
        detail: { changed: true, originalValuePreserved: true }
      });
      return this.#view(conflict);
    }

    if (existing.state === LOCK_STATE.CONFLICT) {
      if (sameValue(value, existing.candidate.value)) {
        this.#emit({
          actor: sourceAgentId,
          action: 'PROPOSE',
          target: key,
          decision: LOCK_STATE.CONFLICT,
          detail: { changed: false, idempotent: true }
        });
        return this.#view(existing);
      }
      const updated = Object.freeze({
        ...existing,
        updatedAt: nowIso,
        candidate: Object.freeze({
          value,
          sourceAgentId,
          evidence: freezeEvidence(evidence),
          proposedAt: nowIso
        })
      });
      this.#entries.set(key, updated);
      this.#emit({
        actor: sourceAgentId,
        action: 'PROPOSE',
        target: key,
        decision: LOCK_STATE.CONFLICT,
        detail: { changed: true, candidateReplaced: true }
      });
      return this.#view(updated);
    }

    // PENDING：锁前最后一次提议生效
    if (sameValue(value, existing.value)) {
      this.#emit({
        actor: sourceAgentId,
        action: 'PROPOSE',
        target: key,
        decision: LOCK_STATE.PENDING,
        detail: { changed: false, idempotent: true }
      });
      return this.#view(existing);
    }
    const pending = Object.freeze({
      ...existing,
      value,
      sourceAgentId,
      evidence: freezeEvidence(evidence),
      updatedAt: nowIso
    });
    this.#entries.set(key, pending);
    this.#emit({
      actor: sourceAgentId,
      action: 'PROPOSE',
      target: key,
      decision: LOCK_STATE.PENDING,
      detail: { changed: true }
    });
    return this.#view(pending);
  }

  /**
   * 属主确认：decision='LOCK' → LOCKED；decision='REJECT' → 回到未锁定。
   * 冲突未解决时 confirm 会拒绝（CONFLICT_PENDING）；已 LOCKED 不可再次覆写（LOCK_IMMUTABLE）。
   */
  confirm({ key, byUserId, decision = 'LOCK' } = {}) {
    const entry = this.#require(key);
    this.#assertOwner(byUserId, 'confirm');
    if (decision !== 'LOCK' && decision !== 'REJECT') {
      throw new ContextLockError(`不支持的确认动作: ${String(decision)}`, CODE.INVALID_VALUE);
    }
    if (decision === 'REJECT') {
      this.#entries.delete(key);
      this.#emit({
        actor: byUserId,
        action: 'CONFIRM',
        target: key,
        decision: 'REJECTED',
        detail: { previousState: entry.state }
      });
      return Object.freeze({ key, state: null, decision: 'REJECTED' });
    }
    if (entry.state === LOCK_STATE.LOCKED) {
      throw new ContextLockError('已锁定的 Context 不可再次确认/覆写', CODE.LOCK_IMMUTABLE);
    }
    if (entry.state === LOCK_STATE.CONFLICT) {
      throw new ContextLockError('存在未决冲突，必须先由属主 unlock 处理', CODE.CONFLICT_PENDING);
    }
    const nowIso = this.#nowIso();
    const locked = Object.freeze({
      key,
      state: LOCK_STATE.LOCKED,
      value: entry.value,
      lockedBy: byUserId,
      lockedAt: nowIso,
      sourceAgentId: entry.sourceAgentId,
      evidence: entry.evidence,
      proposedAt: entry.proposedAt,
      updatedAt: nowIso
    });
    this.#entries.set(key, locked);
    this.#emit({
      actor: byUserId,
      action: 'CONFIRM',
      target: key,
      decision: LOCK_STATE.LOCKED,
      detail: { lockedBy: byUserId, sourceAgentId: entry.sourceAgentId }
    });
    return this.#view(locked);
  }

  /** 读取：LOCKED 返回锁定值+锁定人+时间；CONFLICT 返回锁定值 + candidate */
  read({ key } = {}) {
    const entry = this.#require(key);
    if (!LOCK_STATES.has(entry.state)) {
      throw new ContextLockError(`Context 状态非法: ${String(entry.state)}`, CODE.ILLEGAL_STATE);
    }
    return this.#view(entry);
  }

  /** 唯一解锁路径：只有属主 + 必须给 reason + 必留审计；解后回到未锁定 */
  unlock({ key, byUserId, reason } = {}) {
    const entry = this.#require(key);
    this.#assertOwner(byUserId, 'unlock');
    if (!isNonEmptyString(reason)) {
      throw new ContextLockError('unlock 必须给出 reason（审计要求）', CODE.INVALID_VALUE);
    }
    if (entry.state !== LOCK_STATE.LOCKED && entry.state !== LOCK_STATE.CONFLICT) {
      throw new ContextLockError(`当前状态不可解锁: ${entry.state}`, CODE.ILLEGAL_STATE);
    }
    this.#entries.delete(key);
    this.#emit({
      actor: byUserId,
      action: 'UNLOCK',
      target: key,
      decision: 'UNLOCKED',
      detail: { reason, previousState: entry.state, previousValue: entry.value }
    });
    return Object.freeze({ key, state: null, unlocked: true, reason });
  }

  /** 审计事件（可按 key 过滤）；只追加 */
  history({ key } = {}) {
    const events = isNonEmptyString(key) ? this.#events.filter((event) => event.target === key) : this.#events;
    return Object.freeze([...events]);
  }

  /** 飞轮指标：{ pending, locked, conflicts } */
  stats() {
    let pending = 0;
    let locked = 0;
    let conflicts = 0;
    for (const entry of this.#entries.values()) {
      if (entry.state === LOCK_STATE.PENDING) pending += 1;
      else if (entry.state === LOCK_STATE.LOCKED) locked += 1;
      else if (entry.state === LOCK_STATE.CONFLICT) conflicts += 1;
    }
    return Object.freeze({ pending, locked, conflicts });
  }
}
