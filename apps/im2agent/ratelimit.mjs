/**
 * 模块职责：im2agent 应用层速率限制（固定窗口计数，按客户端 key 隔离）
 * 契约来源：deploy/prod 的加固清单；不依赖任何第三方中间件
 *
 * 为什么放在应用层而不是只靠 Caddy：
 *   · Caddy 的 rate_limit 是插件，公网部署不一定装得上；
 *   · 应用层限流对"已认证但滥用的操作者"同样有效（Caddy 只看到 IP）。
 * 两者叠加才是纵深防御：反代限流挡扫描，应用限流挡滥用。
 */
export class RateLimitError extends Error {
  constructor(message, retryAfterMs) {
    super(message);
    this.name = 'RateLimitError';
    this.code = 'RATE_LIMITED';
    this.retryAfterMs = retryAfterMs;
  }
}

/**
 * @param {object} [options]
 * @param {number} [options.windowMs] 窗口长度（默认 60s）
 * @param {number} [options.max] 窗口内允许次数（<=0 表示禁用）
 * @param {() => number} [options.now] 注入时钟（测试用）
 */
export function createRateLimiter({ windowMs = 60_000, max = 120, now = () => Date.now() } = {}) {
  const buckets = new Map(); // key -> { start, count }

  function prune(current) {
    if (buckets.size <= 10_000) return;
    for (const [key, bucket] of buckets) {
      if (current - bucket.start >= windowMs) buckets.delete(key);
    }
  }

  return {
    enabled: max > 0,
    windowMs,
    max,

    /** @returns {{ok:boolean, remaining:number, retryAfterMs:number}} */
    check(key) {
      if (max <= 0) return { ok: true, remaining: Number.POSITIVE_INFINITY, retryAfterMs: 0 };
      const current = now();
      prune(current);
      let bucket = buckets.get(key);
      if (!bucket || current - bucket.start >= windowMs) {
        bucket = { start: current, count: 0 };
        buckets.set(key, bucket);
      }
      bucket.count += 1;
      if (bucket.count > max) {
        return { ok: false, remaining: 0, retryAfterMs: Math.max(0, bucket.start + windowMs - current) };
      }
      return { ok: true, remaining: max - bucket.count, retryAfterMs: 0 };
    },

    reset(key) {
      buckets.delete(key);
    },

    stats() {
      return { keys: buckets.size, windowMs, max };
    }
  };
}

/** 从请求里取客户端标识：反代后面必须开 TRUST_PROXY 才采信 X-Forwarded-For（否则可被伪造绕过） */
export function clientKey(req, { trustProxy = false } = {}) {
  if (trustProxy) {
    const forwarded = req.headers['x-forwarded-for'];
    const first = (Array.isArray(forwarded) ? forwarded[0] : forwarded ?? '').split(',')[0].trim();
    if (first !== '') return first;
  }
  return req.socket?.remoteAddress ?? 'unknown';
}
