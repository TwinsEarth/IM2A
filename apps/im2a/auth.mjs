/**
 * 模块职责：登录与会话鉴权 —— scrypt 口令校验 + HMAC 签名 Cookie + 按用户授权（不信任客户端传来的 userId）
 * 契约来源：docs/04-SDK与端侧设计.md、docs/08 运营合规；生产替换点为 OIDC/腾讯云账号体系，接口形状保持不变。
 *
 * 安全边界（如实）：
 *   · 默认不开启（IM_AUTH !== '1' 且未设 IM_SESSION_SECRET 时 auth.enabled=false），保证本机 Demo 开箱可跑；
 *   · 开启后：口令 scrypt+盐；会话 HMAC-SHA256 签名、HttpOnly、SameSite=Strict、可设 Secure；
 *   · userId 一律来自会话，**忽略** query/body 里的 userId，防越权冒充；
 *   · 未配置 IM_SESSION_SECRET 时随机生成进程级密钥（重启后会话失效，符合最少配置原则）；
 *   · 演示口令只用于本地 Demo，README 明确要求生产改用真实账号体系。
 */
import { createHmac, randomBytes, scryptSync, timingSafeEqual } from 'node:crypto';

export const DEFAULT_COOKIE = 'im_session';
const SESSION_TTL_MS = 12 * 3600e3;

/** 演示账号：口令来自环境变量；未设置时用仅限本机 Demo 的默认口令（README 已声明）。 */
export const DEMO_ACCOUNTS = Object.freeze([
  Object.freeze({ userId: 'user-lin', name: '林总', passwordEnv: 'IM_PASSWORD_LIN', demoPassword: 'demo-lin-2026' }),
  Object.freeze({ userId: 'user-zhou', name: '周先生', passwordEnv: 'IM_PASSWORD_ZHOU', demoPassword: 'demo-zhou-2026' }),
  Object.freeze({ userId: 'user-lin-mom', name: '林母', passwordEnv: 'IM_PASSWORD_MOM', demoPassword: 'demo-mom-2026' })
]);

function scryptHash(password, salt) {
  return scryptSync(String(password), `agentim:${salt}`, 32);
}

/** 从环境/参数构建账号表：口令只保存哈希与盐，原文不留在内存对象里。 */
export function buildUsers({ accounts = DEMO_ACCOUNTS, env = process.env } = {}) {
  const users = new Map();
  let raw = null;
  if (typeof env.IM_USERS === 'string' && env.IM_USERS.trim() !== '') {
    try {
      raw = JSON.parse(env.IM_USERS);
    } catch {
      throw new Error('IM_USERS 不是合法 JSON（格式：{"user-lin":{"name":"林总","password":"..."}}）');
    }
  }
  for (const account of accounts) {
    const fromEnv = raw?.[account.userId];
    const password = (fromEnv?.password ?? env[account.passwordEnv]) || account.demoPassword;
    const salt = randomBytes(8).toString('hex');
    users.set(account.userId, Object.freeze({
      userId: account.userId,
      name: fromEnv?.name ?? account.name,
      salt,
      passwordHash: scryptHash(password, salt),
      demoPassword: (fromEnv?.password ?? env[account.passwordEnv]) === undefined
    }));
  }
  return users;
}

function timingSafeEq(a, b) {
  if (typeof a !== 'string' || typeof b !== 'string' || a.length !== b.length) return false;
  const bufA = Buffer.from(a, 'hex');
  const bufB = Buffer.from(b, 'hex');
  if (bufA.length !== bufB.length) return false;
  return timingSafeEqual(bufA, bufB);
}

/** 解析 Cookie 头里的某个键（不引入 cookie 库）。 */
export function readCookie(header, name = DEFAULT_COOKIE) {
  if (typeof header !== 'string' || header === '') return null;
  for (const part of header.split(';')) {
    const idx = part.indexOf('=');
    if (idx < 0) continue;
    if (part.slice(0, idx).trim() === name) return decodeURIComponent(part.slice(idx + 1).trim());
  }
  return null;
}

/**
 * 创建鉴权器。
 * @param {object} options
 * @param {boolean} [options.enabled] 显式开关；缺省按 IM_AUTH==='1' 或 IM_SESSION_SECRET 存在判定
 * @param {Map<string,object>} [options.users] buildUsers 产物
 * @param {string} [options.secret] HMAC 密钥
 * @param {boolean} [options.secureCookies] 是否加 Secure（HTTPS 时开启）
 * @param {() => number} [options.now]
 */
export function createAuth({ enabled = undefined, users = undefined, secret = undefined, secureCookies = false, env = process.env, now = () => Date.now() } = {}) {
  const authEnabled = enabled === undefined ? env.IM_AUTH === '1' || typeof env.IM_SESSION_SECRET === 'string' : enabled;
  const userTable = users ?? buildUsers({ env });
  const hmacSecret = secret ?? env.IM_SESSION_SECRET ?? randomBytes(32).toString('hex');
  const cookieName = DEFAULT_COOKIE;

  function verifyPassword(userId, password) {
    const user = userTable.get(userId);
    if (!user) return false;
    return timingSafeEq(scryptHash(password, user.salt).toString('hex'), user.passwordHash.toString('hex'));
  }

  function issue(userId) {
    const user = userTable.get(userId);
    if (!user) return null;
    const expiresAt = now() + SESSION_TTL_MS;
    const payload = `${userId}.${expiresAt}`;
    const sig = createHmac('sha256', hmacSecret).update(payload).digest('base64url');
    return Object.freeze({ userId, name: user.name, token: `${payload}.${sig}`, expiresAt });
  }

  function parse(header) {
    const token = readCookie(header, cookieName);
    if (token === null) return { ok: false, reason: 'NO_SESSION' };
    const parts = token.split('.');
    if (parts.length !== 3) return { ok: false, reason: 'MALFORMED' };
    const [userId, expiresRaw, sig] = parts;
    const expiresAt = Number(expiresRaw);
    if (!Number.isFinite(expiresAt) || expiresAt <= now()) return { ok: false, reason: 'EXPIRED' };
    const expected = createHmac('sha256', hmacSecret).update(`${userId}.${expiresAt}`).digest('base64url');
    if (sig.length !== expected.length || !timingSafeEqual(Buffer.from(sig), Buffer.from(expected))) return { ok: false, reason: 'BAD_SIGNATURE' };
    const user = userTable.get(userId);
    if (!user) return { ok: false, reason: 'UNKNOWN_USER' };
    return { ok: true, user: Object.freeze({ userId: user.userId, name: user.name }), expiresAt };
  }

  function cookieFor(session) {
    const attrs = [`${cookieName}=${encodeURIComponent(session.token)}`, 'Path=/', 'HttpOnly', 'SameSite=Strict', `Max-Age=${Math.floor(SESSION_TTL_MS / 1000)}`];
    if (secureCookies) attrs.push('Secure');
    return attrs.join('; ');
  }

  function clearCookie() {
    return `${cookieName}=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0`;
  }

  function usersPublic() {
    return [...userTable.values()].map((user) => Object.freeze({ userId: user.userId, name: user.name }));
  }

  return Object.freeze({
    enabled: authEnabled,
    cookieName,
    secureCookies,
    verifyPassword,
    issue,
    parse,
    cookieFor,
    clearCookie,
    usersPublic,
    users: userTable
  });
}
