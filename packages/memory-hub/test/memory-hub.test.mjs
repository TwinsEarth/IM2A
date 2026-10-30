import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { MemoryHub } from '../src/index.mjs';

const dir = () => mkdtempSync(join(tmpdir(), 'im2a-memory-'));

test('real FTS5 search and tenant isolation', () => {
  const d = dir();
  const db = new MemoryHub({ path: join(d, 'memory.db'), tenant: 'tenant-a' });
  db.put({ title: 'FTS5 recall', body: 'agent skill evolution memory' });
  db.put({ title: 'unrelated', body: 'billing invoice' });
  assert.equal(db.search('evolution').length, 1);
  assert.match(db.search('evolution')[0].snippet, /evolution/);
  assert.equal(db.search('billing').length, 1);
  assert.equal(db.search('missingterm').length, 0);
  db.close();
  rmSync(d, { recursive: true, force: true });
  assert.equal(existsSync(d), false);
});

test('memory proposal does not become visible until promoted', () => {
  const d = dir();
  const db = new MemoryHub({ path: join(d, 'memory.db'), tenant: 'tenant-a' });
  const p = db.propose({ title: 'new experience', body: 'proposal-only memory' });
  assert.equal(db.search('proposal-only').length, 0);
  const promoted = db.promoteProposal(p.proposalId);
  assert.equal(db.search('proposal-only')[0].id, promoted.id);
  db.close();
  rmSync(d, { recursive: true, force: true });
});

test('memory get is tenant scoped and zero residue', () => {
  const d = dir();
  const db = new MemoryHub({ path: join(d, 'memory.db'), tenant: 'tenant-a' });
  const doc = db.put({ title: 'secret plan', body: 'tenant a only' });
  assert.equal(db.get(doc.id).title, 'secret plan');
  db.close();
  rmSync(d, { recursive: true, force: true });
  assert.equal(existsSync(d), false);
});
