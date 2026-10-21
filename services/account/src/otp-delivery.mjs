// 模块职责：OTP 投递策略——真实短信通道之上的冷却、自然日上限、失败不静默与投递统计。
// 契约来源：contracts/INTERFACES.md §38。
// 关键不变量：①同号冷却期内不重发（OTP_COOLDOWN）；②同一自然日超过 dailyCap → OTP_DAILY_CAP（按投递尝试计数）；
//             ③投递失败 → OTP_DELIVERY_FAILED（计数并上抛，绝不静默成功）；④审计/错误消息中绝不出现验证码明文。
export const OTP_DELIVERY_CODE = Object.freeze({
  OTP_COOLDOWN: 'OTP_COOLDOWN',
  OTP_DAILY_CAP: 'OTP_DAILY_CAP',
  OTP_DELIVERY_FAILED: 'OTP_DELIVERY_FAILED'
});

const CODE = OTP_DELIVERY_CODE;
const CODE_VALUES = new Set(Object.values(CODE));
const DEFAULT_COOLDOWN_MS = 60_000;
const DEFAULT_DAILY_CAP = 5;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

const normalizePhone = (phone) => {
  const digits = String(phone ?? '').replace(/[^\d]/g, '');
  if (digits.length === 13 && digits.startsWith('86')) return digits.slice(2);
  return digits;
};

const sanitize = (text, secrets = []) => {
  let output = String(text ?? '');
  for (const secret of secrets) {
    if (isNonEmptyString(secret)) output = output.split(secret).join('[redacted]');
  }
  return output;
};

export class OtpDeliveryError extends Error {
  constructor(message, code, extra = {}) {
    super(message);
    this.name = 'OtpDeliveryError';
    if (!CODE_VALUES.has(code)) throw new TypeError(`未知 OTP_DELIVERY_CODE: ${String(code)}`);
    this.code = code;
    Object.assign(this, extra);
  }
}

export class OtpDelivery {
  #provider;
  #clock;
  #cooldownMs;
  #dailyCap;
  #onAudit;
  #stats;
  /** @type {Map<string, number>} phone -> 最近一次成功投递的时间（ms） */
  #lastSuccessAt;
  /** @type {Map<string, {day:string, attempts:number}>} phone -> 当日投递尝试计数 */
  #dailyAttempts;

  constructor({
    provider,
    clock = () => new Date(),
    cooldownMs = DEFAULT_COOLDOWN_MS,
    dailyCap = DEFAULT_DAILY_CAP,
    onAudit = () => {}
  } = {}) {
    if (!provider || typeof provider.sendOtp !== 'function') {
      throw new TypeError('OtpDelivery: provider 必须实现 sendOtp');
    }
    if (!Number.isFinite(cooldownMs) || cooldownMs < 0) throw new TypeError('cooldownMs 必须是非负毫秒数');
    if (!Number.isInteger(dailyCap) || dailyCap <= 0) throw new TypeError('dailyCap 必须是正整数');
    this.#provider = provider;
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#cooldownMs = Math.trunc(cooldownMs);
    this.#dailyCap = dailyCap;
    this.#onAudit = typeof onAudit === 'function' ? onAudit : () => {};
    this.#stats = { sent: 0, failed: 0, cooldownHits: 0, dailyCapHits: 0 };
    this.#lastSuccessAt = new Map();
    this.#dailyAttempts = new Map();
  }

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #emit({ action, phone, decision, detail = {} }) {
    const event = Object.freeze({
      at: this.#nowIso(),
      actor: 'system',
      action,
      target: phone ?? null,
      decision,
      detail: Object.freeze({ ...detail })
    });
    this.#onAudit(event);
    return event;
  }

  #fail(code, message, { phone, extra = {} } = {}) {
    throw new OtpDeliveryError(message, code, extra);
  }

  /** 把 provider 的错误收敛成 OTP_DELIVERY_FAILED（保留底层 code，消息不含验证码） */
  #wrapFailure(error, code, challengeId) {
    if (error instanceof OtpDeliveryError) return error;
    const causeCode = error && isNonEmptyString(error.code) ? String(error.code) : 'SMS_FAILED';
    const message = sanitize(`短信投递失败（底层 ${causeCode}）`, [code]);
    return new OtpDeliveryError(message, CODE.OTP_DELIVERY_FAILED, {
      causeCode,
      challengeId: challengeId ?? null
    });
  }

  /**
   * 作为 AccountService 的 `otp.deliver` 注入（必须可脱离 this 调用，故用箭头函数绑定）。
   * 同步 provider：同时同步返回结果或同步抛 OTP_DELIVERY_FAILED；
   * 异步 vendor provider：返回 Promise（失败同样转 OTP_DELIVERY_FAILED）。
   */
  deliver = ({ phone, code, challengeId = null, ttlSeconds = null } = {}) => {
    const normalizedPhone = normalizePhone(phone);
    if (normalizedPhone.length !== 11) {
      throw new TypeError('deliver: phone 必须是 11 位手机号');
    }
    const codeText = String(code ?? '');
    if (!isNonEmptyString(codeText)) throw new TypeError('deliver: code 必填');

    const now = this.#now();
    const nowMs = now.getTime();

    // ① 冷却：只按"最近一次成功投递"计时（失败不占用冷却，用户可立即重试）
    const lastSuccess = this.#lastSuccessAt.get(normalizedPhone);
    if (lastSuccess !== undefined && nowMs - lastSuccess < this.#cooldownMs) {
      this.#stats.cooldownHits += 1;
      this.#emit({
        action: 'OTP_COOLDOWN',
        phone: normalizedPhone,
        decision: 'DENY',
        detail: { challengeId, cooldownMs: this.#cooldownMs }
      });
      this.#fail(CODE.OTP_COOLDOWN, `OTP 冷却中：同一手机号 ${this.#cooldownMs}ms 内不可重复发送`, {
        phone: normalizedPhone,
        extra: { challengeId }
      });
    }

    // ② 自然日上限：按"投递尝试次数"计（失败也算尝试）
    const day = now.toISOString().slice(0, 10);
    let daily = this.#dailyAttempts.get(normalizedPhone);
    if (!daily || daily.day !== day) {
      daily = { day, attempts: 0 };
      this.#dailyAttempts.set(normalizedPhone, daily);
    }
    if (daily.attempts >= this.#dailyCap) {
      this.#stats.dailyCapHits += 1;
      this.#emit({
        action: 'OTP_DAILY_CAP',
        phone: normalizedPhone,
        decision: 'DENY',
        detail: { challengeId, dailyCap: this.#dailyCap, day }
      });
      this.#fail(CODE.OTP_DAILY_CAP, `当日 OTP 发送次数已达上限（${this.#dailyCap}）`, {
        phone: normalizedPhone,
        extra: { challengeId }
      });
    }
    daily.attempts += 1;

    // ③ 投递：失败计数并上抛，绝不静默成功
    try {
      const outcome = this.#provider.sendOtp({ phone: normalizedPhone, code: codeText, ttlSeconds });
      if (outcome && typeof outcome.then === 'function') {
        return Promise.resolve(outcome).then(
          (result) => {
            this.#stats.sent += 1;
            this.#lastSuccessAt.set(normalizedPhone, this.#now().getTime());
            this.#emit({
              action: 'OTP_SEND',
              phone: normalizedPhone,
              decision: 'ALLOW',
              detail: { challengeId, providerMessageId: result?.providerMessageId ?? null }
            });
            return Object.freeze({
              providerMessageId: result?.providerMessageId ?? null,
              sentAt: result?.sentAt ?? this.#nowIso()
            });
          },
          (error) => {
            this.#stats.failed += 1;
            this.#emit({
              action: 'OTP_SEND_FAILED',
              phone: normalizedPhone,
              decision: 'DENY',
              detail: { challengeId, causeCode: error?.code ?? 'SMS_FAILED' }
            });
            throw this.#wrapFailure(error, codeText, challengeId);
          }
        );
      }
      this.#stats.sent += 1;
      this.#lastSuccessAt.set(normalizedPhone, this.#now().getTime());
      this.#emit({
        action: 'OTP_SEND',
        phone: normalizedPhone,
        decision: 'ALLOW',
        detail: { challengeId, providerMessageId: outcome?.providerMessageId ?? null }
      });
      return Object.freeze({
        providerMessageId: outcome?.providerMessageId ?? null,
        sentAt: outcome?.sentAt ?? this.#nowIso()
      });
    } catch (error) {
      this.#stats.failed += 1;
      this.#emit({
        action: 'OTP_SEND_FAILED',
        phone: normalizedPhone,
        decision: 'DENY',
        detail: { challengeId, causeCode: error?.code ?? 'SMS_FAILED' }
      });
      throw this.#wrapFailure(error, codeText, challengeId);
    }
  };

  /** @returns {Readonly<{sent:number, failed:number, cooldownHits:number, dailyCapHits:number}>} */
  stats() {
    return Object.freeze({ ...this.#stats });
  }
}
