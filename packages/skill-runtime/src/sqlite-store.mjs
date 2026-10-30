import { createHash, randomUUID } from 'node:crypto';
import { DatabaseSync } from 'node:sqlite';
import { SkillError, assertId, EVAL_FLOOR } from './index.mjs';

const sha256 = (value) => createHash('sha256').update(value).digest('hex');
const hashFiles = (files) => sha256(JSON.stringify(Object.keys(files).sort().map((key) => [key, sha256(files[key])])));
const safePath = (path) => typeof path === 'string' && /^[A-Za-z0-9._/-]+$/.test(path) && !path.startsWith('/') && !path.split('/').includes('..');

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
