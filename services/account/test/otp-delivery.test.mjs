// 测试：OTP 投递策略（契约 §38）。
// 覆盖：冷却（失败不占冷却）、自然日上限按尝试计数、失败不静默、验证码不入日志/审计、与 AccountService 的 otp.deliver 集成。
import test from 'node:test';
import assert from 'node:assert/strict';

import { AccountService } from '../src/account.mjs';
import { OtpDelivery, OtpDeliveryError, OTP_DELIVERY_CODE } from '../src/otp-delivery.mjs';
import { SmsError, createSmsProvider } from '../src/sms-provider.mjs';

const PHONE = '13800000001';
const OTHER_PHONE = '13800000002';
const CODE = '246810';

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

/** 可控 fake provider：记录调用次数与参数 */
function makeFakeProvider(handler = () => null) {
  const calls = [];
  const provider = {
    name: 'fake',
    kind: 'vendor',
    info: () => ({ name: 'fake', kind: 'vendor', available: true }),
    sendOtp: (args) => {
      calls.push(args);
      return handler(args, calls.length);
    }
  };
  return { provider, calls };
}

const successHandler = (clock) => () => ({ providerMessageId: 'msg-ok', sentAt: clock.iso() });

test('OTP_DELIVERY_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...OTP_DELIVERY_CODE }, {
    OTP_COOLDOWN: 'OTP_COOLDOWN',
    OTP_DAILY_CAP: 'OTP_DAILY_CAP',
    OTP_DELIVERY_FAILED: 'OTP_DELIVERY_FAILED'
  });
  assert.ok(Object.isFrozen(OTP_DELIVERY_CODE));
});

test('正常投递：调用 provider 并返回回执；stats.sent 计数', () => {
  const clock = makeClock();
  const { provider, calls } = makeFakeProvider(successHandler(clock));
  const delivery = new OtpDelivery({ provider, clock });
  const result = delivery.deliver({ phone: PHONE, code: CODE, challengeId: 'otp-1', ttlSeconds: 300 });
  assert.ok(Object.isFrozen(result));
  assert.equal(result.providerMessageId, 'msg-ok');
  assert.equal(result.sentAt, '2026-10-21T00:00:00.000Z');
  assert.deepEqual(calls, [{ phone: PHONE, code: CODE, ttlSeconds: 300 }]);
  assert.deepEqual({ ...delivery.stats() }, { sent: 1, failed: 0, cooldownHits: 0, dailyCapHits: 0 });
  assert.ok(Object.isFrozen(delivery.stats()));
});

test('冷却：同号连续两次 => 第二次 OTP_COOLDOWN 且 provider 只被调用 1 次；60s 后可再发', () => {
  const clock = makeClock();
  const { provider, calls } = makeFakeProvider(successHandler(clock));
  const delivery = new OtpDelivery({ provider, clock });

  delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 });
  assert.doesNotThrow(() => delivery.deliver({ phone: OTHER_PHONE, code: CODE, ttlSeconds: 300 }));
  assert.equal(calls.length, 2);

  assert.throws(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }), {
    code: 'OTP_COOLDOWN'
  });
  assert.equal(calls.length, 2); // 冷却命中不重发
  assert.deepEqual({ ...delivery.stats() }, { sent: 2, failed: 0, cooldownHits: 1, dailyCapHits: 0 });

  clock.advance(60_000 - 1); // 59.999s 仍在冷却
  assert.throws(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }), {
    code: 'OTP_COOLDOWN'
  });
  clock.advance(1); // 恰好 60s：可再发
  assert.doesNotThrow(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }));
  assert.equal(calls.length, 3);
});

test('日上限：同日第 6 次 => OTP_DAILY_CAP（default 5），次日恢复', () => {
  const clock = makeClock();
  const { provider, calls } = makeFakeProvider(successHandler(clock));
  const delivery = new OtpDelivery({ provider, clock });
  for (let attempt = 0; attempt < 5; attempt += 1) {
    assert.doesNotThrow(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }));
    clock.advance(60_000); // 避开冷却，保持同一自然日
  }
  assert.throws(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }), {
    code: 'OTP_DAILY_CAP'
  });
  assert.equal(calls.length, 5); // 超限后不再调用 provider
  assert.equal(delivery.stats().dailyCapHits, 1);

  // 次日恢复
  clock.advance(24 * 3600e3);
  assert.doesNotThrow(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }));
  assert.equal(calls.length, 6);
  assert.equal(delivery.stats().sent, 6);
});

test('失败不静默：provider 抛错 => OTP_DELIVERY_FAILED（failed 计数），且失败不占用冷却可立即重试', () => {
  const clock = makeClock();
  const { provider, calls } = makeFakeProvider((args, count) => {
    if (count <= 2) throw new SmsError('通道故障', 'SMS_FAILED');
    return { providerMessageId: 'after-retry', sentAt: clock.iso() };
  });
  const delivery = new OtpDelivery({ provider, clock });

  assert.throws(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }), (error) => {
    assert.ok(error instanceof OtpDeliveryError);
    assert.equal(error.code, 'OTP_DELIVERY_FAILED');
    assert.equal(error.causeCode, 'SMS_FAILED');
    return true;
  });
  // 失败后立即可重试（没有冷却）
  assert.throws(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }), {
    code: 'OTP_DELIVERY_FAILED'
  });
  assert.equal(calls.length, 2);
  const result = delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 });
  assert.equal(result.providerMessageId, 'after-retry');
  assert.deepEqual({ ...delivery.stats() }, { sent: 1, failed: 2, cooldownHits: 0, dailyCapHits: 0 });
});

test('日上限按"投递尝试"计：连续失败也占用日额度，第 6 次 OTP_DAILY_CAP', () => {
  const clock = makeClock();
  const { provider, calls } = makeFakeProvider(() => {
    throw new SmsError('通道故障', 'SMS_FAILED');
  });
  const delivery = new OtpDelivery({ provider, clock });
  for (let attempt = 0; attempt < 5; attempt += 1) {
    assert.throws(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }), {
      code: 'OTP_DELIVERY_FAILED'
    });
  }
  assert.equal(calls.length, 5);
  assert.throws(() => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }), {
    code: 'OTP_DAILY_CAP'
  });
  assert.equal(calls.length, 5); // 超限后不再调用 provider
  assert.deepEqual({ ...delivery.stats() }, { sent: 0, failed: 5, cooldownHits: 0, dailyCapHits: 1 });
});

test('不变量④：错误消息与审计事件中都不出现验证码明文', () => {
  const clock = makeClock();
  const audits = [];
  const { provider } = makeFakeProvider(() => {
    // 模拟底层把 code 拼进错误消息的坏习惯
    throw new Error(`provider failed for code=${CODE}`);
  });
  const delivery = new OtpDelivery({ provider, clock, onAudit: (event) => audits.push(event) });
  try {
    delivery.deliver({ phone: PHONE, code: CODE, challengeId: 'otp-1', ttlSeconds: 300 });
    assert.fail('应当抛错');
  } catch (error) {
    assert.equal(error.code, 'OTP_DELIVERY_FAILED');
    assert.equal(error.message.includes(CODE), false);
  }
  assert.ok(audits.length > 0);
  assert.equal(JSON.stringify(audits).includes(CODE), false);
  assert.equal(audits.at(-1).detail.causeCode, 'SMS_FAILED');
});

test('附加兼容：deliver 可脱离 this 调用（供 AccountService 注入）；异步 provider 返回 Promise', async () => {
  const clock = makeClock();
  const { provider } = makeFakeProvider(() => Promise.resolve({ providerMessageId: 'async-1', sentAt: clock.iso() }));
  const delivery = new OtpDelivery({ provider, clock });
  const deliver = delivery.deliver; // 不绑定 this 也能用（AccountService 直接传函数引用）
  const result = await deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 });
  assert.equal(result.providerMessageId, 'async-1');
  assert.equal(delivery.stats().sent, 1);

  const failing = makeFakeProvider(() => Promise.reject(new SmsError('async fail', 'SMS_FAILED')));
  const asyncDelivery = new OtpDelivery({ provider: failing.provider, clock });
  await assert.rejects(
    () => asyncDelivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }),
    (error) => {
      assert.equal(error.code, 'OTP_DELIVERY_FAILED');
      assert.equal(error.message.includes(CODE), false);
      return true;
    }
  );
  assert.equal(asyncDelivery.stats().failed, 1);
});

test('集成 AccountService：devMode=false + otp.deliver => 验证码只进通道、返回对象没有 devCode', () => {
  const clock = makeClock();
  const provider = createSmsProvider({ kind: 'dev', clock });
  const delivery = new OtpDelivery({ provider, clock });
  const service = new AccountService({
    clock,
    otp: { devMode: false, deliver: delivery.deliver }
  });

  const response = service.requestOtp({ phone: PHONE });
  assert.equal('devCode' in response, false);
  assert.equal('devCodeOnlyInDev' in response, false);
  assert.match(response.challengeId, /^otp-/);

  const messages = provider.messages();
  assert.equal(messages.length, 1);
  assert.equal(messages[0].phone, PHONE);
  assert.match(messages[0].code, /^\d{6}$/);
  assert.equal(delivery.stats().sent, 1);
});

test('集成 AccountService：投递失败 => requestOtp 抛 OTP_DELIVERY_FAILED（不静默），失败后不占冷却可重试', () => {
  const clock = makeClock();
  const { provider } = makeFakeProvider((args, count) => {
    if (count === 1) throw new SmsError('通道故障', 'SMS_FAILED');
    return { providerMessageId: `msg-${count}`, sentAt: clock.iso() };
  });
  const delivery = new OtpDelivery({ provider, clock });
  const service = new AccountService({
    clock,
    otp: { devMode: false, deliver: delivery.deliver }
  });

  assert.throws(() => service.requestOtp({ phone: PHONE }), (error) => {
    assert.ok(error instanceof OtpDeliveryError);
    assert.equal(error.code, 'OTP_DELIVERY_FAILED');
    return true;
  });
  assert.equal(delivery.stats().failed, 1);

  // 失败不占冷却：立刻重试成功
  let secondResponse = null;
  assert.doesNotThrow(() => {
    secondResponse = service.requestOtp({ phone: PHONE });
  });
  assert.equal('devCode' in secondResponse, false);
  assert.equal(delivery.stats().sent, 1);
  assert.equal(delivery.stats().failed, 1);
});

test('集成 AccountService：vendor 超时最终上抛 OTP_DELIVERY_FAILED（causeCode=SMS_TIMEOUT）', async () => {
  const clock = makeClock();
  const provider = createSmsProvider({
    kind: 'vendor',
    timeoutMs: 20,
    clock,
    transport: () => new Promise(() => {})
  });
  const delivery = new OtpDelivery({ provider, clock });
  await assert.rejects(
    () => delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 }),
    (error) => {
      assert.equal(error.code, 'OTP_DELIVERY_FAILED');
      assert.equal(error.causeCode, 'SMS_TIMEOUT');
      return true;
    }
  );
  assert.equal(delivery.stats().failed, 1);
});

test('入参校验：provider 非法 / 上限非法 / 手机号非法', () => {
  assert.throws(() => new OtpDelivery({ provider: {} }), TypeError);
  assert.throws(() => new OtpDelivery({ provider: { sendOtp() {} }, cooldownMs: -1 }), TypeError);
  assert.throws(() => new OtpDelivery({ provider: { sendOtp() {} }, dailyCap: 0 }), TypeError);

  const clock = makeClock();
  const { provider } = makeFakeProvider(successHandler(clock));
  const delivery = new OtpDelivery({ provider, clock });
  assert.throws(() => delivery.deliver({ phone: '123', code: CODE, ttlSeconds: 300 }), TypeError);
  assert.throws(() => delivery.deliver({ phone: PHONE, code: '', ttlSeconds: 300 }), TypeError);
});

test('OtpDeliveryError 带 code 且是 Error 子类', () => {
  const clock = makeClock();
  const { provider } = makeFakeProvider(() => {
    throw new SmsError('x', 'SMS_FAILED');
  });
  const delivery = new OtpDelivery({ provider, clock });
  try {
    delivery.deliver({ phone: PHONE, code: CODE, ttlSeconds: 300 });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof OtpDeliveryError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'OTP_DELIVERY_FAILED');
  }
});
