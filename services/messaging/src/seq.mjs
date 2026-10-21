// 模块职责：人类 IM 通道序号分配（fnv1a32 哈希 + 64 分片 + 会话内严格单调递增）/ 契约来源：contracts/INTERFACES.md §10 seq.mjs / 关键不变量：同会话从 1 开始严格 +1、跨会话互不影响、shard = fnv1a32(userId) % shards 恒在 0..shards-1、同输入哈希确定不变。

/** FNV-1a 32 位哈希（基于 UTF-8 字节，保证中文/emoji 等输入确定且跨平台一致）。 */
export function fnv1a32(input) {
  const bytes = new TextEncoder().encode(String(input));
  let hash = 0x811c9dc5; // FNV offset basis
  for (let i = 0; i < bytes.length; i += 1) {
    hash ^= bytes[i];
    hash = Math.imul(hash, 0x01000193) >>> 0; // FNV prime
  }
  return hash >>> 0;
}

export class SeqAllocator {
  #shards;
  #allocated = 0;
  /** @type {Map<string, number>} conversationId -> 已分配到的最大 seq */
  #counters = new Map();

  constructor({ shards = 64 } = {}) {
    if (!Number.isInteger(shards) || shards <= 0) {
      throw new RangeError('shards 必须是正整数');
    }
    this.#shards = shards;
  }

  /**
   * 分配下一条序号。seq 以 conversationId 为维度单调递增（从 1 开始），
   * shard 以 userId 为维度哈希取模，二者互不干扰。
   */
  next({ userId, conversationId } = {}) {
    if (typeof userId !== 'string' || userId.length === 0) {
      throw new TypeError('userId 必须是非空字符串');
    }
    if (typeof conversationId !== 'string' || conversationId.length === 0) {
      throw new TypeError('conversationId 必须是非空字符串');
    }
    const seq = (this.#counters.get(conversationId) ?? 0) + 1;
    this.#counters.set(conversationId, seq);
    this.#allocated += 1;
    return Object.freeze({ seq, shard: fnv1a32(userId) % this.#shards });
  }

  stats() {
    return Object.freeze({ shards: this.#shards, allocated: this.#allocated });
  }
}
