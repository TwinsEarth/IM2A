// 测试：短信通道（契约 §38）。
// 覆盖：dev 内存记录/不外发、log 不外泄验证码、vendor 成功/失败/拒绝/超时、错误消息不含验证码、渠道不可用。
import test from 'node:test';
import assert from 'node:assert/strict';

import { SMS_CODE, SmsError, createSmsProvider } from '../src/sms-provider.mjs';

const PHONE = '13800000001';
const CODE = '246810';
const TTL = 300;

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

const assertNoCode = (text, secret = CODE) => {
  assert.equal(String(text).includes(secret), false, `消息泄露了验证码: ${text}`);
};

test('SMS_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...SMS_CODE }, {
    SMS_FAILED: 'SMS_FAILED',
    SMS_TIMEOUT: 'SMS_TIMEOUT',
    SMS_REJECTED: 'SMS_REJECTED',
    INVALID_PHONE: 'INVALID_PHONE',
    PROVIDER_UNAVAILABLE: 'PROVIDER_UNAVAILABLE'
  });
  assert.ok(Object.isFrozen(SMS_CODE));
});

test('dev provider：sendOtp 后可在内存读到 {phone, code}，不产生网络调用', () => {
  const clock = makeClock();
  let transportCalled = false;
  // dev 模式下即使误传 transport 也绝不调用
  const provider = createSmsProvider({
    kind: 'dev',
    transport: () => {
      transportCalled = true;
      throw new Error('should not be called');
    },
    clock
  });
  assert.ok(Object.isFrozen(provider));
  assert.deepEqual({ ...provider.info() }, { name: 'sms-dev', kind: 'dev', available: true });

  const result = provider.sendOtp({ phone: PHONE, code: CODE, ttlSeconds: TTL });
  assert.ok(Object.isFrozen(result));
  assert.match(result.providerMessageId, /^dev-/);
  assert.equal(result.sentAt, '2026-10-21T00:00:00.000Z');
  assert.equal(transportCalled, false);

  const messages = provider.messages();
  assert.equal(messages.length, 1);
  assert.deepEqual({ ...messages[0] }, {
    phone: PHONE,
    code: CODE,
    ttlSeconds: TTL,
    at: '2026-10-21T00:00:00.000Z'
  });
  assert.deepEqual(provider.records(), messages);
  assert.equal(provider.lastMessage().code, CODE);
});

test('log provider：等价外发（console.log），日志中不包含验证码明文', () => {
  const clock = makeClock();
  const provider = createSmsProvider({ kind: 'log', clock });
  const logged = [];
  const originalLog = console.log;
  console.log = (line) => logged.push(String(line));
  try {
    const result = provider.sendOtp({ phone: PHONE, code: CODE, ttlSeconds: TTL });
    assert.match(result.providerMessageId, /^log-/);
  } finally {
    console.log = originalLog;
  }
  assert.equal(logged.length, 1);
  assertNoCode(logged[0]);
  assert.match(logged[0], /phone=138\*\*\*\*0001/); // 手机号掩码
  assert.equal(provider.messages().length, 0); // log 不提供内存记录
});

test('vendor provider：transport 成功路径接收 {phone, code, ttlSeconds} 并返回回执', async () => {
  const clock = makeClock();
  const calls = [];
  const provider = createSmsProvider({
    kind: 'vendor',
    timeoutMs: 1000,
    clock,
    transport: ({ phone, code, ttlSeconds }) => {
      calls.push({ phone, code, ttlSeconds });
      return { providerMessageId: 'vendor-msg-1' };
    }
  });
  assert.deepEqual({ ...provider.info() }, { name: 'sms-vendor', kind: 'vendor', available: true });

  const result = await provider.sendOtp({ phone: PHONE, code: CODE, ttlSeconds: TTL });
  assert.equal(result.providerMessageId, 'vendor-msg-1');
  assert.equal(result.sentAt, '2026-10-21T00:00:00.000Z');
  assert.deepEqual(calls, [{ phone: PHONE, code: CODE, ttlSeconds: TTL }]);
});

test('vendor：transport 抛错 => SMS_FAILED，且错误消息不含验证码（即使底层错误里带 code）', () => {
  const provider = createSmsProvider({
    kind: 'vendor',
    transport: ({ code }) => {
      throw new Error(`vendor exploded for ${code}`);
    }
  });
  try {
    provider.sendOtp({ phone: PHONE, code: CODE, ttlSeconds: TTL });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof SmsError);
    assert.equal(error.code, 'SMS_FAILED');
    assertNoCode(error.message);
  }
});

test('vendor：transport 明确拒绝（不返回回执号）=> SMS_REJECTED，消息不含验证码', () => {
  const provider = createSmsProvider({
    kind: 'vendor',
    transport: ({ code }) => ({ ok: false, reason: `rejected ${code}` })
  });
  try {
    provider.sendOtp({ phone: PHONE, code: CODE, ttlSeconds: TTL });
    assert.fail('应当抛错');
  } catch (error) {
    assert.equal(error.code, 'SMS_REJECTED');
    assertNoCode(error.message);
  }
});

test('vendor：transport 超时（不 resolve，配合 timeoutMs）=> SMS_TIMEOUT', async () => {
  const provider = createSmsProvider({
    kind: 'vendor',
    timeoutMs: 20,
    transport: () => new Promise(() => {}) // 永不 resolve
  });
  await assert.rejects(
    () => provider.sendOtp({ phone: PHONE, code: CODE, ttlSeconds: TTL }),
    (error) => {
      assert.ok(error instanceof SmsError);
      assert.equal(error.code, 'SMS_TIMEOUT');
      assertNoCode(error.message);
      return true;
    }
  );
});

test('vendor：异步 transport 成功/失败都映射正确', async () => {
  const ok = createSmsProvider({
    kind: 'vendor',
    timeoutMs: 1000,
    transport: async ({ phone, code }) => ({ providerMessageId: `async-${phone.slice(-4)}-${code.length}` })
  });
  const result = await ok.sendOtp({ phone: PHONE, code: CODE, ttlSeconds: TTL });
  assert.equal(result.providerMessageId, 'async-0001-6');

  const failed = createSmsProvider({
    kind: 'vendor',
    timeoutMs: 1000,
    transport: async () => {
      throw new Error('network down');
    }
  });
  await assert.rejects(
    () => failed.sendOtp({ phone: PHONE, code: CODE, ttlSeconds: TTL }),
    (error) => {
      assert.equal(error.code, 'SMS_FAILED');
      assertNoCode(error.message);
      return true;
    }
  );
});

test('INVALID_PHONE / PROVIDER_UNAVAILABLE / 入参校验', () => {
  const provider = createSmsProvider({ kind: 'dev' });
  try {
    provider.sendOtp({ phone: '123', code: CODE, ttlSeconds: TTL });
    assert.fail('应当抛错');
  } catch (error) {
    assert.equal(error.code, 'INVALID_PHONE');
    assertNoCode(error.message);
  }
  assert.throws(() => provider.sendOtp({ phone: PHONE, code: '', ttlSeconds: TTL }), (error) => {
    assert.equal(error.code, 'SMS_FAILED');
    assertNoCode(error.message);
    return true;
  });

  assert.throws(() => createSmsProvider({ kind: 'vendor' }), { code: 'PROVIDER_UNAVAILABLE' });
  assert.throws(() => createSmsProvider({ kind: 'wechat' }), { code: 'PROVIDER_UNAVAILABLE' });
  assert.throws(() => createSmsProvider({ timeoutMs: 0 }), TypeError);
  assert.throws(() => createSmsProvider({ clock: 'now' }), TypeError);
});

test('SmsError 带契约 code 且是 Error 子类', () => {
  const provider = createSmsProvider({ kind: 'vendor', transport: () => null });
  try {
    provider.sendOtp({ phone: PHONE, code: CODE, ttlSeconds: TTL });
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof SmsError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'SMS_REJECTED');
  }
});
