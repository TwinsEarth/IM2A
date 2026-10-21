/**
 * 模块职责：v0.1.1 数据迁移测试 —— 备份/原子改写/加载核对/幂等/拒绝未来 schema
 * 契约：contracts/INTERFACES.md §51「迁移工具（v0.1.1 补）」
 *
 * 运行：node tests/migrate.test.mjs
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, rmSync, existsSync, readdirSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

import { migrateStateFile, MigrationError } from '../scripts/migrate-state.mjs';

function withFile(content, fn) {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'im2a-migrate-'));
  const file = path.join(dir, 'state.json');
  writeFileSync(file, typeof content === 'string' ? content : JSON.stringify(content, null, 2), 'utf8');
  try {
    return fn({ dir, file });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const legacyState = {
  agents: [{ agentId: 'agent-a' }],
  trust: { 'user-a>user-b': 3 },
  tickets: [{ id: 'tkt-1' }, { id: 'tkt-2' }],
  messages: [{ id: 'msg-1' }],
  ledger: [{ at: '2026-10-21T00:00:00.000Z', type: 'DEPOSIT', userId: 'user-a', amountCents: 5000, idempotencyKey: 'deposit:x', ref: 'x' }],
  audit: [{ seq: 1, hash: 'aa' }, { seq: 2, hash: 'bb' }]
};

test('迁移：缺 schemaVersion 的旧数据 → 加版本、数据条数不变、有备份且可校验', () => {
  withFile(legacyState, ({ dir, file }) => {
    const result = migrateStateFile({ file });
    assert.equal(result.status, 'MIGRATED');
    assert.equal(result.schemaVersion, 1);
    assert.deepEqual(result.counts, { agents: 1, tickets: 2, messages: 1, ledger: 1, audit: 2 });

    const migrated = JSON.parse(readFileSync(file, 'utf8'));
    assert.equal(migrated.schemaVersion, 1, 'schemaVersion 写在文件里');
    assert.deepEqual(migrated.tickets, legacyState.tickets, '业务字段原样保留');
    assert.deepEqual(migrated.trust, legacyState.trust);

    assert.ok(existsSync(result.backup), '必须生成备份');
    const backup = JSON.parse(readFileSync(result.backup, 'utf8'));
    assert.equal(backup.schemaVersion, undefined, '备份保留迁移前原样');
    assert.equal(result.backupSha256.length, 64);
    assert.ok(readdirSync(dir).some((name) => name.includes('.pre-1.bak')));
  });
});

test('迁移幂等：已迁移文件再跑 → CURRENT，不重复备份/改写', () => {
  withFile(legacyState, ({ file }) => {
    const first = migrateStateFile({ file });
    assert.equal(first.status, 'MIGRATED');
    const second = migrateStateFile({ file });
    assert.equal(second.status, 'CURRENT');
    assert.equal(second.schemaVersion, 1);
  });
});

test('dry-run：只报告不写盘（文件与备份都不变）', () => {
  withFile(legacyState, ({ dir, file }) => {
    const before = readFileSync(file, 'utf8');
    const result = migrateStateFile({ file, dryRun: true });
    assert.equal(result.status, 'DRY_RUN');
    assert.equal(result.wouldWrite, true);
    assert.equal(readFileSync(file, 'utf8'), before, 'dry-run 不得改文件');
    assert.equal(readdirSync(dir).filter((name) => name.includes('.bak')).length, 0, 'dry-run 不得写备份');
  });
});

test('拒绝未来/未知 schema：不猜测降级，文件不动', () => {
  withFile({ ...legacyState, schemaVersion: 2 }, ({ dir, file }) => {
    const before = readFileSync(file, 'utf8');
    assert.throws(() => migrateStateFile({ file }), (error) => error instanceof MigrationError && error.code === 'UNSUPPORTED');
    assert.equal(readFileSync(file, 'utf8'), before);
    assert.equal(readdirSync(dir).filter((name) => name.includes('.bak')).length, 0);
  });
});

test('拒绝损坏文件：非法 JSON / 顶层非对象 / 文件不存在', () => {
  withFile('{ not json', ({ file }) => {
    assert.throws(() => migrateStateFile({ file }), { code: 'INVALID_JSON' });
  });
  withFile('[1,2,3]', ({ file }) => {
    assert.throws(() => migrateStateFile({ file }), { code: 'INVALID_JSON' });
  });
  assert.throws(() => migrateStateFile({ file: path.join(os.tmpdir(), 'definitely-not-exists.json') }), { code: 'NOT_FOUND' });
  assert.throws(() => migrateStateFile({}), { code: 'INVALID_ARGUMENT' });
});

test('迁移后确实能被领域层加载（SCHEMA_MISMATCH 消失）', async () => {
  const { createWorld } = await import('../apps/im2agent/domain.mjs');
  withFile(legacyState, ({ file }) => {
    assert.throws(() => createWorld({ restored: legacyState }), { code: 'SCHEMA_MISMATCH' }, '迁移前必须失败');
    migrateStateFile({ file });
    const migrated = JSON.parse(readFileSync(file, 'utf8'));
    const world = createWorld({ restored: migrated });
    assert.equal(world.state.tickets.length, 2);
    assert.equal(world.state.audit.length, 2);
  });
});
