import test from 'node:test';
import assert from 'node:assert/strict';
import {
  AUTH_CODE_LENGTH,
  AGENT_SUBCODE_LENGTH,
  CodeError,
  CodeRegistry,
  IM2A_NUMBER_LENGTH,
  PUBLIC_UID_LENGTH,
  USER_CODE_LENGTH,
  buildAgentAddress,
  buildAuthCode,
  buildPublicUid,
  buildUserCode,
  datePrefix,
  parsePublicCode,
  validateNumber,
  validateUserCode,
} from '../src/index.mjs';

const USER_ID = '123456789012';
const AGENT = '0000000001';
const DATE = '2026-10-30';

const registry = () => new CodeRegistry({ pepper: 'test-pepper' });

test('user code is exactly 25 characters and public', () => {
  const code = buildUserCode({ createdAt: DATE, userId: USER_ID });
  assert.equal(code.length, USER_CODE_LENGTH);
  assert.equal(code, 'IM2A_20261030123456789012');
  assert.equal(validateUserCode(code).userId, USER_ID);
  assert.equal(registry().resolvePublicCode(code), null);
});

test('public UID is exactly 35 characters', () => {
  const uid = buildPublicUid({ createdAt: DATE, userId: USER_ID, agentSubcode: AGENT });
  assert.equal(uid.length, PUBLIC_UID_LENGTH);
  assert.equal(uid, 'IM2A_202610301234567890120000000001');
});

test('auth code is exactly 128 characters = 35 public + 93 secret', () => {
  const secret = 'A'.repeat(93);
  const uid = buildPublicUid({ createdAt: DATE, userId: USER_ID, agentSubcode: AGENT });
  const code = buildAuthCode({ publicUid: uid, secret });
  assert.equal(code.length, AUTH_CODE_LENGTH);
  assert.equal(code.slice(0, PUBLIC_UID_LENGTH), uid);
  assert.equal(code.slice(PUBLIC_UID_LENGTH).length, 93);
});

test('invalid dates, lengths and types are rejected', () => {
  assert.throws(() => buildUserCode({ createdAt: '2026-02-30', userId: USER_ID }), /INVALID_DATE/);
  assert.throws(() => validateUserCode('IM2A_2026103012345678901'), CodeError);
  assert.throws(() => parsePublicCode('not-a-code'), CodeError);
  assert.throws(() => validateNumber('12345678901'), CodeError);
});

test('number has a Luhn check digit and parses as a number', () => {
  const user = registry().createUser({ createdAt: DATE });
  assert.equal(user.userId.length, IM2A_NUMBER_LENGTH);
  assert.equal(validateNumber(user.userId).kind, 'number');
  assert.throws(() => validateNumber(user.userId.slice(0, -1) + '0'), CodeError);
});

test('agent subcode is 10 digits and rendered as number.subcode', () => {
  const r = registry();
  const user = r.createUser({ createdAt: DATE });
  const agent = r.issueAgentSubcode(user.userId);
  assert.equal(agent.agentSubcode.length, AGENT_SUBCODE_LENGTH);
  assert.equal(buildAgentAddress({ userId: user.userId, agentSubcode: agent.agentSubcode }), `${user.userId}.${agent.agentSubcode}`);
  assert.equal(r.resolvePublicCode(agent.publicUid).kind, 'agent');
});

test('agent subcodes are owner-scoped and monotonically unique', () => {
  const r = registry();
  const a = r.createUser({ createdAt: DATE });
  const b = r.createUser({ createdAt: DATE });
  const a1 = r.issueAgentSubcode(a.userId);
  const a2 = r.issueAgentSubcode(a.userId);
  const b1 = r.issueAgentSubcode(b.userId);
  assert.notEqual(a1.agentSubcode, a2.agentSubcode);
  assert.notEqual(a1.publicUid, b1.publicUid);
  assert.equal(a1.agentSubcode, '0000000001');
  assert.equal(b1.agentSubcode, '0000000001');

  const reusable = new CodeRegistry({ pepper: 'test-pepper', agentQuarantineMs: 0 });
  const u = reusable.createUser({ createdAt: DATE });
  const first = reusable.issueAgentSubcode(u.userId);
  assert.equal(reusable.releaseAgentSubcode(u.userId, first.agentSubcode), true);
  const again = reusable.issueAgentSubcode(u.userId);
  assert.equal(again.agentSubcode, first.agentSubcode);
});

test('public resolver never treats an auth code as a public address', () => {
  const r = registry();
  const user = r.createUser({ createdAt: DATE });
  const agent = r.issueAgentSubcode(user.userId);
  const issued = r.issueAuthCode({ userId: user.userId, agentSubcode: agent.agentSubcode, scope: ['read:registry'], audience: 'workbuddy' });
  assert.equal(r.resolvePublicCode(issued.code), null);
});

test('auth code verifies once with scope and audience', () => {
  const r = registry();
  const user = r.createUser({ createdAt: DATE });
  const agent = r.issueAgentSubcode(user.userId);
  const issued = r.issueAuthCode({ userId: user.userId, agentSubcode: agent.agentSubcode, scope: ['read:registry'], audience: 'workbuddy' });
  const verified = r.verifyAuthCode(issued.code);
  assert.equal(verified.ok, true);
  assert.equal(verified.userId, user.userId);
  assert.deepEqual(verified.scope, ['read:registry']);
  assert.equal(verified.audience, 'workbuddy');
  assert.throws(() => r.verifyAuthCode(issued.code), /CREDENTIAL_INVALID/);
});

test('tampering the public part or secret is rejected', () => {
  const r = registry();
  const user = r.createUser({ createdAt: DATE });
  const agent = r.issueAgentSubcode(user.userId);
  const issued = r.issueAuthCode({ userId: user.userId, agentSubcode: agent.agentSubcode, scope: ['read'], audience: 'dsh' });
  const tamperedPublic = issued.code.slice(0, -1) + (issued.code.at(-1) === 'A' ? 'B' : 'A');
  assert.throws(() => r.verifyAuthCode(tamperedPublic), /CREDENTIAL_INVALID/);
  const tamperedSecret = issued.code.slice(0, 35) + 'B' + issued.code.slice(36);
  assert.throws(() => r.verifyAuthCode(tamperedSecret), /CREDENTIAL_INVALID/);
});

test('expired and revoked credentials fail', () => {
  let now = 1_000_000;
  const r = new CodeRegistry({ pepper: 'test-pepper', now: () => now });
  const user = r.createUser({ createdAt: DATE });
  const agent = r.issueAgentSubcode(user.userId);
  const issued = r.issueAuthCode({ userId: user.userId, agentSubcode: agent.agentSubcode, scope: ['read'], audience: 'dsh', ttlMs: 60_000 });
  now += 60_001;
  assert.throws(() => r.verifyAuthCode(issued.code), /CREDENTIAL_INVALID/);

  now = 2_000_000;
  const r2 = new CodeRegistry({ pepper: 'test-pepper', now: () => now });
  const u2 = r2.createUser({ createdAt: DATE });
  const a2 = r2.issueAgentSubcode(u2.userId);
  const i2 = r2.issueAuthCode({ userId: u2.userId, agentSubcode: a2.agentSubcode, scope: ['read'], audience: 'dsh' });
  assert.equal(r2.revokeAuthCode(i2.credentialId), true);
  assert.throws(() => r2.verifyAuthCode(i2.code), /CREDENTIAL_INVALID/);
});

test('public resolution returns no internal secret or credential data', () => {
  const r = registry();
  const user = r.createUser({ createdAt: DATE });
  const agent = r.issueAgentSubcode(user.userId);
  const projection = r.resolvePublicCode(user.userCode);
  assert.deepEqual(Object.keys(projection).sort(), ['displayName', 'kind', 'userCode', 'userId']);
  assert.equal(JSON.stringify(projection).includes('pepper'), false);
  const agentProjection = r.resolvePublicCode(agent.publicUid);
  assert.deepEqual(Object.keys(agentProjection).sort(), ['agentSubcode', 'kind', 'number']);
});

test('ttl and scope inputs are constrained', () => {
  const r = registry();
  const user = r.createUser({ createdAt: DATE });
  const agent = r.issueAgentSubcode(user.userId);
  assert.throws(() => r.issueAuthCode({ userId: user.userId, agentSubcode: agent.agentSubcode, scope: ['read'], audience: 'dsh', ttlMs: 1_000 }), /INVALID_TTL/);
  assert.throws(() => r.issueAuthCode({ userId: user.userId, agentSubcode: agent.agentSubcode, scope: 'read', audience: 'dsh' }), /INVALID_SCOPE/);
  assert.throws(() => r.issueAuthCode({ userId: user.userId, agentSubcode: agent.agentSubcode, scope: ['read'] }), /INVALID_AUDIENCE/);
});

test('date prefix is stable and ASCII', () => {
  assert.equal(datePrefix(DATE), 'IM2A_20261030');
  assert.match(datePrefix(DATE), /^[A-Z0-9_]+$/);
});
