/**
 * v2.6.2 reserved-ratio scheduler seam.
 *
 * External demand may consume only external_pool. The reserved_pool is
 * allocated only when the external queue was empty at the start of the cycle.
 * The goal transition is a temporary v2.6.3 goal_schedule seam.
 */
import { randomBytes } from "node:crypto";
import { mkdir, readFile, appendFile } from "node:fs/promises";
import path from "node:path";

export const SCHEDULER_SCHEMA_VERSION = "1.0";
export const DEFAULT_RESERVED_RATIO = 0.15;
export const MIN_RESERVED_RATIO = 0.10;
export const MAX_RESERVED_RATIO = 0.30;

export class SchedulerError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "SchedulerError";
    this.code = code;
  }
}

export function loadSchedulerConfig(env = process.env) {
  const ratio = env.IM2A_RESERVED_RATIO === undefined ? DEFAULT_RESERVED_RATIO : Number(env.IM2A_RESERVED_RATIO);
  if (!Number.isFinite(ratio) || ratio < MIN_RESERVED_RATIO || ratio > MAX_RESERVED_RATIO) {
    throw new SchedulerError("SCHEDULER_CONFIG_INVALID", `reserved_ratio must be ${MIN_RESERVED_RATIO}..${MAX_RESERVED_RATIO}`);
  }
  const totalBudget = env.IM2A_CYCLE_BUDGET_TOKENS === undefined ? 100000 : Number(env.IM2A_CYCLE_BUDGET_TOKENS);
  if (!Number.isInteger(totalBudget) || totalBudget < 1) throw new SchedulerError("SCHEDULER_CONFIG_INVALID", "IM2A_CYCLE_BUDGET_TOKENS must be a positive integer");
  const taskTokens = env.IM2A_RESERVED_TASK_TOKENS === undefined ? 2000 : Number(env.IM2A_RESERVED_TASK_TOKENS);
  if (!Number.isInteger(taskTokens) || taskTokens < 1) throw new SchedulerError("SCHEDULER_CONFIG_INVALID", "IM2A_RESERVED_TASK_TOKENS must be a positive integer");
  return {
    reserved_ratio: ratio,
    total_budget_tokens: totalBudget,
    reserved_task_tokens: taskTokens,
  };
}

function normalizeExternal(request) {
  if (!request || typeof request !== "object" || Array.isArray(request)) throw new SchedulerError("EXTERNAL_REQUEST_INVALID", "external request must be an object");
  if (typeof request.request_id !== "string" || !/^[A-Za-z0-9_.:-]{1,200}$/.test(request.request_id)) {
    throw new SchedulerError("EXTERNAL_REQUEST_INVALID", "request_id is invalid");
  }
  if (!Number.isInteger(request.tokens) || request.tokens < 1) throw new SchedulerError("EXTERNAL_REQUEST_INVALID", "tokens must be a positive integer");
  return { request_id: request.request_id, tokens: request.tokens };
}

export class ReservedScheduler {
  #queue = [];
  #cycle = 0;
  #tail = Promise.resolve();
  #lastCycle = null;

  constructor({
    goalBuffer,
    auditPath,
    reservedRatio = DEFAULT_RESERVED_RATIO,
    totalBudgetTokens = 100000,
    reservedTaskTokens = 2000,
    now = () => Date.now(),
  } = {}) {
    if (!goalBuffer || typeof goalBuffer.nextPending !== "function") throw new SchedulerError("SCHEDULER_CONFIG_INVALID", "goalBuffer is required");
    if (!Number.isFinite(reservedRatio) || reservedRatio < MIN_RESERVED_RATIO || reservedRatio > MAX_RESERVED_RATIO) throw new SchedulerError("SCHEDULER_CONFIG_INVALID", "reserved_ratio out of range");
    if (!Number.isInteger(totalBudgetTokens) || totalBudgetTokens < 1) throw new SchedulerError("SCHEDULER_CONFIG_INVALID", "total budget invalid");
    if (!Number.isInteger(reservedTaskTokens) || reservedTaskTokens < 1) throw new SchedulerError("SCHEDULER_CONFIG_INVALID", "reserved task tokens invalid");
    if (typeof auditPath !== "string" || auditPath.length === 0) throw new SchedulerError("SCHEDULER_CONFIG_INVALID", "auditPath is required");
    this.goalBuffer = goalBuffer;
    this.auditPath = auditPath;
    this.reservedRatio = reservedRatio;
    this.totalBudgetTokens = totalBudgetTokens;
    this.reservedTaskTokens = reservedTaskTokens;
    this.now = now;
  }

  get queueDepth() { return this.#queue.length; }
  get reservedLimit() { return Math.floor(this.totalBudgetTokens * this.reservedRatio); }
  get externalLimit() { return this.totalBudgetTokens - this.reservedLimit; }

  canUseReserved(tokens) {
    return Number.isInteger(tokens) && tokens > 0 && tokens <= this.reservedLimit && this.#queue.length === 0;
  }

  state() {
    return {
      schema_version: SCHEDULER_SCHEMA_VERSION,
      config: {
        reserved_ratio: this.reservedRatio,
        total_budget_tokens: this.totalBudgetTokens,
        reserved_task_tokens: this.reservedTaskTokens,
      },
      queue_depth: this.#queue.length,
      queued_requests: this.#queue.map((request) => ({ ...request })),
      next_pending_goal: this.goalBuffer.nextPending(),
      last_cycle: this.#lastCycle,
    };
  }

  async scheduleCycle({ externalRequests = [] } = {}) {
    if (!Array.isArray(externalRequests)) throw new SchedulerError("EXTERNAL_REQUEST_INVALID", "external_requests must be an array");
    const incoming = externalRequests.map(normalizeExternal);
    const atStart = this.#queue.length + incoming.length;
    this.#queue.push(...incoming);
    const externalLimit = this.totalBudgetTokens - Math.floor(this.totalBudgetTokens * this.reservedRatio);
    const reservedLimit = this.totalBudgetTokens - externalLimit;
    const processed = [];
    let externalUsed = 0;
    while (this.#queue.length > 0) {
      const next = this.#queue[0];
      if (externalUsed + next.tokens > externalLimit) break;
      this.#queue.shift();
      processed.push(next);
      externalUsed += next.tokens;
    }
    const idle = atStart === 0;
    const reservedTasks = [];
    let reservedUsed = 0;
    let scheduledGoalId = null;
    if (idle) {
      const goal = this.goalBuffer.nextPending();
      if (goal) {
        const tokens = Math.min(reservedLimit, this.reservedTaskTokens);
        this.goalBuffer.transition(goal.goal_id, "scheduled", {
          actor: "scheduler",
          reason: "external_pool_idle",
        });
        reservedUsed = tokens;
        scheduledGoalId = goal.goal_id;
        reservedTasks.push({
          type: "goal_schedule",
          goal_id: goal.goal_id,
          priority_score: goal.priority_score,
          tokens,
          status: "scheduled",
        });
      }
    }
    const cycleId = `sched_${randomBytes(12).toString("hex")}`;
    const auditEvent = {
      schema_version: SCHEDULER_SCHEMA_VERSION,
      cycle_id: cycleId,
      cycle: ++this.#cycle,
      at: new Date(this.now()).toISOString(),
      total_budget_tokens: this.totalBudgetTokens,
      reserved_ratio: this.reservedRatio,
      external: {
        limit: externalLimit,
        used: externalUsed,
        remaining: externalLimit - externalUsed,
        requested_at_start: atStart,
        processed,
        queued: this.#queue.map((request) => ({ ...request })),
        queue_depth: this.#queue.length,
      },
      reserved: {
        limit: reservedLimit,
        used: reservedUsed,
        remaining: reservedLimit - reservedUsed,
        tasks: reservedTasks,
      },
      idle,
      scheduled_goal_id: scheduledGoalId,
    };
    const run = this.#tail.then(async () => {
      await mkdir(path.dirname(this.auditPath), { recursive: true, mode: 0o750 });
      await appendFile(this.auditPath, `${JSON.stringify(auditEvent)}\n`, { encoding: "utf8", mode: 0o640 });
      return auditEvent;
    });
    this.#tail = run.catch(() => {});
    const event = await run;
    this.#lastCycle = event;
    return {
      schema_version: SCHEDULER_SCHEMA_VERSION,
      cycle_id: cycleId,
      cycle: event.cycle,
      at: event.at,
      total_budget_tokens: this.totalBudgetTokens,
      reserved_ratio: this.reservedRatio,
      external_pool: {
        limit: externalLimit,
        used: externalUsed,
        remaining: externalLimit - externalUsed,
        processed,
        queue_depth: this.#queue.length,
        queued: this.#queue.map((request) => ({ ...request })),
      },
      reserved_pool: {
        limit: reservedLimit,
        used: reservedUsed,
        remaining: reservedLimit - reservedUsed,
        tasks: reservedTasks,
      },
      idle,
      scheduled_goal_id: scheduledGoalId,
      audit_event_id: cycleId,
    };
  }

  async audit({ limit = 50 } = {}) {
    const max = Math.min(Math.max(Number(limit) || 50, 1), 500);
    let raw;
    try { raw = await readFile(this.auditPath, "utf8"); }
    catch (error) {
      if (error.code === "ENOENT") return { events: [], count: 0 };
      throw error;
    }
    const events = raw.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
    return { events: events.slice(-max), count: Math.min(events.length, max) };
  }
}
