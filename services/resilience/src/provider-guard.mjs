// 模块职责：外部依赖熔断与降级——短信/实名/机审这些依赖一定会挂；OPEN 时快速失败（不再打主方），有 fallback 才显式降级，绝不假装成功。
// 契约来源：contracts/INTERFACES.md §43。
// 关键不变量：①OPEN 时不调用主提供方（避免雪崩）；②半开态只允许一次探测，成功→CLOSED、失败→重新 OPEN 并重置冷却；
//             ③只有声明 fallback 才降级，且结果必须标 usedFallback:true + causeCode；④主备都失败 → FALLBACK_FAILED（带两个原因码），不得静默返回空值；
//             ⑤每次熔断状态迁移与每次降级都写审计。
export const CIRCUIT_STATE = Object.freeze({ CLOSED: 'CLOSED', OPEN: 'OPEN', HALF_OPEN: 'HALF_OPEN' });

export const GUARD_CODE = Object.freeze({
  CIRCUIT_OPEN: 'CIRCUIT_OPEN',
  PRIMARY_FAILED: 'PRIMARY_FAILED',
  FALLBACK_FAILED: 'FALLBACK_FAILED',
  NO_FALLBACK: 'NO_FALLBACK',
  INVALID_CONFIG: 'INVALID_CONFIG'
});

const CODE = GUARD_CODE;
const CODE_VALUES = new Set(Object.values(CODE));
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const isThenable = (value) => Boolean(value) && typeof value.then === 'function';

const clockMs = (clock) => {
  const sampled = clock();
  if (sampled instanceof Date) return sampled.getTime();
  const numeric = typeof sampled === 'number' ? sampled : Number(sampled);
  return Number.isFinite(numeric) ? Math.trunc(numeric) : 0;
};

const errorCodeOf = (error) => {
  if (error && isNonEmptyString(error.code)) return error.code;
  if (error && isNonEmptyString(error.name)) return error.name;
  return 'UNKNOWN';
};

export class GuardError extends Error {
  constructor(message, code, { causeCode = null, causeCodes = [], cause = null } = {}) {
    super(message);
    this.name = 'GuardError';
    if (!CODE_VALUES.has(code)) throw new TypeError(`未知 GUARD_CODE: ${String(code)}`);
    this.code = code;
    this.causeCode = causeCode;
    this.causeCodes = Object.freeze([...causeCodes]);
    if (cause !== null) this.cause = cause;
  }
}

// ── 熔断器 ──────────────────────────────────────────────────────────────────

export class CircuitBreaker {
  #name;
  #failureThreshold;
  #cooldownMs;
  #clock;
  #state;
  #failures;
  #successes;
  #openedAt;
  #lastError;
  #probeInFlight;
  /** @type {object[]} 尚未被消费的状态迁移事件（供 ProviderGuard 写审计） */
  #transitions;

  constructor({ name = 'breaker', failureThreshold = 3, cooldownMs = 30_000, clock = () => Date.now() } = {}) {
    if (!isNonEmptyString(name)) {
      throw new GuardError('CircuitBreaker: name 必须是非空字符串', CODE.INVALID_CONFIG);
    }
    if (!Number.isInteger(failureThreshold) || failureThreshold < 1) {
      throw new GuardError('CircuitBreaker: failureThreshold 必须是正整数', CODE.INVALID_CONFIG);
    }
    if (!Number.isFinite(cooldownMs) || cooldownMs < 0) {
      throw new GuardError('CircuitBreaker: cooldownMs 必须是非负毫秒数', CODE.INVALID_CONFIG);
    }
    if (typeof clock !== 'function') {
      throw new GuardError('CircuitBreaker: clock 必须是函数', CODE.INVALID_CONFIG);
    }
    this.#name = name;
    this.#failureThreshold = failureThreshold;
    this.#cooldownMs = Math.trunc(cooldownMs);
    this.#clock = clock;
    this.#state = CIRCUIT_STATE.CLOSED;
    this.#failures = 0;
    this.#successes = 0;
    this.#openedAt = null;
    this.#lastError = null;
    this.#probeInFlight = false;
    this.#transitions = [];
  }

  #nowMs() {
    return clockMs(this.#clock);
  }

  #transition(to, reason) {
    const from = this.#state;
    if (from === to) return;
    this.#state = to;
    this.#transitions.push(
      Object.freeze({
        at: new Date(this.#nowMs()).toISOString(),
        from,
        to,
        reason,
        causeCode: this.#lastError ? errorCodeOf(this.#lastError) : null
      })
    );
  }

  /** 逻辑状态：OPEN 冷却到期后对外呈现 HALF_OPEN（真正迁移发生在下一次 execute） */
  get state() {
    if (this.#state === CIRCUIT_STATE.OPEN && this.#openedAt !== null) {
      if (this.#nowMs() - this.#openedAt >= this.#cooldownMs) return CIRCUIT_STATE.HALF_OPEN;
    }
    return this.#state;
  }

  stats() {
    return Object.freeze({
      name: this.#name,
      state: this.state,
      failures: this.#failures,
      successes: this.#successes,
      openedAt: this.#openedAt,
      lastError: this.#lastError
        ? Object.freeze({ message: String(this.#lastError.message ?? this.#lastError), code: errorCodeOf(this.#lastError) })
        : null
    });
  }

  /** 消费并清空尚未上报的状态迁移事件（ProviderGuard 用来写审计） */
  drainTransitions() {
    const drained = Object.freeze([...this.#transitions]);
    this.#transitions = [];
    return drained;
  }

  #open(reason) {
    this.#openedAt = this.#nowMs();
    this.#probeInFlight = false;
    this.#transition(CIRCUIT_STATE.OPEN, reason);
  }

  #onSuccess() {
    this.#successes += 1;
    this.#failures = 0;
    this.#lastError = null;
    this.#probeInFlight = false;
    if (this.#state !== CIRCUIT_STATE.CLOSED) {
      this.#openedAt = null;
      this.#transition(CIRCUIT_STATE.CLOSED, 'probe_success');
    }
  }

  #onFailure(error) {
    this.#lastError = error;
    this.#failures += 1;
    this.#probeInFlight = false;
    if (this.#state === CIRCUIT_STATE.HALF_OPEN) {
      // 探测失败：重新 OPEN 并重置冷却
      this.#open('probe_failure');
      return;
    }
    if (this.#state === CIRCUIT_STATE.CLOSED && this.#failures >= this.#failureThreshold) {
      this.#open('failure_threshold');
    }
  }

  /**
   * 执行受保护调用。OPEN（或半开探测进行中）→ 抛 GuardError(CIRCUIT_OPEN)，**绝不调用 fn**。
   * 支持同步与异步 fn（异步时状态在 Promise settle 后更新）。
   */
  execute(fn) {
    if (typeof fn !== 'function') throw new TypeError('CircuitBreaker.execute: fn 必须是函数');
    const logicalState = this.state;
    if (logicalState === CIRCUIT_STATE.OPEN) {
      throw new GuardError(
        `电路已打开（${this.#name}），快速失败，不调用主提供方`,
        CODE.CIRCUIT_OPEN,
        { causeCode: this.#lastError ? errorCodeOf(this.#lastError) : null }
      );
    }
    if (logicalState === CIRCUIT_STATE.HALF_OPEN) {
      if (this.#probeInFlight) {
        throw new GuardError(`半开态已有探测在途（${this.#name}），拒绝并发探测`, CODE.CIRCUIT_OPEN);
      }
      this.#transition(CIRCUIT_STATE.HALF_OPEN, 'cooldown_elapsed');
      this.#probeInFlight = true;
    }

    let result;
    try {
      result = fn();
    } catch (error) {
      this.#onFailure(error);
      throw error;
    }
    if (isThenable(result)) {
      return Promise.resolve(result).then(
        (value) => {
          this.#onSuccess();
          return value;
        },
        (error) => {
          this.#onFailure(error);
          throw error;
        }
      );
    }
    this.#onSuccess();
    return result;
  }
}

// ── 带降级的提供方守卫 ──────────────────────────────────────────────────────

export class ProviderGuard {
  #name;
  #primary;
  #fallback;
  #breaker;
  #onAudit;
  #clock;
  #primaryCalls;
  #fallbackCalls;
  #fallbackUsed;
  #lastCauseCode;
  #lastProvider;

  constructor({ name, primary, fallback = null, breaker = null, onAudit = () => {}, clock = () => new Date() } = {}) {
    if (!isNonEmptyString(name)) throw new GuardError('ProviderGuard: name 必须是非空字符串', CODE.INVALID_CONFIG);
    if (typeof primary !== 'function') throw new GuardError('ProviderGuard: primary 必须是函数', CODE.INVALID_CONFIG);
    if (fallback !== null && fallback !== undefined && typeof fallback !== 'function') {
      throw new GuardError('ProviderGuard: fallback 必须是函数或 null', CODE.INVALID_CONFIG);
    }
    if (breaker !== null && breaker !== undefined && (typeof breaker.execute !== 'function' || !('state' in breaker))) {
      throw new GuardError('ProviderGuard: breaker 必须是 CircuitBreaker 实例', CODE.INVALID_CONFIG);
    }
    if (typeof onAudit !== 'function') throw new GuardError('ProviderGuard: onAudit 必须是函数', CODE.INVALID_CONFIG);
    if (typeof clock !== 'function') throw new GuardError('ProviderGuard: clock 必须是函数', CODE.INVALID_CONFIG);
    this.#name = name;
    this.#primary = primary;
    this.#fallback = fallback ?? null;
    this.#onAudit = onAudit;
    this.#clock = clock;
    this.#breaker = breaker ?? new CircuitBreaker({ name: `${name}-breaker`, clock });
    this.#primaryCalls = 0;
    this.#fallbackCalls = 0;
    this.#fallbackUsed = 0;
    this.#lastCauseCode = null;
    this.#lastProvider = null;
  }

  #nowIso() {
    return new Date(clockMs(this.#clock)).toISOString();
  }

  #emit({ action, decision, detail }) {
    const event = Object.freeze({
      at: this.#nowIso(),
      actor: 'provider-guard',
      action,
      target: this.#name,
      decision,
      detail: Object.freeze({ ...detail })
    });
    this.#onAudit(event);
    return event;
  }

  #drainBreakerAudit() {
    if (typeof this.#breaker.drainTransitions !== 'function') return;
    for (const transition of this.#breaker.drainTransitions()) {
      this.#emit({
        action: `CIRCUIT_${transition.to}`,
        decision: transition.to,
        detail: { from: transition.from, to: transition.to, reason: transition.reason, causeCode: transition.causeCode }
      });
    }
  }

  #success(value, provider, usedFallback, causeCode) {
    this.#lastProvider = provider;
    this.#lastCauseCode = causeCode;
    if (usedFallback) this.#fallbackUsed += 1;
    return Object.freeze({ value, provider, usedFallback, causeCode });
  }

  /** 主提供方失败后的降级/失败路径（同步抛出或 async 返回/抛出） */
  #handlePrimaryFailure(error, args) {
    const primaryCauseCode = errorCodeOf(error);
    this.#drainBreakerAudit();

    if (this.#fallback === null) {
      const code = primaryCauseCode === CODE.CIRCUIT_OPEN ? CODE.NO_FALLBACK : CODE.PRIMARY_FAILED;
      this.#lastCauseCode = primaryCauseCode;
      this.#lastProvider = null;
      this.#emit({
        action: 'PROVIDER_FAILED',
        decision: 'FAILED',
        detail: { causeCode: primaryCauseCode, noFallback: true, circuitCode: code }
      });
      throw new GuardError(`主提供方失败且未声明 fallback（${this.#name}，原因 ${primaryCauseCode}）`, code, {
        causeCode: primaryCauseCode,
        cause: error
      });
    }

    this.#fallbackCalls += 1;
    let fallbackResult;
    try {
      fallbackResult = this.#fallback(args);
    } catch (fallbackError) {
      throw this.#fallbackFailed(primaryCauseCode, fallbackError);
    }
    if (isThenable(fallbackResult)) {
      return Promise.resolve(fallbackResult).then(
        (value) => this.#fallbackSuccess(value, primaryCauseCode),
        (fallbackError) => {
          throw this.#fallbackFailed(primaryCauseCode, fallbackError);
        }
      );
    }
    return this.#fallbackSuccess(fallbackResult, primaryCauseCode);
  }

  #fallbackSuccess(value, primaryCauseCode) {
    this.#emit({
      action: 'PROVIDER_FALLBACK',
      decision: 'DEGRADED',
      detail: { provider: 'fallback', causeCode: primaryCauseCode }
    });
    return this.#success(value, 'fallback', true, primaryCauseCode);
  }

  #fallbackFailed(primaryCauseCode, fallbackError) {
    const fallbackCauseCode = errorCodeOf(fallbackError);
    this.#lastCauseCode = fallbackCauseCode;
    this.#lastProvider = null;
    this.#emit({
      action: 'PROVIDER_FALLBACK_FAILED',
      decision: 'FAILED',
      detail: { primaryCauseCode, fallbackCauseCode }
    });
    return new GuardError(
      `主备均失败（${this.#name}）：primary=${primaryCauseCode}，fallback=${fallbackCauseCode}`,
      CODE.FALLBACK_FAILED,
      {
        causeCode: primaryCauseCode,
        causeCodes: [primaryCauseCode, fallbackCauseCode],
        cause: fallbackError
      }
    );
  }

  /**
   * 执行主提供方（经熔断器）；失败时按声明降级。
   * @returns {Readonly<{value:any, provider:'primary'|'fallback', usedFallback:boolean, causeCode:string|null}>}
   */
  execute(args) {
    let primaryResult;
    try {
      primaryResult = this.#breaker.execute(() => {
        this.#primaryCalls += 1;
        return this.#primary(args);
      });
    } catch (error) {
      return this.#handlePrimaryFailure(error, args);
    }

    if (isThenable(primaryResult)) {
      return Promise.resolve(primaryResult).then(
        (value) => {
          this.#drainBreakerAudit();
          return this.#success(value, 'primary', false, null);
        },
        (error) => this.#handlePrimaryFailure(error, args)
      );
    }
    this.#drainBreakerAudit();
    return this.#success(primaryResult, 'primary', false, null);
  }

  stats() {
    return Object.freeze({
      name: this.#name,
      breaker: this.#breaker.stats(),
      primaryCalls: this.#primaryCalls,
      fallbackCalls: this.#fallbackCalls,
      fallbackUsed: this.#fallbackUsed,
      lastProvider: this.#lastProvider,
      lastCauseCode: this.#lastCauseCode
    });
  }
}
