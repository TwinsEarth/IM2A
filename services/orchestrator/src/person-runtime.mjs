// 模块职责：Person-scoped Runtime（跨天/跨周持续存在的 Personal Agent 执行状态）：PERSONAL / TASK / WORKING 三层显式管理，可迁移且换实例可续跑。
// 契约来源：contracts/INTERFACES.md §33。
// 关键不变量：①WORKING 永不进入 snapshot（执行期状态不迁移）；②任何 secret:* key 写入 → SECRET_IN_LAYER（凭证归 Vault）；
//             ③任务终态不可回退；④snapshot 带 hash，restore 必须校验 version + hash，损坏即拒绝（不"尽力恢复"）。
import { canonicalize, sha256Hex } from '../../identity/src/agent-id.mjs';

export const STATE_LAYER = Object.freeze({ PERSONAL: 'PERSONAL', TASK: 'TASK', WORKING: 'WORKING' });

export const RUNTIME_TASK_STATE = Object.freeze({
  CREATED: 'CREATED',
  RUNNING: 'RUNNING',
  WAITING: 'WAITING',
  COMPLETED: 'COMPLETED',
  FAILED: 'FAILED',
  CANCELED: 'CANCELED'
});

export const RUNTIME_CODE = Object.freeze({
  NOT_FOUND: 'NOT_FOUND',
  ILLEGAL_STATE: 'ILLEGAL_STATE',
  LAYER_INVALID: 'LAYER_INVALID',
  SECRET_IN_LAYER: 'SECRET_IN_LAYER',
  BUNDLE_INVALID: 'BUNDLE_INVALID',
  VERSION_MISMATCH: 'VERSION_MISMATCH'
});

const CODE = RUNTIME_CODE;
const TASK_STATES = new Set(Object.values(RUNTIME_TASK_STATE));
const TERMINAL_TASK_STATES = new Set([
  RUNTIME_TASK_STATE.COMPLETED,
  RUNTIME_TASK_STATE.FAILED,
  RUNTIME_TASK_STATE.CANCELED
]);
/** 迁移白名单（契约 §33）：CREATED→RUNNING→WAITING→RUNNING→COMPLETED/FAILED；任意非终态→CANCELED */
const TRANSITIONS = Object.freeze({
  [RUNTIME_TASK_STATE.CREATED]: Object.freeze([RUNTIME_TASK_STATE.RUNNING, RUNTIME_TASK_STATE.CANCELED]),
  [RUNTIME_TASK_STATE.RUNNING]: Object.freeze([
    RUNTIME_TASK_STATE.WAITING,
    RUNTIME_TASK_STATE.COMPLETED,
    RUNTIME_TASK_STATE.FAILED,
    RUNTIME_TASK_STATE.CANCELED
  ]),
  [RUNTIME_TASK_STATE.WAITING]: Object.freeze([RUNTIME_TASK_STATE.RUNNING, RUNTIME_TASK_STATE.CANCELED]),
  [RUNTIME_TASK_STATE.COMPLETED]: Object.freeze([]),
  [RUNTIME_TASK_STATE.FAILED]: Object.freeze([]),
  [RUNTIME_TASK_STATE.CANCELED]: Object.freeze([])
});
const WAIT_KINDS = new Set(['external_event', 'approval', 'schedule']);
const DEFAULT_VERSION = 'im2a-runtime/1';
const isNonEmptyString = (value) => typeof value === 'string' && value.trim() !== '';

export class PersonRuntimeError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'PersonRuntimeError';
    this.code = code;
  }
}

const freezeArray = (value) => Object.freeze(Array.isArray(value) ? [...value] : []);

/** 递归扫描对象键：任何 `secret:` 前缀都不允许进入 Agent 上下文 */
function assertNoSecretKeys(value, path = 'value', depth = 0) {
  if (value === null || typeof value !== 'object' || depth > 12) return;
  if (Array.isArray(value)) {
    value.forEach((item, index) => assertNoSecretKeys(item, `${path}[${index}]`, depth + 1));
    return;
  }
  for (const key of Object.keys(value)) {
    if (typeof key === 'string' && key.startsWith('secret:')) {
      throw new PersonRuntimeError(`禁止把凭证写入 Agent 上下文: ${path}.${key}`, CODE.SECRET_IN_LAYER);
    }
    assertNoSecretKeys(value[key], `${path}.${key}`, depth + 1);
  }
}

const freezeEntry = (entry) => Object.freeze({ ...entry });

export class PersonRuntime {
  #userId;
  #clock;
  #version;
  /** @type {Map<string, object>} personal key -> frozen entry */
  #personal;
  /** @type {Map<string, object>} taskId -> frozen task */
  #tasks;
  /** WORKING 层：单次执行周期工作集，永不进 snapshot */
  #working;

  constructor({ userId, clock = () => new Date(), version = DEFAULT_VERSION } = {}) {
    if (!isNonEmptyString(userId)) throw new TypeError('PersonRuntime: userId 必填');
    if (!isNonEmptyString(version)) throw new TypeError('PersonRuntime: version 必填');
    this.#userId = userId;
    this.#clock = typeof clock === 'function' ? clock : () => new Date();
    this.#version = version;
    this.#personal = new Map();
    this.#tasks = new Map();
    this.#working = null;
  }

  #nowIso() {
    const sampled = this.#clock();
    return (sampled instanceof Date ? sampled : new Date(sampled)).toISOString();
  }

  #assertKey(key, field = 'key') {
    if (!isNonEmptyString(key)) throw new TypeError(`${field} 必须是非空字符串`);
    if (key.startsWith('secret:')) {
      throw new PersonRuntimeError(`禁止把凭证写入 Agent 上下文: ${key}`, CODE.SECRET_IN_LAYER);
    }
  }

  #requireTask(taskId) {
    const task = this.#tasks.get(taskId);
    if (!task) throw new PersonRuntimeError(`任务不存在: ${String(taskId)}`, CODE.NOT_FOUND);
    return task;
  }

  #buildTask({ taskId, title, state, checkpoint, waitingFor, createdAt, lastReason = null }) {
    const nowIso = this.#nowIso();
    return freezeEntry({
      layer: STATE_LAYER.TASK,
      taskId,
      title,
      state,
      checkpoint: Object.freeze({ ...checkpoint }),
      waitingFor: waitingFor === null ? null : Object.freeze({ ...waitingFor }),
      createdAt,
      updatedAt: nowIso,
      finishedAt: TERMINAL_TASK_STATES.has(state) ? nowIso : null,
      lastReason
    });
  }

  // ── PERSONAL 层（年）──────────────────────────────────────────────────────

  putPersonal({ key, value, evidence = [] } = {}) {
    this.#assertKey(key);
    assertNoSecretKeys(value, `${key}.value`);
    const entry = freezeEntry({
      layer: STATE_LAYER.PERSONAL,
      key,
      value,
      evidence: freezeArray(evidence),
      updatedAt: this.#nowIso()
    });
    this.#personal.set(key, entry);
    return entry;
  }

  personal({ key } = {}) {
    if (!isNonEmptyString(key)) return null;
    return this.#personal.get(key) ?? null;
  }

  // ── TASK 层（天/小时）────────────────────────────────────────────────────

  putTask({ taskId, title, state = RUNTIME_TASK_STATE.CREATED, checkpoint = {}, waitingFor = null } = {}) {
    if (!isNonEmptyString(taskId) || !isNonEmptyString(title)) {
      throw new TypeError('putTask: taskId/title 必填');
    }
    if (!TASK_STATES.has(state)) {
      throw new PersonRuntimeError(`非法任务状态: ${String(state)}`, CODE.ILLEGAL_STATE);
    }
    assertNoSecretKeys(checkpoint, `${taskId}.checkpoint`);
    assertNoSecretKeys(waitingFor, `${taskId}.waitingFor`);
    const existing = this.#tasks.get(taskId) ?? null;
    const task = this.#buildTask({
      taskId,
      title,
      state,
      checkpoint,
      waitingFor,
      createdAt: existing?.createdAt ?? this.#nowIso()
    });
    this.#tasks.set(taskId, task);
    return task;
  }

  task(taskId) {
    return this.#requireTask(taskId);
  }

  tasks({ state = null } = {}) {
    if (state !== null && !TASK_STATES.has(state)) {
      throw new PersonRuntimeError(`非法任务状态过滤: ${String(state)}`, CODE.ILLEGAL_STATE);
    }
    const all = [...this.#tasks.values()].filter((task) => state === null || task.state === state);
    all.sort((a, b) => {
      if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1;
      return a.taskId < b.taskId ? -1 : a.taskId > b.taskId ? 1 : 0;
    });
    return Object.freeze(all);
  }

  /** 白名单状态迁移；终态不可回退 */
  transition({ taskId, to, reason = null } = {}) {
    const task = this.#requireTask(taskId);
    if (!TASK_STATES.has(to)) {
      throw new PersonRuntimeError(`非法任务状态: ${String(to)}`, CODE.ILLEGAL_STATE);
    }
    const allowed = TRANSITIONS[task.state] ?? [];
    if (!allowed.includes(to)) {
      throw new PersonRuntimeError(`非法任务状态迁移 ${task.state} -> ${to}`, CODE.ILLEGAL_STATE);
    }
    const next = this.#buildTask({
      taskId: task.taskId,
      title: task.title,
      state: to,
      checkpoint: task.checkpoint,
      waitingFor: task.waitingFor,
      createdAt: task.createdAt,
      lastReason: reason
    });
    this.#tasks.set(taskId, next);
    return next;
  }

  /** 等待外部条件（RUNNING→WAITING；已在 WAITING 可更新条件） */
  wait({ taskId, condition } = {}) {
    const task = this.#requireTask(taskId);
    if (!condition || typeof condition !== 'object' || !WAIT_KINDS.has(condition.kind)) {
      throw new TypeError('wait: condition 必须是 { kind: external_event|approval|schedule, ref, until? }');
    }
    if (!isNonEmptyString(condition.ref)) throw new TypeError('wait: condition.ref 必填');
    assertNoSecretKeys(condition, `${taskId}.condition`);
    if (task.state === RUNTIME_TASK_STATE.RUNNING) {
      const next = this.#buildTask({
        taskId: task.taskId,
        title: task.title,
        state: RUNTIME_TASK_STATE.WAITING,
        checkpoint: task.checkpoint,
        waitingFor: condition,
        createdAt: task.createdAt,
        lastReason: 'WAIT'
      });
      this.#tasks.set(taskId, next);
      return next;
    }
    if (task.state === RUNTIME_TASK_STATE.WAITING) {
      const next = freezeEntry({ ...task, waitingFor: Object.freeze({ ...condition }), updatedAt: this.#nowIso() });
      this.#tasks.set(taskId, next);
      return next;
    }
    throw new PersonRuntimeError(`当前状态不可等待: ${task.state}`, CODE.ILLEGAL_STATE);
  }

  /** 等待条件满足后续跑（WAITING→RUNNING） */
  resume({ taskId, reason = null } = {}) {
    const task = this.#requireTask(taskId);
    if (task.state !== RUNTIME_TASK_STATE.WAITING) {
      throw new PersonRuntimeError(`只有 WAITING 可 resume，当前 ${task.state}`, CODE.ILLEGAL_STATE);
    }
    const next = this.#buildTask({
      taskId: task.taskId,
      title: task.title,
      state: RUNTIME_TASK_STATE.RUNNING,
      checkpoint: task.checkpoint,
      waitingFor: null,
      createdAt: task.createdAt,
      lastReason: reason
    });
    this.#tasks.set(taskId, next);
    return next;
  }

  // ── WORKING 层（单次执行周期，绝不迁移）──────────────────────────────────

  setWorking({ turnId, context } = {}) {
    if (!isNonEmptyString(turnId)) throw new TypeError('setWorking: turnId 必填');
    if (!context || typeof context !== 'object') throw new TypeError('setWorking: context 必须是对象');
    assertNoSecretKeys(context, `${turnId}.context`);
    this.#working = Object.freeze({
      layer: STATE_LAYER.WORKING,
      turnId,
      context: Object.freeze({ ...context }),
      setAt: this.#nowIso()
    });
    return this.#working;
  }

  /** 单次执行周期结束立即失效 */
  clearWorking() {
    const existed = this.#working !== null;
    this.#working = null;
    return existed;
  }

  /** 附加方法：读取当前 WORKING 工作集（不进 snapshot；clearWorking 后为 null） */
  working() {
    return this.#working;
  }

  // ── 迁移（snapshot / restore）────────────────────────────────────────────

  #bundlePayload(personal, tasks, exportedAt) {
    return {
      version: this.#version,
      userId: this.#userId,
      personal,
      tasks,
      exportedAt
    };
  }

  /** 只含 PERSONAL + TASK（WORKING 绝不进包），带 hash */
  snapshot() {
    const personal = [...this.#personal.values()].sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
    const tasks = this.tasks(); // 已排序且冻结
    const exportedAt = this.#nowIso();
    const payload = this.#bundlePayload(personal, tasks, exportedAt);
    return Object.freeze({
      ...payload,
      personal: Object.freeze(personal),
      tasks,
      hash: sha256Hex(canonicalize(payload))
    });
  }

  /** 校验 version + hash 后整体恢复；损坏即拒绝（不"尽力恢复"） */
  restore(bundle) {
    if (!bundle || typeof bundle !== 'object') {
      throw new PersonRuntimeError('restore: bundle 必须是对象', CODE.BUNDLE_INVALID);
    }
    if (bundle.version !== this.#version) {
      throw new PersonRuntimeError(
        `迁移包版本不匹配: ${String(bundle.version)}（期望 ${this.#version}）`,
        CODE.VERSION_MISMATCH
      );
    }
    if (bundle.userId !== this.#userId) {
      throw new PersonRuntimeError('迁移包 userId 与运行时不一致', CODE.BUNDLE_INVALID);
    }
    if (!Array.isArray(bundle.personal) || !Array.isArray(bundle.tasks) || !isNonEmptyString(bundle.hash)) {
      throw new PersonRuntimeError('迁移包结构非法', CODE.BUNDLE_INVALID);
    }
    const payload = this.#bundlePayload(bundle.personal, bundle.tasks, bundle.exportedAt);
    if (sha256Hex(canonicalize(payload)) !== bundle.hash) {
      throw new PersonRuntimeError('迁移包 hash 校验失败（内容被篡改）', CODE.BUNDLE_INVALID);
    }

    // 结构 / 分层校验：WORKING 或未知层不得借迁移包进入上下文
    const restoredPersonal = [];
    for (const entry of bundle.personal) {
      if (!entry || typeof entry !== 'object' || !isNonEmptyString(entry.key)) {
        throw new PersonRuntimeError('迁移包 personal 条目结构非法', CODE.BUNDLE_INVALID);
      }
      if (entry.layer !== STATE_LAYER.PERSONAL) {
        throw new PersonRuntimeError(`迁移包中 PERSONAL 层条目分层非法: ${String(entry.layer)}`, CODE.LAYER_INVALID);
      }
      if (entry.key.startsWith('secret:')) {
        throw new PersonRuntimeError(`迁移包含凭证 key: ${entry.key}`, CODE.SECRET_IN_LAYER);
      }
      restoredPersonal.push(
        freezeEntry({
          layer: STATE_LAYER.PERSONAL,
          key: entry.key,
          value: entry.value,
          evidence: freezeArray(entry.evidence),
          updatedAt: entry.updatedAt ?? null
        })
      );
    }
    const restoredTasks = [];
    for (const task of bundle.tasks) {
      if (
        !task ||
        typeof task !== 'object' ||
        !isNonEmptyString(task.taskId) ||
        !isNonEmptyString(task.title) ||
        !TASK_STATES.has(task.state)
      ) {
        throw new PersonRuntimeError('迁移包 tasks 条目结构非法', CODE.BUNDLE_INVALID);
      }
      if (task.layer !== STATE_LAYER.TASK) {
        throw new PersonRuntimeError(`迁移包中 TASK 层条目分层非法: ${String(task.layer)}`, CODE.LAYER_INVALID);
      }
      restoredTasks.push(
        freezeEntry({
          layer: STATE_LAYER.TASK,
          taskId: task.taskId,
          title: task.title,
          state: task.state,
          checkpoint: Object.freeze({ ...(task.checkpoint ?? {}) }),
          waitingFor: task.waitingFor == null ? null : Object.freeze({ ...task.waitingFor }),
          createdAt: task.createdAt ?? null,
          updatedAt: task.updatedAt ?? null,
          finishedAt: task.finishedAt ?? null,
          lastReason: task.lastReason ?? null
        })
      );
    }

    this.#personal = new Map(restoredPersonal.map((entry) => [entry.key, entry]));
    this.#tasks = new Map(restoredTasks.map((task) => [task.taskId, task]));
    this.#working = null; // 执行期状态不迁移
    return Object.freeze({
      personal: Object.freeze(restoredPersonal),
      tasks: Object.freeze(restoredTasks)
    });
  }

  /** Context 飞轮最小指标 */
  metrics() {
    let tasksCompleted = 0;
    let tasksWaiting = 0;
    for (const task of this.#tasks.values()) {
      if (task.state === RUNTIME_TASK_STATE.COMPLETED) tasksCompleted += 1;
      if (task.state === RUNTIME_TASK_STATE.WAITING) tasksWaiting += 1;
    }
    return Object.freeze({
      personalKeys: this.#personal.size,
      tasksTotal: this.#tasks.size,
      tasksCompleted,
      tasksWaiting
    });
  }
}
