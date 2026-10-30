import { createHmac, randomBytes, randomInt, timingSafeEqual } from 'node:crypto';

export const CONTRACT_VERSION = '1.0';
export const PRODUCT_PREFIX = 'IM2A';
export const USER_CODE_LENGTH = 25;
export const PUBLIC_UID_LENGTH = 35;
export const AUTH_CODE_LENGTH = 128;
export const IM2A_NUMBER_LENGTH = 12;
export const AGENT_SUBCODE_LENGTH = 10;
export const SECRET_LENGTH = 93;
export const AUTH_TTL_MS = 15 * 60 * 1000;

const DIGITS_12 = /^\d{12}$/;
const DIGITS_10 = /^\d{10}$/;
const PREFIX_RE = /^IM2A_(\d{8})/;
const SECRET_RE = /^[A-Za-z0-9_-]{93}$/;

export class CodeError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = 'CodeError';
    this.code = code;
  }
}

export function normalizeDate(value) {
  if (typeof value === 'string' && /^\d{8}$/.test(value)) {
    if (!isCalendarDate(value)) throw new CodeError('INVALID_DATE');
    return value;
  }
  if (typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value)) {
    const digits = value.replaceAll('-', '');
    if (!isCalendarDate(digits)) throw new CodeError('INVALID_DATE');
    return digits;
  }
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) throw new CodeError('INVALID_DATE');
  const digits = date.toISOString().slice(0, 10).replaceAll('-', '');
  if (!isCalendarDate(digits)) throw new CodeError('INVALID_DATE');
  return digits;
}

export function datePrefix(createdAt) {
  const date = normalizeDate(createdAt);
  if (!/^\d{8}$/.test(date)) throw new CodeError('INVALID_DATE');
  return `${PRODUCT_PREFIX}_${date}`;
}

export function buildUserCode({ createdAt, userId }) {
  assertDigits(userId, 12, 'INVALID_USER_ID');
  return `${datePrefix(createdAt)}${userId}`;
}

export function buildPublicUid({ createdAt, userId, agentSubcode }) {
  assertDigits(userId, 12, 'INVALID_USER_ID');
  assertDigits(agentSubcode, 10, 'INVALID_AGENT_SUBCODE');
  return `${datePrefix(createdAt)}${userId}${agentSubcode}`;
}

export function buildAgentAddress({ userId, agentSubcode }) {
  assertDigits(userId, 12, 'INVALID_USER_ID');
  assertDigits(agentSubcode, 10, 'INVALID_AGENT_SUBCODE');
  return `${userId}.${agentSubcode}`;
}

export function validateUserCode(value) {
  const text = String(value ?? '').trim();
  if (text.length !== USER_CODE_LENGTH) throw new CodeError('INVALID_USER_CODE');
  const match = PREFIX_RE.exec(text);
  if (!match || !isCalendarDate(match[1])) throw new CodeError('INVALID_USER_CODE');
  if (!DIGITS_12.test(text.slice(13))) throw new CodeError('INVALID_USER_CODE');
  return { kind: 'user', createdAt: match[1], userId: text.slice(13), code: text };
}

export function validatePublicUid(value) {
  const text = String(value ?? '').trim();
  if (text.length !== PUBLIC_UID_LENGTH) throw new CodeError('INVALID_PUBLIC_UID');
  const match = PREFIX_RE.exec(text);
  if (!match || !isCalendarDate(match[1])) throw new CodeError('INVALID_PUBLIC_UID');
  const body = text.slice(13);
  if (!DIGITS_12.test(body.slice(0, 12)) || !DIGITS_10.test(body.slice(12))) {
    throw new CodeError('INVALID_PUBLIC_UID');
  }
  return {
    kind: 'agent',
    createdAt: match[1],
    userId: body.slice(0, 12),
    agentSubcode: body.slice(12),
    code: text,
  };
}

export function validateNumber(value) {
  const text = String(value ?? '').replaceAll(' ', '').replaceAll('-', '');
  if (!DIGITS_12.test(text) || !luhnValid(text)) throw new CodeError('INVALID_NUMBER');
  return { kind: 'number', number: text };
}

export function validateAgentSubcode(value) {
  const text = String(value ?? '').trim();
  if (!DIGITS_10.test(text)) throw new CodeError('INVALID_AGENT_SUBCODE');
  return text;
}

export function parsePublicCode(value) {
  const text = String(value ?? '').trim();
  if (text.length === USER_CODE_LENGTH) return validateUserCode(text);
  if (text.length === PUBLIC_UID_LENGTH) return validatePublicUid(text);
  if (text.length === IM2A_NUMBER_LENGTH) return validateNumber(text);
  if (text.includes('.') && /^\d{12}\.\d{10}$/.test(text)) {
    const [userId, agentSubcode] = text.split('.');
    return { kind: 'agent-address', userId, agentSubcode };
  }
  throw new CodeError('INVALID_CODE');
}

export function generateNumberCandidate() {
  const payload = String(randomInt(0, 10 ** 11)).padStart(11, '0');
  return payload + luhnCheckDigit(payload);
}

export function buildAuthCode({ publicUid, secret }) {
  if (publicUid.length !== PUBLIC_UID_LENGTH) throw new CodeError('INVALID_PUBLIC_UID');
  if (!SECRET_RE.test(secret)) throw new CodeError('INVALID_SECRET');
  const code = `${publicUid}${secret}`;
  if (code.length !== AUTH_CODE_LENGTH) throw new CodeError('INVALID_AUTH_CODE');
  return code;
}

export function splitAuthCode(value) {
  const text = String(value ?? '');
  if (text.length !== AUTH_CODE_LENGTH) throw new CodeError('CREDENTIAL_INVALID');
  const publicUid = text.slice(0, PUBLIC_UID_LENGTH);
  const secret = text.slice(PUBLIC_UID_LENGTH);
  if (!SECRET_RE.test(secret)) throw new CodeError('CREDENTIAL_INVALID');
  validatePublicUid(publicUid);
  return { publicUid, secret };
}

export function fingerprintSecret(secret, pepper) {
  if (!pepper) throw new CodeError('MISSING_PEPPER');
  return createHmac('sha256', pepper).update(secret, 'utf8').digest('base64url');
}

export class CodeRegistry {
  constructor({ pepper, now = () => Date.now(), agentQuarantineMs = 90 * 24 * 60 * 60 * 1000 } = {}) {
    if (!pepper) throw new CodeError('MISSING_PEPPER');
    this.pepper = pepper;
    this.now = now;
    this.agentQuarantineMs = agentQuarantineMs;
    this.users = new Map();
    this.numbers = new Map();
    this.agents = new Map();
    this.releasedAgents = new Map();
    this.auth = new Map();
    this.authByPublic = new Map();
    this.agentSeq = new Map();
  }

  createUser({ createdAt = new Date(), displayName = '' } = {}) {
    let userId;
    do { userId = generateNumberCandidate(); } while (this.users.has(userId));
    const userCode = buildUserCode({ createdAt, userId });
    const record = { userId, userCode, createdAt: normalizeDate(createdAt), displayName: String(displayName) };
    this.users.set(userId, record);
    this.numbers.set(userId, record);
    return publicUser(record);
  }

  findUserByNumber(number) {
    for (const user of this.users.values()) {
      if (user.userId === String(number)) return publicUser(user);
    }
    return null;
  }

  issueAgentSubcode(userId) {
    if (!this.users.has(userId)) throw new CodeError('UNKNOWN_USER');
    const released = this.releasedAgents.get(userId) ?? [];
    const reusableIndex = released.findIndex((entry) => this.now() - entry.releasedAt >= this.agentQuarantineMs);
    let agentSubcode;
    if (reusableIndex >= 0) {
      agentSubcode = released.splice(reusableIndex, 1)[0].agentSubcode;
    } else {
      const seq = (this.agentSeq.get(userId) ?? 0) + 1;
      if (seq > 9_999_999_999) throw new CodeError('AGENT_SUBCODE_EXHAUSTED');
      this.agentSeq.set(userId, seq);
      agentSubcode = String(seq).padStart(10, '0');
    }
    const publicUid = buildPublicUid({ createdAt: this.users.get(userId).createdAt, userId, agentSubcode });
    const record = { userId, agentSubcode, publicUid, active: true };
    this.agents.set(publicUid, record);
    return { ...record };
  }

  releaseAgentSubcode(userId, agentSubcode) {
    const record = [...this.agents.values()].find((entry) => entry.userId === userId && entry.agentSubcode === agentSubcode && entry.active);
    if (!record) return false;
    record.active = false;
    const released = this.releasedAgents.get(userId) ?? [];
    released.push({ agentSubcode, releasedAt: this.now() });
    this.releasedAgents.set(userId, released);
    return true;
  }

  resolvePublicCode(value) {
    const text = String(value ?? '').trim();
    if (text.length === AUTH_CODE_LENGTH) return null;
    if (text.length === IM2A_NUMBER_LENGTH) {
      const user = this.findUserByNumber(text);
      return user ? { kind: 'user', number: user.userId, displayName: user.displayName } : null;
    }
    if (text.length === PUBLIC_UID_LENGTH) {
      const agent = this.agents.get(text);
      if (!agent || !agent.active) return null;
      const user = this.users.get(agent.userId);
      return user ? { kind: 'agent', number: user.userId, agentSubcode: agent.agentSubcode } : null;
    }
    if (text.length === USER_CODE_LENGTH) {
      for (const user of this.users.values()) if (user.userCode === text) return publicUser(user);
    }
    return null;
  }

  issueAuthCode({ userId, agentSubcode, scope = [], audience, ttlMs = AUTH_TTL_MS } = {}) {
    const user = this.users.get(userId);
    if (!user) throw new CodeError('UNKNOWN_USER');
    assertDigits(agentSubcode, 10, 'INVALID_AGENT_SUBCODE');
    if (!this.agents.has(buildPublicUid({ createdAt: user.createdAt, userId, agentSubcode }))) throw new CodeError('UNKNOWN_AGENT');
    if (!Array.isArray(scope) || scope.some((item) => typeof item !== 'string')) throw new CodeError('INVALID_SCOPE');
    if (!audience || typeof audience !== 'string') throw new CodeError('INVALID_AUDIENCE');
    if (ttlMs < 60_000 || ttlMs > 30 * 60 * 1000) throw new CodeError('INVALID_TTL');
    const publicUid = buildPublicUid({ createdAt: user.createdAt, userId, agentSubcode });
    let secret;
    let digest;
    do {
      secret = randomBytes(70).toString('base64url').slice(0, SECRET_LENGTH);
      digest = fingerprintSecret(secret, this.pepper);
    } while (this.auth.has(digest));
    const code = buildAuthCode({ publicUid, secret });
    const record = {
      digest,
      publicUid,
      userId,
      agentSubcode,
      scope: [...scope],
      audience,
      issuedAt: this.now(),
      expiresAt: this.now() + ttlMs,
      used: false,
      revoked: false,
    };
    this.auth.set(digest, record);
    this.authByPublic.set(publicUid, digest);
    return { code, credentialId: digest, expiresAt: record.expiresAt, scope: [...scope], audience };
  }

  verifyAuthCode(value) {
    let parts;
    try { parts = splitAuthCode(value); } catch { throw new CodeError('CREDENTIAL_INVALID'); }
    const digest = fingerprintSecret(parts.secret, this.pepper);
    const record = this.auth.get(digest);
    if (!record || record.used || record.revoked || record.expiresAt <= this.now()) {
      throw new CodeError('CREDENTIAL_INVALID');
    }
    if (record.publicUid !== parts.publicUid) throw new CodeError('CREDENTIAL_INVALID');
    record.used = true;
    return {
      ok: true,
      userId: record.userId,
      agentSubcode: record.agentSubcode,
      scope: [...record.scope],
      audience: record.audience,
      expiresAt: record.expiresAt,
      credentialId: record.credentialId ?? digest,
    };
  }

  revokeAuthCode(credentialId) {
    const record = this.auth.get(credentialId);
    if (!record) return false;
    record.revoked = true;
    return true;
  }
}

export function publicUser(record) {
  return { kind: 'user', userId: record.userId, userCode: record.userCode, displayName: record.displayName };
}

function assertDigits(value, length, code) {
  if (!new RegExp(`^\\d{${length}}$`).test(String(value ?? ''))) throw new CodeError(code);
}

function isCalendarDate(yyyymmdd) {
  const year = Number(yyyymmdd.slice(0, 4));
  const month = Number(yyyymmdd.slice(4, 6));
  const day = Number(yyyymmdd.slice(6, 8));
  const date = new Date(Date.UTC(year, month - 1, day));
  return date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day;
}

function luhnCheckDigit(payload) {
  let sum = 0;
  for (let i = payload.length - 1, alt = true; i >= 0; i--, alt = !alt) {
    let digit = Number(payload[i]);
    if (alt) {
      digit *= 2;
      if (digit > 9) digit -= 9;
    }
    sum += digit;
  }
  return String((10 - (sum % 10)) % 10);
}

function luhnValid(number) {
  return luhnCheckDigit(number.slice(0, -1)) === number.slice(-1);
}
