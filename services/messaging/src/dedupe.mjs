// 模块职责：人类 IM 通道 bizId 去重（TTL 窗口）/ 契约来源：contracts/INTERFACES.md §10 dedupe.mjs / 关键不变量：窗口内首次 false、重复 true、TTL 过期后可重新标记、时间来源必须注入的 clock。

export class DedupeStore {
  #ttlMs;
  #clock;
  /** @type {Map<string, number>} key -> 首次标记的毫秒时间戳（过期项在 prune() 前仍保留） */
  #seen = new Map();

  constructor({ ttlMs = 864e5, clock = () => new Date() } = {}) {
    if (typeof ttlMs !== 'number' || !Number.isFinite(ttlMs) || ttlMs < 0) {
      throw new RangeError('ttlMs 必须是 >= 0 的有限数字（毫秒）');
    }
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    this.#ttlMs = ttlMs;
    this.#clock = clock;
  }

  /**
   * 检查并标记 key。返回 { duplicate, firstSeenAt }：
   * - 窗口内已标记过：duplicate=true，firstSeenAt 为首次标记时间（不刷新）。
   * - 首次或已过期：duplicate=false，firstSeenAt 为本次标记时间。
   */
  checkAndMark(key) {
    if (typeof key !== 'string' || key.length === 0) {
      throw new TypeError('key 必须是非空字符串');
    }
    const nowMs = this.#now();
    const firstSeenMs = this.#seen.get(key);
    if (firstSeenMs !== undefined && nowMs - firstSeenMs < this.#ttlMs) {
      return Object.freeze({ duplicate: true, firstSeenAt: new Date(firstSeenMs).toISOString() });
    }
    this.#seen.set(key, nowMs);
    return Object.freeze({ duplicate: false, firstSeenAt: new Date(nowMs).toISOString() });
  }

  /** 当前保留的键数量（含尚未 prune 的过期项；跨窗口有效键数请先 prune()）。 */
  size() {
    return this.#seen.size;
  }

  /** 清理过期键，返回被清理的数量（幂等：重复调用不会重复计数）。 */
  prune() {
    const nowMs = this.#now();
    let removed = 0;
    for (const [key, firstSeenMs] of this.#seen) {
      if (nowMs - firstSeenMs >= this.#ttlMs) {
        this.#seen.delete(key);
        removed += 1;
      }
    }
    return removed;
  }

  #now() {
    const now = this.#clock();
    if (!(now instanceof Date) || Number.isNaN(now.getTime())) {
      throw new TypeError('clock 必须返回合法的 Date 实例');
    }
    return now.getTime();
  }
}
