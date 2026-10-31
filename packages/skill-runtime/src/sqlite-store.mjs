import { createHash, randomUUID } from 'node:crypto';
import { DatabaseSync } from 'node:sqlite';
import { SkillError, assertId, EVAL_FLOOR } from './index.mjs';

const sha256 = (value) => createHash('sha256').update(value).digest('hex');
const hashFiles = (files) => sha256(JSON.stringify(Object.keys(files).sort().map((key) => [key, sha256(files[key])])));
const safePath = (path) => typeof path === 'string' && /^[A-Za-z0-9._/-]+$/.test(path) && !path.startsWith('/') && !path.split('/').includes('..');

function descriptionTokens(text) {
  return new Set(String(text ?? '').toLowerCase().match(/[a-z0-9\u4e00-\u9fff]+/g) ?? []);
}

function jaccard(a, b) {
  if (a.size === 0 || b.size === 0) return 0;
  let intersection = 0;
  for (const token of a) if (b.has(token)) intersection += 1;
  return intersection / new Set([...a, ...b]).size;
}

function parseSince(value) {
  if (value === undefined || value === null || value === '') return null;
  const parsed = typeof value === 'number' ? value : Date.parse(String(value));
  if (!Number.isFinite(parsed)) throw new SkillError('INVALID_SINCE');
  return parsed;
}

export class SqliteSkillStore {
  constructor({ path, evalFloor = EVAL_FLOOR, now = () => Date.now() } = {}) {
    if (!path) throw new SkillError('DB_PATH_REQUIRED');
    this.db = new DatabaseSync(path);
    this.evalFloor = evalFloor;
    this.now = now;
    this.db.exec(`
      PRAGMA foreign_keys = ON;
      CREATE TABLE IF NOT EXISTS packages (
        package_sha TEXT PRIMARY KEY,
        skill_id TEXT NOT NULL,
        version TEXT NOT NULL,
        description TEXT NOT NULL,
        tags_json TEXT NOT NULL,
        files_json TEXT NOT NULL,
        created_at INTEGER NOT NULL
      );
      CREATE TABLE IF NOT EXISTS active (
        skill_id TEXT PRIMARY KEY,
        package_sha TEXT NOT NULL REFERENCES packages(package_sha),
        updated_at INTEGER NOT NULL
      );
      CREATE TABLE IF NOT EXISTS proposals (
        proposal_id TEXT PRIMARY KEY,
        action TEXT NOT NULL,
        skill_id TEXT NOT NULL,
        proposer TEXT NOT NULL,
        base_sha TEXT,
        candidate_sha TEXT NOT NULL,
        files_json TEXT NOT NULL,
        status TEXT NOT NULL,
        evidence_json TEXT,
        eval_cases_json TEXT,
        created_at INTEGER NOT NULL,
        staged_at INTEGER
      );
      CREATE TABLE IF NOT EXISTS ledger (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        at INTEGER NOT NULL,
        skill_id TEXT NOT NULL,
        package_sha TEXT NOT NULL,
        previous_sha TEXT,
        reason TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS idempotency (
        idem_key TEXT PRIMARY KEY,
        result_json TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS sessions (
        session_id TEXT PRIMARY KEY,
        skill_id TEXT NOT NULL,
        package_sha TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS skill_usage (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        skill_id TEXT NOT NULL,
        used_at INTEGER NOT NULL,
        agent_ref TEXT,
        session_id TEXT,
        source TEXT NOT NULL
      );
      CREATE INDEX IF NOT EXISTS idx_skill_usage_skill_time ON skill_usage(skill_id, used_at);
      CREATE TABLE IF NOT EXISTS skill_owners (
        skill_id TEXT PRIMARY KEY,
        owner_agent_did TEXT NOT NULL,
        updated_at INTEGER NOT NULL
      );
    `);
  }

  registerPackage({ id, version = '0.1.0', description = '', tags = [], files = {} }) {
    assertId(id);
    if (!files['SKILL.md']) throw new SkillError('SKILL_BODY_REQUIRED');
    for (const name of Object.keys(files)) if (!safePath(name)) throw new SkillError('INVALID_PATH', name);
    const packageSha = hashFiles(files);
    this.transaction(() => {
      this.db.prepare('INSERT OR IGNORE INTO packages VALUES (?, ?, ?, ?, ?, ?, ?)')
        .run(packageSha, id, version, description, JSON.stringify(tags), JSON.stringify(files), this.now());
      const active = this.db.prepare('SELECT package_sha FROM active WHERE skill_id = ?').get(id);
      if (!active) this.setActive(id, packageSha, 'register');
    });
    return { packageSha };
  }

  list() {
    return this.db.prepare(`
      SELECT p.skill_id AS id, p.description, p.version, p.package_sha AS packageSha, p.tags_json
      FROM active a JOIN packages p ON p.package_sha = a.package_sha
      ORDER BY p.skill_id
    `).all().map((row) => ({ ...row, tags: JSON.parse(row.tags_json), tags_json: undefined }));
  }

  view(id, { path, offset = 0, limit = 64 * 1024, sessionId } = {}) {
    const packageSha = this.resolvePackage(id, sessionId);
    const row = this.db.prepare('SELECT files_json FROM packages WHERE package_sha = ?').get(packageSha);
    if (!row) throw new SkillError('SKILL_VERSION_GONE');
    const files = JSON.parse(row.files_json);
    if (!path) return {
      level: 'L1', packageSha, body: files['SKILL.md'],
      files: Object.keys(files).filter((name) => name !== 'SKILL.md'),
      updateAvailable: this.activeSha(id) !== packageSha,
    };
    if (!safePath(path) || !files[path]) throw new SkillError('PATH_NOT_ALLOWED');
    return {
      level: 'L2', packageSha, fileSha: sha256(files[path]), path,
      content: files[path].slice(offset, offset + limit),
      lines: { total: files[path].split('\n').length },
      updateAvailable: this.activeSha(id) !== packageSha,
    };
  }

  openSession(id) {
    const packageSha = this.activeSha(id);
    if (!packageSha) throw new SkillError('SKILL_NOT_FOUND');
    const sessionId = randomUUID();
    this.db.prepare('INSERT INTO sessions VALUES (?, ?, ?)').run(sessionId, id, packageSha);
    return { sessionId, packageSha };
  }

  manage({ action, id, baseSha, content, files, proposer = 'agent' } = {}) {
    assertId(id);
    if (!['create', 'patch', 'archive'].includes(action)) throw new SkillError('INVALID_ACTION');
    if (action !== 'create' && (!baseSha || this.activeSha(id) !== baseSha)) throw new SkillError('BASE_MISMATCH');
    const nextFiles = { ...(files ?? {}) };
    if (content) nextFiles['SKILL.md'] = String(content);
    if (!nextFiles['SKILL.md']) throw new SkillError('SKILL_BODY_REQUIRED');
    const proposalId = randomUUID();
    const candidateSha = hashFiles(nextFiles);
    this.db.prepare(`INSERT INTO proposals (proposal_id, action, skill_id, proposer, base_sha, candidate_sha, files_json, status, created_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, 'proposed', ?)`)
      .run(proposalId, action, id, proposer, baseSha ?? null, candidateSha, JSON.stringify(nextFiles), this.now());
    return { proposalId, status: 'proposed', candidateSha };
  }

  stage(proposalId, { evidence = [], evalCases = [], stagedBy = 'curator' } = {}) {
    const proposal = this.getProposal(proposalId);
    if (proposal.status !== 'proposed') throw new SkillError('INVALID_PROPOSAL_STATE');
    if (proposal.action !== 'create' && this.activeSha(proposal.skill_id) !== proposal.base_sha) throw new SkillError('BASE_MISMATCH');
    if (!evidence.length) throw new SkillError('EVIDENCE_REQUIRED');
    if (!evalCases.length || evalCases.length < this.evalFloor) throw new SkillError('EVAL_FLOOR_NOT_MET');
    this.db.prepare('UPDATE proposals SET status = ?, evidence_json = ?, eval_cases_json = ?, staged_at = ? WHERE proposal_id = ? AND status = ?')
      .run('staged', JSON.stringify(evidence), JSON.stringify(evalCases), this.now(), proposalId, 'proposed');
    return { proposalId, status: 'staged', candidateSha: proposal.candidate_sha, stagedBy };
  }

  promote({ proposalId, approvalId, expectedActiveSha, idempotencyKey, approvedBy = 'operator' } = {}) {
    if (idempotencyKey) {
      const prior = this.db.prepare('SELECT result_json FROM idempotency WHERE idem_key = ?').get(`promote:${idempotencyKey}`);
      if (prior) return JSON.parse(prior.result_json);
    }
    const proposal = this.getProposal(proposalId);
    if (proposal.status !== 'staged') throw new SkillError('NOT_STAGED');
    if (!approvalId) throw new SkillError('APPROVAL_REQUIRED');
    if (approvedBy === proposal.proposer) throw new SkillError('SELF_APPROVAL_FORBIDDEN');
    if (proposal.action !== 'create' && this.activeSha(proposal.skill_id) !== expectedActiveSha) throw new SkillError('ACTIVE_SHA_MISMATCH');
    const result = this.transaction(() => {
      this.db.prepare('INSERT OR IGNORE INTO packages VALUES (?, ?, ?, ?, ?, ?, ?)')
        .run(proposal.candidate_sha, proposal.skill_id, '2.0.0', `promoted ${proposal.skill_id}`, '[]', proposal.files_json, this.now());
      this.setActive(proposal.skill_id, proposal.candidate_sha, 'promote');
      this.db.prepare('UPDATE proposals SET status = ? WHERE proposal_id = ?').run('promoted', proposalId);
      const out = { proposalId, packageSha: proposal.candidate_sha, activeSha: proposal.candidate_sha };
      if (idempotencyKey) this.db.prepare('INSERT INTO idempotency VALUES (?, ?)')
        .run(`promote:${idempotencyKey}`, JSON.stringify(out));
      return out;
    });
    return result;
  }

  rollback({ id, toSha, approvalId, idempotencyKey } = {}) {
    if (idempotencyKey) {
      const prior = this.db.prepare('SELECT result_json FROM idempotency WHERE idem_key = ?').get(`rollback:${idempotencyKey}`);
      if (prior) return JSON.parse(prior.result_json);
    }
    if (!approvalId) throw new SkillError('APPROVAL_REQUIRED');
    const target = this.db.prepare('SELECT skill_id FROM packages WHERE package_sha = ?').get(toSha);
    if (!target || target.skill_id !== id) throw new SkillError('UNKNOWN_VERSION');
    const current = this.activeSha(id);
    if (!current) throw new SkillError('SKILL_NOT_FOUND');
    return this.transaction(() => {
      this.setActive(id, toSha, 'rollback');
      const out = { id, previousSha: current, activeSha: toSha };
      if (idempotencyKey) this.db.prepare('INSERT INTO idempotency VALUES (?, ?)')
        .run(`rollback:${idempotencyKey}`, JSON.stringify(out));
      return out;
    });
  }

  curator({ action = 'status' } = {}) {
    if (!['status', 'scan', 'report'].includes(action)) throw new SkillError('FORBIDDEN');
    return { action, active: this.list(), proposals: this.db.prepare('SELECT proposal_id, skill_id, status FROM proposals ORDER BY created_at').all() };
  }

  recordUsage(id, { agentRef = null, sessionId = null, source = 'skill_view', at = this.now() } = {}) {
    assertId(id);
    const usedAt = Number(at);
    if (!Number.isInteger(usedAt)) throw new SkillError('INVALID_USAGE_TIME');
    this.db.prepare('INSERT INTO skill_usage (skill_id, used_at, agent_ref, session_id, source) VALUES (?, ?, ?, ?, ?)')
      .run(id, usedAt, agentRef === null ? null : String(agentRef).slice(0, 200), sessionId === null ? null : String(sessionId).slice(0, 200), String(source).slice(0, 32));
  }

  setOwner(id, agentDid) {
    assertId(id);
    if (typeof agentDid !== 'string' || agentDid.trim() === '') throw new SkillError('INVALID_AGENT_DID');
    this.db.prepare(`INSERT INTO skill_owners (skill_id, owner_agent_did, updated_at) VALUES (?, ?, ?)
      ON CONFLICT(skill_id) DO UPDATE SET owner_agent_did = excluded.owner_agent_did, updated_at = excluded.updated_at`)
      .run(id, agentDid.trim(), this.now());
  }

  /**
   * Read-only lifecycle audit. It performs SELECTs only; recommended_action is
   * never executed here.
   */
  curatorAudit({
    agentDid = null,
    scope = 'network',
    since,
    now = this.now(),
    staleDays = 30,
    archiveDays = 90,
    mergeSimilarity = 0.8,
  } = {}) {
    if (!['network', 'agent_owned'].includes(scope)) throw new SkillError('INVALID_SCOPE');
    if (scope === 'agent_owned' && (typeof agentDid !== 'string' || agentDid.trim() === '')) {
      throw new SkillError('AGENT_DID_REQUIRED');
    }
    if (!Number.isFinite(staleDays) || staleDays <= 0 || !Number.isFinite(archiveDays) || archiveDays <= staleDays) {
      throw new SkillError('INVALID_LIFECYCLE_THRESHOLDS');
    }
    if (!Number.isFinite(mergeSimilarity) || mergeSimilarity < 0 || mergeSimilarity > 1) {
      throw new SkillError('INVALID_MERGE_SIMILARITY');
    }
    const sinceMs = parseSince(since);
    const nowMs = Number(now);
    if (!Number.isFinite(nowMs)) throw new SkillError('INVALID_AUDIT_TIME');
    const usageCutoff = nowMs - 30 * 24 * 60 * 60 * 1000;
    const rows = this.db.prepare(`
      SELECT p.skill_id AS id, p.description, p.version, p.package_sha AS packageSha,
             p.tags_json, a.updated_at AS updatedAt,
             (SELECT MAX(used_at) FROM skill_usage su WHERE su.skill_id = p.skill_id) AS lastUsedAt,
             (SELECT COUNT(*) FROM skill_usage su WHERE su.skill_id = p.skill_id AND su.used_at >= ?) AS useCount30d,
             (SELECT COUNT(*) FROM proposals pr WHERE pr.skill_id = p.skill_id AND pr.action = 'patch') AS patchCount,
             (SELECT COUNT(*) FROM proposals pr WHERE pr.skill_id = p.skill_id AND pr.action = 'archive' AND pr.status = 'promoted') AS archivePromoted,
             (SELECT owner_agent_did FROM skill_owners so WHERE so.skill_id = p.skill_id) AS ownerAgentDid,
             (SELECT pr.proposer FROM proposals pr WHERE pr.skill_id = p.skill_id ORDER BY pr.created_at DESC LIMIT 1) AS lastProposer
      FROM active a JOIN packages p ON p.package_sha = a.package_sha
      ORDER BY p.skill_id
    `).all(usageCutoff).map((row) => {
      const lastUsedMs = Number(row.lastUsedAt ?? row.updatedAt);
      const effectiveMs = Math.max(lastUsedMs, Number(row.updatedAt));
      if (sinceMs !== null && effectiveMs < sinceMs) return null;
      if (scope === 'agent_owned' && row.ownerAgentDid !== agentDid && row.lastProposer !== agentDid) return null;
      const ageDays = Math.max(0, (nowMs - lastUsedMs) / (24 * 60 * 60 * 1000));
      const archived = Number(row.archivePromoted) > 0;
      const description = String(row.description ?? '');
      const l0 = {
        id: row.id,
        description,
        version: row.version,
        packageSha: row.packageSha,
        tags: JSON.parse(row.tags_json),
      };
      return {
        id: row.id,
        description,
        tags: l0.tags,
        ageDays,
        archived,
        lastUsedAt: lastUsedMs,
        useCount30d: Number(row.useCount30d),
        patchCount: Number(row.patchCount),
        estimatedTokens: Math.ceil(Buffer.byteLength(JSON.stringify(l0), 'utf8') / 4),
      };
    }).filter(Boolean);

    for (const row of rows) {
      row.recommendedAction = row.archived
        ? 'keep'
        : row.ageDays >= archiveDays
          ? 'archive_candidate'
          : row.ageDays >= staleDays
            ? 'flag_for_review'
            : 'keep';
    }
    for (let i = 0; i < rows.length; i += 1) {
      for (let j = i + 1; j < rows.length; j += 1) {
        const a = rows[i];
        const b = rows[j];
        if (a.archived || b.archived || a.recommendedAction === 'archive_candidate' || b.recommendedAction === 'archive_candidate') continue;
        if (jaccard(descriptionTokens(a.description), descriptionTokens(b.description)) >= mergeSimilarity) {
          a.recommendedAction = 'merge_candidate';
          b.recommendedAction = 'merge_candidate';
        }
      }
    }

    const skills = rows.map((row) => ({
      skill_id: row.id,
      name: row.id,
      lifecycle_state: row.archived ? 'archived' : row.ageDays >= staleDays ? 'stale' : 'active',
      last_used_at: new Date(row.lastUsedAt).toISOString(),
      use_count_30d: row.useCount30d,
      patch_count: row.patchCount,
      estimated_context_cost_tokens: row.estimatedTokens,
      recommended_action: row.recommendedAction,
    }));
    return {
      skills,
      audit: {
        generated_at: new Date(nowMs).toISOString(),
        scope,
        agent_did: agentDid,
        since: sinceMs === null ? null : new Date(sinceMs).toISOString(),
        thresholds: { staleDays, archiveDays, mergeSimilarity },
      },
    };
  }

  activeSha(id) {
    return this.db.prepare('SELECT package_sha FROM active WHERE skill_id = ?').get(id)?.package_sha ?? null;
  }

  resolvePackage(id, sessionId) {
    if (sessionId) {
      const row = this.db.prepare('SELECT skill_id, package_sha FROM sessions WHERE session_id = ?').get(sessionId);
      if (!row || row.skill_id !== id) throw new SkillError('SESSION_SKILL_MISMATCH');
      return row.package_sha;
    }
    const sha = this.activeSha(id);
    if (!sha) throw new SkillError('SKILL_NOT_FOUND');
    return sha;
  }

  getProposal(id) {
    const row = this.db.prepare('SELECT * FROM proposals WHERE proposal_id = ?').get(id);
    if (!row) throw new SkillError('PROPOSAL_NOT_FOUND');
    return row;
  }

  setActive(id, packageSha, reason) {
    const previous = this.activeSha(id);
    this.db.prepare('INSERT INTO active (skill_id, package_sha, updated_at) VALUES (?, ?, ?) ON CONFLICT(skill_id) DO UPDATE SET package_sha = excluded.package_sha, updated_at = excluded.updated_at')
      .run(id, packageSha, this.now());
    this.db.prepare('INSERT INTO ledger (at, skill_id, package_sha, previous_sha, reason) VALUES (?, ?, ?, ?, ?)')
      .run(this.now(), id, packageSha, previous, reason);
  }

  transaction(fn) {
    this.db.exec('BEGIN IMMEDIATE');
    try { const result = fn(); this.db.exec('COMMIT'); return result; }
    catch (error) { this.db.exec('ROLLBACK'); throw error; }
  }

  close() { this.db.close(); }
}
