// 测试：上线前预检（契约 §45）。
// 覆盖：dev 内存实例可达+鉴权通过、错 token AUTH_FAILED、未持久化/真实依赖判定、只读、不可达、时钟漂移 WARN。
import test from 'node:test';
import assert from 'node:assert/strict';

import { PREFLIGHT_CODE, PreflightError, runPreflight } from '../src/preflight.mjs';
import { ADMIN_TOKEN, withServer } from './_helpers.mjs';

const byId = (result) => new Map(result.checks.map((check) => [check.id, check]));

test('PREFLIGHT_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...PREFLIGHT_CODE }, {
    TARGET_UNREACHABLE: 'TARGET_UNREACHABLE',
    AUTH_FAILED: 'AUTH_FAILED',
    NODE_TOO_OLD: 'NODE_TOO_OLD',
    STORE_NOT_DURABLE: 'STORE_NOT_DURABLE',
    PROVIDER_NOT_CONFIGURED: 'PROVIDER_NOT_CONFIGURED',
    CLOCK_SKEW: 'CLOCK_SKEW',
    TLS_INSECURE: 'TLS_INSECURE'
  });
  assert.ok(Object.isFrozen(PREFLIGHT_CODE));
  assert.ok(PreflightError.prototype instanceof Error);
});

test('dev 内存实例 + expectDurable=false：可达/鉴权/Node 全通过，ok=true 且预检只读', async () => {
  await withServer({}, async ({ started, target }) => {
    const before = {
      audit: started.holder.world.audit.records({}).length,
      ledger: started.holder.world.ledger.entries().length,
      tickets: started.holder.world.state.tickets.length
    };
    const result = await runPreflight({
      target,
      token: ADMIN_TOKEN,
      expectDurable: false,
      requireRealProviders: false
    });
    assert.ok(Object.isFrozen(result));
    assert.equal(result.ok, true);
    assert.equal(result.target, target);
    assert.equal(result.node.major, Number(process.versions.node.split('.')[0]));
    assert.match(result.generatedAt, /^\d{4}-\d{2}-\d{2}T/);

    const checks = byId(result);
    for (const id of Object.values(PREFLIGHT_CODE)) assert.ok(checks.has(id), `缺少 check ${id}`);
    assert.equal(checks.get('TARGET_UNREACHABLE').ok, true);
    assert.equal(checks.get('AUTH_FAILED').ok, true);
    assert.equal(checks.get('NODE_TOO_OLD').ok, true);
    assert.equal(checks.get('STORE_NOT_DURABLE').ok, true); // 显式跳过内存检查
    assert.equal(checks.get('PROVIDER_NOT_CONFIGURED').ok, true); // 显式跳过真实依赖检查
    assert.equal(checks.get('CLOCK_SKEW').severity, 'WARN');
    assert.equal(checks.get('TLS_INSECURE').ok, true); // 回环地址允许 http
    assert.ok(result.checks.every((check) => typeof check.detail === 'string'));

    // 只读：预检前后目标状态一致
    assert.equal(started.holder.world.audit.records({}).length, before.audit);
    assert.equal(started.holder.world.ledger.entries().length, before.ledger);
    assert.equal(started.holder.world.state.tickets.length, before.tickets);
  });
});

test('错 token → AUTH_FAILED（ok=false）；无 token 同样无法通过', async () => {
  await withServer({}, async ({ target }) => {
    const wrong = await runPreflight({ target, token: 'wrong-token', expectDurable: false });
    assert.equal(wrong.ok, false);
    const wrongAuth = byId(wrong).get('AUTH_FAILED');
    assert.equal(wrongAuth.ok, false);
    assert.match(wrongAuth.detail, /401/);

    const noToken = await runPreflight({ target, token: null, expectDurable: false });
    assert.equal(noToken.ok, false);
    assert.equal(byId(noToken).get('AUTH_FAILED').ok, false);
    assert.match(byId(noToken).get('AUTH_FAILED').detail, /token/i);
  });
});

test('expectDurable=true 且目标为 memory → STORE_NOT_DURABLE（ok=false）', async () => {
  await withServer({}, async ({ target }) => {
    const result = await runPreflight({ target, token: ADMIN_TOKEN, expectDurable: true });
    assert.equal(result.ok, false);
    const storeCheck = byId(result).get('STORE_NOT_DURABLE');
    assert.equal(storeCheck.ok, false);
    assert.equal(storeCheck.severity, 'BLOCKER');
    assert.match(storeCheck.detail, /memory/);
  });
});

test('requireRealProviders=true 且短信主通道为 dev → PROVIDER_NOT_CONFIGURED（ok=false）', async () => {
  await withServer({}, async ({ target }) => {
    const result = await runPreflight({
      target,
      token: ADMIN_TOKEN,
      expectDurable: false,
      requireRealProviders: true
    });
    assert.equal(result.ok, false);
    const providerCheck = byId(result).get('PROVIDER_NOT_CONFIGURED');
    assert.equal(providerCheck.ok, false);
    assert.match(providerCheck.detail, /dev/);
  });
});

test('目标不可达 → TARGET_UNREACHABLE（ok=false，不抛异常）', async () => {
  const result = await runPreflight({
    target: 'http://127.0.0.1:1',
    token: ADMIN_TOKEN,
    expectDurable: false
  });
  assert.equal(result.ok, false);
  const targetCheck = byId(result).get('TARGET_UNREACHABLE');
  assert.equal(targetCheck.ok, false);
  assert.equal(targetCheck.severity, 'BLOCKER');
});

test('时钟漂移 >5min → CLOCK_SKEW 仅 WARN，不影响 ok', async () => {
  await withServer({}, async ({ target }) => {
    const skewedClock = () => new Date(Date.now() + 10 * 60e3);
    const result = await runPreflight({
      target,
      token: ADMIN_TOKEN,
      expectDurable: false,
      clock: skewedClock
    });
    const skewCheck = byId(result).get('CLOCK_SKEW');
    assert.equal(skewCheck.ok, false);
    assert.equal(skewCheck.severity, 'WARN');
    assert.equal(result.ok, true); // WARN 不会把整体 ok 拉成 false
  });
});

test('入参校验：非法 target / 非法 fetchImpl / 非法 minNodeMajor', async () => {
  await assert.rejects(() => runPreflight({ target: '' }), TypeError);
  await assert.rejects(() => runPreflight({ target: 'not-a-url' }), TypeError);
  await assert.rejects(
    () => runPreflight({ target: 'http://127.0.0.1:1', fetchImpl: 'x' }),
    TypeError
  );
  await assert.rejects(
    () => runPreflight({ target: 'http://127.0.0.1:1', minNodeMajor: 0 }),
    TypeError
  );
});
