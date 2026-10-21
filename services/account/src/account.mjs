// 模块职责：账户、手机号 OTP 登录、实名核验与付费准入——"能对真实用户开付费"的前置：没有实名就没有可追责主体。
// 契约来源：contracts/INTERFACES.md §36。
// 关键不变量：①idNumber 只存 sha256(salt+idNumber) 与尾号掩码，绝不存明文；②OTP 单次有效、有 TTL、有尝试次数上限；
//             ③未实名账号 canUsePaidMessaging 恒 {ok:false, code:'REALNAME_REQUIRED'}；④SUSPENDED 阻断登录以外的写操作；⑤access token 过期是 SESSION_EXPIRED，绝不静默续期。
import { createHash, randomBytes, randomInt, randomUUID } from 'node:crypto';

export const ACCOUNT_STATE = Object.freeze({
  UNVERIFIED: 'UNVERIFIED',
  VERIFIED: 'VERIFIED',
  REJECTED: 'REJECTED',
  SUSPENDED: 'SUSPENDED'
});

export const ACCOUNT_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  ALREADY_REGISTERED: 'ALREADY_REGISTERED',
  OTP_INVALID: 'OTP_INVALID',
  OTP_EXPIRED: 'OTP_EXPIRED',
  OTP_ATTEMPTS_EXCEEDED: 'OTP_ATTEMPTS_EXCEEDED',
  REALNAME_REQUIRED: 'REALNAME_REQUIRED',
  REALNAME_REJECTED: 'REALNAME_REJECTED',
  SESSION_INVALID: 'SESSION_INVALID',
  SESSION_EXPIRED: 'SESSION_EXPIRED',
  FORBIDDEN: 'FORBIDDEN'
});

const CODE = ACCOUNT_CODE;
const DEFAULT_OTP_TTL_MS = 5 * 60e3;
const DEFAULT_OTP_MAX_ATTEMPTS = 5;
const DEFAULT_SESSION_TTL_MS = 7 * 864e5;
const ID_NUMBER_PATTERN = /^\d{17}[\dXx]$/;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

/** 手机号归一：只留数字并去掉中国区号前缀（不改变用户可见展示字段） */
function normalizePhone(phone) {
  const digits = String(phone ?? '').replace(/[^\d]/g, '');
  if (digits.length === 13 && digits.startsWith('86')) return digits.slice(2);
  return digits;
}

const sha256Hex = (text) => createHash('sha256').update(text, 'utf8').digest('hex');

/** 默认 mock 实名提供方：只校验基本形态；生产替换为持牌三要素核验（接口保持 {ok, code?, reason?}） */
function defaultRealNameProvider({ realName, idNumber }) {
  if (!isNonEmptyString(realName) || realName.trim().length < 2) {
    return { ok: false, reason: 'realName invalid' };
  }
  if (!ID_NUMBER_PATTERN.test(String(idNumber ?? ''))) return { ok: false, reason: 'idNumber invalid' };
  return { ok: true };
}

export class AccountError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'AccountError';
    this.code = code;
  }
}

const freezeAccount = (account) => Object.freeze({ ...account });

export class AccountService {
  #clock;
  #otpTtlMs;
  #otpMaxAttempts;
  #devMode;
  #sessionTtlMs;
  #realNameProvider;
  #saltBytes;
  /** @type {Map<string, object>} accountId -> frozen account */
  #accounts;
  /** @type {Map<string, string>} normalized phone -> accountId */
  #phoneIndex;
  /** @type {Map<string, object>} challengeId -> frozen challenge（含 code，仅内存，绝不持久化） */
  #challenges;
  /** @type {Map<string, string[]>} phone -> challengeId[]（按时间先后） */
  #challengesByPhone;
  /** @type {Map<string, object>} accessToken -> frozen session */
  #sessions;
  /** @type {Function|null} 验证码投递回调（v0.0.2：真实短信通道由此注入，验证码永不离开本服务） */
  #otpDeliver;

  constructor({ clock = () => new Date(), otp = {}, realName = {}, session = {} } = {}) {
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#otpTtlMs =
      Number.isFinite(otp.ttlMs) && otp.ttlMs > 0 ? Math.trunc(otp.ttlMs) : DEFAULT_OTP_TTL_MS;
    this.#otpMaxAttempts =
      Number.isInteger(otp.maxAttempts) && otp.maxAttempts > 0 ? otp.maxAttempts : DEFAULT_OTP_MAX_ATTEMPTS;
    this.#devMode = otp.devMode === true; // 生产默认 false：devCode 绝不下发
    // v0.0.2 §38：投递回调（短信通道）。可选；不注入时保持 v0.0.1 行为（dev 模式取 devCode）。
    this.#otpDeliver = typeof otp.deliver === 'function' ? otp.deliver : null;
    this.#sessionTtlMs =
      Number.isFinite(session.ttlMs) && session.ttlMs > 0 ? Math.trunc(session.ttlMs) : DEFAULT_SESSION_TTL_MS;
    if (realName.provider !== undefined && realName.provider !== null && typeof realName.provider !== 'function') {
      throw new TypeError('realName.provider 必须是函数');
    }
    this.#realNameProvider = realName.provider ?? defaultRealNameProvider;
    this.#saltBytes = Number.isInteger(realName.saltBytes) && realName.saltBytes >= 8 ? realName.saltBytes : 16;
    this.#accounts = new Map();
    this.#phoneIndex = new Map();
    this.#challenges = new Map();
    this.#challengesByPhone = new Map();
    this.#sessions = new Map();
  }

  #now() {
    const sampled = this.#clock();
    return sampled instanceof Date ? new Date(sampled.getTime()) : new Date(sampled);
  }

  #nowIso() {
    return this.#now().toISOString();
  }

  #requireAccount(accountId) {
    const account = this.#accounts.get(accountId);
    if (!account) throw new AccountError(`账户不存在: ${String(accountId)}`, CODE.NOT_FOUND);
    return account;
  }

  #assertWritable(account) {
    // 不变量④：SUSPENDED 阻断登录以外的所有写操作
    if (account.state === ACCOUNT_STATE.SUSPENDED) {
      throw new AccountError('账户已被暂停，写操作被禁止', CODE.FORBIDDEN);
    }
  }

  #putAccount(next) {
    const frozen = freezeAccount(next);
    this.#accounts.set(frozen.id, frozen);
    return frozen;
  }

  #createSession(accountId) {
    const now = this.#now();
    const session = Object.freeze({
      accessToken: `sess-${randomUUID()}`,
      accountId,
      issuedAt: now.toISOString(),
      expiresAt: new Date(now.getTime() + this.#sessionTtlMs).toISOString()
    });
    this.#sessions.set(session.accessToken, session);
    return session;
  }

  /** 取该手机号最新一条 OTP challenge */
  #latestChallenge(phone) {
    const ids = this.#challengesByPhone.get(phone) ?? [];
    for (let index = ids.length - 1; index >= 0; index -= 1) {
      const challenge = this.#challenges.get(ids[index]);
      if (challenge) return challenge;
    }
    return null;
  }

  /** OTP 校验：单次有效 + TTL + 尝试次数上限（不变量②） */
  #consumeOtp(phone, code) {
    const challenge = this.#latestChallenge(phone);
    if (!challenge) throw new AccountError('OTP 不存在，请先获取验证码', CODE.OTP_INVALID);
    if (challenge.consumed) {
      throw new AccountError('OTP 已使用（单次有效）', CODE.OTP_INVALID);
    }
    const nowMs = this.#now().getTime();
    if (Date.parse(challenge.expiresAt) <= nowMs) {
      throw new AccountError('OTP 已过期', CODE.OTP_EXPIRED);
    }
    if (challenge.attempts >= challenge.maxAttempts) {
      throw new AccountError('OTP 尝试次数已用尽', CODE.OTP_ATTEMPTS_EXCEEDED);
    }
    if (String(code ?? '') !== challenge.code) {
      const attempts = challenge.attempts + 1;
      const updated = Object.freeze({ ...challenge, attempts });
      this.#challenges.set(challenge.challengeId, updated);
      if (attempts >= challenge.maxAttempts) {
        throw new AccountError('OTP 尝试次数已用尽', CODE.OTP_ATTEMPTS_EXCEEDED);
      }
      throw new AccountError('OTP 错误', CODE.OTP_INVALID);
    }
    const consumed = Object.freeze({ ...challenge, consumed: true, consumedAt: this.#nowIso() });
    this.#challenges.set(challenge.challengeId, consumed);
    return consumed;
  }

  // ── 公开 API（契约 §36）────────────────────────────────────────────────────

  /** v0.1.2：导出可持久化状态（账号/手机号索引/会话）。**OTP challenge 不导出**（短命且含明文验证码） */
  exportState() {
    return {
      accounts: [...this.#accounts.values()].map((account) => ({ ...account })),
      phoneIndex: [...this.#phoneIndex.entries()],
      sessions: [...this.#sessions.values()].map((session) => ({ ...session }))
    };
  }

  /** v0.1.2：从持久化状态恢复（只恢复账号/索引/会话；challenge 一律重建） */
  importState(state = {}) {
    if (state === null || typeof state !== 'object' || !Array.isArray(state.accounts)) {
      throw new TypeError('importState 需要 { accounts: [] }');
    }
    this.#accounts = new Map(state.accounts.map((account) => [account.id, freezeAccount(account)]));
    this.#phoneIndex = new Map(Array.isArray(state.phoneIndex) ? state.phoneIndex : []);
    this.#sessions = new Map(
      (Array.isArray(state.sessions) ? state.sessions : []).map((session) => [session.accessToken, Object.freeze({ ...session })])
    );
    return { accounts: this.#accounts.size, sessions: this.#sessions.size };
  }

  /** 生成一次性验证码挑战；只有 devMode 才返回 devCode（生产绝不下发） */
  requestOtp({ phone } = {}) {
    const normalizedPhone = normalizePhone(phone);
    if (normalizedPhone.length !== 11) throw new TypeError('requestOtp: phone 必须是 11 位手机号');
    const now = this.#now();
    const expiresAt = new Date(now.getTime() + this.#otpTtlMs).toISOString();
    const challenge = Object.freeze({
      challengeId: `otp-${randomUUID()}`,
      phone: normalizedPhone,
      code: String(randomInt(0, 1_000_000)).padStart(6, '0'),
      createdAt: now.toISOString(),
      expiresAt,
      attempts: 0,
      maxAttempts: this.#otpMaxAttempts,
      consumed: false,
      consumedAt: null
    });
    this.#challenges.set(challenge.challengeId, challenge);
    const ids = this.#challengesByPhone.get(normalizedPhone) ?? [];
    ids.push(challenge.challengeId);
    this.#challengesByPhone.set(normalizedPhone, ids);

    const response = { challengeId: challenge.challengeId, expiresAt };
    // v0.0.2：投递（真实短信）。失败会向上抛（由调用方转成 SMS_FAILED），不会静默"假装发了"
    if (this.#otpDeliver !== null) {
      try {
        this.#otpDeliver({
          phone: normalizedPhone,
          code: challenge.code,
          challengeId: challenge.challengeId,
          expiresAt,
          ttlSeconds: Math.floor(this.#otpTtlMs / 1000)
        });
      } catch (error) {
        // 投递失败必须回滚挑战：否则"冷却期内重发失败"会留下一个用户永远收不到的
        // 最新挑战，导致用户拿上一条短信的码去校验却报 OTP_INVALID（真实踩过）。
        this.#challenges.delete(challenge.challengeId);
        const remaining = (this.#challengesByPhone.get(normalizedPhone) ?? []).filter((id) => id !== challenge.challengeId);
        if (remaining.length === 0) this.#challengesByPhone.delete(normalizedPhone);
        else this.#challengesByPhone.set(normalizedPhone, remaining);
        throw error;
      }
    }
    if (this.#devMode) {
      // dev 模式才回传明文验证码；生产环境该字段不存在
      response.devCode = challenge.code;
      response.devCodeOnlyInDev = true;
    }
    return Object.freeze(response);
  }

  /** 注册：OTP 通过 → UNVERIFIED 账号 + session；同手机号重复注册 => ALREADY_REGISTERED */
  register({ phone, code, displayName = null } = {}) {
    const normalizedPhone = normalizePhone(phone);
    if (normalizedPhone.length !== 11) throw new TypeError('register: phone 必须是 11 位手机号');
    if (this.#phoneIndex.has(normalizedPhone)) {
      throw new AccountError('该手机号已注册', CODE.ALREADY_REGISTERED);
    }
    this.#consumeOtp(normalizedPhone, code);
    const nowIso = this.#nowIso();
    const account = this.#putAccount({
      id: `acct-${randomUUID()}`,
      phone: normalizedPhone,
      displayName: isNonEmptyString(displayName) ? displayName : `用户${normalizedPhone.slice(-4)}`,
      state: ACCOUNT_STATE.UNVERIFIED,
      createdAt: nowIso,
      updatedAt: nowIso,
      realNameInfo: null,
      suspendedReason: null,
      suspendedAt: null
    });
    this.#phoneIndex.set(normalizedPhone, account.id);
    return Object.freeze({ account, session: this.#createSession(account.id) });
  }

  /**
   * 实名核验：只存 sha256(salt + idNumber) 与尾号掩码，绝不存明文。
   * @returns {Readonly<{state:string, idNumberHash:string, maskedTail:string}>}
   */
  verifyRealName({ accountId, realName, idNumber } = {}) {
    const account = this.#requireAccount(accountId);
    this.#assertWritable(account);

    const rawIdNumber = String(idNumber ?? '');
    // mock/持牌提供方接口：{ ok, code?, reason? }；生产替换实现，调用方接口不变
    let verdict;
    try {
      verdict = this.#realNameProvider({ realName, idNumber: rawIdNumber });
    } catch {
      verdict = { ok: false, reason: 'provider error' };
    }
    if (!verdict || verdict.ok !== true) {
      // 核验失败是可重试的 REJECTED（不是 SUSPENDED，后者才阻断所有写操作）
      this.#putAccount({ ...account, state: ACCOUNT_STATE.REJECTED, updatedAt: this.#nowIso() });
      const error = new AccountError(
        `实名核验失败${verdict?.reason ? `: ${verdict.reason}` : ''}`,
        CODE.REALNAME_REJECTED
      );
      error.providerCode = verdict?.code ?? null;
      throw error;
    }

    // 已实名账号不可更换身份（同身份证幂等，不同身份证拒绝）
    if (account.state === ACCOUNT_STATE.VERIFIED && account.realNameInfo) {
      const sameId = sha256Hex(account.realNameInfo.salt + rawIdNumber) === account.realNameInfo.idNumberHash;
      if (!sameId) throw new AccountError('已实名账户不可更换实名信息', CODE.REALNAME_REJECTED);
      return Object.freeze({
        state: ACCOUNT_STATE.VERIFIED,
        idNumberHash: account.realNameInfo.idNumberHash,
        maskedTail: account.realNameInfo.maskedTail
      });
    }

    const salt = randomBytes(this.#saltBytes).toString('hex');
    const idNumberHash = sha256Hex(salt + rawIdNumber);
    const maskedTail = `****${rawIdNumber.slice(-4)}`;
    const nowIso = this.#nowIso();
    this.#putAccount({
      ...account,
      state: ACCOUNT_STATE.VERIFIED,
      updatedAt: nowIso,
      realNameInfo: Object.freeze({
        realName: isNonEmptyString(realName) ? realName.trim() : null,
        idNumberHash,
        maskedTail,
        salt,
        verifiedAt: nowIso
      })
    });
    return Object.freeze({ state: ACCOUNT_STATE.VERIFIED, idNumberHash, maskedTail });
  }

  /** 手机号 + OTP 登录（SUSPENDED 仍允许登录查看状态，但写操作被阻断） */
  login({ phone, code } = {}) {
    const normalizedPhone = normalizePhone(phone);
    const accountId = this.#phoneIndex.get(normalizedPhone);
    if (!accountId) throw new AccountError('账户不存在', CODE.NOT_FOUND);
    this.#consumeOtp(normalizedPhone, code);
    const account = this.#requireAccount(accountId);
    return Object.freeze({ account, session: this.#createSession(accountId) });
  }

  /** 校验 access token；过期是 SESSION_EXPIRED，绝不静默续期 */
  authenticate({ accessToken } = {}) {
    if (!isNonEmptyString(accessToken)) {
      throw new AccountError('缺少 access token', CODE.SESSION_INVALID);
    }
    const session = this.#sessions.get(accessToken);
    if (!session) throw new AccountError('access token 非法', CODE.SESSION_INVALID);
    if (Date.parse(session.expiresAt) <= this.#now().getTime()) {
      // 不续期、不删除：重复调用仍明确报告过期
      throw new AccountError('access token 已过期，请重新登录', CODE.SESSION_EXPIRED);
    }
    const account = this.#requireAccount(session.accountId);
    return Object.freeze({ account, session });
  }

  /** 付费消息准入：必须 VERIFIED 且非 SUSPENDED */
  canUsePaidMessaging({ accountId } = {}) {
    const account = this.#accounts.get(accountId);
    if (!account) return Object.freeze({ ok: false, code: CODE.NOT_FOUND });
    if (account.state === ACCOUNT_STATE.SUSPENDED) {
      return Object.freeze({ ok: false, code: CODE.FORBIDDEN });
    }
    if (account.state !== ACCOUNT_STATE.VERIFIED) {
      return Object.freeze({ ok: false, code: CODE.REALNAME_REQUIRED });
    }
    return Object.freeze({ ok: true, code: null });
  }

  /** 暂停账户（人工风控）：阻断登录以外的所有写操作 */
  suspend({ accountId, reason = null } = {}) {
    const account = this.#requireAccount(accountId);
    if (account.state === ACCOUNT_STATE.SUSPENDED) return account; // 幂等
    const nowIso = this.#nowIso();
    return this.#putAccount({
      ...account,
      state: ACCOUNT_STATE.SUSPENDED,
      suspendedReason: isNonEmptyString(reason) ? reason : null,
      suspendedAt: nowIso,
      updatedAt: nowIso
    });
  }

  get(accountId) {
    return this.#requireAccount(accountId);
  }

  /**
   * 附加方法：可序列化内部快照（供测试/持久化检查）——只含账户与 session，
   * 绝不包含 idNumber 明文，也不包含 OTP 明文。
   */
  snapshot() {
    const accounts = [...this.#accounts.values()].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
    const sessions = [...this.#sessions.values()].sort((a, b) =>
      a.accessToken < b.accessToken ? -1 : a.accessToken > b.accessToken ? 1 : 0
    );
    return Object.freeze({
      accounts: Object.freeze(accounts.map(freezeAccount)),
      sessions: Object.freeze(sessions.map((session) => Object.freeze({ ...session })))
    });
  }
}
