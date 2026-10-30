import { createHash, randomUUID } from 'node:crypto';
import { DatabaseSync } from 'node:sqlite';

const sha = (value) => createHash('sha256').update(value).digest('hex');

export class MemoryHub {
  constructor({ path, tenant = 'local', now = () => Date.now() } = {}) {
    if (!path) throw new Error('MEMORY_DB_PATH_REQUIRED');
    this.tenant = tenant;
    this.now = now;
    this.db = new DatabaseSync(path);
    this.db.exec(`
      PRAGMA foreign_keys = ON;
      CREATE TABLE IF NOT EXISTS memory (
        id TEXT PRIMARY KEY,
        tenant TEXT NOT NULL,
        kind TEXT NOT NULL,
        title TEXT NOT NULL,
        body TEXT NOT NULL,
        metadata_json TEXT NOT NULL,
        created_at INTEGER NOT NULL
      );
      CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
        id UNINDEXED, tenant UNINDEXED, kind UNINDEXED, title, body
      );
      CREATE TABLE IF NOT EXISTS memory_proposals (
        proposal_id TEXT PRIMARY KEY,
        tenant TEXT NOT NULL,
        kind TEXT NOT NULL,
        title TEXT NOT NULL,
        body TEXT NOT NULL,
        metadata_json TEXT NOT NULL,
        status TEXT NOT NULL,
        created_at INTEGER NOT NULL
      );
    `);
  }

  put({ id = randomUUID(), kind = 'fact', title, body, metadata = {} } = {}) {
    if (!title || !body) throw new Error('title and body are required');
    return this.transaction(() => {
      this.db.prepare('INSERT INTO memory VALUES (?, ?, ?, ?, ?, ?, ?)')
        .run(id, this.tenant, kind, title, body, JSON.stringify(metadata), this.now());
      this.db.prepare('INSERT INTO memory_fts VALUES (?, ?, ?, ?, ?)')
        .run(id, this.tenant, kind, title, body);
      return { id, tenant: this.tenant, kind, title };
    });
  }

  propose({ kind = 'fact', title, body, metadata = {} } = {}) {
    if (!title || !body) throw new Error('title and body are required');
    const proposalId = randomUUID();
    this.db.prepare('INSERT INTO memory_proposals VALUES (?, ?, ?, ?, ?, ?, ?, ?)')
      .run(proposalId, this.tenant, kind, title, body, JSON.stringify(metadata), 'proposed', this.now());
    return { proposalId, status: 'proposed', tenant: this.tenant };
  }

  promoteProposal(proposalId) {
    return this.transaction(() => {
      const row = this.db.prepare('SELECT * FROM memory_proposals WHERE proposal_id = ? AND tenant = ?').get(proposalId, this.tenant);
      if (!row) throw new Error('PROPOSAL_NOT_FOUND');
      if (row.status !== 'proposed') throw new Error('INVALID_PROPOSAL_STATE');
      const id = randomUUID();
      this.db.prepare('INSERT INTO memory VALUES (?, ?, ?, ?, ?, ?, ?)')
        .run(id, this.tenant, row.kind, row.title, row.body, row.metadata_json, this.now());
      this.db.prepare('INSERT INTO memory_fts VALUES (?, ?, ?, ?, ?)')
        .run(id, this.tenant, row.kind, row.title, row.body);
      this.db.prepare('UPDATE memory_proposals SET status = ? WHERE proposal_id = ?').run('promoted', proposalId);
      return { id, proposalId, tenant: this.tenant };
    });
  }

  get(id) {
    const row = this.db.prepare('SELECT * FROM memory WHERE id = ? AND tenant = ?').get(id, this.tenant);
    if (!row) return null;
    return { id: row.id, kind: row.kind, title: row.title, body: row.body, metadata: JSON.parse(row.metadata_json), createdAt: row.created_at };
  }

  search(query, { limit = 20 } = {}) {
    const terms = String(query ?? '').trim().split(/\s+/).filter(Boolean).map((term) => `"${term.replaceAll('"', '""')}"`);
    if (terms.length === 0) return [];
    const ftsQuery = terms.join(' AND ');
    const rows = this.db.prepare(`
      SELECT m.id, m.kind, m.title, snippet(memory_fts, 4, '[', ']', '…', 12) AS snippet, bm25(memory_fts) AS rank
      FROM memory_fts JOIN memory m ON m.id = memory_fts.id
      WHERE memory_fts MATCH ? AND memory_fts.tenant = ?
      ORDER BY rank LIMIT ?
    `).all(ftsQuery, this.tenant, Math.min(Math.max(Number(limit), 1), 100));
    return rows.map((row) => ({ ...row, rank: Number(row.rank) }));
  }

  transaction(fn) {
    this.db.exec('BEGIN IMMEDIATE');
    try { const result = fn(); this.db.exec('COMMIT'); return result; }
    catch (error) { this.db.exec('ROLLBACK'); throw error; }
  }

  close() { this.db.close(); }
}

export { sha };
