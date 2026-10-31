import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { GoalBuffer } from "../packages/im2a-http-mcp/goal-buffer.mjs";
import { assertAgentReservedRatioWrite, ReservedScheduler } from "../packages/im2a-http-mcp/scheduler.mjs";

const now = Date.UTC(2026, 10, 2, 0, 0, 0);

async function withGovernance(fn) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-scheduler-governance-"));
  const buffer = new GoalBuffer({ path: path.join(dir, "goal-buffer.db"), capacity: 8, now: () => now });
  const scheduler = new ReservedScheduler({
    goalBuffer: buffer,
    auditPath: path.join(dir, "scheduler-audit.jsonl"),
    reservedRatio: 0.15,
    now: () => now,
  });
  try {
    return await fn({ buffer, scheduler, dir });
  } finally {
    buffer.close();
    await rm(dir, { recursive: true, force: true });
  }
}

function put(buffer, index, priority = 0.5) {
  return buffer.admit({
    driveId: "drive-governance",
    intentSummary: `governance goal ${index}`,
    sourceSignals: [{ type: "skill", id: `governance-skill-${index}` }],
    priorityScore: priority,
  }).goal;
}

test("high completion at capacity raises reserved_ratio within +5% and audits the metrics", async () => {
  await withGovernance(async ({ buffer, scheduler }) => {
    for (let i = 0; i < 8; i += 1) {
      const goal = put(buffer, i);
      buffer.transition(goal.goal_id, "scheduled", { actor: "scheduler" });
      buffer.transition(goal.goal_id, "executing", { actor: "scheduler" });
      buffer.transition(goal.goal_id, "completed", { actor: "scheduler" });
    }
    for (let i = 0; i < 8; i += 1) put(buffer, 100 + i, 0.1 + i / 100);
    const result = await scheduler.applyGovernance();
    assert.equal(result.changed, true);
    assert.equal(result.decision, "increase");
    assert.equal(result.reserved_ratio, 0.2);
    assert.equal(result.event.type, "reserved_ratio_adjustment");
    assert.equal(result.event.before_reserved_ratio, 0.15);
    assert.equal(result.event.after_reserved_ratio, 0.2);
    assert.equal(result.event.metrics.completion_rate, 1);
    assert.equal(result.event.metrics.capacity_usage, 1);
    assert.equal(result.event.metrics.draft_to_skill_rate, null);
    const audit = await scheduler.audit();
    assert.equal(audit.count, 1);
    const second = await scheduler.applyGovernance();
    assert.equal(second.changed, false);
    assert.equal(scheduler.reservedRatio, 0.2);
  });
});

test("low completion lowers reserved_ratio within -5% and agent writes are refused", async () => {
  await withGovernance(async ({ buffer, scheduler }) => {
    for (let i = 0; i < 8; i += 1) {
      const goal = put(buffer, i);
      buffer.transition(goal.goal_id, "scheduled", { actor: "scheduler" });
      if (i === 0) {
        buffer.transition(goal.goal_id, "executing", { actor: "scheduler" });
        buffer.transition(goal.goal_id, "completed", { actor: "scheduler" });
      }
    }
    const result = await scheduler.applyGovernance();
    assert.equal(result.changed, true);
    assert.equal(result.decision, "decrease");
    assert.equal(result.reserved_ratio, 0.1);
    assert.equal(result.event.metrics.completion_rate, 0.125);
    assert.throws(
      () => assertAgentReservedRatioWrite({ action: "patch", id: "some-skill", reserved_ratio: 0.25 }),
      (error) => error.code === "RESERVED_RATIO_AGENT_WRITE_DENIED",
    );
  });
});
