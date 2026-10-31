import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { GoalBuffer } from "../packages/im2a-http-mcp/goal-buffer.mjs";

async function withBuffer(capacity, fn) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-goal-arbitration-"));
  const buffer = new GoalBuffer({ path: path.join(dir, "goal-buffer.db"), capacity, now: () => Date.UTC(2026, 10, 2, 0, 0, 0) });
  try {
    return await fn(buffer);
  } finally {
    buffer.close();
    await rm(dir, { recursive: true, force: true });
  }
}

function signals(...ids) {
  return ids.map((id) => ({ type: "skill", id }));
}

test("capacity arbitration expires the lowest-priority intent and preserves its signals in audit", async () => {
  await withBuffer(3, async (buffer) => {
    const low = await buffer.admit({ driveId: "drive-a", intentSummary: "low", sourceSignals: signals("skill-low"), priorityScore: 0.2 });
    await buffer.admit({ driveId: "drive-a", intentSummary: "mid", sourceSignals: signals("skill-mid"), priorityScore: 0.5 });
    await buffer.admit({ driveId: "drive-a", intentSummary: "high", sourceSignals: signals("skill-high"), priorityScore: 0.8 });
    const admitted = await buffer.admit({ driveId: "drive-b", intentSummary: "new", sourceSignals: signals("skill-new"), priorityScore: 0.4 });
    assert.equal(admitted.expired_goal_id, low.goal.goal_id);
    const expired = buffer.query({ status: "expired" });
    assert.equal(expired.count, 1);
    assert.equal(expired.goals[0].priority_score, 0.2);
    assert.deepEqual(expired.goals[0].source_signals, signals("skill-low"));
    const audit = buffer.audit(expired.goals[0].goal_id);
    const expiry = audit.events.find((event) => event.to_status === "expired");
    assert.ok(expiry);
    assert.deepEqual(expiry.details.source_signals, signals("skill-low"));
    assert.equal(expiry.actor, "goal_buffer_capacity");
    assert.equal(buffer.query({ status: "pending" }).count, 3);
  });
});

test("semantically overlapping intents merge source_signals and keep the higher priority", async () => {
  await withBuffer(8, async (buffer) => {
    const first = buffer.admit({
      driveId: "drive-a",
      intentSummary: "patch skill stale_skill_caretaker because its stale reference is unpatched",
      sourceSignals: signals("skill-a", "skill-b"),
      priorityScore: 0.5,
    });
    const second = buffer.admit({
      driveId: "drive-b",
      intentSummary: "patch skill stale_skill_caretaker because its stale reference is unpatched",
      sourceSignals: signals("skill-b", "skill-c"),
      priorityScore: 0.8,
    });
    assert.equal(first.merged, false);
    assert.equal(second.merged, true);
    assert.equal(second.merged_goal_id, first.goal.goal_id);
    assert.ok(second.similarity >= 0.85 && second.similarity <= 1);
    assert.equal(second.goal.priority_score, 0.8);
    assert.equal(second.goal.source_signals.length, 3);
    assert.deepEqual(new Set(second.goal.source_signals.map((signal) => signal.id)), new Set(["skill-a", "skill-b", "skill-c"]));
    assert.equal(buffer.activeGoals().length, 1);
    assert.equal(buffer.query({ status: "pending" }).count, 1);
    assert.equal(buffer.db.prepare("SELECT COUNT(*) AS n FROM goal_merges").get().n, 1);
  });
});
