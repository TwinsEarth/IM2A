import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { GoalBuffer } from "../packages/im2a-http-mcp/goal-buffer.mjs";
import { ReservedScheduler, loadSchedulerConfig } from "../packages/im2a-http-mcp/scheduler.mjs";

const DAY = 24 * 60 * 60 * 1000;
const now = Date.UTC(2026, 10, 2, 0, 0, 0);

async function withScheduler(fn) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-scheduler-"));
  const buffer = new GoalBuffer({ path: path.join(dir, "goal-buffer.db"), now: () => now });
  const scheduler = new ReservedScheduler({
    goalBuffer: buffer,
    auditPath: path.join(dir, "scheduler-audit.jsonl"),
    reservedRatio: 0.15,
    totalBudgetTokens: 100000,
    reservedTaskTokens: 2000,
    now: () => now,
  });
  try {
    return await fn({ dir, buffer, scheduler });
  } finally {
    buffer.close();
    await rm(dir, { recursive: true, force: true });
  }
}

function putGoal(buffer, priority, summary) {
  return buffer.put({
    driveId: "stale_skill_caretaker",
    intentSummary: summary,
    sourceSignals: [{ type: "skill", id: "stale-skill-a" }],
    priorityScore: priority,
  });
}

test("external-heavy cycle queues demand and does not borrow reserved_pool", async () => {
  await withScheduler(async ({ buffer, scheduler }) => {
    putGoal(buffer, 0.5, "low priority");
    putGoal(buffer, 0.9, "high priority");
    const requests = Array.from({ length: 10 }, (_, i) => ({ request_id: `req-${i}`, tokens: 10000 }));
    const result = await scheduler.scheduleCycle({ externalRequests: requests });
    assert.equal(result.external_pool.limit, 85000);
    assert.equal(result.external_pool.used, 80000);
    assert.equal(result.external_pool.queue_depth, 2);
    assert.equal(result.reserved_pool.used, 0);
    assert.equal(result.idle, false);
    assert.equal(result.scheduled_goal_id, null);
    assert.equal(buffer.query({ status: "pending" }).count, 2);
    assert.equal(buffer.query({ status: "scheduled" }).count, 0);
  });
});

test("leftover external queue is drained before idle scheduling", async () => {
  await withScheduler(async ({ buffer, scheduler }) => {
    putGoal(buffer, 0.5, "low priority");
    putGoal(buffer, 0.9, "high priority");
    await scheduler.scheduleCycle({ externalRequests: Array.from({ length: 10 }, (_, i) => ({ request_id: `req-${i}`, tokens: 10000 })) });
    const drain = await scheduler.scheduleCycle();
    assert.equal(drain.idle, false);
    assert.equal(drain.external_pool.used, 20000);
    assert.equal(drain.external_pool.queue_depth, 0);
    assert.equal(drain.reserved_pool.used, 0);
    assert.equal(buffer.query({ status: "pending" }).count, 2);

    const idle = await scheduler.scheduleCycle();
    assert.equal(idle.idle, true);
    assert.equal(idle.external_pool.used, 0);
    assert.equal(idle.reserved_pool.used, 2000);
    assert.equal(idle.reserved_pool.remaining, 13000);
    assert.equal(idle.reserved_pool.tasks.length, 1);
    assert.equal(idle.reserved_pool.tasks[0].type, "goal_schedule");
    assert.equal(idle.reserved_pool.tasks[0].priority_score, 0.9);
    assert.ok(idle.scheduled_goal_id);
    const goals = buffer.query({});
    assert.equal(goals.goals.find((goal) => goal.priority_score === 0.9).status, "scheduled");
    assert.equal(goals.goals.find((goal) => goal.priority_score === 0.5).status, "pending");
  });
});

test("scheduler audit keeps external and reserved trajectories separate", async () => {
  await withScheduler(async ({ dir, buffer, scheduler }) => {
    putGoal(buffer, 0.9, "high priority");
    await scheduler.scheduleCycle({ externalRequests: Array.from({ length: 10 }, (_, i) => ({ request_id: `external-${i}`, tokens: 10000 })) });
    await scheduler.scheduleCycle();
    await scheduler.scheduleCycle();
    const audit = await scheduler.audit({ limit: 10 });
    assert.equal(audit.count, 3);
    assert.equal(audit.events[0].external.used, 80000);
    assert.equal(audit.events[0].external.queued.length, 2);
    assert.equal(audit.events[0].reserved.used, 0);
    assert.equal(audit.events[1].external.used, 20000);
    assert.equal(audit.events[1].reserved.used, 0);
    assert.equal(audit.events[2].idle, true);
    assert.equal(audit.events[2].external.used, 0);
    assert.equal(audit.events[2].reserved.used, 2000);
    const raw = await readFile(path.join(dir, "scheduler-audit.jsonl"), "utf8");
    const lines = raw.trim().split("\n").map((line) => JSON.parse(line));
    assert.equal(lines.length, 3);
    assert.ok(lines.every((line) => Object.hasOwn(line, "external") && Object.hasOwn(line, "reserved")));
    const state = scheduler.state();
    assert.equal(state.config.reserved_ratio, 0.15);
    assert.equal(state.queue_depth, 0);
    assert.equal(state.last_cycle.cycle, 3);
  });
});

test("reserved_ratio config is bounded to 0.10..0.30", () => {
  assert.equal(loadSchedulerConfig({}).reserved_ratio, 0.15);
  assert.equal(loadSchedulerConfig({ IM2A_RESERVED_RATIO: "0.10" }).reserved_ratio, 0.10);
  assert.equal(loadSchedulerConfig({ IM2A_RESERVED_RATIO: "0.30" }).reserved_ratio, 0.30);
  assert.throws(() => loadSchedulerConfig({ IM2A_RESERVED_RATIO: "0.09" }), (error) => error.code === "SCHEDULER_CONFIG_INVALID");
  assert.throws(() => loadSchedulerConfig({ IM2A_RESERVED_RATIO: "0.31" }), (error) => error.code === "SCHEDULER_CONFIG_INVALID");
});
