import { createHash, randomUUID } from 'node:crypto';

export const SKILL_RUNTIME_VERSION = '0.1.0';
export const EVAL_FLOOR = 20;

export class SkillError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = 'SkillError';
    this.code = code;
  }
}

const sha256 = (value) => createHash('sha256').update(value).digest('hex');
const hashFiles = (files) => sha256(JSON.stringify(Object.keys(files).sort().map((key) => [key, sha256(files[key])])));
const safePath = (path) => typeof path === 'string' && /^[A-Za-z0-9._/-]+$/.test(path) && !path.startsWith('/') && !path.split('/').includes('..');

export class SkillRuntime {
  constructor({ evalFloor = EVAL_FLOOR, now = () => Date.now() } = {}) {
    this.evalFloor = evalFloor;
    this.now = now;
    this.packages = new Map();
    this.active = new Map();
    this.history = new Map();
    this.proposals = new Map();
    this.ledger = [];
    this.idempotency = new Map();
    this.sessions = new Map();
  }

  registerPackage({ id, version = '0.1.0', description = '', tags = [], files = {} }) {
    assertId(id);
    if (!files['SKILL.md']) throw new SkillError('SKILL_BODY_REQUIRED');
    for (const name of Object.keys(files)) if (!safePath(name)) throw new SkillError('INVALID_PATH', name);
    const packageSha = hashFiles(files);
    this.packages.set(packageSha, { id, version, description, tags, files, packageSha });
    if (!this.active.has(id)) this.setActive(id, packageSha, 'register');
    return { packageSha };
  }

  list() {
    return [...this.active.entries()].map(([id, packageSha]) => {
      const pkg = this.packages.get(packageSha);
      return { id, description: pkg.description, version: pkg.version, packageSha, tags: pkg.tags };
    });
  }

  view(id, { path, offset = 0, limit = 64 * 1024, sessionId } = {}) {
    const packageSha = this.resolvePackage(id, sessionId);
    const pkg = this.packages.get(packageSha);
    if (!pkg) throw new SkillError('SKILL_VERSION_GONE');
    if (!path) {
      const files = Object.keys(pkg.files).filter((name) => name !== 'SKILL.md');
      return { level: 'L1', packageSha, body: pkg.files['SKILL.md'], files, updateAvailable: this.active.get(id) !== packageSha };
    }
    if (!safePath(path) || !pkg.files[path]) throw new SkillError('PATH_NOT_ALLOWED');
    const content = pkg.files[path].slice(offset, offset + limit);
    return { level: 'L2', packageSha, fileSha: sha256(pkg.files[path]), path, content, lines: { total: pkg.files[path].split('\n').length }, updateAvailable: this.active.get(id) !== packageSha };
  }

  openSession(id) {
    const packageSha = this.active.get(id);
    if (!packageSha) throw new SkillError('SKILL_NOT_FOUND');
    const sessionId = randomUUID();
    this.sessions.set(sessionId, { skillId: id, packageSha });
    return { sessionId, packageSha };
  }

  manage({ action, id, baseSha, content, files, proposer = 'agent' } = {}) {
    assertId(id);
    if (!['create', 'patch', 'archive'].includes(action)) throw new SkillError('INVALID_ACTION');
    if (action !== 'create') {
      if (!baseSha || this.active.get(id) !== baseSha) throw new SkillError('BASE_MISMATCH');
    }
    const nextFiles = { ...(files ?? {}) };
    if (content) nextFiles['SKILL.md'] = String(content);
    if (!nextFiles['SKILL.md']) throw new SkillError('SKILL_BODY_REQUIRED');
    const proposalId = randomUUID();
    this.proposals.set(proposalId, {
      proposalId,
      action,
      id,
      proposer,
      baseSha: baseSha ?? null,
      files: nextFiles,
      candidateSha: hashFiles(nextFiles),
      status: 'proposed',
      createdAt: this.now(),
    });
    return { proposalId, status: 'proposed', candidateSha: this.proposals.get(proposalId).candidateSha };
  }

  stage(proposalId, { evidence = [], evalCases = [], stagedBy = 'curator' } = {}) {
    const proposal = this.getProposal(proposalId);
    if (proposal.status !== 'proposed') throw new SkillError('INVALID_PROPOSAL_STATE');
    if (proposal.action !== 'create' && this.active.get(proposal.id) !== proposal.baseSha) throw new SkillError('BASE_MISMATCH');
    if (!Array.isArray(evidence) || evidence.length === 0) throw new SkillError('EVIDENCE_REQUIRED');
    if (!Array.isArray(evalCases) || evalCases.length < this.evalFloor) throw new SkillError('EVAL_FLOOR_NOT_MET');
    proposal.status = 'staged';
    proposal.stagedBy = stagedBy;
    proposal.evidence = [...evidence];
    proposal.evalCases = [...evalCases];
    proposal.stagedAt = this.now();
    return { proposalId, status: 'staged', candidateSha: proposal.candidateSha };
  }

  promote({ proposalId, approvalId, expectedActiveSha, idempotencyKey, approvedBy = 'operator' } = {}) {
    if (idempotencyKey && this.idempotency.has(`promote:${idempotencyKey}`)) return this.idempotency.get(`promote:${idempotencyKey}`);
    const proposal = this.getProposal(proposalId);
    if (proposal.status !== 'staged') throw new SkillError('NOT_STAGED');
    if (!approvalId) throw new SkillError('APPROVAL_REQUIRED');
    if (approvedBy === proposal.proposer) throw new SkillError('SELF_APPROVAL_FORBIDDEN');
    if (proposal.action !== 'create' && expectedActiveSha !== proposal.baseSha) throw new SkillError('ACTIVE_SHA_MISMATCH');
    if (proposal.action !== 'create' && this.active.get(proposal.id) !== expectedActiveSha) throw new SkillError('ACTIVE_SHA_MISMATCH');
    this.packages.set(proposal.candidateSha, {
      id: proposal.id,
      version: '2.0.0',
      description: `promoted ${proposal.id}`,
      tags: [],
      files: proposal.files,
      packageSha: proposal.candidateSha,
    });
    this.setActive(proposal.id, proposal.candidateSha, 'promote');
    proposal.status = 'promoted';
    const result = { proposalId, packageSha: proposal.candidateSha, activeSha: proposal.candidateSha };
    if (idempotencyKey) this.idempotency.set(`promote:${idempotencyKey}`, result);
    return result;
  }

  rollback({ id, toSha, approvalId, idempotencyKey } = {}) {
    if (idempotencyKey && this.idempotency.has(`rollback:${idempotencyKey}`)) return this.idempotency.get(`rollback:${idempotencyKey}`);
    if (!approvalId) throw new SkillError('APPROVAL_REQUIRED');
    if (!toSha || !this.packages.has(toSha) || this.packages.get(toSha).id !== id) throw new SkillError('UNKNOWN_VERSION');
    const current = this.active.get(id);
    if (!current) throw new SkillError('SKILL_NOT_FOUND');
    this.setActive(id, toSha, 'rollback');
    const result = { id, previousSha: current, activeSha: toSha };
    if (idempotencyKey) this.idempotency.set(`rollback:${idempotencyKey}`, result);
    return result;
  }

  curator({ action = 'status', proposalId } = {}) {
    if (!['status', 'scan', 'report', 'stage'].includes(action)) throw new SkillError('FORBIDDEN');
    if (action === 'stage') return this.stage(proposalId);
    return { action, active: this.list(), proposals: [...this.proposals.values()].map(({ files, ...rest }) => rest) };
  }

  resolvePackage(id, sessionId) {
    if (sessionId) {
      const session = this.sessions.get(sessionId);
      if (!session || session.skillId !== id) throw new SkillError('SESSION_SKILL_MISMATCH');
      return session.packageSha;
    }
    const packageSha = this.active.get(id);
    if (!packageSha) throw new SkillError('SKILL_NOT_FOUND');
    return packageSha;
  }

  getProposal(id) {
    const proposal = this.proposals.get(id);
    if (!proposal) throw new SkillError('PROPOSAL_NOT_FOUND');
    return proposal;
  }

  setActive(id, packageSha, reason) {
    const previous = this.active.get(id) ?? null;
    this.active.set(id, packageSha);
    const list = this.history.get(id) ?? [];
    list.push(packageSha);
    this.history.set(id, list);
    this.ledger.push({ at: this.now(), id, packageSha, previous, reason });
  }
}

export function assertId(id) {
  if (!/^[a-z0-9][a-z0-9-]{1,63}$/.test(String(id ?? ''))) throw new SkillError('INVALID_SKILL_ID');
}
