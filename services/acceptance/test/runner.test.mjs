// 测试：验收执行器（契约 §46）。
// 覆盖：完整 Gate 套件、only 过滤稳定、单 Gate 异常/超时→FAIL 不中断、SKIP/MANUAL 不影响 ok、memory 下 G-STORE FAIL、durable 下 PASS、副作用标注。
import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';

import { AcceptanceRunner, DEFAULT_GATES, GATE_STATUS } from '../src/runner.mjs';
import {
  ADMIN_TOKEN,
  BACKUP_DIR,
  cleanupNewFiles,
  listDirQuiet,
  makeTempDir,
  removeDirQuiet,
  withServer
} from './_helpers.mjs';

test('GATE_STATUS 常量冻结且与契约一致；DEFAULT_GATES 覆盖八个 Gate', () => {
  assert.deepEqual({ ...GATE_STATUS }, { PASS: 'PASS', FAIL: 'FAIL', SKIP: 'SKIP', MANUAL: 'MANUAL' });
  assert.ok(Object.isFrozen(GATE_STATUS));
  assert.deepEqual(
    DEFAULT_GATES.map((gate) => gate.id),
    ['G-HEALTH', 'G-AUTH', 'G-ROLE', 'G-AUDIT', 'G-STORE', 'G-METRICS', 'G-BACKUP', 'G-PROVIDERS']
  );
  assert.ok(DEFAULT_GATES.every((gate) => Object.isFrozen(gate) && typeof gate.run === 'function'));
});

test('完整 Gate 套件（memory 实例）：G-STORE FAIL、G-ROLE/G-PROVIDERS SKIP，ok=false，备份 Gate 标注副作用', async () => {
  const backupsBefore = await listDirQuiet(BACKUP_DIR);
  try {
    await withServer({}, async ({ target }) => {
      const runner = new AcceptanceRunner({ target, token: ADMIN_TOKEN });
      const report = await runner.run();
      assert.ok(Object.isFrozen(report));
      assert.equal(report.target, target);
      assert.equal(report.ok, false); // 有 FAIL
      assert.match(report.startedAt, /^\d{4}-\d{2}-\d{2}T/);
      assert.match(report.finishedAt, /^\d{4}-\d{2}-\d{2}T/);
      assert.deepEqual(
        report.results.map((result) => result.id),
        DEFAULT_GATES.map((gate) => gate.id)
      );

      const statusOf = new Map(report.results.map((result) => [result.id, result.status]));
      assert.equal(statusOf.get('G-HEALTH'), GATE_STATUS.PASS);
      assert.equal(statusOf.get('G-AUTH'), GATE_STATUS.PASS);
      assert.equal(statusOf.get('G-ROLE'), GATE_STATUS.SKIP); // 未配置 viewerToken
      assert.equal(statusOf.get('G-AUDIT'), GATE_STATUS.PASS);
      assert.equal(statusOf.get('G-STORE'), GATE_STATUS.FAIL); // memory 不允许
      assert.equal(statusOf.get('G-METRICS'), GATE_STATUS.PASS);
      assert.equal(statusOf.get('G-BACKUP'), GATE_STATUS.PASS);
      assert.equal(statusOf.get('G-PROVIDERS'), GATE_STATUS.SKIP); // dev 短信通道

      assert.deepEqual({ ...report.summary }, {
        pass: report.summary.pass,
        fail: 1,
        skip: 2,
        manual: 0
      });
      assert.equal(report.summary.fail, 1);
      assert.equal(report.summary.skip, 2);
      assert.equal(report.summary.manual, 0);

      const backupResult = report.results.find((result) => result.id === 'G-BACKUP');
      assert.match(backupResult.detail, /副作用/);
      const storeResult = report.results.find((result) => result.id === 'G-STORE');
      assert.match(storeResult.detail, /memory/);
      for (const result of report.results) {
        assert.ok(Number.isInteger(result.ms) && result.ms >= 0);
        assert.ok(Object.isFrozen(result));
      }
      assert.ok(Object.isFrozen(report.results));
      assert.ok(Object.isFrozen(report.summary));
    });
  } finally {
    // G-BACKUP 是唯一允许写目标的 Gate；只清理本轮新增的备份文件
    await cleanupNewFiles(BACKUP_DIR, backupsBefore);
  }
});

test('only 过滤：结果顺序稳定、只跑指定 Gate；SKIP 不影响 ok', async () => {
  await withServer({}, async ({ target }) => {
    const runner = new AcceptanceRunner({ target, token: ADMIN_TOKEN });
    const first = await runner.run({ only: ['G-AUTH', 'G-HEALTH'] });
    assert.deepEqual(first.results.map((result) => result.id), ['G-HEALTH', 'G-AUTH']); // 保持默认顺序
    assert.equal(first.ok, true);
    assert.equal(first.summary.pass, 2);

    const second = await runner.run({ only: 'G-ROLE' });
    assert.deepEqual(second.results.map((result) => result.id), ['G-ROLE']);
    assert.equal(second.results[0].status, GATE_STATUS.SKIP);
    assert.equal(second.ok, true); // SKIP 不影响 ok
    assert.equal(second.summary.skip, 1);
  });
});

test('单个 Gate 抛异常 → FAIL 且不中断其它 Gate；超时同样 FAIL', async () => {
  await withServer({}, async ({ target }) => {
    const runner = new AcceptanceRunner({ target, token: ADMIN_TOKEN, gateTimeoutMs: 40 });
    let boomRuns = 0;
    runner.register({
      id: 'G-BOOM',
      name: '故意抛错',
      tier: 'CORE',
      run: () => {
        boomRuns += 1;
        throw new Error('boom-gate');
      }
    });
    runner.register({
      id: 'G-SLOW',
      name: '故意超时',
      tier: 'CORE',
      run: () => new Promise(() => {}) // 永不 resolve
    });

    const report = await runner.run({ only: ['G-HEALTH', 'G-BOOM', 'G-SLOW'] });
    assert.deepEqual(report.results.map((result) => result.id), ['G-HEALTH', 'G-BOOM', 'G-SLOW']);
    assert.equal(report.results[0].status, GATE_STATUS.PASS);
    assert.equal(report.results[1].status, GATE_STATUS.FAIL);
    assert.match(report.results[1].detail, /boom-gate/);
    assert.equal(boomRuns, 1);
    assert.equal(report.results[2].status, GATE_STATUS.FAIL);
    assert.match(report.results[2].detail, /超时|GATE_TIMEOUT/);
    assert.equal(report.summary.fail, 2);
    assert.equal(report.ok, false);
  });
});

test('MANUAL Gate：默认不执行（状态 MANUAL）；includeManual=true 才执行', async () => {
  await withServer({}, async ({ target }) => {
    let manualRuns = 0;
    const runner = new AcceptanceRunner({ target, token: ADMIN_TOKEN });
    runner.register({
      id: 'G-MANUAL',
      name: '人工确认项',
      tier: 'MANUAL',
      run: () => {
        manualRuns += 1;
        return { ok: true, detail: '人工确认完成' };
      }
    });

    const skipped = await runner.run({ only: ['G-MANUAL'] });
    assert.equal(skipped.results[0].status, GATE_STATUS.MANUAL);
    assert.equal(manualRuns, 0);
    assert.equal(skipped.summary.manual, 1);
    assert.equal(skipped.ok, true); // MANUAL 不影响 ok

    const included = await runner.run({ only: ['G-MANUAL'], includeManual: true });
    assert.equal(included.results[0].status, GATE_STATUS.PASS);
    assert.equal(manualRuns, 1);
    assert.equal(included.summary.manual, 0);
  });
});

test('durable 实例（STORE=jsonfile 临时目录）：G-STORE PASS', async () => {
  const tempDir = await makeTempDir('acceptance-durable-');
  try {
    await withServer(
      { STORE: 'jsonfile', STORE_FILE: path.join(tempDir, 'im2agent.json') },
      async ({ target }) => {
        const runner = new AcceptanceRunner({ target, token: ADMIN_TOKEN });
        const report = await runner.run({ only: ['G-STORE'] });
        assert.equal(report.results.length, 1);
        assert.equal(report.results[0].status, GATE_STATUS.PASS);
        assert.match(report.results[0].detail, /jsonfile/);
        assert.equal(report.ok, true);
      }
    );
  } finally {
    await removeDirQuiet(tempDir);
  }
});

test('入参校验：缺 target / 非法 Gate 注册', () => {
  assert.throws(() => new AcceptanceRunner({}), TypeError);
  assert.throws(() => new AcceptanceRunner({ target: 'http://127.0.0.1:1', gateTimeoutMs: 0 }), TypeError);
  const runner = new AcceptanceRunner({ target: 'http://127.0.0.1:1', token: 't' });
  assert.throws(() => runner.register({ id: 'G-X', name: 'x' }), TypeError);
  assert.throws(() => runner.register({ id: '', name: 'x', run: () => ({ ok: true }) }), TypeError);
});
