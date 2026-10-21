// 模块职责：验收执行器——把"本机已验证"的 Gate 变成对真实目标可执行的检查，输出可归档报告；不能自动化的显式 MANUAL，不假装通过。
// 契约来源：contracts/INTERFACES.md §46。
// 关键不变量：①每个 Gate 超时/异常 → FAIL 并保留 detail，不得中断整轮；②only 过滤与结果顺序稳定；
//             ③ok 仅在无 FAIL 时为 true（SKIP/MANUAL 不影响）；④不在目标创建业务数据（备份 Gate 除外，且报告标注副作用）；
//             ⑤报告含开始/结束时间与 target，可归档。
import { basename } from 'node:path';

export const GATE_STATUS = Object.freeze({ PASS: 'PASS', FAIL: 'FAIL', SKIP: 'SKIP', MANUAL: 'MANUAL' });

const HTTP_TIMEOUT_MS = 5000;
const GATE_TIMEOUT_MS = 10_000;
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

const joinUrl = (target, prefix, path) => {
  const base = String(target ?? '').replace(/\/+$/, '');
  const cleanPrefix = isNonEmptyString(prefix) ? `/${String(prefix).replace(/^\/+|\/+$/g, '')}` : '';
  const cleanPath = path.startsWith('/') ? path : `/${path}`;
  return `${base}${cleanPrefix}${cleanPath}`;
};

const clockIso = (clock) => {
  const sampled = clock();
  const date = sampled instanceof Date ? sampled : new Date(sampled);
  return date.toISOString();
};

/** 统一 HTTP helper：返回 { status, headers, json, text }；网络异常向上抛，由 Gate 转 FAIL */
async function httpRequest(ctx, { method = 'GET', path, headers = {}, body = null } = {}) {
  const fetchFn = ctx.fetch;
  if (typeof fetchFn !== 'function') throw new TypeError('运行环境缺少 fetch');
  const controller = typeof AbortController === 'function' ? new AbortController() : null;
  const timer = controller ? setTimeout(() => controller.abort(), HTTP_TIMEOUT_MS) : null;
  if (timer && typeof timer.unref === 'function') timer.unref();
  try {
    const requestHeaders = { accept: 'application/json', ...headers };
    const init = { method, headers: requestHeaders, ...(controller ? { signal: controller.signal } : {}) };
    if (body !== null) {
      requestHeaders['content-type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    const response = await fetchFn(joinUrl(ctx.target, ctx.prefix, path), init);
    const text = await response.text();
    let json = null;
    try {
      json = JSON.parse(text);
    } catch {
      json = null;
    }
    return { status: response.status, headers: response.headers, json, text };
  } finally {
    if (timer) clearTimeout(timer);
  }
}

const adminHeaders = (ctx) => (isNonEmptyString(ctx.token) ? { 'x-admin-token': ctx.token } : {});

const requireToken = (ctx) => {
  if (!isNonEmptyString(ctx.token)) return { ok: false, detail: '未配置 admin token，无法执行该 Gate' };
  return null;
};

// ── 默认 Gate（契约 §46 冻结语义）───────────────────────────────────────────

export const DEFAULT_GATES = Object.freeze([
  Object.freeze({
    id: 'G-HEALTH',
    name: '健康检查',
    tier: 'CORE',
    async run(ctx) {
      const response = await httpRequest(ctx, { path: '/healthz' });
      const ok =
        response.status === 200 &&
        (response.json?.status === 'OK' || String(response.text ?? '').trim().toUpperCase() === 'OK');
      return { ok, detail: `GET /healthz -> ${response.status}${ok ? ' OK' : ' 不符合约定'}` };
    }
  }),
  Object.freeze({
    id: 'G-AUTH',
    name: '管理面鉴权',
    tier: 'CORE',
    async run(ctx) {
      const missing = requireToken(ctx);
      if (missing) return missing;
      const noToken = await httpRequest(ctx, { path: '/api/admin/state' });
      const withToken = await httpRequest(ctx, { path: '/api/admin/state', headers: adminHeaders(ctx) });
      const ok = noToken.status === 401 && withToken.status === 200;
      return { ok, detail: `无token=${noToken.status}（期望401） / 有token=${withToken.status}（期望200）` };
    }
  }),
  Object.freeze({
    id: 'G-ROLE',
    name: 'viewer 角色边界',
    tier: 'CORE',
    async run(ctx) {
      if (!isNonEmptyString(ctx.viewerToken)) {
        return { ok: true, skip: true, detail: 'viewerToken 未配置：SKIP 角色边界验证（显式跳过，不假装通过）' };
      }
      const read = await httpRequest(ctx, {
        path: '/api/admin/state',
        headers: { 'x-admin-token': ctx.viewerToken }
      });
      const write = await httpRequest(ctx, {
        method: 'POST',
        path: '/api/ops/backup',
        headers: { 'x-admin-token': ctx.viewerToken },
        body: {}
      });
      const ok = read.status === 200 && write.status === 403;
      return { ok, detail: `viewer 读=${read.status}（期望200） / viewer 写=${write.status}（期望403）` };
    }
  }),
  Object.freeze({
    id: 'G-AUDIT',
    name: '审计链完整性',
    tier: 'CORE',
    async run(ctx) {
      const missing = requireToken(ctx);
      if (missing) return missing;
      const response = await httpRequest(ctx, { path: '/api/audit', headers: adminHeaders(ctx) });
      const chainOk = response.status === 200 && response.json?.chain?.ok === true;
      return {
        ok: chainOk,
        detail: `GET /api/audit -> ${response.status}，chain.ok=${String(response.json?.chain?.ok)}，entries=${
          Array.isArray(response.json?.entries) ? response.json.entries.length : 'n/a'
        }`
      };
    }
  }),
  Object.freeze({
    id: 'G-STORE',
    name: '存储非内存（可持久化）',
    tier: 'CORE',
    async run(ctx) {
      const missing = requireToken(ctx);
      if (missing) return missing;
      const response = await httpRequest(ctx, { path: '/api/admin/state', headers: adminHeaders(ctx) });
      const kind =
        response.json?.store && typeof response.json.store === 'object'
          ? response.json.store.kind
          : response.json?.store;
      const ok = response.status === 200 && isNonEmptyString(kind) && kind !== 'memory';
      return { ok, detail: `store.kind=${String(kind)}（memory 不允许进入内测验收）` };
    }
  }),
  Object.freeze({
    id: 'G-METRICS',
    name: '指标漏斗数值有限',
    tier: 'OPS',
    async run(ctx) {
      const missing = requireToken(ctx);
      if (missing) return missing;
      const response = await httpRequest(ctx, { path: '/api/ops/metrics', headers: adminHeaders(ctx) });
      if (response.status !== 200 || !response.json?.funnel) {
        return { ok: false, detail: `GET /api/ops/metrics -> ${response.status}，缺少 funnel` };
      }
      const values = [];
      for (const group of [response.json.funnel.conversionRates, response.json.funnel.counts]) {
        if (!group || typeof group !== 'object') continue;
        for (const [key, value] of Object.entries(group)) values.push([`${key}=${value}`, value]);
      }
      const bad = values.filter(([, value]) => typeof value !== 'number' || !Number.isFinite(value));
      return {
        ok: bad.length === 0 && values.length > 0,
        detail:
          bad.length === 0
            ? `funnel 数值全部有限（${values.length} 项）`
            : `funnel 存在非法数值：${bad.map(([label]) => label).join(', ')}`
      };
    }
  }),
  Object.freeze({
    id: 'G-BACKUP',
    name: '备份创建→列出→验签恢复',
    tier: 'OPS',
    async run(ctx) {
      const missing = requireToken(ctx);
      if (missing) return missing;
      const created = await httpRequest(ctx, {
        method: 'POST',
        path: '/api/ops/backup',
        headers: adminHeaders(ctx),
        body: { label: 'acceptance-runner' }
      });
      const backup = created.json?.backup ?? null;
      if (created.status !== 200 || !isNonEmptyString(backup?.file)) {
        return { ok: false, detail: `创建备份失败：status=${created.status} ${String(created.json?.error ?? '')}` };
      }
      const list = await httpRequest(ctx, { path: '/api/ops/backup', headers: adminHeaders(ctx) });
      const backups = Array.isArray(list.json?.backups) ? list.json.backups : [];
      const listed = backups.some((item) => item.file === backup.file || item.name === basename(backup.file));
      if (!listed) {
        return { ok: false, detail: `备份列表未包含刚创建的备份 ${basename(backup.file)}` };
      }
      const restored = await httpRequest(ctx, {
        method: 'POST',
        path: '/api/ops/backup/restore',
        headers: adminHeaders(ctx),
        body: { file: backup.file }
      });
      const verified = restored.status === 200 && restored.json?.ok === true && restored.json?.applied === false;
      return {
        ok: verified,
        detail: `create/list/restore 完成；restore applied=${String(restored.json?.applied)}；⚠ 副作用：在目标创建备份 ${basename(
          backup.file
        )}（本 Gate 是唯一允许写目标状态的检查）`
      };
    }
  }),
  Object.freeze({
    id: 'G-PROVIDERS',
    name: '外部依赖非 dev',
    tier: 'OPS',
    async run(ctx) {
      const missing = requireToken(ctx);
      if (missing) return missing;
      const response = await httpRequest(ctx, { path: '/api/sms', headers: adminHeaders(ctx) });
      const provider = response.json?.provider ?? null;
      const primaryKind = provider?.primary?.kind ?? provider?.kind ?? null;
      if (response.status !== 200 || !isNonEmptyString(primaryKind)) {
        return { ok: false, detail: `无法读取短信通道信息：status=${response.status}` };
      }
      if (primaryKind === 'dev') {
        return {
          ok: true,
          skip: true,
          detail: '短信主通道为 dev（未配置真实供应商）：SKIP，等待真实供应商接入后再验收'
        };
      }
      return { ok: true, detail: `sms.primary.kind=${primaryKind}（非 dev）` };
    }
  })
]);

export class AcceptanceRunner {
  #target;
  #token;
  #viewerToken;
  #fetch;
  #clock;
  #prefix;
  #gates;
  #gateTimeoutMs;
  #includeManual = false;
  #lastGateMs = 0;

  constructor({
    target,
    token = null,
    viewerToken = null,
    fetchImpl = null,
    clock = () => new Date(),
    prefix = '',
    gateTimeoutMs = GATE_TIMEOUT_MS
  } = {}) {
    if (!isNonEmptyString(target)) throw new TypeError('AcceptanceRunner: target 必填');
    if (typeof clock !== 'function') throw new TypeError('AcceptanceRunner: clock 必须是函数');
    if (!Number.isFinite(gateTimeoutMs) || gateTimeoutMs <= 0) {
      throw new TypeError('AcceptanceRunner: gateTimeoutMs 必须是正毫秒数');
    }
    this.#target = String(target).replace(/\/+$/, '');
    this.#token = token;
    this.#viewerToken = viewerToken;
    this.#fetch = fetchImpl ?? globalThis.fetch;
    this.#clock = clock;
    this.#prefix = prefix;
    this.#gateTimeoutMs = Math.trunc(gateTimeoutMs);
    this.#gates = [...DEFAULT_GATES];
  }

  /** 注册自定义 Gate（同 id 覆盖；支持 tier='MANUAL' 的手动步骤） */
  register({ id, name, tier, run } = {}) {
    if (!isNonEmptyString(id) || !isNonEmptyString(name)) {
      throw new TypeError('register: id/name 必填');
    }
    if (typeof run !== 'function') throw new TypeError('register: run 必须是函数');
    const gate = Object.freeze({ id, name, tier: tier ?? 'CUSTOM', run });
    const index = this.#gates.findIndex((item) => item.id === id);
    if (index >= 0) this.#gates[index] = gate;
    else this.#gates.push(gate);
    return this;
  }

  #context() {
    return Object.freeze({
      target: this.#target,
      token: this.#token,
      viewerToken: this.#viewerToken,
      fetch: this.#fetch,
      prefix: this.#prefix,
      clock: this.#clock
    });
  }

  async #runGate(gate, ctx) {
    if (gate.tier === 'MANUAL' && this.#includeManual !== true) {
      this.#lastGateMs = 0;
      return { status: GATE_STATUS.MANUAL, detail: '手动步骤：未自动执行（需人工按文档验证）' };
    }
    const started = process.hrtime.bigint();
    let timer = null;
    try {
      const timeout = new Promise((_, reject) => {
        timer = setTimeout(() => {
          const error = new Error(`Gate 超时（>${this.#gateTimeoutMs}ms）`);
          error.code = 'GATE_TIMEOUT';
          reject(error);
        }, this.#gateTimeoutMs);
        if (typeof timer.unref === 'function') timer.unref();
      });
      const result = await Promise.race([Promise.resolve().then(() => gate.run(ctx)), timeout]);
      if (result && result.skip === true) {
        return { status: GATE_STATUS.SKIP, detail: result.detail ?? 'SKIP' };
      }
      if (result && result.ok === true) {
        return { status: GATE_STATUS.PASS, detail: result.detail ?? 'ok' };
      }
      return {
        status: GATE_STATUS.FAIL,
        detail: result?.detail ?? 'Gate 返回非 ok 结果（不得静默视为通过）'
      };
    } catch (error) {
      return {
        status: GATE_STATUS.FAIL,
        detail: `${error?.code ? `${error.code}: ` : ''}${String(error?.message ?? error)}`
      };
    } finally {
      if (timer) clearTimeout(timer);
      this.#lastGateMs = Number(process.hrtime.bigint() - started) / 1e6;
    }
  }

  /**
   * 执行验收（逐个 Gate；异常/超时 → FAIL 且不中断）。
   * @returns {Promise<Readonly<object>>} 可归档报告
   */
  async run({ only = null, includeManual = false } = {}) {
    const startedAt = clockIso(this.#clock);
    this.#includeManual = includeManual === true;
    const onlyIds =
      only === null || only === undefined
        ? null
        : new Set((Array.isArray(only) ? only : [only]).filter((id) => isNonEmptyString(id)));
    const gates = onlyIds === null ? [...this.#gates] : this.#gates.filter((gate) => onlyIds.has(gate.id));
    const ctx = this.#context();
    const results = [];
    for (const gate of gates) {
      this.#lastGateMs = 0;
      const outcome = await this.#runGate(gate, ctx);
      const ms = Math.max(0, Math.round(this.#lastGateMs));
      results.push(
        Object.freeze({
          id: gate.id,
          name: gate.name,
          tier: gate.tier,
          status: outcome.status,
          detail: outcome.detail ?? null,
          ms
        })
      );
    }
    const summary = {
      pass: results.filter((result) => result.status === GATE_STATUS.PASS).length,
      fail: results.filter((result) => result.status === GATE_STATUS.FAIL).length,
      skip: results.filter((result) => result.status === GATE_STATUS.SKIP).length,
      manual: results.filter((result) => result.status === GATE_STATUS.MANUAL).length
    };
    return Object.freeze({
      ok: summary.fail === 0,
      target: this.#target,
      startedAt,
      finishedAt: clockIso(this.#clock),
      results: Object.freeze(results),
      summary: Object.freeze(summary)
    });
  }
}
