// 模块职责：上线前预检——把"能不能开始验收"变成机器判定：目标可达、鉴权真验、存储持久、外部依赖不是 dev、时钟不漂、Node 版本达标。
// 契约来源：contracts/INTERFACES.md §45。
// 关键不变量：①目标不可达/非 200 → ok:false；②管理面鉴权必须真验（无 token 401、错 token 401、对 token 200，任一不符 AUTH_FAILED）；
//             ③STORE=memory 且 expectDurable → STORE_NOT_DURABLE；④requireRealProviders 时短信主通道 dev → PROVIDER_NOT_CONFIGURED；
//             ⑤Date 响应头与本机差 >5min → CLOCK_SKEW（WARN）；⑥预检只读，绝不写目标状态。
export const PREFLIGHT_CODE = Object.freeze({
  TARGET_UNREACHABLE: 'TARGET_UNREACHABLE',
  AUTH_FAILED: 'AUTH_FAILED',
  NODE_TOO_OLD: 'NODE_TOO_OLD',
  STORE_NOT_DURABLE: 'STORE_NOT_DURABLE',
  PROVIDER_NOT_CONFIGURED: 'PROVIDER_NOT_CONFIGURED',
  CLOCK_SKEW: 'CLOCK_SKEW',
  TLS_INSECURE: 'TLS_INSECURE'
});

const CODE = PREFLIGHT_CODE;
const REQUEST_TIMEOUT_MS = 5000;
const CLOCK_SKEW_LIMIT_MS = 5 * 60e3;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';
const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '::1', '[::1]']);

export class PreflightError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'PreflightError';
    this.code = code;
  }
}

function normalizeTarget(target) {
  if (!isNonEmptyString(target)) throw new TypeError('runPreflight: target 必填');
  const normalized = target.trim().replace(/\/+$/, '');
  try {
    // 只校验合法性；不做任何请求
    new URL(normalized);
  } catch {
    throw new TypeError(`runPreflight: target 不是合法 URL: ${target}`);
  }
  return normalized;
}

function clockIso(clock) {
  const sampled = clock();
  const date = sampled instanceof Date ? sampled : new Date(sampled);
  return date.toISOString();
}

/** 只读 GET（带超时）；任何网络异常向上抛，由调用方转成 check 失败 */
async function request(fetchImpl, url, { headers = {} } = {}) {
  const controller = typeof AbortController === 'function' ? new AbortController() : null;
  const timer = controller ? setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS) : null;
  if (timer && typeof timer.unref === 'function') timer.unref();
  try {
    const response = await fetchImpl(url, {
      method: 'GET',
      headers: { accept: 'application/json', ...headers },
      ...(controller ? { signal: controller.signal } : {})
    });
    const text = await response.text();
    let json = null;
    try {
      json = JSON.parse(text);
    } catch {
      json = null;
    }
    return { status: response.status, headers: response.headers, text, json };
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/**
 * 执行预检（只读）。
 * @returns {Promise<Readonly<{ok:boolean, checks:ReadonlyArray<object>, target:string, node:object, generatedAt:string}>>}
 */
export async function runPreflight({
  target,
  token = null,
  fetchImpl = null,
  minNodeMajor = 22,
  expectDurable = true,
  requireRealProviders = false,
  clock = () => new Date()
} = {}) {
  const base = normalizeTarget(target);
  if (!Number.isInteger(minNodeMajor) || minNodeMajor < 1) throw new TypeError('minNodeMajor 必须是正整数');
  if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
  if (token !== null && token !== undefined && !isNonEmptyString(token)) {
    throw new TypeError('token 必须是非空字符串或 null');
  }
  const fetchFn = fetchImpl ?? globalThis.fetch;
  if (typeof fetchFn !== 'function') throw new TypeError('fetchImpl 必须是函数（或运行环境提供全局 fetch）');

  const checks = [];
  const add = (id, ok, severity, detail) => {
    checks.push(Object.freeze({ id, ok, severity, detail }));
  };

  // ① 目标可达 + /healthz OK
  let health = null;
  let healthError = null;
  try {
    health = await request(fetchFn, `${base}/healthz`);
  } catch (error) {
    healthError = error;
  }
  const healthOk =
    health !== null &&
    health.status === 200 &&
    (health.json?.status === 'OK' || String(health.text ?? '').trim().toUpperCase() === 'OK');
  add(
    CODE.TARGET_UNREACHABLE,
    healthOk,
    'BLOCKER',
    healthError
      ? `目标不可达：${String(healthError?.message ?? healthError)}`
      : healthOk
        ? 'GET /healthz -> 200 OK'
        : `GET /healthz -> ${health.status}，响应不符合健康约定`
  );

  // ② 鉴权三连：无 token 401 / 错 token 401 / 对 token 200
  let authOk = false;
  let authDetail = '';
  if (!isNonEmptyString(token)) {
    authDetail = '未提供 token：无法校验正确凭证（无 token 必须 401）';
  } else {
    try {
      const noToken = await request(fetchFn, `${base}/api/admin/state`);
      const wrongToken = await request(fetchFn, `${base}/api/admin/state`, {
        headers: { 'x-admin-token': 'wrong-token-for-preflight' }
      });
      const rightToken = await request(fetchFn, `${base}/api/admin/state`, {
        headers: { 'x-admin-token': token }
      });
      authOk = noToken.status === 401 && wrongToken.status === 401 && rightToken.status === 200;
      authDetail = `无token=${noToken.status} / 错token=${wrongToken.status} / 对token=${rightToken.status}（期望 401/401/200）`;
    } catch (error) {
      authDetail = `鉴权探测失败：${String(error?.message ?? error)}`;
    }
  }
  add(CODE.AUTH_FAILED, authOk, 'BLOCKER', authDetail);

  // ③ Node 版本
  const nodeMajor = Number(process.versions.node.split('.')[0]);
  add(
    CODE.NODE_TOO_OLD,
    nodeMajor >= minNodeMajor,
    'BLOCKER',
    `node ${process.versions.node}（要求 major >= ${minNodeMajor}）`
  );

  // ④ 存储持久性（只读读取管理面状态）
  let adminState = null;
  if (!expectDurable) {
    add(CODE.STORE_NOT_DURABLE, true, 'WARN', 'expectDurable=false：跳过持久化检查（内存实例仅用于本地验证）');
  } else {
    try {
      if (isNonEmptyString(token)) {
        const stateResponse = await request(fetchFn, `${base}/api/admin/state`, {
          headers: { 'x-admin-token': token }
        });
        if (stateResponse.status === 200) adminState = stateResponse.json;
      }
      const storeKind =
        adminState?.store && typeof adminState.store === 'object' ? adminState.store.kind : adminState?.store;
      const durable = isNonEmptyString(storeKind) && storeKind !== 'memory';
      add(
        CODE.STORE_NOT_DURABLE,
        durable,
        'BLOCKER',
        isNonEmptyString(storeKind)
          ? `store.kind=${storeKind}${durable ? '' : '（内测验收不允许内存存储）'}`
          : '无法读取 store.kind（管理面状态不可用或鉴权失败）'
      );
    } catch (error) {
      add(CODE.STORE_NOT_DURABLE, false, 'BLOCKER', `读取 store.kind 失败：${String(error?.message ?? error)}`);
    }
  }

  // ⑤ 外部依赖是否真实（短信主通道不是 dev）
  if (!requireRealProviders) {
    add(CODE.PROVIDER_NOT_CONFIGURED, true, 'WARN', 'requireRealProviders=false：跳过真实供应商检查');
  } else {
    try {
      const smsResponse = await request(fetchFn, `${base}/api/sms`, {
        headers: isNonEmptyString(token) ? { 'x-admin-token': token } : {}
      });
      const provider = smsResponse.json?.provider ?? null;
      const primaryKind = provider?.primary?.kind ?? provider?.kind ?? null;
      const real = smsResponse.status === 200 && isNonEmptyString(primaryKind) && primaryKind !== 'dev';
      add(
        CODE.PROVIDER_NOT_CONFIGURED,
        real,
        'BLOCKER',
        `短信主通道 kind=${String(primaryKind)}${real ? '' : '（验收要求非 dev 真实供应商）'}`
      );
    } catch (error) {
      add(
        CODE.PROVIDER_NOT_CONFIGURED,
        false,
        'BLOCKER',
        `读取供应商信息失败：${String(error?.message ?? error)}`
      );
    }
  }

  // ⑥ 时钟漂移（Date 响应头 vs 本机时钟；仅 WARN）
  const serverDate = health?.headers?.get?.('date') ?? null;
  if (!serverDate) {
    add(CODE.CLOCK_SKEW, true, 'WARN', '未获取到 Date 响应头，无法判定时钟漂移');
  } else {
    const serverMs = Date.parse(serverDate);
    const localMs = new Date(clock()).getTime();
    if (!Number.isFinite(serverMs) || !Number.isFinite(localMs)) {
      add(CODE.CLOCK_SKEW, true, 'WARN', `Date 头不可解析：${serverDate}`);
    } else {
      const skewMs = Math.abs(localMs - serverMs);
      add(
        CODE.CLOCK_SKEW,
        skewMs <= CLOCK_SKEW_LIMIT_MS,
        'WARN',
        `本机与目标时间差 ${skewMs}ms（阈值 ${CLOCK_SKEW_LIMIT_MS}ms）`
      );
    }
  }

  // ⑦ 传输安全（非回环地址的 http 仅 WARN，生产验收要求 HTTPS）
  const parsed = new URL(base);
  const loopback = LOOPBACK_HOSTS.has(parsed.hostname);
  const secure = parsed.protocol === 'https:' || loopback;
  add(
    CODE.TLS_INSECURE,
    secure,
    'WARN',
    parsed.protocol === 'https:'
      ? '目标为 HTTPS'
      : loopback
        ? '目标为本机回环地址（允许 http）'
        : '目标为 http 且非回环地址：生产验收要求 HTTPS'
  );

  const ok = checks.every((check) => check.ok || check.severity === 'WARN');
  return Object.freeze({
    ok,
    checks: Object.freeze(checks),
    target: base,
    node: Object.freeze({ major: nodeMajor, version: process.versions.node }),
    generatedAt: clockIso(clock)
  });
}
