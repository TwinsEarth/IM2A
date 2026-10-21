// 测试：签名审计（契约 §16）。覆盖哈希链 + Ed25519 双校验、篡改定位、无 identity 拒绝、checkpoint 独立验签、恢复重放。
import test from 'node:test';
import assert from 'node:assert/strict';
import { createPublicKey, verify as cryptoVerify } from 'node:crypto';

import { AuditLog, GENESIS_HASH } from '../src/audit-log.mjs';
import { SIGNED_AUDIT_CODE, SignedAuditError, SignedAuditLog } from '../src/signed-audit.mjs';
import { createAgentIdentity } from '../../identity/src/agent-id.mjs';

function makeClock(startIso = '2026-10-21T00:00:00.000Z') {
  let currentMs = Date.parse(startIso);
  const clock = () => new Date(currentMs);
  clock.advance = (ms) => {
    currentMs += ms;
    return clock;
  };
  clock.iso = () => new Date(currentMs).toISOString();
  return clock;
}

function makeSignedLog(options = {}) {
  const clock = options.clock ?? makeClock();
  const identity = options.identity ?? createAgentIdentity({ clock });
  const audit = new SignedAuditLog({ clock, identity, ...(options.log ? { log: options.log } : {}) });
  return { clock, identity, audit };
}

const sampleEntry = (overrides = {}) => ({
  source: 'agent-gateway',
  action: 'route',
  actor: 'agent-a',
  target: 'skill.invoke',
  decision: 'ALLOW',
  detail: { skillId: 'read' },
  traceId: 'trace-1',
  ...overrides
});

const independentVerify = (publicKeyPem, data, signature) =>
  cryptoVerify(null, Buffer.from(data, 'utf8'), createPublicKey(publicKeyPem), Buffer.from(signature, 'base64'));

test('SIGNED_AUDIT_CODE 常量冻结且与契约一致', () => {
  assert.deepEqual({ ...SIGNED_AUDIT_CODE }, {
    SIGNER_REQUIRED: 'SIGNER_REQUIRED',
    CHAIN_BROKEN: 'CHAIN_BROKEN',
    SIGNATURE_INVALID: 'SIGNATURE_INVALID',
    UNKNOWN_SEQ: 'UNKNOWN_SEQ'
  });
  assert.ok(Object.isFrozen(SIGNED_AUDIT_CODE));
});

test('SIGNER_REQUIRED：缺 identity / identity 不完整时拒绝构造（不允许无签名模式）', () => {
  const identity = createAgentIdentity();
  for (const options of [
    {},
    { identity: null },
    { identity: {} },
    { identity: { agentId: identity.agentId } },
    { identity: { privateKeyPem: identity.privateKeyPem } },
    { identity: { publicKeyPem: identity.publicKeyPem } }
  ]) {
    assert.throws(() => new SignedAuditLog(options), { code: 'SIGNER_REQUIRED' });
  }
});

test('append：返回冻结 entry，含审计字段 + signer/signature，at 来自注入 clock', () => {
  const { audit, identity, clock } = makeSignedLog();
  const first = audit.append(sampleEntry());
  assert.ok(Object.isFrozen(first));
  assert.equal(first.seq, 1);
  assert.equal(first.at, '2026-10-21T00:00:00.000Z');
  assert.equal(first.source, 'agent-gateway');
  assert.equal(first.action, 'route');
  assert.equal(first.actor, 'agent-a');
  assert.equal(first.target, 'skill.invoke');
  assert.equal(first.decision, 'ALLOW');
  assert.equal(first.prevHash, GENESIS_HASH);
  assert.match(first.hash, /^[0-9a-f]{64}$/);
  assert.equal(first.signer, identity.agentId);
  assert.equal(typeof first.signature, 'string');
  assert.ok(first.signature.length > 0);

  clock.advance(5000);
  const second = audit.append(sampleEntry({ action: 'route2' }));
  assert.equal(second.seq, 2);
  assert.equal(second.at, '2026-10-21T00:00:05.000Z');
  assert.equal(second.prevHash, first.hash);
  assert.equal(audit.entries().length, 2);
  assert.equal(audit.head(), second.hash);
});

test('verify：链与签名都通过（ok=true，chain/signatures 子结构冻结）', () => {
  const { audit } = makeSignedLog();
  audit.append(sampleEntry());
  audit.append(sampleEntry({ decision: 'REQUIRE_APPROVAL' }));
  const result = audit.verify();
  assert.equal(result.ok, true);
  assert.equal(result.length, 2);
  assert.deepEqual({ ...result.chain }, { ok: true, brokenAt: null });
  assert.deepEqual({ ...result.signatures }, { ok: true, invalidAt: null });
  assert.equal(result.head, audit.head());
  assert.ok(Object.isFrozen(result));
  assert.ok(Object.isFrozen(result.chain));
  assert.ok(Object.isFrozen(result.signatures));
  assert.doesNotThrow(() => audit.assertVerified());
});

test('篡改任一条 entry 内容 => chain.ok=false 且 brokenAt 指向该 seq（签名仍对旧 hash 成立）', () => {
  const { audit, identity, clock } = makeSignedLog();
  audit.append(sampleEntry());
  audit.append(sampleEntry({ action: 'route2' }));
  audit.append(sampleEntry({ action: 'route3' }));

  const tamperedRecords = audit.entries().map((entry) => ({ ...entry }));
  tamperedRecords[1] = { ...tamperedRecords[1], decision: 'REJECTED', detail: { evil: true } };
  const restoredLog = new AuditLog({ clock, records: tamperedRecords });
  const tamperedAudit = new SignedAuditLog({ clock, identity, log: restoredLog });

  const result = tamperedAudit.verify();
  assert.equal(result.ok, false);
  assert.equal(result.chain.ok, false);
  assert.equal(result.chain.brokenAt, 2);
  // 签名是对原始 hash 的签名，hash 未变 => 签名本身仍有效；是链校验抓住了内容篡改
  assert.equal(result.signatures.ok, true);
  assert.throws(() => tamperedAudit.assertVerified(), { code: 'CHAIN_BROKEN' });
});

test('篡改 signature => signatures.ok=false 且 invalidAt 指向该 seq（链仍完整）', () => {
  const { audit, identity, clock } = makeSignedLog();
  audit.append(sampleEntry());
  audit.append(sampleEntry({ action: 'route2' }));

  const tamperedRecords = audit.entries().map((entry) => ({ ...entry }));
  const signatureBytes = Buffer.from(tamperedRecords[1].signature, 'base64');
  signatureBytes[0] ^= 0xff;
  tamperedRecords[1].signature = signatureBytes.toString('base64');
  const restoredLog = new AuditLog({ clock, records: tamperedRecords });
  const tamperedAudit = new SignedAuditLog({ clock, identity, log: restoredLog });

  const result = tamperedAudit.verify();
  assert.equal(result.ok, false);
  assert.equal(result.chain.ok, true);
  assert.equal(result.signatures.ok, false);
  assert.equal(result.signatures.invalidAt, 2);
  assert.throws(() => tamperedAudit.assertVerified(), { code: 'SIGNATURE_INVALID' });
});

test('缺失签名（未签名恢复）=> signatures.ok=false，且不能用别人的 identity 验', () => {
  const { audit, identity, clock } = makeSignedLog();
  audit.append(sampleEntry());
  const unsignedRecords = audit.entries().map(({ signer, signature, ...entry }) => entry);
  const unsignedAudit = new SignedAuditLog({
    clock,
    identity,
    log: new AuditLog({ clock, records: unsignedRecords })
  });
  const result = unsignedAudit.verify();
  assert.equal(result.chain.ok, true);
  assert.equal(result.signatures.ok, false);
  assert.equal(result.signatures.invalidAt, 1);

  const otherIdentity = createAgentIdentity({ clock });
  const wrongSignerAudit = new SignedAuditLog({
    clock,
    identity: otherIdentity,
    log: new AuditLog({ clock, records: audit.entries() })
  });
  assert.equal(wrongSignerAudit.verify().signatures.ok, false);
});

test('边界：空日志 verify 通过且 head=GENESIS；checkpoint 可被持公钥者独立验签', () => {
  const { audit, identity, clock } = makeSignedLog();
  const empty = audit.verify();
  assert.equal(empty.ok, true);
  assert.equal(empty.length, 0);
  assert.equal(empty.head, GENESIS_HASH);
  assert.equal(empty.chain.ok, true);
  assert.equal(empty.signatures.ok, true);

  const emptyCheckpoint = audit.checkpoint();
  assert.deepEqual(Object.keys(emptyCheckpoint).sort(), ['alg', 'at', 'hash', 'seq', 'signature']);
  assert.equal(emptyCheckpoint.seq, 0);
  assert.equal(emptyCheckpoint.hash, GENESIS_HASH);
  assert.equal(independentVerify(identity.publicKeyPem, emptyCheckpoint.hash, emptyCheckpoint.signature), true);

  audit.append(sampleEntry());
  const checkpoint = audit.checkpoint();
  assert.equal(checkpoint.seq, 1);
  assert.equal(checkpoint.hash, audit.head());
  assert.equal(checkpoint.alg, 'ed25519');
  assert.equal(checkpoint.at, clock.iso());
  assert.ok(Object.isFrozen(checkpoint));
  assert.equal(independentVerify(identity.publicKeyPem, checkpoint.hash, checkpoint.signature), true);

  const tampered = { ...checkpoint, hash: 'deadbeef' };
  assert.equal(independentVerify(identity.publicKeyPem, tampered.hash, tampered.signature), false);
});

test('entries({limit}) 只取最近 N 条且仍按 seq 正序；entry(seq) 可取单条，未知 seq => UNKNOWN_SEQ', () => {
  const { audit } = makeSignedLog();
  audit.append(sampleEntry());
  audit.append(sampleEntry());
  audit.append(sampleEntry());
  const latest = audit.entries({ limit: 2 });
  assert.deepEqual(latest.map((entry) => entry.seq), [2, 3]);
  assert.equal(audit.entries().length, 3);
  assert.equal(audit.entry(2).seq, 2);
  assert.throws(() => audit.entry(99), { code: 'UNKNOWN_SEQ' });
  assert.ok(Object.isFrozen(audit.entries()));
});

test('持久化恢复：把签名 entry 灌回 AuditLog 后可继续验签与追加（checkpoint 不依赖进程内状态）', () => {
  const clock = makeClock();
  const identity = createAgentIdentity({ clock });
  const firstLog = new SignedAuditLog({ clock, identity });
  firstLog.append(sampleEntry());
  firstLog.append(sampleEntry({ action: 'route2' }));

  const restoredLog = new AuditLog({ clock, records: firstLog.entries() });
  const restored = new SignedAuditLog({ clock, identity, log: restoredLog });
  assert.equal(restored.verify().ok, true);
  assert.equal(restored.entries().length, 2);
  assert.equal(restored.head(), firstLog.head());

  const third = restored.append(sampleEntry({ action: 'route3' }));
  assert.equal(third.seq, 3);
  assert.equal(restored.verify().ok, true);
  assert.equal(restored.entries().length, 3);
  assert.equal(independentVerify(identity.publicKeyPem, restored.checkpoint().hash, restored.checkpoint().signature), true);
});

test('SignedAuditError 带 code 且是 Error 子类', () => {
  try {
    new SignedAuditLog({});
    assert.fail('应当抛错');
  } catch (error) {
    assert.ok(error instanceof SignedAuditError);
    assert.ok(error instanceof Error);
    assert.equal(error.code, 'SIGNER_REQUIRED');
  }
});
