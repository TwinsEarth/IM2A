/**
 * 模块职责：TaskLedger —— 只追加任务事件账本 + 状态机 + 幂等键 + 租约（v0.2 核心）
 * 契约来源：docs/ARCHITECTURE.md §3（TaskLedger 行）/ §6（v0.2 重写执行方案）
 *
 * 设计（借鉴 w2m 任务账本的工程经验，独立实现）：
 *   ① 只追加：状态变化 = 一条事件；任务快照永远由事件流推导（`completed` 不等于改字段）
 *   ② 幂等：create 的 idempotencyKey 唯一；complete 对已成功任务返回 duplicate，不产生第二条事件
 *   ③ 租约：worker 领取任务获得有限租约；过期未心跳 → sweepExpired 回收重排（attempt+1）或终态失败
 *   ④ 防串写：完成/失败必须来自当前 attempt 的持有者；租约过期后旧 worker 的完成被 TOKEN_STALE 拒绝
 *   ⑤ 校验：非法参数与未知任务都抛具名错误，绝不静默修正
 *
 * 边界（v0.2 第一刀）：内存账本；持久化到 SQLite / 多进程消费在后续切片（ARCHITECTURE §6 顺序）。
 */
import { randomUUID } from 'node:crypto';

export const TASK_STATE = Object.freeze({
  QUEUED: 'QUEUED',
  RUNNING: 'RUNNING',
  SUCCEEDED: 'SUCCEEDED',
  FAILED: 'FAILED',
  CANCELLED: 'CANCELLED'
});

export const TASK_EVENT = Object.freeze({
  CREATED: 'task.created',
  LEASED: 'task.leased',
  HEARTBEAT: 'task.heartbeat',
  COMPLETED: 'task.completed',
  FAILED: 'task.failed',
  REQUEUED: 'task.requeued',
  CANCELLED: 'task.cancelled',
  LEASE_EXPIRED: 'lease.expired',
  DEADLINE_EXCEEDED: 'task.deadline_exceeded'
});

const TERMINAL = Object.freeze([TASK_STATE.SUCCEEDED, TASK_STATE.FAILED, TASK_STATE.CANCELLED]);
const DEFAULT_LEASE_MS = 30_000;
const DEFAULT_MAX_ATTEMPTS = 3;

function fail(code, message, detail = null) {
  const error = new Error(code + (message ? ': ' + message : ''));
  error.code = code;
  if (detail !== null) error.detail = detail;
  return error;
}

function freezeTask(task) {
  return Object.freeze({
    id: task.id,
    kind: task.kind,
    payload: task.payload,
    idempotencyKey: task.idempotencyKey,
    state: task.state,
    attempt: task.attempt,
    maxAttempts: task.maxAttempts,
    leaseMs: task.leaseMs,
    createdAt: task.createdAt,
    deadlineAt: task.deadlineAt,
    lease: task.lease === null ? null : Object.freeze({ ...task.lease }),
    result: task.result,
    error: task.error,
    updatedAt: task.updatedAt,
    cancelledReason: task.cancelledReason ?? null
  });
}

export class TaskLedger {
  #clock;
  #events = [];
  #tasks = new Map();
  #byKey = new Map();
  #leaseSeq = 0;

  constructor({ clock = () => Date.now(), leaseMs = DEFAULT_LEASE_MS } = {}) {
    if (typeof clock !== 'function') throw new TypeError('clock 必须是函数');
    if (!Number.isInteger(leaseMs) || leaseMs <= 0) throw fail('INVALID_LEASE', 'leaseMs 必须是正整数');
    this.#clock = clock;
    this.defaultLeaseMs = leaseMs;
  }

  #now() {
    return Number(this.#clock());
  }

  #emit(type, task, extra = {}) {
    const event = Object.freeze({
      seq: this.#events.length + 1,
      at: this.#now(),
      type,
      taskId: task.id,
      payload: Object.freeze({ ...extra })
    });
    this.#events.push(event);
    return event;
  }

  /** 创建任务；同一 idempotencyKey 命中时返回既有任务且不追加事件。 */
  create({ kind, payload = {}, idempotencyKey = null, leaseMs = null, maxAttempts = null, deadlineMs = null } = {}) {
    if (typeof kind !== 'string' || kind.trim() === '') throw fail('INVALID_KIND', 'kind 不能为空');
    if (leaseMs !== null && (!Number.isInteger(leaseMs) || leaseMs <= 0)) throw fail('INVALID_LEASE', 'leaseMs 必须是正整数');
    if (maxAttempts !== null && (!Number.isInteger(maxAttempts) || maxAttempts < 1)) throw fail('INVALID_MAX_ATTEMPTS', 'maxAttempts 必须是正整数');
    if (deadlineMs !== null && (!Number.isInteger(deadlineMs) || deadlineMs < 0)) throw fail('INVALID_DEADLINE', 'deadlineMs 必须是非负整数');
    if (payload !== null && typeof payload !== 'object') throw fail('INVALID_PAYLOAD', 'payload 必须是对象');
    if (idempotencyKey !== null) {
      const existing = this.#byKey.get(idempotencyKey);
      if (existing !== undefined) return { duplicate: true, task: freezeTask(this.#tasks.get(existing)) };
    }
    const now = this.#now();
    const task = {
      id: 'task-' + randomUUID(),
      kind: kind.trim(),
      payload: Object.freeze({ ...payload }),
      idempotencyKey,
      state: TASK_STATE.QUEUED,
      attempt: 0,
      maxAttempts: maxAttempts ?? DEFAULT_MAX_ATTEMPTS,
      leaseMs: leaseMs ?? this.defaultLeaseMs,
      createdAt: now,
      deadlineAt: deadlineMs === null ? null : now + deadlineMs,
      lease: null,
      result: null,
      error: null,
      updatedAt: now,
      cancelledReason: null
    };
    this.#tasks.set(task.id, task);
    if (idempotencyKey !== null) this.#byKey.set(idempotencyKey, task.id);
    this.#emit(TASK_EVENT.CREATED, task, { kind: task.kind, idempotencyKey });
    return { duplicate: false, task: freezeTask(task) };
  }

  /** worker 领取下一条可执行任务（FIFO）；没有则返回 null。已过 deadline 的任务直接判失败。 */
  lease({ workerId, leaseMs = null } = {}) {
    if (typeof workerId !== 'string' || workerId.trim() === '') throw fail('INVALID_WORKER', 'workerId 不能为空');
    const now = this.#now();
    for (const task of this.#tasks.values()) {
      if (task.state === TASK_STATE.QUEUED) {
        if (task.deadlineAt !== null && now >= task.deadlineAt) {
          this.#finish(task, TASK_STATE.FAILED, { error: 'DEADLINE_EXCEEDED' }, TASK_EVENT.DEADLINE_EXCEEDED, { now, attempt: task.attempt });
          continue;
        }
        this.#leaseSeq += 1;
        task.state = TASK_STATE.RUNNING;
        task.attempt += 1;
        task.updatedAt = now;
        task.lease = { workerId, leaseId: 'lease-' + this.#leaseSeq, attempt: task.attempt, startedAt: now, expiresAt: now + (leaseMs ?? task.leaseMs) };
        this.#emit(TASK_EVENT.LEASED, task, { workerId, attempt: task.attempt, expiresAt: task.lease.expiresAt });
        return { task: freezeTask(task) };
      }
      if (task.state === TASK_STATE.RUNNING && task.lease !== null && now > task.lease.expiresAt) {
        this.#reap(task);
      }
    }
    return null;
  }

  heartbeat({ taskId, workerId, extendMs = null } = {}) {
    const task = this.#require(taskId);
    if (task.state !== TASK_STATE.RUNNING || task.lease === null) throw fail('NOT_RUNNING', '任务不在运行中', { state: task.state });
    if (task.lease.workerId !== workerId) throw fail('WRONG_WORKER', '租约不属于该 worker', { owner: task.lease.workerId, caller: workerId });
    const now = this.#now();
    if (now > task.lease.expiresAt) throw fail('TOKEN_STALE', '租约已过期，完成/续租必须重新领取');
    const extend = extendMs ?? task.leaseMs;
    if (!Number.isInteger(extend) || extend <= 0) throw fail('INVALID_LEASE', 'extendMs 必须是正整数');
    task.lease = { ...task.lease, expiresAt: now + extend };
    task.updatedAt = now;
    this.#emit(TASK_EVENT.HEARTBEAT, task, { workerId, attempt: task.attempt, expiresAt: task.lease.expiresAt });
    return { ok: true, task: freezeTask(task) };
  }

  complete({ taskId, workerId, result = null } = {}) {
    const task = this.#require(taskId);
    if (task.state === TASK_STATE.SUCCEEDED) return { duplicate: true, task: freezeTask(task) };
    if (TERMINAL.includes(task.state)) throw fail('TERMINAL', '任务已处于终态', { state: task.state });
    if (task.state === TASK_STATE.QUEUED && task.attempt > 0) throw fail('TOKEN_STALE', '租约已过期并被回收，旧 attempt 的结果不得落账', { attempt: task.attempt });
    if (task.state !== TASK_STATE.RUNNING || task.lease === null) throw fail('NOT_RUNNING', '任务不在运行中', { state: task.state });
    if (task.lease.workerId !== workerId) throw fail('WRONG_WORKER', '租约不属于该 worker', { owner: task.lease.workerId, caller: workerId });
    const now = this.#now();
    if (now > task.lease.expiresAt) throw fail('TOKEN_STALE', '租约已过期，结果不得落账');
    this.#finish(task, TASK_STATE.SUCCEEDED, { result: result === null ? null : Object.freeze({ ...result }) }, TASK_EVENT.COMPLETED, { now, workerId, attempt: task.attempt });
    return { ok: true, duplicate: false, task: freezeTask(task) };
  }

  /** 失败：可重试且未耗尽 attempts → 重新排队；否则终态 FAILED。 */
  fail({ taskId, workerId, error = 'UNKNOWN', retryable = false } = {}) {
    const task = this.#require(taskId);
    if (TERMINAL.includes(task.state)) throw fail('TERMINAL', '任务已处于终态', { state: task.state });
    if (task.state === TASK_STATE.QUEUED && task.attempt > 0) throw fail('TOKEN_STALE', '租约已过期并被回收，旧 attempt 的失败不得落账', { attempt: task.attempt });
    if (task.state !== TASK_STATE.RUNNING || task.lease === null) throw fail('NOT_RUNNING', '任务不在运行中', { state: task.state });
    if (task.lease.workerId !== workerId) throw fail('WRONG_WORKER', '租约不属于该 worker', { owner: task.lease.workerId, caller: workerId });
    const now = this.#now();
    if (now > task.lease.expiresAt) throw fail('TOKEN_STALE', '租约已过期，失败不得落账');
    const canRetry = retryable === true && task.attempt < task.maxAttempts;
    task.updatedAt = now;
    task.error = String(error);
    task.lease = null;
    if (canRetry) {
      task.state = TASK_STATE.QUEUED;
      this.#emit(TASK_EVENT.FAILED, task, { workerId, error: task.error, retryable: true, attempt: task.attempt });
      this.#emit(TASK_EVENT.REQUEUED, task, { attempt: task.attempt, reason: task.error });
    } else {
      task.state = TASK_STATE.FAILED;
      this.#emit(TASK_EVENT.FAILED, task, { workerId, error: task.error, retryable: false, attempt: task.attempt });
    }
    return { duplicate: false, task: freezeTask(task) };
  }

  cancel({ taskId, reason = null } = {}) {
    const task = this.#require(taskId);
    if (TERMINAL.includes(task.state)) return { duplicate: true, task: freezeTask(task) };
    task.state = TASK_STATE.CANCELLED;
    task.cancelledReason = reason === null ? null : String(reason);
    task.lease = null;
    task.updatedAt = this.#now();
    this.#emit(TASK_EVENT.CANCELLED, task, { reason: task.cancelledReason });
    return { duplicate: false, task: freezeTask(task) };
  }

  /** 回收过期租约：每次过期的运行中任务只产生一组事件（幂等）。 */
  sweepExpired() {
    const now = this.#now();
    const actions = [];
    for (const task of this.#tasks.values()) {
      if (task.state !== TASK_STATE.RUNNING || task.lease === null) continue;
      if (now <= task.lease.expiresAt) continue;
      actions.push(this.#reap(task, now));
    }
    return Object.freeze(actions);
  }

  #reap(task, now = this.#now()) {
    this.#emit(TASK_EVENT.LEASE_EXPIRED, task, { workerId: task.lease?.workerId ?? null, attempt: task.attempt });
    const canRetry = task.attempt < task.maxAttempts;
    task.lease = null;
    task.error = 'LEASE_EXPIRED';
    task.updatedAt = now;
    if (canRetry) {
      task.state = TASK_STATE.QUEUED;
      this.#emit(TASK_EVENT.REQUEUED, task, { attempt: task.attempt, reason: 'LEASE_EXPIRED' });
      return Object.freeze({ taskId: task.id, action: 'requeued' });
    }
    task.state = TASK_STATE.FAILED;
    this.#emit(TASK_EVENT.FAILED, task, { error: 'LEASE_EXPIRED', retryable: false, attempt: task.attempt });
    return Object.freeze({ taskId: task.id, action: 'failed' });
  }

  #finish(task, state, fields = {}, eventType, extra = {}) {
    task.state = state;
    task.updatedAt = extra.now ?? this.#now();
    if ('result' in fields) task.result = fields.result;
    if ('error' in fields) task.error = fields.error;
    task.lease = null;
    this.#emit(eventType, task, { ...extra, state, result: task.result, error: task.error, attempt: task.attempt });
  }

  #require(taskId) {
    const task = this.#tasks.get(taskId);
    if (task === undefined) throw fail('NOT_FOUND', '未知 taskId', { taskId });
    return task;
  }

  inspect(taskId) {
    return freezeTask(this.#require(taskId));
  }

  list({ state = null, limit = null } = {}) {
    let tasks = [...this.#tasks.values()];
    if (state !== null) tasks = tasks.filter((task) => task.state === state);
    if (Number.isInteger(limit) && limit > 0) tasks = tasks.slice(-limit);
    return Object.freeze(tasks.map(freezeTask));
  }

  stats() {
    const byState = {};
    for (const state of Object.values(TASK_STATE)) byState[state] = 0;
    for (const task of this.#tasks.values()) byState[task.state] += 1;
    return Object.freeze({ total: this.#tasks.size, byState: Object.freeze(byState), events: this.#events.length });
  }

  events() {
    return Object.freeze(this.#events.map((event) => Object.freeze({ ...event, payload: event.payload })));
  }

  /** 由事件流重建账本（重放/持久化恢复用）。 */
  static fromEvents(events) {
    const ledger = new TaskLedger();
    for (const event of Array.isArray(events) ? events : []) ledger.#apply(event);
    return ledger;
  }

  #apply(event) {
    if (event === null || typeof event !== 'object' || typeof event.type !== 'string') throw fail('INVALID_EVENT', '事件必须是对象且带 type');
    const existing = this.#tasks.get(event.taskId);
    const payload = event.payload ?? {};
    if (event.type === TASK_EVENT.CREATED) {
      const task = {
        id: event.taskId,
        kind: payload.kind ?? existing?.kind ?? 'unknown',
        payload: Object.freeze(existing?.payload ?? {}),
        idempotencyKey: payload.idempotencyKey ?? null,
        state: TASK_STATE.QUEUED,
        attempt: 0,
        maxAttempts: DEFAULT_MAX_ATTEMPTS,
        leaseMs: this.defaultLeaseMs,
        createdAt: event.at,
        deadlineAt: null,
        lease: null,
        result: null,
        error: null,
        updatedAt: event.at,
        cancelledReason: null
      };
      this.#tasks.set(task.id, task);
      if (task.idempotencyKey !== null) this.#byKey.set(task.idempotencyKey, task.id);
    } else if (existing !== undefined) {
      if (event.type === TASK_EVENT.LEASED) {
        existing.state = TASK_STATE.RUNNING;
        existing.attempt = payload.attempt ?? existing.attempt + 1;
        existing.lease = { workerId: payload.workerId ?? null, leaseId: 'replay-' + event.seq, attempt: existing.attempt, startedAt: event.at, expiresAt: payload.expiresAt ?? event.at };
        existing.updatedAt = event.at;
    } else if (event.type === TASK_EVENT.HEARTBEAT) {
        if (existing.lease !== null) existing.lease.expiresAt = payload.expiresAt ?? existing.lease.expiresAt;
        existing.updatedAt = event.at;
      } else if (event.type === TASK_EVENT.COMPLETED) {
        existing.state = TASK_STATE.SUCCEEDED;
        existing.result = payload.result ?? null;
        existing.lease = null;
        existing.updatedAt = event.at;
      } else if (event.type === TASK_EVENT.FAILED) {
        existing.state = TASK_STATE.FAILED;
        existing.error = payload.error ?? 'UNKNOWN';
        existing.lease = null;
        existing.updatedAt = event.at;
      } else if (event.type === TASK_EVENT.REQUEUED) {
        existing.state = TASK_STATE.QUEUED;
        existing.lease = null;
        existing.updatedAt = event.at;
      } else if (event.type === TASK_EVENT.CANCELLED) {
        existing.state = TASK_STATE.CANCELLED;
        existing.cancelledReason = payload.reason ?? null;
        existing.lease = null;
        existing.updatedAt = event.at;
      } else if (event.type === TASK_EVENT.LEASE_EXPIRED || event.type === TASK_EVENT.DEADLINE_EXCEEDED) {
        existing.error = event.type === TASK_EVENT.DEADLINE_EXCEEDED ? 'DEADLINE_EXCEEDED' : 'LEASE_EXPIRED';
        existing.updatedAt = event.at;
      }
    }
    this.#events.push(Object.freeze({ seq: event.seq ?? this.#events.length + 1, at: event.at, type: event.type, taskId: event.taskId, payload: Object.freeze({ ...payload }) }));
  }
}
