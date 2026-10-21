// 模块职责：Agent 人格文件（SOUL）+ 分层记忆生命周期（WORKING/EPISODIC/SEMANTIC/ARCHIVED）——人格稳定、记忆会衰老，分开存分开授权。
// 契约来源：contracts/INTERFACES.md §17（隔离规则与 §6 memory-store 一致）。
// 关键不变量：①SOUL 与会话记忆分离，SOUL 写入必留审计；②晋升只能由「时间 + access() 访问次数」决定，模型无权自称重要；
//             ③遗忘是软删除：ARCHIVED/tombstone 保留可审计，recall 永不返回内容；④跨 Agent 读抛 MEMORY_ISOLATED。
import { randomUUID } from 'node:crypto';

import { canonicalize, signPayload, verifyPayload } from '../../identity/src/agent-id.mjs';

export const MEMORY_STAGE = Object.freeze({
  WORKING: 'WORKING',
  EPISODIC: 'EPISODIC',
  SEMANTIC: 'SEMANTIC',
  ARCHIVED: 'ARCHIVED'
});

export const PERSONA_CODE = Object.freeze({
  SOUL_INVALID: 'SOUL_INVALID',
  MEMORY_ISOLATED: 'MEMORY_ISOLATED',
  STAGE_INVALID: 'STAGE_INVALID',
  NOT_FOUND: 'NOT_FOUND',
  PROMOTION_DENIED: 'PROMOTION_DENIED',
  TOMBSTONED: 'TOMBSTONED',
  EXPORT_INVALID: 'EXPORT_INVALID',
  IMPORT_REJECTED: 'IMPORT_REJECTED',
  // 契约 §17：version/schema 不符 → IMPORT_INVALID；EXPORT_INVALID 仅用于导出侧（bundle 非法/无法序列化）。
  IMPORT_INVALID: 'IMPORT_INVALID'
});

const CODE = PERSONA_CODE;
const STAGE_VALUES = new Set(Object.values(MEMORY_STAGE));
const PORTABLE_VERSION = 'agentim-persona/1';
const HOUR_MS = 60 * 60e3;
const DAY_MS = 24 * HOUR_MS;

/** 晋升/衰减规则（契约 §17 冻结常量，实现不得改） */
const PROMOTION_RULES = Object.freeze({
  [MEMORY_STAGE.WORKING]: Object.freeze({
    to: MEMORY_STAGE.EPISODIC,
    minAgeMs: 24 * HOUR_MS,
    minAccesses: 2,
    reason: 'WORKING_AGE_24H_ACCESS_2'
  }),
  [MEMORY_STAGE.EPISODIC]: Object.freeze({
    to: MEMORY_STAGE.SEMANTIC,
    minAgeMs: 30 * DAY_MS,
    minAccesses: 5,
    reason: 'EPISODIC_AGE_30D_ACCESS_5'
  })
});
const DECAY_RULE = Object.freeze({
  maxSalienceExclusive: 0.2,
  idleMs: 90 * DAY_MS,
  to: MEMORY_STAGE.ARCHIVED,
  reason: 'LOW_SALIENCE_IDLE_90D'
});

const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isStringArray = (value) => Array.isArray(value) && value.every((item) => typeof item === 'string');

const normalizeTags = (value) => {
  const unique = [];
  for (const item of Array.isArray(value) ? value : []) {
    if (isNonEmptyString(item) && !unique.includes(item)) unique.push(item);
  }
  return Object.freeze(unique);
};

const normalizeStringArrayStrict = (value, fieldName) => {
  if (!isStringArray(value)) {
    throw new PersonaError(`${fieldName} 必须是字符串数组`, CODE.SOUL_INVALID);
  }
  return Object.freeze(value.map((item) => item.trim()).filter((item) => item !== ''));
};

const clampSalience = (value) => {
  if (!Number.isFinite(value)) return 0.5;
  return Math.min(1, Math.max(0, value));
};

function resolveNowMs(nowMs, clock) {
  if (nowMs === null || nowMs === undefined) {
    const sampled = clock();
    return sampled instanceof Date ? sampled.getTime() : new Date(sampled).getTime();
  }
  if (nowMs instanceof Date) return nowMs.getTime();
  if (typeof nowMs === 'number') return nowMs;
  const parsed = Date.parse(nowMs);
  return Number.isFinite(parsed) ? parsed : Number.NaN;
}

const tokenize = (text) =>
  String(text ?? '')
    .toLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter((term) => term.length > 0);

const tombstoneKey = (agentId, userId, recordId) => `${agentId}\u0000${userId}\u0000${recordId}`;

export class PersonaError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'PersonaError';
    this.code = code;
  }
}

export class PersonaMemory {
  #clock;
  #identity;
  #onAudit;
  /** @type {Map<string, object>} `${agentId}\0${userId}` -> frozen soul */
  #souls;
  /** @type {Map<string, object>} recordId -> frozen record（含 ARCHIVED，不含已 forget 的） */
  #records;
  /** @type {Map<string, Map<string, object>>} agentId -> recordId -> tombstone（无 content） */
  #tombstones;
  /** @type {ReadonlyArray<object>} 只追加治理事件 */
  #history;

  constructor({ clock = () => new Date(), identity = null, onAudit = () => {} } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#identity = identity;
    this.#onAudit = typeof onAudit === 'function' ? onAudit : () => {};
    this.#souls = new Map();
    this.#records = new Map();
    this.#tombstones = new Map();
    this.#history = [];
  }

  // ── 内部工具 ────────────────────────────────────────────────────────────────

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #audit({ action, actor = null, target = null, decision = null, detail = {} }) {
    const event = Object.freeze({
      at: this.#nowIso(),
      actor,
      action,
      target,
      decision,
      detail: Object.freeze({ ...detail })
    });
    this.#history.push(event);
    this.#onAudit(event);
    return event;
  }

  #soulKey(agentId, userId) {
    return `${agentId}\u0000${userId}`;
  }

  #assertOwner(agentId, userId) {
    if (!isNonEmptyString(agentId) || !isNonEmptyString(userId)) {
      throw new PersonaError('agentId/userId 缺失：记忆属主不存在', CODE.NOT_FOUND);
    }
  }

  #assertStage(stage) {
    if (!STAGE_VALUES.has(stage)) {
      throw new PersonaError(`非法记忆阶段: ${String(stage)}`, CODE.STAGE_INVALID);
    }
  }

  #recordsOf(agentId, userId) {
    const out = [];
    for (const record of this.#records.values()) {
      if (record.agentId === agentId && record.userId === userId) out.push(record);
    }
    return out;
  }

  #tombstoneOf(agentId, userId, recordId) {
    return this.#tombstones.get(agentId)?.get(tombstoneKey(agentId, userId, recordId)) ?? null;
  }

  #putTombstone(tombstone) {
    let bucket = this.#tombstones.get(tombstone.agentId);
    if (!bucket) {
      bucket = new Map();
      this.#tombstones.set(tombstone.agentId, bucket);
    }
    bucket.set(tombstoneKey(tombstone.agentId, tombstone.userId, tombstone.recordId), tombstone);
    return tombstone;
  }

  #tombstonesOf(agentId, userId) {
    const out = [];
    for (const tombstone of this.#tombstones.get(agentId)?.values() ?? []) {
      if (tombstone.userId === userId) out.push(tombstone);
    }
    return out;
  }

  #isExpired(record, nowMs) {
    if (record.expiresAt === null) return false;
    const expiresMs = Date.parse(record.expiresAt);
    return Number.isFinite(expiresMs) && expiresMs <= nowMs;
  }

  #isEligible(record, nowMs) {
    const rule = PROMOTION_RULES[record.stage];
    if (!rule) return false;
    const createdMs = Date.parse(record.createdAt);
    if (!Number.isFinite(createdMs)) return false;
    return nowMs - createdMs >= rule.minAgeMs && record.accesses >= rule.minAccesses;
  }

  // ── SOUL（人格基座）────────────────────────────────────────────────────────

  /** values 必须是非空字符串数组，否则 SOUL_INVALID；返回 { soul, changed } 并留审计 */
  setSoul({ agentId, userId, values, rules = [], boundaries = [], voice = null } = {}) {
    this.#assertOwner(agentId, userId);
    if (!Array.isArray(values) || values.length === 0 || !values.every((item) => typeof item === 'string')) {
      throw new PersonaError('SOUL.values 必须是非空字符串数组', CODE.SOUL_INVALID);
    }
    const normalizedValues = Object.freeze(values.map((item) => item.trim()).filter((item) => item !== ''));
    if (normalizedValues.length === 0) {
      throw new PersonaError('SOUL.values 必须是非空字符串数组（不得全为空白）', CODE.SOUL_INVALID);
    }
    const normalizedRules = normalizeStringArrayStrict(rules, 'SOUL.rules');
    const normalizedBoundaries = normalizeStringArrayStrict(boundaries, 'SOUL.boundaries');
    if (voice !== null && !isNonEmptyString(voice)) {
      throw new PersonaError('SOUL.voice 必须是字符串或 null', CODE.SOUL_INVALID);
    }

    const nowIso = this.#nowIso();
    const key = this.#soulKey(agentId, userId);
    const previous = this.#souls.get(key) ?? null;
    const soul = Object.freeze({
      agentId,
      userId,
      values: normalizedValues,
      rules: normalizedRules,
      boundaries: normalizedBoundaries,
      voice: voice ?? null,
      createdAt: previous?.createdAt ?? nowIso,
      updatedAt: nowIso
    });
    const changed =
      previous === null ||
      canonicalize({ values: previous.values, rules: previous.rules, boundaries: previous.boundaries, voice: previous.voice }) !==
        canonicalize({ values: normalizedValues, rules: normalizedRules, boundaries: normalizedBoundaries, voice: voice ?? null });
    this.#souls.set(key, soul);
    this.#audit({
      action: 'SOUL_SET',
      actor: agentId,
      target: userId,
      decision: changed ? 'UPDATED' : 'UNCHANGED',
      detail: { changed, valuesCount: normalizedValues.length, updatedAt: nowIso }
    });
    return Object.freeze({ soul, changed });
  }

  /** @returns {Readonly<object>|null} */
  soul({ agentId, userId } = {}) {
    if (!isNonEmptyString(agentId) || !isNonEmptyString(userId)) return null;
    return this.#souls.get(this.#soulKey(agentId, userId)) ?? null;
  }

  // ── 记忆写入/读取 ──────────────────────────────────────────────────────────

  remember({ agentId, userId, content, stage = MEMORY_STAGE.WORKING, tags = [], salience = 0.5, ttlMs = null } = {}) {
    this.#assertOwner(agentId, userId);
    this.#assertStage(stage);
    const now = this.#now();
    const nowIso = now.toISOString();
    let expiresAt = null;
    if (Number.isFinite(ttlMs) && ttlMs > 0) expiresAt = new Date(now.getTime() + Math.trunc(ttlMs)).toISOString();
    const record = Object.freeze({
      id: `pmem-${randomUUID()}`,
      agentId,
      userId,
      content: String(content ?? ''),
      stage,
      tags: normalizeTags(tags),
      salience: clampSalience(salience),
      accesses: 0,
      createdAt: nowIso,
      updatedAt: nowIso,
      lastAccessedAt: nowIso,
      archivedAt: stage === MEMORY_STAGE.ARCHIVED ? nowIso : null,
      promotedAt: null,
      expiresAt
    });
    this.#records.set(record.id, record);
    return record;
  }

  /** 召回：ARCHIVED/已遗忘/已过期永不返回；跨 Agent => MEMORY_ISOLATED */
  recall({ agentId, requesterAgentId = agentId, userId, stage = null, query = null, limit = 10 } = {}) {
    if (requesterAgentId !== agentId) {
      throw new PersonaError(
        `人格记忆按 Agent 隔离：${String(requesterAgentId)} 无权读取 ${String(agentId)} 的记忆`,
        CODE.MEMORY_ISOLATED
      );
    }
    this.#assertOwner(agentId, userId);
    if (stage !== null && stage !== undefined) {
      this.#assertStage(stage);
      // ARCHIVED 永不通过 recall 返回内容（遗忘语义），显式查询也返回空
      if (stage === MEMORY_STAGE.ARCHIVED) return Object.freeze([]);
    }
    const nowMs = this.#now().getTime();
    const terms = isNonEmptyString(query) ? tokenize(query) : [];
    const max = Number.isInteger(limit) && limit >= 0 ? limit : 10;
    const matches = this.#recordsOf(agentId, userId).filter((record) => {
      if (record.stage === MEMORY_STAGE.ARCHIVED) return false;
      if (stage && record.stage !== stage) return false;
      if (this.#isExpired(record, nowMs)) return false;
      if (terms.length > 0) {
        const haystack = record.content.toLowerCase();
        if (!terms.some((term) => haystack.includes(term))) return false;
      }
      return true;
    });
    matches.sort((a, b) => {
      if (a.lastAccessedAt !== b.lastAccessedAt) return a.lastAccessedAt < b.lastAccessedAt ? 1 : -1;
      if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? 1 : -1;
      return a.id < b.id ? -1 : 1;
    });
    return Object.freeze(matches.slice(0, max));
  }

  /** 访问计数 +1 —— 晋升的唯一依据（模型无法自称重要） */
  access({ agentId, recordId } = {}) {
    if (!isNonEmptyString(agentId) || !isNonEmptyString(recordId)) {
      throw new PersonaError('access 需要 agentId/recordId', CODE.NOT_FOUND);
    }
    const record = this.#records.get(recordId);
    if (!record || record.agentId !== agentId) {
      if (this.#tombstones.get(agentId) && [...this.#tombstones.get(agentId).values()].some((item) => item.recordId === recordId)) {
        throw new PersonaError(`记忆已遗忘: ${recordId}`, CODE.TOMBSTONED);
      }
      throw new PersonaError(`记忆不存在: ${recordId}`, CODE.NOT_FOUND);
    }
    if (record.stage === MEMORY_STAGE.ARCHIVED) {
      throw new PersonaError(`记忆已归档，不可再访问: ${recordId}`, CODE.TOMBSTONED);
    }
    const nowIso = this.#nowIso();
    const updated = Object.freeze({
      ...record,
      accesses: record.accesses + 1,
      lastAccessedAt: nowIso,
      updatedAt: nowIso
    });
    this.#records.set(recordId, updated);
    return updated;
  }

  // ── 晋升 / 衰减 / 遗忘 ─────────────────────────────────────────────────────

  promoteEligible({ agentId, userId, nowMs = null } = {}) {
    this.#assertOwner(agentId, userId);
    const now = resolveNowMs(nowMs, this.#clock);
    const out = [];
    for (const record of this.#recordsOf(agentId, userId)) {
      const rule = PROMOTION_RULES[record.stage];
      if (!rule || !this.#isEligible(record, now)) continue;
      out.push(
        Object.freeze({
          recordId: record.id,
          from: record.stage,
          to: rule.to,
          reason: rule.reason
        })
      );
    }
    out.sort((a, b) => (a.recordId < b.recordId ? -1 : a.recordId > b.recordId ? 1 : 0));
    return Object.freeze(out);
  }

  promote({ agentId, userId, recordId } = {}) {
    this.#assertOwner(agentId, userId);
    const record = this.#records.get(recordId);
    if (!record || record.agentId !== agentId || record.userId !== userId) {
      throw new PersonaError(`记忆不存在: ${String(recordId)}`, CODE.NOT_FOUND);
    }
    const rule = PROMOTION_RULES[record.stage];
    const now = resolveNowMs(null, this.#clock);
    if (!rule || !this.#isEligible(record, now)) {
      throw new PersonaError(
        `不满足晋升条件（${record.stage}, accesses=${record.accesses}）`,
        CODE.PROMOTION_DENIED
      );
    }
    const nowIso = this.#nowIso();
    const updated = Object.freeze({
      ...record,
      stage: rule.to,
      promotedAt: nowIso,
      updatedAt: nowIso
    });
    this.#records.set(recordId, updated);
    this.#audit({
      action: 'PROMOTE',
      actor: agentId,
      target: recordId,
      decision: rule.to,
      detail: { from: record.stage, to: rule.to, accesses: record.accesses, reason: rule.reason }
    });
    return updated;
  }

  /** 低 salience + 久未访问 => ARCHIVED（软归档，不删除；stats/审计仍可见） */
  decay({ agentId, userId, nowMs = null } = {}) {
    this.#assertOwner(agentId, userId);
    const now = resolveNowMs(nowMs, this.#clock);
    const nowIso = new Date(now).toISOString();
    const out = [];
    for (const record of this.#recordsOf(agentId, userId)) {
      if (record.stage === MEMORY_STAGE.ARCHIVED) continue;
      const lastAccessedMs = Date.parse(record.lastAccessedAt);
      const idle = Number.isFinite(lastAccessedMs) ? now - lastAccessedMs : 0;
      if (record.salience >= DECAY_RULE.maxSalienceExclusive || idle < DECAY_RULE.idleMs) continue;
      const updated = Object.freeze({
        ...record,
        stage: DECAY_RULE.to,
        archivedAt: nowIso,
        updatedAt: nowIso
      });
      this.#records.set(record.id, updated);
      this.#audit({
        action: 'DECAY',
        actor: agentId,
        target: record.id,
        decision: DECAY_RULE.to,
        detail: { from: record.stage, salience: record.salience, idleMs: idle, reason: DECAY_RULE.reason }
      });
      out.push(
        Object.freeze({
          recordId: record.id,
          from: record.stage,
          to: DECAY_RULE.to,
          reason: DECAY_RULE.reason
        })
      );
    }
    out.sort((a, b) => (a.recordId < b.recordId ? -1 : a.recordId > b.recordId ? 1 : 0));
    return Object.freeze(out);
  }

  /** 软删除：记录移出活跃集合并写无 content 的 tombstone；recall 不再返回 */
  forget({ agentId, userId, recordId } = {}) {
    this.#assertOwner(agentId, userId);
    if (this.#tombstoneOf(agentId, userId, recordId)) {
      throw new PersonaError(`记忆已被遗忘: ${String(recordId)}`, CODE.TOMBSTONED);
    }
    const record = this.#records.get(recordId);
    if (!record || record.agentId !== agentId || record.userId !== userId) {
      throw new PersonaError(`记忆不存在: ${String(recordId)}`, CODE.NOT_FOUND);
    }
    this.#records.delete(recordId);
    const tombstone = Object.freeze({
      recordId,
      agentId,
      userId,
      stage: record.stage,
      forgottenAt: this.#nowIso()
    });
    this.#putTombstone(tombstone);
    this.#audit({
      action: 'FORGET',
      actor: agentId,
      target: recordId,
      decision: 'TOMBSTONED',
      detail: { userId, stage: record.stage }
    });
    return tombstone;
  }

  // ── 跨平台迁移 ─────────────────────────────────────────────────────────────

  /** 可携带包：tombstone 不含 content；注入 identity 时对包签名（签名覆盖除 signature 外的全部字段） */
  exportPortable({ agentId, userId } = {}) {
    this.#assertOwner(agentId, userId);
    const memories = this.#recordsOf(agentId, userId).sort((a, b) => {
      if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1;
      return a.id < b.id ? -1 : 1;
    });
    const tombstones = this.#tombstonesOf(agentId, userId).sort((a, b) => {
      if (a.forgottenAt !== b.forgottenAt) return a.forgottenAt < b.forgottenAt ? -1 : 1;
      return a.recordId < b.recordId ? -1 : 1;
    });
    const base = {
      version: PORTABLE_VERSION,
      agentId,
      soul: this.#souls.get(this.#soulKey(agentId, userId)) ?? null,
      memories: Object.freeze([...memories]),
      tombstones: Object.freeze([...tombstones]),
      exportedAt: this.#nowIso()
    };
    let bundle = base;
    if (this.#identity) {
      try {
        const signed = signPayload({ identity: this.#identity, payload: base });
        bundle = {
          ...base,
          signature: Object.freeze({
            alg: signed.alg,
            signature: signed.signature,
            payloadHash: signed.payloadHash,
            signerPublicKeyPem: this.#identity.publicKeyPem
          })
        };
      } catch {
        // 导出侧错误：identity 非法 / 包无法签名 => EXPORT_INVALID（契约 §17）
        throw new PersonaError('exportPortable: 迁移包签名失败（identity 非法）', CODE.EXPORT_INVALID);
      }
      try {
        canonicalize(bundle);
      } catch {
        throw new PersonaError('exportPortable: 迁移包无法序列化', CODE.EXPORT_INVALID);
      }
    }
    const frozen = Object.freeze(bundle);
    this.#audit({
      action: 'EXPORT',
      actor: agentId,
      target: userId,
      decision: this.#identity ? 'SIGNED' : 'UNSIGNED',
      detail: { memories: memories.length, tombstones: tombstones.length }
    });
    return frozen;
  }

  /**
   * 导入可携带包。校验顺序：结构/version → agentId → 签名 → 逐条（tombstone 拦截/冲突不覆盖）。
   * @returns {Readonly<{imported:ReadonlyArray<string>, skipped:ReadonlyArray<object>, conflicts:ReadonlyArray<object>}>}
   */
  importPortable({ bundle, expectAgentId = null } = {}) {
    if (!bundle || typeof bundle !== 'object') {
      throw new PersonaError('importPortable: bundle 必须是对象（schema 不符）', CODE.IMPORT_INVALID);
    }
    if (bundle.version !== PORTABLE_VERSION) {
      throw new PersonaError(
        `不支持的迁移包版本: ${String(bundle.version)}（期望 ${PORTABLE_VERSION}）`,
        CODE.IMPORT_INVALID
      );
    }
    if (!isNonEmptyString(bundle.agentId)) {
      throw new PersonaError('importPortable: bundle.agentId 缺失（schema 不符）', CODE.IMPORT_INVALID);
    }
    if (expectAgentId !== null && expectAgentId !== undefined && bundle.agentId !== expectAgentId) {
      throw new PersonaError(
        `迁移包 agentId 不匹配：期望 ${String(expectAgentId)}，包内 ${bundle.agentId}`,
        CODE.IMPORT_REJECTED
      );
    }
    if (bundle.signature !== null && bundle.signature !== undefined) {
      const signature = bundle.signature;
      if (
        !signature ||
        typeof signature !== 'object' ||
        !isNonEmptyString(signature.signature) ||
        !isNonEmptyString(signature.signerPublicKeyPem)
      ) {
        throw new PersonaError('迁移包签名结构非法', CODE.IMPORT_REJECTED);
      }
      const { signature: _ignored, ...payload } = bundle;
      const verdict = verifyPayload({
        publicKeyPem: signature.signerPublicKeyPem,
        payload,
        signature: signature.signature
      });
      if (!verdict.ok) {
        throw new PersonaError(`迁移包签名校验失败: ${verdict.code}`, CODE.IMPORT_REJECTED);
      }
    }
    if (bundle.memories !== undefined && !Array.isArray(bundle.memories)) {
      throw new PersonaError('迁移包 memories 必须是数组（schema 不符）', CODE.IMPORT_INVALID);
    }
    if (bundle.tombstones !== undefined && !Array.isArray(bundle.tombstones)) {
      throw new PersonaError('迁移包 tombstones 必须是数组（schema 不符）', CODE.IMPORT_INVALID);
    }

    const targetAgentId = bundle.agentId;
    const imported = [];
    const skipped = [];
    const conflicts = [];

    // 先合并 tombstone：迁出的遗忘事实必须能在目标侧继续拦截复活；与本地活跃记录冲突则记 conflicts
    for (const tombstone of bundle.tombstones ?? []) {
      if (
        !tombstone ||
        typeof tombstone !== 'object' ||
        !isNonEmptyString(tombstone.recordId) ||
        !isNonEmptyString(tombstone.userId) ||
        !isNonEmptyString(tombstone.forgottenAt)
      ) {
        throw new PersonaError('迁移包 tombstone 结构非法（schema 不符）', CODE.IMPORT_INVALID);
      }
      if (this.#tombstoneOf(targetAgentId, tombstone.userId, tombstone.recordId)) continue;
      const localRecord = this.#records.get(tombstone.recordId);
      if (localRecord && localRecord.agentId === targetAgentId && localRecord.userId === tombstone.userId) {
        // 迁入「遗忘」但本地仍持有内容：不静默删除本地记忆，记冲突交给人工裁决
        conflicts.push(
          Object.freeze({
            recordId: tombstone.recordId,
            reason: 'TOMBSTONE_CONFLICT',
            existing: localRecord,
            incoming: Object.freeze({
              recordId: tombstone.recordId,
              agentId: targetAgentId,
              userId: tombstone.userId,
              stage: STAGE_VALUES.has(tombstone.stage) ? tombstone.stage : MEMORY_STAGE.ARCHIVED,
              forgottenAt: tombstone.forgottenAt
            })
          })
        );
        continue;
      }
      this.#putTombstone(
        Object.freeze({
          recordId: tombstone.recordId,
          agentId: targetAgentId,
          userId: tombstone.userId,
          stage: STAGE_VALUES.has(tombstone.stage) ? tombstone.stage : MEMORY_STAGE.ARCHIVED,
          forgottenAt: tombstone.forgottenAt
        })
      );
    }

    for (const memory of bundle.memories ?? []) {
      if (
        !memory ||
        typeof memory !== 'object' ||
        !isNonEmptyString(memory.id) ||
        !isNonEmptyString(memory.userId) ||
        typeof memory.content !== 'string' ||
        !STAGE_VALUES.has(memory.stage) ||
        !Number.isFinite(memory.salience) ||
        !Number.isInteger(memory.accesses) ||
        !isNonEmptyString(memory.createdAt) ||
        !isNonEmptyString(memory.lastAccessedAt)
      ) {
        throw new PersonaError('迁移包 memory 结构非法（schema 不符）', CODE.IMPORT_INVALID);
      }
      if (memory.agentId !== targetAgentId) {
        throw new PersonaError('迁移包 memory.agentId 与包 agentId 不一致（schema 不符）', CODE.IMPORT_INVALID);
      }
      const incoming = Object.freeze({
        ...memory,
        agentId: targetAgentId,
        tags: normalizeTags(memory.tags)
      });
      if (this.#tombstoneOf(targetAgentId, memory.userId, memory.id)) {
        // 本地 tombstone 拦截复活：记入 conflicts（不静默覆盖、不悄悄丢弃）
        conflicts.push(
          Object.freeze({
            recordId: memory.id,
            reason: 'TOMBSTONED',
            tombstone: this.#tombstoneOf(targetAgentId, memory.userId, memory.id),
            incoming
          })
        );
        continue;
      }
      const existing = this.#records.get(memory.id);
      if (existing) {
        if (existing.agentId !== targetAgentId) {
          conflicts.push(
            Object.freeze({ recordId: memory.id, reason: 'ID_COLLISION', existing, incoming })
          );
          continue;
        }
        if (canonicalize(existing) === canonicalize(incoming)) {
          skipped.push(Object.freeze({ recordId: memory.id, reason: 'DUPLICATE' }));
          continue;
        }
        conflicts.push(
          Object.freeze({ recordId: memory.id, reason: 'CONTENT_CONFLICT', existing, incoming })
        );
        continue;
      }
      this.#records.set(incoming.id, incoming);
      imported.push(memory.id);
    }

    // SOUL：同目标键只允许导入一次；冲突不覆盖
    if (bundle.soul !== null && bundle.soul !== undefined) {
      const soul = bundle.soul;
      if (
        !soul ||
        typeof soul !== 'object' ||
        !isNonEmptyString(soul.userId) ||
        !Array.isArray(soul.values) ||
        soul.values.length === 0 ||
        !soul.values.every((item) => typeof item === 'string')
      ) {
        throw new PersonaError('迁移包 soul 结构非法（schema 不符）', CODE.IMPORT_INVALID);
      }
      const key = this.#soulKey(targetAgentId, soul.userId);
      const existingSoul = this.#souls.get(key) ?? null;
      if (!existingSoul) {
        this.#souls.set(
          key,
          Object.freeze({
            ...soul,
            agentId: targetAgentId,
            rules: normalizeStringArrayStrict(soul.rules ?? [], 'SOUL.rules'),
            boundaries: normalizeStringArrayStrict(soul.boundaries ?? [], 'SOUL.boundaries')
          })
        );
        imported.push(`soul:${soul.userId}`);
      } else if (canonicalize(existingSoul) === canonicalize(soul)) {
        skipped.push(Object.freeze({ recordId: `soul:${soul.userId}`, reason: 'DUPLICATE' }));
      } else {
        conflicts.push(
          Object.freeze({
            recordId: `soul:${soul.userId}`,
            reason: 'SOUL_CONFLICT',
            existing: existingSoul,
            incoming: soul
          })
        );
      }
    }

    const result = Object.freeze({
      imported: Object.freeze(imported),
      skipped: Object.freeze(skipped),
      conflicts: Object.freeze(conflicts)
    });
    this.#audit({
      action: 'IMPORT',
      actor: targetAgentId,
      target: null,
      decision: conflicts.length > 0 ? 'CONFLICT' : 'IMPORTED',
      detail: { imported: imported.length, skipped: skipped.length, conflicts: conflicts.length }
    });
    return result;
  }

  // ── 统计 / 审计 ────────────────────────────────────────────────────────────

  /** ARCHIVED 不通过 recall 返回，但统计与审计仍可见 */
  stats({ agentId, userId } = {}) {
    this.#assertOwner(agentId, userId);
    const records = this.#recordsOf(agentId, userId);
    const byStage = {
      [MEMORY_STAGE.WORKING]: 0,
      [MEMORY_STAGE.EPISODIC]: 0,
      [MEMORY_STAGE.SEMANTIC]: 0,
      [MEMORY_STAGE.ARCHIVED]: 0
    };
    let accesses = 0;
    let lastUpdatedAt = null;
    for (const record of records) {
      byStage[record.stage] += 1;
      accesses += record.accesses;
      if (lastUpdatedAt === null || record.updatedAt > lastUpdatedAt) lastUpdatedAt = record.updatedAt;
    }
    const soul = this.#souls.get(this.#soulKey(agentId, userId)) ?? null;
    return Object.freeze({
      agentId,
      userId,
      soul: Object.freeze({ present: soul !== null, updatedAt: soul?.updatedAt ?? null }),
      memories: records.length,
      byStage: Object.freeze({ ...byStage }),
      accesses,
      tombstoned: this.#tombstonesOf(agentId, userId).length,
      lastUpdatedAt
    });
  }

  /** 附加方法：治理事件只追加（SOUL 变更/晋升/衰减/遗忘/迁移），供审计面板消费 */
  history({ agentId = null } = {}) {
    const events = agentId === null ? this.#history : this.#history.filter((event) => event.actor === agentId);
    return Object.freeze([...events]);
  }
}
