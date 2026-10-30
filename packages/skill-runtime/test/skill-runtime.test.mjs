import test from 'node:test';
import assert from 'node:assert/strict';
import { SkillRuntime, SkillError } from '../src/index.mjs';

const files = { 'SKILL.md': '# Base\n\nUse the base workflow.', 'references/a.md': 'reference a' };

function runtime() {
  const r = new SkillRuntime();
  r.registerPackage({ id: 'demo', version: '1.0.0', description: 'demo', files });
  return r;
}

test('disclosure L0/L1/L2 boundaries', () => {
  const r = runtime();
  assert.equal(r.list()[0].body, undefined);
  const l1 = r.view('demo');
  assert.equal(l1.level, 'L1');
  assert.match(l1.body, /Base/);
  assert.deepEqual(l1.files, ['references/a.md']);
  const l2 = r.view('demo', { path: 'references/a.md' });
  assert.equal(l2.level, 'L2');
  assert.equal(l2.content, 'reference a');
});

test('package path boundary rejects traversal', () => {
  const r = runtime();
  assert.throws(() => r.view('demo', { path: '../secret' }), /PATH_NOT_ALLOWED/);
  assert.throws(() => r.registerPackage({ id: 'bad', files: { 'SKILL.md': 'x', '../escape': 'x' } }), (error) => error.code === 'INVALID_PATH');
});

test('skill_manage only creates proposals and checks base', () => {
  const r = runtime();
  const before = r.list()[0].packageSha;
  const p = r.manage({ action: 'patch', id: 'demo', baseSha: before, content: '# Patched' });
  assert.equal(p.status, 'proposed');
  assert.equal(r.list()[0].packageSha, before);
  assert.throws(() => r.manage({ action: 'patch', id: 'demo', baseSha: 'deadbeef', content: '# x' }), /BASE_MISMATCH/);
});

test('stage requires evidence and eval floor', () => {
  const r = runtime();
  const p = r.manage({ action: 'patch', id: 'demo', baseSha: r.list()[0].packageSha, content: '# v2' });
  assert.throws(() => r.stage(p.proposalId), /EVIDENCE_REQUIRED/);
  assert.throws(() => r.stage(p.proposalId, { evidence: ['e'], evalCases: ['x'] }), /EVAL_FLOOR_NOT_MET/);
  const staged = r.stage(p.proposalId, { evidence: ['e'], evalCases: Array.from({ length: 20 }, (_, i) => `case-${i}`) });
  assert.equal(staged.status, 'staged');
});

test('promotion gates and idempotency', () => {
  const r = runtime();
  const base = r.list()[0].packageSha;
  const p = r.manage({ action: 'patch', id: 'demo', baseSha: base, content: '# v2', proposer: 'agent' });
  r.stage(p.proposalId, { evidence: ['e'], evalCases: Array.from({ length: 20 }, (_, i) => `case-${i}`) });
  assert.throws(() => r.promote({ proposalId: p.proposalId, approvalId: 'a', approvedBy: 'agent' }), /SELF_APPROVAL_FORBIDDEN/);
  const first = r.promote({ proposalId: p.proposalId, approvalId: 'a', approvedBy: 'operator', expectedActiveSha: base, idempotencyKey: 'k' });
  const second = r.promote({ proposalId: p.proposalId, approvalId: 'a', approvedBy: 'operator', expectedActiveSha: base, idempotencyKey: 'k' });
  assert.deepEqual(second, first);
});

test('rollback moves a pointer to a known version', () => {
  const r = runtime();
  const v1 = r.list()[0].packageSha;
  const p = r.manage({ action: 'patch', id: 'demo', baseSha: v1, content: '# v2' });
  r.stage(p.proposalId, { evidence: ['e'], evalCases: Array.from({ length: 20 }, (_, i) => `case-${i}`) });
  const promoted = r.promote({ proposalId: p.proposalId, approvalId: 'a', expectedActiveSha: v1 });
  const back = r.rollback({ id: 'demo', toSha: v1, approvalId: 'a' });
  assert.equal(back.activeSha, v1);
  assert.notEqual(back.activeSha, promoted.packageSha);
  assert.throws(() => r.rollback({ id: 'demo', toSha: 'unknown', approvalId: 'a' }), /UNKNOWN_VERSION/);
});

test('curator cannot promote, rollback, or stage without a proposal', () => {
  const r = runtime();
  assert.throws(() => r.curator({ action: 'promote' }), /FORBIDDEN/);
  assert.throws(() => r.curator({ action: 'rollback' }), /FORBIDDEN/);
  assert.equal(r.curator({ action: 'status' }).active.length, 1);
});

test('session pin keeps old version and reports updateAvailable', () => {
  const r = runtime();
  const session = r.openSession('demo');
  const p = r.manage({ action: 'patch', id: 'demo', baseSha: session.packageSha, content: '# v2' });
  r.stage(p.proposalId, { evidence: ['e'], evalCases: Array.from({ length: 20 }, (_, i) => `case-${i}`) });
  r.promote({ proposalId: p.proposalId, approvalId: 'a', expectedActiveSha: session.packageSha });
  const pinned = r.view('demo', { sessionId: session.sessionId });
  assert.match(pinned.body, /Base/);
  assert.equal(pinned.updateAvailable, true);
  const fresh = r.view('demo');
  assert.match(fresh.body, /v2/);
});
