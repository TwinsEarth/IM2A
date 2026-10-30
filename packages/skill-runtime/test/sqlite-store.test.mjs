import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { SqliteSkillStore } from '../src/sqlite-store.mjs';

const files = { 'SKILL.md': '# DB base', 'references/a.md': 'ref' };
const tmp = () => mkdtempSync(join(tmpdir(), 'im2a-skill-db-'));

test('real SQLite persistence and zero-residue cleanup', () => {
  const dir = tmp();
  const dbPath = join(dir, 'skills.db');
  const store = new SqliteSkillStore({ path: dbPath });
  const { packageSha } = store.registerPackage({ id: 'demo', files });
  assert.equal(store.list()[0].packageSha, packageSha);
  store.close();
  assert.equal(existsSync(dbPath), true);
  const reopened = new SqliteSkillStore({ path: dbPath });
  assert.equal(reopened.list()[0].packageSha, packageSha);
  reopened.close();
  for (const name of ['skills.db', 'skills.db-wal', 'skills.db-shm']) rmSync(join(dir, name), { force: true });
  assert.equal(existsSync(dbPath), false);
  rmSync(dir, { recursive: true, force: true });
  assert.equal(existsSync(dir), false);
});

test('proposal does not mutate active and base mismatch is rejected', () => {
  const store = new SqliteSkillStore({ path: join(tmp(), 'skills.db') });
  const base = store.registerPackage({ id: 'demo', files }).packageSha;
  const p = store.manage({ action: 'patch', id: 'demo', baseSha: base, content: '# v2' });
  assert.equal(store.list()[0].packageSha, base);
  assert.equal(store.db.prepare('SELECT status FROM proposals WHERE proposal_id = ?').get(p.proposalId).status, 'proposed');
  assert.throws(() => store.manage({ action: 'patch', id: 'demo', baseSha: 'bad', content: '# bad' }), /BASE_MISMATCH/);
  store.close();
});

test('SQLite stage gates and persistence', () => {
  const store = new SqliteSkillStore({ path: join(tmp(), 'skills.db') });
  const base = store.registerPackage({ id: 'demo', files }).packageSha;
  const p = store.manage({ action: 'patch', id: 'demo', baseSha: base, content: '# v2' });
  assert.throws(() => store.stage(p.proposalId, { evidence: [], evalCases: [] }), /EVIDENCE_REQUIRED/);
  assert.throws(() => store.stage(p.proposalId, { evidence: ['e'], evalCases: ['x'] }), /EVAL_FLOOR_NOT_MET/);
  store.stage(p.proposalId, { evidence: ['e'], evalCases: Array.from({ length: 20 }, (_, i) => `c${i}`) });
  assert.equal(store.db.prepare('SELECT status FROM proposals WHERE proposal_id = ?').get(p.proposalId).status, 'staged');
  store.close();
});

test('SQLite promotion writes package, active, ledger, idempotency', () => {
  const store = new SqliteSkillStore({ path: join(tmp(), 'skills.db') });
  const base = store.registerPackage({ id: 'demo', files }).packageSha;
  const p = store.manage({ action: 'patch', id: 'demo', baseSha: base, content: '# v2' });
  store.stage(p.proposalId, { evidence: ['e'], evalCases: Array.from({ length: 20 }, (_, i) => `c${i}`) });
  const result = store.promote({ proposalId: p.proposalId, approvalId: 'approval-1', expectedActiveSha: base, idempotencyKey: 'idem-1' });
  assert.equal(store.activeSha('demo'), result.packageSha);
  assert.equal(store.db.prepare('SELECT COUNT(*) AS n FROM ledger WHERE skill_id = ?').get('demo').n, 2);
  assert.equal(store.db.prepare('SELECT COUNT(*) AS n FROM idempotency').get().n, 1);
  assert.deepEqual(store.promote({ proposalId: p.proposalId, approvalId: 'approval-1', expectedActiveSha: base, idempotencyKey: 'idem-1' }), result);
  store.close();
});

test('SQLite rollback moves pointer and rejects unknown hash', () => {
  const store = new SqliteSkillStore({ path: join(tmp(), 'skills.db') });
  const base = store.registerPackage({ id: 'demo', files }).packageSha;
  const p = store.manage({ action: 'patch', id: 'demo', baseSha: base, content: '# v2' });
  store.stage(p.proposalId, { evidence: ['e'], evalCases: Array.from({ length: 20 }, (_, i) => `c${i}`) });
  const promoted = store.promote({ proposalId: p.proposalId, approvalId: 'a', expectedActiveSha: base });
  const rolled = store.rollback({ id: 'demo', toSha: base, approvalId: 'a' });
  assert.equal(rolled.activeSha, base);
  assert.notEqual(rolled.previousSha, base);
  assert.throws(() => store.rollback({ id: 'demo', toSha: 'nope', approvalId: 'a' }), /UNKNOWN_VERSION/);
  assert.equal(store.activeSha('demo'), base);
  assert.equal(promoted.activeSha, promoted.packageSha);
  store.close();
});

test('curator cannot promote or rollback in SQLite store', () => {
  const store = new SqliteSkillStore({ path: join(tmp(), 'skills.db') });
  store.registerPackage({ id: 'demo', files });
  assert.throws(() => store.curator({ action: 'promote' }), /FORBIDDEN/);
  assert.throws(() => store.curator({ action: 'rollback' }), /FORBIDDEN/);
  assert.equal(store.curator({ action: 'status' }).active.length, 1);
  store.close();
});
