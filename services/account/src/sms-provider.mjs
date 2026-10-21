// 模块职责：真实短信通道（可插拔）——dev（内存记录/不外发）、log（写日志/等价外发）、vendor（注入 transport 走真实供应商）。
// 契约来源：contracts/INTERFACES.md §38。
// 关键不变量：①投递失败一律抛 SmsError，绝不吞错；②任何错误消息/日志中都不得出现验证码明文；
//             ③dev 通道只在内存记录（供测试读取），不产生任何网络调用。
import { randomUUID } from 'node:crypto';

export const SMS_CODE = Object.freeze({
  SMS_FAILED: 'SMS_FAILED',
  SMS_TIMEOUT: 'SMS_TIMEOUT',
  SMS_REJECTED: 'SMS_REJECTED',
  INVALID_PHONE: 'INVALID_PHONE',
  PROVIDER_UNAVAILABLE: 'PROVIDER_UNAVAILABLE'
});

const CODE = SMS_CODE;
const CODE_VALUES = new Set(Object.values(CODE));
const KINDS = new Set(['dev', 'log', 'vendor']);
const TIMEOUT_MARKER = '__SMS_TIMEOUT__';
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class SmsError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'SmsError';
    if (!CODE_VALUES.has(code)) throw new TypeError(`未知 SMS_CODE: ${String(code)}`);
    this.code = code;
  }
}

/** 错误消息脱敏：把验证码从任意字符串里抹掉（不变量②） */
const sanitize = (text, secrets = []) => {
  let output = String(text ?? '');
  for (const secret of secrets) {
    if (isNonEmptyString(secret)) output = output.split(secret).join('[redacted]');
  }
  return output;
};

const normalizePhone = (phone) => {
  const digits = String(phone ?? '').replace(/[^\d]/g, '');
  if (digits.length === 13 && digits.startsWith('86')) return digits.slice(2);
  return digits;
};

const maskPhone = (phone) => `${phone.slice(0, 3)}****${phone.slice(-4)}`;

/** 给 Promise 加超时，避免真实计时器在成功后继续挂住事件循环 */
function withTimeout(promise, timeoutMs) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      const error = new Error('sms transport timeout');
      error.code = TIMEOUT_MARKER;
      reject(error);
    }, timeoutMs);
    if (typeof timer.unref === 'function') timer.unref();
    Promise.resolve(promise).then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error) => {
        clearTimeout(timer);
        reject(error);
      }
    );
  });
}

/**
 * @returns {Readonly<{name:string, kind:string, info:Function, sendOtp:Function, messages:Function, lastMessage:Function}>}
 */
export function createSmsProvider({
  kind = 'dev',
  transport = null,
  timeoutMs = 3000,
  clock = () => new Date()
} = {}) {
  if (!KINDS.has(kind)) {
    throw new SmsError(`未知短信通道类型: ${String(kind)}`, CODE.PROVIDER_UNAVAILABLE);
  }
  if (kind === 'vendor' && typeof transport !== 'function') {
    throw new SmsError('vendor 通道必须注入 transport', CODE.PROVIDER_UNAVAILABLE);
  }
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) throw new TypeError('timeoutMs 必须是正毫秒数');
  if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');

  const name = `sms-${kind}`;
  const devMessages = [];
  const nowIso = () => {
    const sampled = clock();
    return (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
  };

  const mapFailure = (error, code) => {
    if (error instanceof SmsError) return new SmsError(sanitize(error.message, [code]), error.code);
    const errorName = error && error.name ? String(error.name) : 'UnknownError';
    return new SmsError(sanitize(`短信通道失败(${errorName})`, [code]), CODE.SMS_FAILED);
  };

  const finalizeSuccess = (outcome, code) => {
    const providerMessageId =
      outcome && typeof outcome === 'object' && isNonEmptyString(outcome.providerMessageId)
        ? outcome.providerMessageId
        : null;
    if (!providerMessageId) {
      throw new SmsError(sanitize('短信通道拒绝投递（未返回回执号）', [code]), CODE.SMS_REJECTED);
    }
    return Object.freeze({ providerMessageId, sentAt: nowIso() });
  };

  const sendOtp = ({ phone, code, ttlSeconds } = {}) => {
    const normalizedPhone = normalizePhone(phone);
    if (normalizedPhone.length !== 11) {
      throw new SmsError('手机号格式非法', CODE.INVALID_PHONE);
    }
    const codeText = String(code ?? '');
    if (!isNonEmptyString(codeText)) {
      throw new SmsError('验证码参数非法', CODE.SMS_FAILED);
    }
    const ttl = Number.isFinite(ttlSeconds) ? Math.trunc(ttlSeconds) : null;

    if (kind === 'dev') {
      // dev：只在内存记录，不产生任何网络调用（测试可直接读取）
      devMessages.push(
        Object.freeze({ phone: normalizedPhone, code: codeText, ttlSeconds: ttl, at: nowIso() })
      );
      return Object.freeze({ providerMessageId: `dev-${randomUUID()}`, sentAt: nowIso() });
    }

    if (kind === 'log') {
      // log：等价外发；日志中绝不出现验证码明文
      const providerMessageId = `log-${randomUUID()}`;
      const line = sanitize(
        `[${name}] providerMessageId=${providerMessageId} phone=${maskPhone(normalizedPhone)} ttl=${String(ttl)}`,
        [codeText]
      );
      console.log(line);
      return Object.freeze({ providerMessageId, sentAt: nowIso() });
    }

    // vendor：注入 transport({phone, code, ttlSeconds}) -> {providerMessageId}
    let outcome;
    try {
      outcome = transport({ phone: normalizedPhone, code: codeText, ttlSeconds: ttl });
    } catch (error) {
      throw mapFailure(error, codeText);
    }
    if (outcome && typeof outcome.then === 'function') {
      return withTimeout(outcome, timeoutMs).then(
        (value) => finalizeSuccess(value, codeText),
        (error) => {
          if (error && error.code === TIMEOUT_MARKER) {
            throw new SmsError(sanitize('短信通道超时', [codeText]), CODE.SMS_TIMEOUT);
          }
          throw mapFailure(error, codeText);
        }
      );
    }
    return finalizeSuccess(outcome, codeText);
  };

  return Object.freeze({
    name,
    kind,
    info: () =>
      Object.freeze({
        name,
        kind,
        available: kind !== 'vendor' || typeof transport === 'function'
      }),
    sendOtp,
    /** 附加方法：dev 通道的内存记录（测试/演示读取；其它 kind 为空） */
    messages: () => Object.freeze([...devMessages]),
    lastMessage: () => devMessages.at(-1) ?? null,
    /** 兼容别名 */
    records: () => Object.freeze([...devMessages])
  });
}
