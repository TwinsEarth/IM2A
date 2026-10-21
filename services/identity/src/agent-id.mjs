// 模块职责：Agent 自持身份（Ed25519）与 AgentCard 签发/验签 —— v2 起 Agent 不再是一个内部字符串 id。
// 契约来源：contracts/INTERFACES.md §11。
// 关键不变量：①私钥只出现在 createAgentIdentity 的返回值里；②验签只返回判定 `{ok, code}`，绝不抛异常；
//             ③agentId 允许「平台名」与「自证 id」两种形态，自证性由 derivedId（公钥指纹重算值）单独表达；
//             ④canonicalize 与审计链 audit-log 的稳定序列化逐字一致（同一语义只有一种字节表示）。
import { createHash, createPrivateKey, createPublicKey, generateKeyPairSync, sign, verify } from 'node:crypto';

export const KEY_ALG = Object.freeze({ ED25519: 'ed25519' });

export const IDENTITY_CODE = Object.freeze({
  INVALID_KEY: 'INVALID_KEY',
  BAD_CARD: 'BAD_CARD',
  CARD_EXPIRED: 'CARD_EXPIRED',
  SIGNATURE_INVALID: 'SIGNATURE_INVALID',
  HASH_MISMATCH: 'HASH_MISMATCH'
});

const CODE = IDENTITY_CODE;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class IdentityError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'IdentityError';
    this.code = code;
  }
}

/**
 * 稳定序列化：键按字典序，undefined 归一 null，无空格。
 * 与 services/audit/src/audit-log.mjs 的 canonicalize 保持同一规则（契约 §11 要求「共用同一规则」）。
 */
export function canonicalize(value) {
  if (value === null || value === undefined) return 'null';
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : 'null';
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'string') return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(canonicalize).join(',')}]`;
  if (typeof value === 'object') {
    const keys = Object.keys(value).sort();
    return `{${keys.map((key) => `${JSON.stringify(key)}:${canonicalize(value[key])}`).join(',')}}`;
  }
  return 'null';
}

/** sha256 hex（与审计链同一算法） */
export function sha256Hex(text) {
  return createHash('sha256').update(text, 'utf8').digest('hex');
}

const toIso = (clock) => {
  const sampled = clock();
  const date = sampled instanceof Date ? sampled : new Date(sampled);
  return date.toISOString();
};

/** 解析 verify 的 now：Date / 毫秒数 / ISO 字符串；缺省走注入 clock（默认系统时钟） */
function resolveNow(now, clock) {
  if (now === null || now === undefined) {
    const sampled = clock();
    const date = sampled instanceof Date ? sampled : new Date(sampled);
    return date.getTime();
  }
  if (now instanceof Date) return now.getTime();
  if (typeof now === 'number') return now;
  const parsed = Date.parse(now);
  return Number.isFinite(parsed) ? parsed : Number.NaN;
}

function normalizeStringArray(value) {
  const unique = [];
  for (const item of Array.isArray(value) ? value : []) {
    if (isNonEmptyString(item) && !unique.includes(item)) unique.push(item);
  }
  return Object.freeze(unique);
}

function normalizeTrustCeiling(value) {
  if (!Number.isInteger(value)) return 4;
  return Math.min(4, Math.max(0, value));
}

function signatureBytes(signature) {
  return Buffer.from(signature, 'base64');
}

function verifySignature({ text, signature, publicKeyPem }) {
  try {
    return verify(null, Buffer.from(text, 'utf8'), createPublicKey(publicKeyPem), signatureBytes(signature));
  } catch {
    return false;
  }
}

const fail = (code, card = null) => Object.freeze({ ok: false, code, card });

/**
 * 生成/恢复 Agent 身份。
 * - 不传 agentId 或传派生值 => 自证 id（agentId === derivedId）；
 * - 传平台可读名（如 agent-lin-butler）=> agentId 为平台权威名，自证性由 derivedId 表达。
 * 可选参数 clock 用于 createdAt（契约签名前两个位置参数不变，§17.6 允许追加可选参数）。
 * @returns {Readonly<{agentId:string,derivedId:string,keyAlg:string,publicKeyPem:string,privateKeyPem:string,createdAt:string}>}
 */
export function createAgentIdentity({ agentId = null, privateKeyPem = null, clock = () => new Date() } = {}) {
  let privatePem;
  let publicPem;
  if (privateKeyPem !== null && privateKeyPem !== undefined) {
    try {
      const privateKey = createPrivateKey(privateKeyPem);
      privatePem = privateKey.export({ type: 'pkcs8', format: 'pem' });
      publicPem = createPublicKey(privateKey).export({ type: 'spki', format: 'pem' });
    } catch {
      throw new IdentityError('privateKeyPem 不是合法的 Ed25519 私钥 PEM', CODE.INVALID_KEY);
    }
  } else {
    const pair = generateKeyPairSync('ed25519', {
      publicKeyEncoding: { type: 'spki', format: 'pem' },
      privateKeyEncoding: { type: 'pkcs8', format: 'pem' }
    });
    privatePem = pair.privateKey;
    publicPem = pair.publicKey;
  }

  // 自证性永远可由公钥重算；平台名只是对外展示/注册表查找用的权威名
  const derivedId = agentIdFromPublicKey(publicPem);
  let resolvedAgentId = derivedId;
  if (agentId !== null && agentId !== undefined) {
    if (!isNonEmptyString(agentId)) {
      throw new IdentityError('显式 agentId（平台名）必须是非空字符串', CODE.INVALID_KEY);
    }
    resolvedAgentId = agentId;
  }

  return Object.freeze({
    agentId: resolvedAgentId,
    derivedId,
    keyAlg: KEY_ALG.ED25519,
    publicKeyPem: publicPem,
    privateKeyPem: privatePem,
    createdAt: toIso(clock)
  });
}

/** @returns {string} `agent-<22 位 base64url(sha256(规范化 spki PEM))>` */
export function agentIdFromPublicKey(publicKeyPem) {
  if (!isNonEmptyString(publicKeyPem)) {
    throw new IdentityError('publicKeyPem 必须是非空 PEM 字符串', CODE.INVALID_KEY);
  }
  let normalizedPem;
  try {
    normalizedPem = createPublicKey(publicKeyPem).export({ type: 'spki', format: 'pem' });
  } catch {
    throw new IdentityError('publicKeyPem 不是合法的 Ed25519 公钥 PEM', CODE.INVALID_KEY);
  }
  const digest = createHash('sha256').update(normalizedPem, 'utf8').digest('base64url').slice(0, 22);
  return `agent-${digest}`;
}

/** 创建未签名 AgentCard（issuedAt/expiresAt 来自注入 clock） */
export function createAgentCard({
  identity,
  ownerUserId,
  displayName = null,
  capabilities = [],
  domains = [],
  trustCeiling = 4,
  endpoint = null,
  ttlMs = 7 * 864e5,
  clock = () => new Date()
} = {}) {
  if (!identity || !isNonEmptyString(identity.publicKeyPem)) {
    throw new IdentityError('createAgentCard 需要合法 identity', CODE.INVALID_KEY);
  }
  if (!isNonEmptyString(ownerUserId)) {
    throw new IdentityError('AgentCard 必须有 ownerUserId', CODE.BAD_CARD);
  }
  if (!Number.isFinite(ttlMs) || ttlMs < 0) {
    throw new IdentityError('ttlMs 必须是非负毫秒数', CODE.BAD_CARD);
  }
  const issuedAt = toIso(clock);
  const expiresAt = new Date(Date.parse(issuedAt) + Math.trunc(ttlMs)).toISOString();
  return Object.freeze({
    agentId: identity.agentId,
    // 自证字段：平台名与跨平台验真共存（契约 §11：createAgentCard 必须写入 derivedId）
    derivedId: agentIdFromPublicKey(identity.publicKeyPem),
    ownerUserId,
    displayName: displayName ?? null,
    capabilities: normalizeStringArray(capabilities),
    domains: normalizeStringArray(domains),
    trustCeiling: normalizeTrustCeiling(trustCeiling),
    endpoint: endpoint ?? null,
    keyAlg: KEY_ALG.ED25519,
    publicKeyPem: identity.publicKeyPem,
    issuedAt,
    expiresAt
  });
}

/** 卡片内容哈希：sha256(canonical(card)) */
export function cardHash(card) {
  return sha256Hex(canonicalize(card));
}

/** 对卡片签名：签名对象是 cardHash 的 UTF-8 字节（与审计链「签名 entry.hash」同一约定） */
export function signAgentCard({ card, identity } = {}) {
  if (!card || typeof card !== 'object') {
    throw new IdentityError('signAgentCard 需要 card 对象', CODE.BAD_CARD);
  }
  if (!identity || !isNonEmptyString(identity.privateKeyPem)) {
    throw new IdentityError('signAgentCard 需要 privateKeyPem', CODE.INVALID_KEY);
  }
  const hash = cardHash(card);
  let signature;
  try {
    signature = sign(null, Buffer.from(hash, 'utf8'), identity.privateKeyPem).toString('base64');
  } catch {
    throw new IdentityError('Ed25519 签名失败（私钥非法）', CODE.INVALID_KEY);
  }
  return Object.freeze({
    card,
    cardHash: hash,
    alg: KEY_ALG.ED25519,
    signature,
    signerPublicKeyPem: identity.publicKeyPem
  });
}

/**
 * 验签（verification is a verdict，绝不抛异常）。
 * 依次：①结构 ②cardHash ③签名 ④未过期 ⑤若存在 card.derivedId 则必须等于公钥重算值。
 * @returns {Readonly<{ok:boolean, code:string|null, card:object|null}>}
 */
export function verifyAgentCard({ signedCard, now = null, clock = () => new Date() } = {}) {
  try {
    if (!signedCard || typeof signedCard !== 'object') return fail(CODE.BAD_CARD);
    const { card, cardHash: claimedHash, alg, signature, signerPublicKeyPem } = signedCard;
    if (alg !== KEY_ALG.ED25519) return fail(CODE.BAD_CARD);
    if (!card || typeof card !== 'object') return fail(CODE.BAD_CARD);
    if (!isNonEmptyString(claimedHash) || !isNonEmptyString(signature) || !isNonEmptyString(signerPublicKeyPem)) {
      return fail(CODE.BAD_CARD);
    }
    if (!isNonEmptyString(card.agentId) || !isNonEmptyString(card.ownerUserId)) return fail(CODE.BAD_CARD, card);
    const expiresMs = Date.parse(card.expiresAt);
    if (!Number.isFinite(expiresMs)) return fail(CODE.BAD_CARD, card);
    // 卡片携带的公钥必须与签名侧公钥一致（否则哈希覆盖的公钥无意义）
    if (isNonEmptyString(card.publicKeyPem) && card.publicKeyPem !== signerPublicKeyPem) {
      return fail(CODE.BAD_CARD, card);
    }

    // ② 内容哈希
    if (claimedHash !== cardHash(card)) return fail(CODE.HASH_MISMATCH, card);

    // ③ Ed25519 验签（签名对象 = cardHash 文本）
    if (!verifySignature({ text: claimedHash, signature, publicKeyPem: signerPublicKeyPem })) {
      return fail(CODE.SIGNATURE_INVALID, card);
    }

    // ④ 过期
    const nowMs = resolveNow(now, clock);
    if (Number.isFinite(nowMs) && expiresMs <= nowMs) return fail(CODE.CARD_EXPIRED, card);

    // ⑤ derivedId（存在才校验：平台名卡片的自证性字段；老卡片无此字段 => 向后兼容）
    if (card.derivedId !== undefined && card.derivedId !== null) {
      if (!isNonEmptyString(card.derivedId) || !isNonEmptyString(card.publicKeyPem)) return fail(CODE.BAD_CARD, card);
      let recomputedDerivedId;
      try {
        recomputedDerivedId = agentIdFromPublicKey(card.publicKeyPem);
      } catch {
        return fail(CODE.BAD_CARD, card);
      }
      if (recomputedDerivedId !== card.derivedId) return fail(CODE.BAD_CARD, card);
    }

    return Object.freeze({ ok: true, code: null, card });
  } catch {
    return fail(CODE.BAD_CARD);
  }
}

/** 对任意 payload 签名：签名对象 = canonical(payload) 的 UTF-8 字节 */
export function signPayload({ identity, payload = null } = {}) {
  if (!identity || !isNonEmptyString(identity.privateKeyPem)) {
    throw new IdentityError('signPayload 需要 privateKeyPem', CODE.INVALID_KEY);
  }
  const canonical = canonicalize(payload);
  let signature;
  try {
    signature = sign(null, Buffer.from(canonical, 'utf8'), identity.privateKeyPem).toString('base64');
  } catch {
    throw new IdentityError('Ed25519 签名失败（私钥非法）', CODE.INVALID_KEY);
  }
  return Object.freeze({ alg: KEY_ALG.ED25519, signature, payloadHash: sha256Hex(canonical) });
}

/** @returns {Readonly<{ok:boolean, code:string|null}>} 只返回判定，不抛异常 */
export function verifyPayload({ publicKeyPem, payload = null, signature } = {}) {
  if (!isNonEmptyString(publicKeyPem) || !isNonEmptyString(signature)) {
    return Object.freeze({ ok: false, code: CODE.BAD_CARD });
  }
  try {
    const publicKey = createPublicKey(publicKeyPem);
    const ok = verify(null, Buffer.from(canonicalize(payload), 'utf8'), publicKey, signatureBytes(signature));
    return Object.freeze({ ok, code: ok ? null : CODE.SIGNATURE_INVALID });
  } catch {
    return Object.freeze({ ok: false, code: CODE.INVALID_KEY });
  }
}
