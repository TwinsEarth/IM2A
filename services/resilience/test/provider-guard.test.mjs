// 测试：外部依赖熔断与降级（契约 §43）。
// 覆盖：失败阈值→OPEN、OPEN 时不再调用主方（快速失败）、半开只允许一次探测、探测失败重置冷却、fallback 降级与主备双失败、审计迁移事件。
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  CIRCUIT_STATE,
  CircuitBreaker,
  GUARD_CODE,
  GuardError,
  ProviderGuard
} from '../src/provider-guard.mjs';

const START = Date.parse('2026-10-21T00:00:00.000Z');

function makeClock(startMs = START) {
  let currentMs = startMs;
  const clock = () => currentMs;
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.currentMs = () => currentMs;
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

const failing = (code = 'PRIMARY_DOWN') => () => {
  const error = new Error(`provider failed (${code})`);
  error.code = code;
  throw error;
};

test('CIRCUIT_STATE / GUARD_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...CIRCUIT_STATE }, { CLOSED: 'CLOSED', OPEN: 'OPEN', HALF_OPEN: 'HALF_OPEN' });
  assert.deepEqual({ ...GUARD_CODE }, {
    CIRCUIT_OPEN: 'CIRCUIT_OPEN',
    PRIMARY_FAILED: 'PRIMARY_FAILED',
    FALLBACK_FAILED: 'FALLBACK_FAILED',
    NO_FALLBACK: 'NO_FALLBACK',
    INVALID_CONFIG: 'INVALID_CONFIG'
  });
  assert.ok(Object.isFrozen(CIRCUIT_STATE));
  assert.ok(Object.isFrozen(GUARD_CODE));
});

test('CircuitBreaker：初始 CLOSED，成功执行返回值并累计 successes', () => {
  const clock = makeClock();
  const breaker = new CircuitBreaker({ name: 'sms', clock });
  assert.equal(breaker.state, CIRCUIT_STATE.CLOSED);
  const value = breaker.execute(() => 'ok');
  assert.equal(value, 'ok');
  const stats = breaker.stats();
  assert.equal(stats.name, 'sms');
  assert.equal(stats.state, CIRCUIT_STATE.CLOSED);
  assert.equal(stats.successes, 1);
  assert.equal(stats.failures, 0);
  assert.equal(stats.openedAt, null);
  assert.equal(stats.lastError, null);
  assert.ok(Object.isFrozen(stats));
});

test('3 次失败后 OPEN；第 4 次 execute 抛 CIRCUIT_OPEN 且主函数调用计数不增加（注入 clock 不推进）', () => {
  const clock = makeClock();
  const breaker = new CircuitBreaker({ name: 'kyc', failureThreshold: 3, cooldownMs: 30_000, clock });
  let calls = 0;
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    assert.throws(() => breaker.execute(() => {
      calls += 1;
      return failing('KYC_DOWN')();
    }), /provider failed/);
    assert.equal(calls, attempt);
  }
  assert.equal(breaker.state, CIRCUIT_STATE.OPEN);
  assert.equal(breaker.stats().failures, 3);
  assert.equal(breaker.stats().lastError.code, 'KYC_DOWN');
  assert.ok(breaker.stats().openedAt !== null);

  const openedAtBefore = breaker.stats().openedAt;
  let fourthCalls = 0;
  assert.throws(
    () => breaker.execute(() => {
      fourthCalls += 1;
      return 'never';
    }),
    { code: 'CIRCUIT_OPEN' }
  );
  assert.equal(fourthCalls, 0); // OPEN 时绝不调用主方
  assert.equal(calls, 3);
  assert.equal(breaker.stats().openedAt, openedAtBefore); // 未推进 clock，未发生新迁移
});

test('未达阈值：连续失败 2 次后成功会清零 failures，不会打开', () => {
  const clock = makeClock();
  const breaker = new CircuitBreaker({ name: 'moderation', failureThreshold: 3, clock });
  for (let attempt = 0; attempt < 2; attempt += 1) {
    assert.throws(() => breaker.execute(failing()), /provider failed/);
  }
  assert.equal(breaker.stats().failures, 2);
  assert.equal(breaker.state, CIRCUIT_STATE.CLOSED);
  assert.equal(breaker.execute(() => 'recovered'), 'recovered');
  assert.equal(breaker.stats().failures, 0);
  assert.equal(breaker.stats().successes, 1);
  for (let attempt = 0; attempt < 3; attempt += 1) {
    assert.throws(() => breaker.execute(failing()), /provider failed/);
  }
  assert.equal(breaker.state, CIRCUIT_STATE.OPEN); // 连续失败才打开
});

test('冷却到期：OPEN → HALF_OPEN，探测成功 → CLOSED，后续正常', () => {
  const clock = makeClock();
  const breaker = new CircuitBreaker({ name: 'sms', failureThreshold: 2, cooldownMs: 30_000, clock });
  assert.throws(() => breaker.execute(failing()), /provider failed/);
  assert.throws(() => breaker.execute(failing()), /provider failed/);
  assert.equal(breaker.state, CIRCUIT_STATE.OPEN);

  clock.advance(29_999);
  assert.equal(breaker.state, CIRCUIT_STATE.OPEN);
  assert.throws(() => breaker.execute(() => 'not allowed'), { code: 'CIRCUIT_OPEN' });

  clock.advance(1); // 恰好冷却到期
  assert.equal(breaker.state, CIRCUIT_STATE.HALF_OPEN);
  let probeCalls = 0;
  assert.equal(breaker.execute(() => { probeCalls += 1; return 'probe-ok'; }), 'probe-ok');
  assert.equal(probeCalls, 1);
  assert.equal(breaker.state, CIRCUIT_STATE.CLOSED);
  assert.equal(breaker.stats().failures, 0);
  assert.equal(breaker.execute(() => 'normal'), 'normal');
});

test('半开只允许一次探测：异步探测在途时第二次调用立即拒绝且不增加主函数计数', async () => {
  const clock = makeClock();
  const breaker = new CircuitBreaker({ name: 'kyc', failureThreshold: 2, cooldownMs: 1000, clock });
  assert.throws(() => breaker.execute(failing()), /provider failed/);
  assert.throws(() => breaker.execute(failing()), /provider failed/);
  clock.advance(1000);

  let probeCalls = 0;
  let resolveProbe;
  const pending = new Promise((resolve) => {
    resolveProbe = resolve;
  });
  const probe = breaker.execute(() => {
    probeCalls += 1;
    return pending;
  });
  assert.equal(breaker.state, CIRCUIT_STATE.HALF_OPEN);
  assert.equal(probeCalls, 1);

  assert.throws(
    () => breaker.execute(() => {
      probeCalls += 1;
      return 'second';
    }),
    { code: 'CIRCUIT_OPEN' }
  );
  assert.equal(probeCalls, 1); // 并发探测被拒绝，主方不再被调用

  resolveProbe('late-ok');
  assert.equal(await probe, 'late-ok');
  assert.equal(breaker.state, CIRCUIT_STATE.CLOSED);
});

test('探测失败：重新 OPEN 且冷却重新计时，立刻再调用仍被拒绝', () => {
  const clock = makeClock();
  const breaker = new CircuitBreaker({ name: 'sms', failureThreshold: 2, cooldownMs: 1000, clock });
  assert.throws(() => breaker.execute(failing()), /provider failed/);
  assert.throws(() => breaker.execute(failing()), /provider failed/);
  const firstOpenedAt = breaker.stats().openedAt;

  clock.advance(1000);
  assert.equal(breaker.state, CIRCUIT_STATE.HALF_OPEN);
  assert.throws(() => breaker.execute(failing('STILL_DOWN')), /provider failed/);
  assert.equal(breaker.state, CIRCUIT_STATE.OPEN);
  const secondOpenedAt = breaker.stats().openedAt;
  assert.ok(secondOpenedAt > firstOpenedAt); // 冷却重新计时

  let callsAfterReopen = 0;
  assert.throws(
    () => breaker.execute(() => {
      callsAfterReopen += 1;
      return 'x';
    }),
    { code: 'CIRCUIT_OPEN' }
  );
  assert.equal(callsAfterReopen, 0);

  clock.advance(999);
  assert.equal(breaker.state, CIRCUIT_STATE.OPEN);
  clock.advance(1);
  assert.equal(breaker.state, CIRCUIT_STATE.HALF_OPEN);
});

test('drainTransitions：状态迁移事件按 CLOSED→OPEN→HALF_OPEN→CLOSED 顺序可消费', () => {
  const clock = makeClock();
  const breaker = new CircuitBreaker({ name: 'moderation', failureThreshold: 2, cooldownMs: 1000, clock });
  assert.throws(() => breaker.execute(failing()), /provider failed/);
  assert.throws(() => breaker.execute(failing()), /provider failed/);
  const openTransitions = breaker.drainTransitions();
  assert.deepEqual(
    openTransitions.map((item) => `${item.from}->${item.to}`),
    ['CLOSED->OPEN']
  );
  assert.equal(openTransitions[0].reason, 'failure_threshold');
  assert.ok(Object.isFrozen(openTransitions));

  clock.advance(1000);
  assert.equal(breaker.execute(() => 'ok'), 'ok');
  const closeTransitions = breaker.drainTransitions();
  assert.deepEqual(
    closeTransitions.map((item) => `${item.from}->${item.to}`),
    ['OPEN->HALF_OPEN', 'HALF_OPEN->CLOSED']
  );
  assert.deepEqual([...breaker.drainTransitions()], []);
});

test('ProviderGuard：主提供方成功 → provider=primary、usedFallback=false、causeCode=null；不走 fallback', () => {
  const clock = makeClock();
  let primaryCalls = 0;
  let fallbackCalls = 0;
  const guard = new ProviderGuard({
    name: 'sms',
    clock,
    primary: () => {
      primaryCalls += 1;
      return { messageId: 'm-1' };
    },
    fallback: () => {
      fallbackCalls += 1;
      return { messageId: 'fallback' };
    }
  });
  const result = guard.execute({ phone: '13800000000' });
  assert.deepEqual({ ...result }, {
    value: { messageId: 'm-1' },
    provider: 'primary',
    usedFallback: false,
    causeCode: null
  });
  assert.ok(Object.isFrozen(result));
  assert.equal(primaryCalls, 1);
  assert.equal(fallbackCalls, 0);
  const stats = guard.stats();
  assert.equal(stats.primaryCalls, 1);
  assert.equal(stats.fallbackCalls, 0);
  assert.equal(stats.lastProvider, 'primary');
  assert.ok(Object.isFrozen(stats));
  assert.ok(Object.isFrozen(stats.breaker));
});

test('降级：主失败 → 返回 fallback 值、usedFallback=true、causeCode 正确，并写降级审计', () => {
  const clock = makeClock();
  const audits = [];
  const guard = new ProviderGuard({
    name: 'sms',
    clock,
    primary: failing('SMS_FAILED'),
    fallback: () => ({ messageId: 'fallback-message' }),
    onAudit: (event) => audits.push(event)
  });
  const result = guard.execute({ phone: '13800000000' });
  assert.equal(result.value.messageId, 'fallback-message');
  assert.equal(result.provider, 'fallback');
  assert.equal(result.usedFallback, true);
  assert.equal(result.causeCode, 'SMS_FAILED');
  assert.equal(guard.stats().fallbackCalls, 1);
  assert.equal(guard.stats().fallbackUsed, 1);
  assert.equal(guard.stats().lastCauseCode, 'SMS_FAILED');

  const fallbackEvent = audits.find((event) => event.action === 'PROVIDER_FALLBACK');
  assert.ok(fallbackEvent);
  assert.equal(fallbackEvent.decision, 'DEGRADED');
  assert.equal(fallbackEvent.detail.causeCode, 'SMS_FAILED');
  assert.equal(fallbackEvent.detail.provider, 'fallback');
  assert.ok(Object.isFrozen(fallbackEvent));
});

test('无 fallback：主失败 → 抛 PRIMARY_FAILED（带底层原因码）', () => {
  const clock = makeClock();
  const audits = [];
  const guard = new ProviderGuard({
    name: 'kyc',
    clock,
    primary: failing('KYC_DOWN'),
    onAudit: (event) => audits.push(event)
  });
  let caught = null;
  try {
    guard.execute({ realName: '林某', idNumber: '110101199003071234' });
  } catch (error) {
    caught = error;
  }
  assert.ok(caught instanceof GuardError);
  assert.equal(caught.code, 'PRIMARY_FAILED');
  assert.equal(caught.causeCode, 'KYC_DOWN');
  assert.match(caught.message, /KYC_DOWN/);
  const failedEvent = audits.find((event) => event.action === 'PROVIDER_FAILED');
  assert.ok(failedEvent);
  assert.equal(failedEvent.decision, 'FAILED');
  assert.equal(failedEvent.detail.noFallback, true);
});

test('熔断打开且无 fallback：快速失败为 NO_FALLBACK（causeCode=CIRCUIT_OPEN），主函数不再被调用', () => {
  const clock = makeClock();
  let primaryCalls = 0;
  const guard = new ProviderGuard({
    name: 'sms',
    clock,
    primary: () => {
      primaryCalls += 1;
      throw failing('SMS_FAILED')();
    },
    breaker: new CircuitBreaker({ name: 'sms-breaker', failureThreshold: 2, cooldownMs: 1000, clock })
  });
  assert.throws(() => guard.execute({}), { code: 'PRIMARY_FAILED' });
  assert.throws(() => guard.execute({}), { code: 'PRIMARY_FAILED' });
  assert.equal(primaryCalls, 2);
  assert.equal(guard.stats().breaker.state, CIRCUIT_STATE.OPEN);

  let caught = null;
  try {
    guard.execute({});
  } catch (error) {
    caught = error;
  }
  assert.equal(caught.code, 'NO_FALLBACK');
  assert.equal(caught.causeCode, 'CIRCUIT_OPEN');
  assert.equal(primaryCalls, 2); // OPEN 时绝不再打主方
});

test('主备都失败 → FALLBACK_FAILED，消息与 causeCodes 同时包含两个原因码（不返回 undefined）', () => {
  const clock = makeClock();
  const audits = [];
  const guard = new ProviderGuard({
    name: 'moderation',
    clock,
    primary: failing('PRIMARY_E'),
    fallback: failing('FALLBACK_E'),
    onAudit: (event) => audits.push(event)
  });
  let caught = null;
  try {
    guard.execute({ content: 'x' });
  } catch (error) {
    caught = error;
  }
  assert.ok(caught instanceof GuardError);
  assert.equal(caught.code, 'FALLBACK_FAILED');
  assert.deepEqual([...caught.causeCodes], ['PRIMARY_E', 'FALLBACK_E']);
  assert.match(caught.message, /PRIMARY_E/);
  assert.match(caught.message, /FALLBACK_E/);
  assert.equal(guard.stats().fallbackCalls, 1);
  assert.equal(guard.stats().fallbackUsed, 0);

  const failedEvent = audits.find((event) => event.action === 'PROVIDER_FALLBACK_FAILED');
  assert.ok(failedEvent);
  assert.equal(failedEvent.detail.primaryCauseCode, 'PRIMARY_E');
  assert.equal(failedEvent.detail.fallbackCauseCode, 'FALLBACK_E');
});

test('经 ProviderGuard：失败阈值后 OPEN，第 4 次不再打主方但可走 fallback（显式降级）', () => {
  const clock = makeClock();
  let primaryCalls = 0;
  let fallbackCalls = 0;
  const guard = new ProviderGuard({
    name: 'sms',
    clock,
    primary: () => {
      primaryCalls += 1;
      throw failing('SMS_FAILED')();
    },
    fallback: () => {
      fallbackCalls += 1;
      return { queued: true };
    },
    breaker: new CircuitBreaker({ name: 'sms-breaker', failureThreshold: 3, cooldownMs: 30_000, clock })
  });

  for (let attempt = 0; attempt < 3; attempt += 1) {
    const result = guard.execute({});
    assert.equal(result.usedFallback, true);
    assert.equal(result.causeCode, 'SMS_FAILED');
  }
  assert.equal(guard.stats().breaker.state, CIRCUIT_STATE.OPEN);
  assert.equal(primaryCalls, 3);

  const degraded = guard.execute({});
  assert.equal(degraded.provider, 'fallback');
  assert.equal(degraded.usedFallback, true);
  assert.equal(degraded.causeCode, 'CIRCUIT_OPEN'); // 快速失败原因透明
  assert.equal(primaryCalls, 3); // OPEN 后主方调用计数不增加
  assert.equal(fallbackCalls, 4);
});

test('审计：熔断迁移 OPEN/HALF_OPEN/CLOSED 与降级事件都会通过 onAudit 上报', () => {
  const clock = makeClock();
  const audits = [];
  let shouldFail = true;
  const guard = new ProviderGuard({
    name: 'kyc',
    clock,
    primary: () => {
      if (shouldFail) throw failing('KYC_DOWN')();
      return { verified: true };
    },
    fallback: () => ({ verified: false, pending: true }),
    breaker: new CircuitBreaker({ name: 'kyc-breaker', failureThreshold: 2, cooldownMs: 1000, clock }),
    onAudit: (event) => audits.push(event)
  });

  guard.execute({});
  guard.execute({});
  assert.equal(guard.stats().breaker.state, CIRCUIT_STATE.OPEN);
  assert.ok(audits.some((event) => event.action === 'CIRCUIT_OPEN'));

  shouldFail = false;
  clock.advance(1000); // 冷却到期 → 半开 → 探测成功 → CLOSED
  const recovered = guard.execute({});
  assert.equal(recovered.provider, 'primary');
  const actions = audits.map((event) => event.action);
  assert.ok(actions.includes('CIRCUIT_OPEN'));
  assert.ok(actions.includes('CIRCUIT_HALF_OPEN'));
  assert.ok(actions.includes('CIRCUIT_CLOSED'));
  assert.ok(actions.includes('PROVIDER_FALLBACK'));

  const transitionEvent = audits.find((event) => event.action === 'CIRCUIT_OPEN');
  assert.equal(transitionEvent.actor, 'provider-guard');
  assert.equal(transitionEvent.target, 'kyc');
  assert.equal(transitionEvent.decision, 'OPEN');
  assert.equal(typeof transitionEvent.at, 'string');
});

test('异步提供方：异步 primary 失败 → 异步 fallback 成功/双失败都正确处理', async () => {
  const clock = makeClock();
  const asyncGuard = new ProviderGuard({
    name: 'sms-async',
    clock,
    primary: async () => {
      throw failing('ASYNC_PRIMARY')();
    },
    fallback: async () => ({ queued: 'async-fallback' })
  });
  const value = await asyncGuard.execute({});
  assert.equal(value.provider, 'fallback');
  assert.equal(value.value.queued, 'async-fallback');
  assert.equal(value.causeCode, 'ASYNC_PRIMARY');

  const bothFailGuard = new ProviderGuard({
    name: 'sms-async-both',
    clock,
    primary: async () => {
      throw failing('ASYNC_PRIMARY')();
    },
    fallback: async () => {
      throw failing('ASYNC_FALLBACK')();
    }
  });
  await assert.rejects(() => bothFailGuard.execute({}), (error) => {
    assert.equal(error.code, 'FALLBACK_FAILED');
    assert.deepEqual([...error.causeCodes], ['ASYNC_PRIMARY', 'ASYNC_FALLBACK']);
    return true;
  });

  const primaryOnly = new ProviderGuard({
    name: 'sms-async-ok',
    clock,
    primary: async () => 'async-ok'
  });
  assert.equal((await primaryOnly.execute({})).value, 'async-ok');
});

test('入参校验：CircuitBreaker/ProviderGuard 非法配置 => GuardError(INVALID_CONFIG)；execute(fn) 非函数 TypeError', () => {
  assert.throws(() => new CircuitBreaker({ name: '' }), { code: 'INVALID_CONFIG' });
  assert.throws(() => new CircuitBreaker({ failureThreshold: 0 }), { code: 'INVALID_CONFIG' });
  assert.throws(() => new CircuitBreaker({ failureThreshold: 1.5 }), { code: 'INVALID_CONFIG' });
  assert.throws(() => new CircuitBreaker({ cooldownMs: -1 }), { code: 'INVALID_CONFIG' });
  assert.throws(() => new CircuitBreaker({ clock: 'now' }), { code: 'INVALID_CONFIG' });
  assert.throws(() => new CircuitBreaker().execute('not-a-function'), TypeError);

  assert.throws(() => new ProviderGuard({ name: 'x' }), { code: 'INVALID_CONFIG' });
  assert.throws(() => new ProviderGuard({ name: '', primary: () => 1 }), { code: 'INVALID_CONFIG' });
  assert.throws(() => new ProviderGuard({ name: 'x', primary: 'p' }), { code: 'INVALID_CONFIG' });
  assert.throws(() => new ProviderGuard({ name: 'x', primary: () => 1, fallback: 'f' }), {
    code: 'INVALID_CONFIG'
  });
  assert.throws(
    () => new ProviderGuard({ name: 'x', primary: () => 1, breaker: { execute: () => 1 } }),
    { code: 'INVALID_CONFIG' }
  );
});

test('GuardError 带契约 code 且是 Error 子类', () => {
  try {
    new CircuitBreaker({ name: 'x' }).execute(failing());
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'PRIMARY_DOWN'); // 直接抛原始错误（不吞）
  }

  const breaker = new CircuitBreaker({ name: 'x', failureThreshold: 1 });
  assert.throws(() => breaker.execute(failing()), /provider failed/);
  try {
    breaker.execute(() => 'never');
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof GuardError);
    assert.equal(error.code, 'CIRCUIT_OPEN');
  }
});
