// 模块职责：三层记忆（working/episodic/semantic），按 Agent 隔离；总管只能读协调摘要（digest），读不到子 Agent 原始记忆。
// 契约来源：contracts/INTERFACES.md §6。
// 关键不变量：①requesterAgentId !== agentId 时 read/search 抛 MEMORY_ISOLATED；②digest 只含统计摘要、绝不含 content；
//             ③记录不可变，用户删除权通过 purge 留下无 content 的 tombstone。
import { randomUUID } from 'node:crypto';

export const MEMORY_LAYER = Object.freeze({ WORKING: 'working', EPISODIC: 'episodic', SEMANTIC: 'semantic' });

const LAYER_VALUES = new Set(Object.values(MEMORY_LAYER));
/** 各层默认 TTL（生产替换点：应由配置中心下发；此处给出参考实现的确定性默认值） */
const LAYER_TTL_MS = Object.freeze({
  [MEMORY_LAYER.WORKING]: 60 * 60e3,
  [MEMORY_LAYER.EPISODIC]: 7 * 864e5,
  [MEMORY_LAYER.SEMANTIC]: 90 * 864e5
});
const LAYER_ORDER = Object.freeze([MEMORY_LAYER.WORKING, MEMORY_LAYER.EPISODIC, MEMORY_LAYER.SEMANTIC]);
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

/** tags 归一：去空、去重、冻结 */
const toTags = (value) => {
  const unique = [];
  for (const item of Array.isArray(value) ? value : []) {
    if (isNonEmptyString(item) && !unique.includes(item)) unique.push(item);
  }
  return Object.freeze(unique);
};

export class MemoryIsolationError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'MemoryIsolationError';
    this.code = code;
  }
}

/** 确定性分词：小写 + 非字母数字切分（不引入向量库；向量检索为生产替换点） */
const tokenize = (text) =>
  String(text ?? '')
    .toLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter((term) => term.length > 0);

const countOccurrences = (haystack, needle) => {
  if (needle.length === 0) return 0;
  let count = 0;
  let index = 0;
  while ((index = haystack.indexOf(needle, index)) !== -1) {
    count += 1;
    index += needle.length;
  }
  return count;
};

export class MemoryStore {
  #clock;
  /** @type {Map<string, object[]>} agentId -> 只追加记录数组（按写入顺序） */
  #byAgent;

  constructor({ clock = () => new Date() } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#byAgent = new Map();
  }

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #validateLayer(layer) {
    if (!LAYER_VALUES.has(layer)) {
      throw new MemoryIsolationError(`非法记忆层: ${String(layer)}`, 'LAYER_INVALID');
    }
  }

  #assertSameAgent({ agentId, requesterAgentId }) {
    if (requesterAgentId !== agentId) {
      throw new MemoryIsolationError(
        `记忆按 Agent 隔离：${String(requesterAgentId)} 无权读取 ${String(agentId)} 的原始记忆`,
        'MEMORY_ISOLATED'
      );
    }
  }

  #isLive(record, nowMs) {
    if (record.expiresAt === null) return true; // 无过期时间 => 永不过期
    const expiresMs = Date.parse(record.expiresAt);
    return !Number.isFinite(expiresMs) ? true : expiresMs > nowMs;
  }

  /** 写入一条记忆；ttlMs 可选覆盖该层默认 TTL（<=0 表示永不过期） */
  write({ agentId, userId, layer, content, tags = [], ttlMs = null } = {}) {
    if (!isNonEmptyString(agentId) || !isNonEmptyString(userId)) {
      throw new MemoryIsolationError('agentId/userId 缺失：记忆属主不存在（NOT_FOUND）', 'NOT_FOUND');
    }
    this.#validateLayer(layer);

    const now = this.#now();
    const at = now.toISOString();
    let expiresAt = null;
    if (ttlMs === null || ttlMs === undefined) {
      expiresAt = new Date(now.getTime() + LAYER_TTL_MS[layer]).toISOString();
    } else if (Number.isFinite(ttlMs) && ttlMs > 0) {
      expiresAt = new Date(now.getTime() + Math.trunc(ttlMs)).toISOString();
    }

    const record = Object.freeze({
      id: `mem-${randomUUID()}`,
      agentId,
      userId,
      layer,
      content: String(content ?? ''),
      tags: toTags(tags),
      at,
      expiresAt
    });
    let records = this.#byAgent.get(agentId);
    if (!records) {
      records = [];
      this.#byAgent.set(agentId, records);
    }
    records.push(record);
    return record;
  }

  /** 读取本 Agent 的记忆；跨 Agent => MEMORY_ISOLATED */
  read({ agentId, requesterAgentId = agentId, userId, layer, limit = 20 } = {}) {
    this.#assertSameAgent({ agentId, requesterAgentId });
    this.#validateLayer(layer);
    const records = this.#byAgent.get(agentId) ?? [];
    const nowMs = this.#now().getTime();
    const max = Number.isInteger(limit) && limit >= 0 ? limit : 20;
    const matched = records
      .filter((record) => record.layer === layer && (userId === undefined || record.userId === userId))
      .filter((record) => this.#isLive(record, nowMs))
      .sort((a, b) => (a.at === b.at ? (a.id < b.id ? -1 : 1) : a.at < b.at ? 1 : -1))
      .slice(0, max);
    return Object.freeze(matched);
  }

  /**
   * 确定性词频检索：score = sum(termHits) / (1 + ln(内容长度))；不引入向量库。
   * @returns {ReadonlyArray<{record:object, score:number}>}
   */
  search({ agentId, requesterAgentId = agentId, userId, layer, query, limit = 5 } = {}) {
    this.#assertSameAgent({ agentId, requesterAgentId });
    this.#validateLayer(layer);
    const terms = tokenize(query);
    const records = this.#byAgent.get(agentId) ?? [];
    const nowMs = this.#now().getTime();
    const max = Number.isInteger(limit) && limit >= 0 ? limit : 5;
    const scored = [];
    for (const record of records) {
      if (record.layer !== layer) continue;
      if (userId !== undefined && record.userId !== userId) continue;
      if (!this.#isLive(record, nowMs)) continue;
      const haystack = record.content.toLowerCase();
      const normalizedLength = Math.max(1, [...haystack].length);
      let hits = 0;
      for (const term of terms) hits += countOccurrences(haystack, term);
      if (hits === 0) continue;
      const score = hits / (1 + Math.log(normalizedLength));
      scored.push(Object.freeze({ record, score }));
    }
    scored.sort((a, b) => {
      if (a.score !== b.score) return b.score - a.score;
      if (a.record.at !== b.record.at) return a.record.at < b.record.at ? 1 : -1;
      return a.record.id < b.record.id ? -1 : 1;
    });
    return Object.freeze(scored.slice(0, max));
  }

  /**
   * 协调摘要：只返回按子 Agent 聚合的统计（agentId/lastAt/count/summary），绝不含 content。
   * 契约签名不含 registry，参考实现聚合“该用户下除 butler 本身之外的所有 Agent”；
   * 生产可在注入 registry 后收窄为 butler 的子 Agent（文档标注为替换点）。
   */
  digest({ butlerAgentId, userId } = {}) {
    if (!isNonEmptyString(butlerAgentId)) {
      throw new MemoryIsolationError('butlerAgentId 缺失', 'NOT_FOUND');
    }
    const nowMs = this.#now().getTime();
    const perAgent = new Map();
    for (const [agentId, records] of this.#byAgent) {
      if (agentId === butlerAgentId) continue;
      for (const record of records) {
        if (record.userId !== userId) continue;
        if (!this.#isLive(record, nowMs)) continue;
        let bucket = perAgent.get(agentId);
        if (!bucket) {
          bucket = { agentId, lastAt: record.at, count: 0, layers: new Set() };
          perAgent.set(agentId, bucket);
        }
        bucket.count += 1;
        bucket.layers.add(record.layer);
        if (record.at > bucket.lastAt) bucket.lastAt = record.at;
      }
    }
    const coordination = [...perAgent.values()]
      .sort((a, b) => (a.agentId < b.agentId ? -1 : a.agentId > b.agentId ? 1 : 0))
      .map((bucket) => {
        const layers = LAYER_ORDER.filter((layer) => bucket.layers.has(layer));
        return Object.freeze({
          agentId: bucket.agentId,
          lastAt: bucket.lastAt,
          count: bucket.count,
          summary: `${bucket.count} 条记忆 · 图层 ${layers.join('/')} · 最近 ${bucket.lastAt}`
        });
      });
    return Object.freeze({
      coordination: Object.freeze(coordination),
      contentExposed: false
    });
  }

  /** 用户删除权：删除该用户全部记忆，返回统计与无 content 的 tombstone */
  purge({ userId } = {}) {
    if (!isNonEmptyString(userId)) {
      throw new MemoryIsolationError('userId 缺失：无法执行用户删除权（NOT_FOUND）', 'NOT_FOUND');
    }
    let deleted = 0;
    const byAgent = {};
    const tombstones = [];
    for (const [agentId, records] of [...this.#byAgent.entries()]) {
      const kept = [];
      for (const record of records) {
        if (record.userId !== userId) {
          kept.push(record);
          continue;
        }
        deleted += 1;
        byAgent[agentId] = (byAgent[agentId] ?? 0) + 1;
        tombstones.push(
          Object.freeze({ id: record.id, agentId: record.agentId, layer: record.layer, at: record.at })
        );
      }
      if (kept.length === 0) this.#byAgent.delete(agentId);
      else this.#byAgent.set(agentId, kept);
    }
    return Object.freeze({
      deleted,
      byAgent: Object.freeze(byAgent),
      tombstones: Object.freeze(tombstones)
    });
  }
}
