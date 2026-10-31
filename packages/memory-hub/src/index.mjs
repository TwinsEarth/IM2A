import { createHash, randomUUID } from 'node:crypto';
import { DatabaseSync } from 'node:sqlite';

const sha = (value) => createHash('sha256').update(value).digest('hex');
const LAYERS = new Set(['raw_event', 'atom', 'skill_description', 'entity_page']);
const LAYER_PRIORITY = { skill_description: 1, atom: 0.8, entity_page: 0.55, raw_event: 0.25 };

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
        created_at INTEGER NOT NULL,
        updated_at INTEGER,
        layer TEXT NOT NULL DEFAULT 'atom',
        source_trace_id TEXT,
        confidence REAL NOT NULL DEFAULT 0.5,
        conflict_key TEXT
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
        created_at INTEGER NOT NULL,
        updated_at INTEGER,
        layer TEXT NOT NULL DEFAULT 'atom',
        source_trace_id TEXT,
        confidence REAL NOT NULL DEFAULT 0.5,
        conflict_key TEXT
      );
    `);
    this.ensureColumns('memory', {
      updated_at: 'INTEGER',
      layer: "TEXT NOT NULL DEFAULT 'atom'",
      source_trace_id: 'TEXT',
      confidence: 'REAL NOT NULL DEFAULT 0.5',
      conflict_key: 'TEXT',
    });
    this.ensureColumns('memory_proposals', {
      updated_at: 'INTEGER',
      layer: "TEXT NOT NULL DEFAULT 'atom'",
      source_trace_id: 'TEXT',
      confidence: 'REAL NOT NULL DEFAULT 0.5',
      conflict_key: 'TEXT',
    });
    this.db.exec('UPDATE memory SET updated_at = created_at WHERE updated_at IS NULL');
    this.db.exec('UPDATE memory_proposals SET updated_at = created_at WHERE updated_at IS NULL');
  }

  ensureColumns(table, definitions) {
    const existing = new Set(this.db.prepare(`PRAGMA table_info(${table})`).all().map((row) => row.name));
    for (const [name, ddl] of Object.entries(definitions)) {
      if (!existing.has(name)) this.db.exec(`ALTER TABLE ${table} ADD COLUMN ${name} ${ddl}`);
    }
  }

  put({
    id = randomUUID(),
    kind = 'fact',
    title,
    body,
    metadata = {},
    layer = 'atom',
    sourceTraceId = null,
    confidence = 0.5,
    conflictKey = null,
    updatedAt = null,
  } = {}) {
    if (!title || !body) throw new Error('title and body are required');
    if (!LAYERS.has(layer)) throw new Error('INVALID_LAYER');
    if (!Number.isFinite(confidence) || confidence < 0 || confidence > 1) throw new Error('INVALID_CONFIDENCE');
    const at = this.now();
    return this.transaction(() => {
      this.db.prepare(`INSERT INTO memory
        (id, tenant, kind, title, body, metadata_json, created_at, updated_at, layer, source_trace_id, confidence, conflict_key)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
        .run(id, this.tenant, kind, title, body, JSON.stringify(metadata), at, Number(updatedAt ?? at), layer, sourceTraceId, confidence, conflictKey);
      this.db.prepare('INSERT INTO memory_fts VALUES (?, ?, ?, ?, ?)')
        .run(id, this.tenant, kind, title, body);
      return { id, tenant: this.tenant, kind, title, layer, confidence };
    });
  }

  propose({
    kind = 'fact',
    title,
    body,
    metadata = {},
    layer = 'atom',
    sourceTraceId = null,
    confidence = 0.5,
    conflictKey = null,
    updatedAt = null,
  } = {}) {
    if (!title || !body) throw new Error('title and body are required');
    if (!LAYERS.has(layer)) throw new Error('INVALID_LAYER');
    if (!Number.isFinite(confidence) || confidence < 0 || confidence > 1) throw new Error('INVALID_CONFIDENCE');
    const proposalId = randomUUID();
    const at = this.now();
    this.db.prepare(`INSERT INTO memory_proposals
      (proposal_id, tenant, kind, title, body, metadata_json, status, created_at, updated_at, layer, source_trace_id, confidence, conflict_key)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
      .run(proposalId, this.tenant, kind, title, body, JSON.stringify(metadata), 'proposed', at, Number(updatedAt ?? at), layer, sourceTraceId, confidence, conflictKey);
    return { proposalId, status: 'proposed', tenant: this.tenant };
  }

  promoteProposal(proposalId) {
    return this.transaction(() => {
      const row = this.db.prepare('SELECT * FROM memory_proposals WHERE proposal_id = ? AND tenant = ?').get(proposalId, this.tenant);
      if (!row) throw new Error('PROPOSAL_NOT_FOUND');
      if (row.status !== 'proposed') throw new Error('INVALID_PROPOSAL_STATE');
      const id = randomUUID();
      this.db.prepare(`INSERT INTO memory
        (id, tenant, kind, title, body, metadata_json, created_at, updated_at, layer, source_trace_id, confidence, conflict_key)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
        .run(id, this.tenant, row.kind, row.title, row.body, row.metadata_json, this.now(), row.updated_at ?? this.now(), row.layer ?? 'atom', row.source_trace_id ?? null, row.confidence ?? 0.5, row.conflict_key ?? null);
      this.db.prepare('INSERT INTO memory_fts VALUES (?, ?, ?, ?, ?)')
        .run(id, this.tenant, row.kind, row.title, row.body);
      this.db.prepare('UPDATE memory_proposals SET status = ? WHERE proposal_id = ?').run('promoted', proposalId);
      return { id, proposalId, tenant: this.tenant };
    });
  }

  get(id) {
    const row = this.db.prepare('SELECT * FROM memory WHERE id = ? AND tenant = ?').get(id, this.tenant);
    if (!row) return null;
    return {
      id: row.id,
      kind: row.kind,
      title: row.title,
      body: row.body,
      metadata: JSON.parse(row.metadata_json),
      createdAt: row.created_at,
      updatedAt: row.updated_at ?? row.created_at,
      layer: row.layer ?? 'atom',
      sourceTraceId: row.source_trace_id ?? null,
      confidence: row.confidence ?? 0.5,
      conflictKey: row.conflict_key ?? null,
    };
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

  /**
   * Layered memory recall with quality flags and a hard token budget.
   * This is a read-only pipeline: parse -> route -> gather -> rerank ->
   * diversify -> suppress -> budget.
   */
  recall({
    query,
    agentDid = null,
    budgetTokens = 2000,
    layers,
    includeStale = false,
  } = {}) {
    const text = String(query ?? '').trim();
    if (!text) return { items: [], budget_used: 0, suppressed_count: 0 };
    const budget = Number(budgetTokens);
    if (!Number.isInteger(budget) || budget < 1 || budget > 200000) throw new Error('INVALID_BUDGET_TOKENS');
    const requestedLayers = layers === undefined
      ? [...LAYERS]
      : Array.isArray(layers) ? [...new Set(layers)] : null;
    if (!requestedLayers || requestedLayers.some((layer) => !LAYERS.has(layer))) throw new Error('INVALID_LAYERS');
    const now = this.now();
    const terms = text.split(/\s+/).filter(Boolean);
    const ftsQuery = terms.map((term) => `"${term.replaceAll('"', '""')}"`).join(' AND ');
    const placeholders = requestedLayers.map(() => '?').join(',');
    let rows = [];
    try {
      rows = this.db.prepare(`
        SELECT m.*, bm25(memory_fts) AS rank
        FROM memory_fts JOIN memory m ON m.id = memory_fts.id
        WHERE memory_fts MATCH ? AND memory_fts.tenant = ? AND m.tenant = ?
          AND m.layer IN (${placeholders})
        ORDER BY rank LIMIT 200
      `).all(ftsQuery, this.tenant, this.tenant, ...requestedLayers);
    } catch {
      rows = [];
    }
    if (rows.length === 0) {
      const like = `%${terms[0].replaceAll('%', '\\%').replaceAll('_', '\\_')}%`;
      rows = this.db.prepare(`
        SELECT * FROM memory
        WHERE tenant = ? AND layer IN (${placeholders}) AND (title LIKE ? ESCAPE '\\' OR body LIKE ? ESCAPE '\\')
        ORDER BY updated_at DESC LIMIT 200
      `).all(this.tenant, ...requestedLayers, like, like);
    }

    const conflicts = new Map();
    for (const row of this.db.prepare('SELECT conflict_key, confidence FROM memory WHERE tenant = ? AND conflict_key IS NOT NULL').all(this.tenant)) {
      const best = conflicts.get(row.conflict_key) ?? -1;
      if (row.confidence > best) conflicts.set(row.conflict_key, row.confidence);
    }

    const candidates = rows.map((row, index) => {
      const updatedAt = Number(row.updated_at ?? row.created_at);
      const ageDays = Math.max(0, (now - updatedAt) / (24 * 60 * 60 * 1000));
      const confidence = Number(row.confidence ?? 0.5);
      const stale = row.layer === 'atom' && ageDays > 30;
      const conflicted = row.conflict_key && (conflicts.get(row.conflict_key) ?? -1) > confidence;
      const lowConfidence = confidence < 0.4;
      const qualityFlag = stale ? 'stale' : conflicted ? 'conflicted' : lowConfidence ? 'low_confidence' : 'fresh';
      const relevance = rows.length <= 1 ? 1 : 1 - (index / (rows.length - 1)) * 0.8;
      const recency = Math.max(0, 1 - ageDays / 120);
      const score = Math.round((
        0.45 * (LAYER_PRIORITY[row.layer] ?? 0.2)
        + 0.25 * confidence
        + 0.2 * relevance
        + 0.1 * recency
      ) * 1000) / 1000;
      const content = `${row.title}\n${row.body}`;
      const tokenCost = Math.max(1, Math.ceil(Buffer.byteLength(content, 'utf8') / 4));
      return {
        id: row.id,
        content,
        layer: row.layer,
        score,
        quality_flag: qualityFlag,
        source_trace_id: row.source_trace_id ?? null,
        tokenCost,
        ageDays,
        confidence,
      };
    });

    candidates.sort((a, b) =>
      (LAYER_PRIORITY[b.layer] - LAYER_PRIORITY[a.layer])
      || (b.score - a.score)
      || a.id.localeCompare(b.id));

    const items = [];
    let used = 0;
    let suppressed = 0;
    let rawUsed = 0;
    const rawBudget = Math.floor(budget * 0.15);
    for (const candidate of candidates) {
      if (candidate.quality_flag === 'stale' && !includeStale) {
        suppressed += 1;
        continue;
      }
      if (candidate.layer === 'raw_event' && rawUsed + candidate.tokenCost > rawBudget) {
        suppressed += 1;
        continue;
      }
      if (used + candidate.tokenCost > budget) {
        suppressed += 1;
        continue;
      }
      used += candidate.tokenCost;
      if (candidate.layer === 'raw_event') rawUsed += candidate.tokenCost;
      items.push({
        content: candidate.content,
        layer: candidate.layer,
        score: candidate.score,
        quality_flag: candidate.quality_flag,
        source_trace_id: candidate.source_trace_id,
      });
    }

    return {
      items,
      budget_used: used,
      suppressed_count: suppressed,
      namespace: this.tenant,
      agent_did: agentDid,
    };
  }

  textSimilarity(left, right) {
    const tokenize = (text) => String(text ?? "").toLowerCase().match(/[\p{L}\p{N}_:-]+/gu) ?? [];
    const a = tokenize(left);
    const b = tokenize(right);
    if (a.length === 0 || b.length === 0) return 0;
    const countsA = new Map();
    const countsB = new Map();
    for (const token of a) countsA.set(token, (countsA.get(token) ?? 0) + 1);
    for (const token of b) countsB.set(token, (countsB.get(token) ?? 0) + 1);
    let dot = 0;
    for (const [token, count] of countsA) dot += count * (countsB.get(token) ?? 0);
    const normA = Math.sqrt([...countsA.values()].reduce((sum, count) => sum + count * count, 0));
    const normB = Math.sqrt([...countsB.values()].reduce((sum, count) => sum + count * count, 0));
    return dot / (normA * normB);
  }

  transaction(fn) {
    this.db.exec('BEGIN IMMEDIATE');
    try { const result = fn(); this.db.exec('COMMIT'); return result; }
    catch (error) { this.db.exec('ROLLBACK'); throw error; }
  }

  close() { this.db.close(); }
}

export { sha };
