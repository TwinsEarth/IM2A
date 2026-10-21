// 测试：账户/OTP/实名/付费准入（契约 §36）。
// 覆盖：未实名 REALNAME_REQUIRED、明文永不落库（hash+掩码）、OTP TTL/尝试次数/单次有效、SESSION_INVALID/EXPIRED、SUSPENDED 写阻断。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  ACCOUNT_CODE,
  ACCOUNT_STATE,
  AccountError,
  AccountService
} from '../src/account.mjs';

const PHONE_A = '13800000001';
const PHONE_B = '13800000002';
const VALID_ID = '110101199003071234';

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.currentMs = () => currentMs;
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

const makeService = (options = {}) => {
  const clock = options.clock ?? makeClock();
  const service = new AccountService({
    clock,
    otp: { devMode: options.devMode ?? true, ...(options.otp ?? {}) },
    ...(options.realName ? { realName: options.realName } : {}),
    ...(options.session ? { session: options.session } : {})
  });
  return { clock, service };
};

const registerUser = (service, phone = PHONE_A, displayName = '林某') => {
  const challenge = service.requestOtp({ phone });
  return service.register({ phone, code: challenge.devCode, displayName });
};

const verifyUser = (service, accountId, overrides = {}) =>
  service.verifyRealName({ accountId, realName: '林某', idNumber: VALID_ID, ...overrides });

test('ACCOUNT_STATE / ACCOUNT_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...ACCOUNT_STATE }, {
    UNVERIFIED: 'UNVERIFIED',
    VERIFIED: 'VERIFIED',
    REJECTED: 'REJECTED',
    SUSPENDED: 'SUSPENDED'
  });
  assert.deepEqual({ ...ACCOUNT_CODE }, {
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
  assert.ok(Object.isFrozen(ACCOUNT_STATE));
  assert.ok(Object.isFrozen(ACCOUNT_CODE));
});

test('requestOtp：dev 模式返回 devCode；生产模式（默认）绝不下发 devCode', () => {
  const { clock, service } = makeService();
  const challenge = service.requestOtp({ phone: PHONE_A });
  assert.ok(Object.isFrozen(challenge));
  assert.match(challenge.challengeId, /^otp-/);
  assert.equal(challenge.expiresAt, '2026-10-21T00:05:00.000Z'); // 默认 TTL 5 分钟
  assert.match(challenge.devCode, /^\d{6}$/);
  assert.equal(challenge.devCodeOnlyInDev, true);
  assert.equal(clock.iso(), '2026-10-21T00:00:00.000Z');

  const { service: prod } = makeService({ devMode: false });
  const prodChallenge = prod.requestOtp({ phone: PHONE_A });
  assert.equal('devCode' in prodChallenge, false);
  assert.equal('devCodeOnlyInDev' in prodChallenge, false);
});

test('未实名：canUsePaidMessaging => REALNAME_REQUIRED；实名后 => ok:true；未知账户 => NOT_FOUND', () => {
  const { service } = makeService();
  const { account } = registerUser(service);
  assert.equal(account.state, ACCOUNT_STATE.UNVERIFIED);

  const before = service.canUsePaidMessaging({ accountId: account.id });
  assert.deepEqual({ ...before }, { ok: false, code: 'REALNAME_REQUIRED' });
  assert.ok(Object.isFrozen(before));
  assert.deepEqual({ ...service.canUsePaidMessaging({ accountId: 'acct-missing' }) }, {
    ok: false,
    code: 'NOT_FOUND'
  });

  verifyUser(service, account.id);
  assert.match(service.get(account.id).state, /^VERIFIED$/);
  assert.deepEqual({ ...service.canUsePaidMessaging({ accountId: account.id }) }, { ok: true, code: null });
});

test('实名核验：只返回 hash+尾号掩码；账号状态 VERIFIED；已实名不可更换身份（同号幂等）', () => {
  const { service } = makeService();
  const { account } = registerUser(service);
  const result = verifyUser(service, account.id);
  assert.equal(result.state, ACCOUNT_STATE.VERIFIED);
  assert.match(result.idNumberHash, /^[0-9a-f]{64}$/);
  assert.equal(result.maskedTail, '****1234');
  assert.ok(Object.isFrozen(result));

  // 同身份证再验：幂等返回同一 hash
  const again = verifyUser(service, account.id);
  assert.equal(again.idNumberHash, result.idNumberHash);
  // 不同身份证：拒绝更换
  assert.throws(
    () => verifyUser(service, account.id, { idNumber: '11010119900307123X' }),
    { code: 'REALNAME_REJECTED' }
  );
  assert.equal(service.get(account.id).state, ACCOUNT_STATE.VERIFIED);
});

test('不变量①：idNumber 明文永不落库（序列化/快照只含 hash + 掩码，两次 salt 同号 hash 不同）', () => {
  const { service } = makeService();
  const first = registerUser(service, PHONE_A);
  const second = registerUser(service, PHONE_B);
  const resultA = verifyUser(service, first.account.id);
  const resultB = verifyUser(service, second.account.id);

  const account = service.get(first.account.id);
  assert.equal('idNumber' in account, false);
  assert.equal('idNumber' in account.realNameInfo, false);
  assert.equal(account.realNameInfo.idNumberHash, resultA.idNumberHash);
  assert.equal(account.realNameInfo.maskedTail, '****1234');
  assert.notEqual(account.realNameInfo.salt, service.get(second.account.id).realNameInfo.salt);

  // 同一身份证在不同账号（不同 salt）下 hash 必不相同
  assert.notEqual(resultA.idNumberHash, resultB.idNumberHash);

  const serialized = JSON.stringify(service.snapshot());
  assert.equal(serialized.includes(VALID_ID), false);
  assert.equal(serialized.includes('11010119900307'), false);
  assert.equal(serialized.includes('****1234'), true); // 掩码可以出现
});

test('OTP 单次有效：注册用过的 code 再用于登录 => OTP_INVALID', () => {
  const { service } = makeService();
  const challenge = service.requestOtp({ phone: PHONE_A });
  service.register({ phone: PHONE_A, code: challenge.devCode });
  assert.throws(() => service.login({ phone: PHONE_A, code: challenge.devCode }), {
    code: 'OTP_INVALID'
  });
  // 重新获取验证码后才能登录
  const newChallenge = service.requestOtp({ phone: PHONE_A });
  assert.doesNotThrow(() => service.login({ phone: PHONE_A, code: newChallenge.devCode }));
});

test('边界：OTP 错误码 5 次 => OTP_ATTEMPTS_EXCEEDED；之后即使给对码也拒绝', () => {
  const { service } = makeService();
  const challenge = service.requestOtp({ phone: PHONE_A });
  const wrong = challenge.devCode === '000000' ? '111111' : '000000';
  for (let attempt = 1; attempt <= 4; attempt += 1) {
    assert.throws(() => service.register({ phone: PHONE_A, code: wrong }), { code: 'OTP_INVALID' });
  }
  // 第 5 次错误 => 超过尝试上限
  assert.throws(() => service.register({ phone: PHONE_A, code: wrong }), {
    code: 'OTP_ATTEMPTS_EXCEEDED'
  });
  // 之后即使输入正确验证码也不能再用该 challenge
  assert.throws(() => service.register({ phone: PHONE_A, code: challenge.devCode }), {
    code: 'OTP_ATTEMPTS_EXCEEDED'
  });
});

test('边界：OTP 过期（注入 clock 推过 TTL）=> OTP_EXPIRED', () => {
  const { clock, service } = makeService({ otp: { ttlMs: 1000 } });
  const challenge = service.requestOtp({ phone: PHONE_A });
  clock.advance(1000); // 恰好到期
  assert.throws(() => service.register({ phone: PHONE_A, code: challenge.devCode }), {
    code: 'OTP_EXPIRED'
  });
  // 过期后重新申请可继续
  const fresh = service.requestOtp({ phone: PHONE_A });
  assert.doesNotThrow(() => service.register({ phone: PHONE_A, code: fresh.devCode }));
});

test('ALREADY_REGISTERED：同手机号重复注册被拒（新 OTP 也不行）', () => {
  const { service } = makeService();
  registerUser(service, PHONE_A);
  const challenge = service.requestOtp({ phone: PHONE_A });
  assert.throws(() => service.register({ phone: PHONE_A, code: challenge.devCode }), {
    code: 'ALREADY_REGISTERED'
  });
});

test('login/authenticate：正常登录返回账号+session；非法 token => SESSION_INVALID', () => {
  const { service } = makeService();
  const registered = registerUser(service);
  const challenge = service.requestOtp({ phone: PHONE_A });
  const loggedIn = service.login({ phone: PHONE_A, code: challenge.devCode });
  assert.equal(loggedIn.account.id, registered.account.id);
  assert.match(loggedIn.session.accessToken, /^sess-/);
  assert.equal(loggedIn.session.accountId, registered.account.id);
  assert.ok(Object.isFrozen(loggedIn.session));

  const authenticated = service.authenticate({ accessToken: loggedIn.session.accessToken });
  assert.equal(authenticated.account.id, registered.account.id);
  assert.equal(authenticated.session.accessToken, loggedIn.session.accessToken);

  assert.throws(() => service.authenticate({ accessToken: 'sess-forged' }), { code: 'SESSION_INVALID' });
  assert.throws(() => service.authenticate({}), { code: 'SESSION_INVALID' });
  assert.throws(() => service.login({ phone: '13900000000', code: '123456' }), { code: 'NOT_FOUND' });
});

test('会话过期 => SESSION_EXPIRED（不静默续期，重复调用仍明确报过期）', () => {
  const { clock, service } = makeService({ session: { ttlMs: 1000 } });
  const { session } = registerUser(service);
  assert.equal(service.authenticate({ accessToken: session.accessToken }).account.state, 'UNVERIFIED');

  clock.advance(1000); // 恰好到期
  assert.throws(() => service.authenticate({ accessToken: session.accessToken }), {
    code: 'SESSION_EXPIRED'
  });
  // 不静默续期：没有生成新 session，再次调用依旧 SESSION_EXPIRED
  assert.equal(service.snapshot().sessions.length, 1);
  assert.throws(() => service.authenticate({ accessToken: session.accessToken }), {
    code: 'SESSION_EXPIRED'
  });
});

test('实名失败 => REALNAME_REJECTED（可重试）；自定义 mock provider 可拒绝', () => {
  const { service } = makeService();
  const { account } = registerUser(service);
  assert.throws(() => verifyUser(service, account.id, { idNumber: '123' }), {
    code: 'REALNAME_REJECTED'
  });
  assert.equal(service.get(account.id).state, ACCOUNT_STATE.REJECTED);
  assert.deepEqual({ ...service.canUsePaidMessaging({ accountId: account.id }) }, {
    ok: false,
    code: 'REALNAME_REQUIRED'
  });
  // 修正信息后可重试成功
  assert.equal(verifyUser(service, account.id).state, 'VERIFIED');

  const { service: strict } = makeService({
    realName: {
      provider: ({ realName }) => (realName === '被拒绝' ? { ok: false, reason: 'mock deny' } : { ok: true })
    }
  });
  const strictAccount = registerUser(strict);
  assert.throws(
    () => strict.verifyRealName({ accountId: strictAccount.account.id, realName: '被拒绝', idNumber: VALID_ID }),
    { code: 'REALNAME_REJECTED' }
  );
  assert.equal(
    strict.verifyRealName({ accountId: strictAccount.account.id, realName: '林某', idNumber: VALID_ID }).state,
    'VERIFIED'
  );
});

test('SUSPENDED：阻断登录以外的写操作（实名/付费准入 FORBIDDEN），但允许登录与读取', () => {
  const { service } = makeService();
  const { account } = registerUser(service);
  verifyUser(service, account.id);

  const suspended = service.suspend({ accountId: account.id, reason: '风控复核' });
  assert.equal(suspended.state, ACCOUNT_STATE.SUSPENDED);
  assert.equal(suspended.suspendedReason, '风控复核');
  assert.ok(suspended.suspendedAt !== null);
  assert.equal(service.suspend({ accountId: account.id, reason: '再次' }).state, 'SUSPENDED'); // 幂等

  assert.deepEqual({ ...service.canUsePaidMessaging({ accountId: account.id }) }, {
    ok: false,
    code: 'FORBIDDEN'
  });
  assert.throws(() => verifyUser(service, account.id), { code: 'FORBIDDEN' });

  // 登录仍允许（用于查看封禁原因）
  const challenge = service.requestOtp({ phone: PHONE_A });
  const loggedIn = service.login({ phone: PHONE_A, code: challenge.devCode });
  assert.equal(loggedIn.account.state, ACCOUNT_STATE.SUSPENDED);
  assert.equal(service.authenticate({ accessToken: loggedIn.session.accessToken }).account.state, 'SUSPENDED');
  assert.throws(() => service.suspend({ accountId: 'acct-missing', reason: 'x' }), { code: 'NOT_FOUND' });
});

test('get / verifyRealName：未知账户 => NOT_FOUND；缺 idNumber => REALNAME_REJECTED', () => {
  const { service } = makeService();
  assert.throws(() => service.get('acct-missing'), { code: 'NOT_FOUND' });
  assert.throws(() => verifyUser(service, 'acct-missing'), { code: 'NOT_FOUND' });
  const { account } = registerUser(service);
  assert.throws(() => verifyUser(service, account.id, { idNumber: undefined }), {
    code: 'REALNAME_REJECTED'
  });
});

test('AccountError 带 code 且是 Error 子类', () => {
  const { service } = makeService();
  try {
    service.get('acct-missing');
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof AccountError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'NOT_FOUND');
  }
});
