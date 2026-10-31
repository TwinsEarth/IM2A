import assert from "node:assert/strict";
import { readFile, mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { GoalBuffer } from "../packages/im2a-http-mcp/goal-buffer.mjs";
import { writeDraft } from "../packages/im2a-http-mcp/drafts.mjs";

const now = Date.UTC(2026, 10, 2, 0, 0, 0);

test("restart restores pending, downgrades executing, and never stores draft body in SQLite", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-goal-persist-"));
  const dbPath = path.join(dir, "goal-buffer.db");
  const draftsDir = path.join(dir, "drafts");
  const marker = "DRAFT_BODY_MUST_NOT_APPEAR_IN_SQLITE";
  try {
    const buffer = new GoalBuffer({ path: dbPath, now: () => now });
    const goals = [];
    for (let i = 0; i < 4; i += 1) {
      goals.push(await buffer.admit({
        driveId: "stale_skill_caretaker",
        intentSummary: `persistent goal ${i}`,
        sourceSignals: [{ type: "skill", id: `stale-skill-${i}` }],
        priorityScore: (i + 1) / 10,
      }));
    }
    buffer.transition(goals[3].goal.goal_id, "scheduled", { actor: "scheduler" });
    buffer.transition(goals[3].goal.goal_id, "executing", { actor: "scheduler" });
    const draft = await writeDraft({
      dir: draftsDir,
      goalId: goals[3].goal.goal_id,
      targetSkillId: "stale-skill-3",
      intentSummary: marker,
      sourceSignals: [{ type: "skill", id: "stale-skill-3" }],
      now,
    });
    buffer.recordExecution({
      goalId: goals[3].goal.goal_id,
      driveId: "stale_skill_caretaker",
      status: "completed",
      confidenceDelta: 0.2,
      draftPatch: { ...draft, content: marker },
      reflectionNotes: "read-only draft metadata",
      recommendedNext: "patch",
      at: new Date(now).toISOString(),
    });
    buffer.close();

    const raw = await readFile(dbPath);
    assert.equal(raw.includes(Buffer.from(marker)), false, "draft body must not be in SQLite");
    const draftText = await readFile(draft.path, "utf8");
    assert.match(draftText, new RegExp(marker));

    const recovered = new GoalBuffer({ path: dbPath, now: () => now });
    try {
      assert.equal(recovered.query({ status: "pending" }).count, 4);
      assert.equal(recovered.query({ status: "executing" }).count, 0);
      assert.deepEqual(recovered.lastRecovery.downgraded_goal_ids, [goals[3].goal.goal_id]);
      assert.equal(recovered.lastRecovery.pending_count, 4);
      assert.equal(recovered.lastRecovery.executing_count, 0);
      assert.equal(recovered.lastRecovery.completed_or_expired_restored, false);
      const audit = recovered.audit(goals[3].goal.goal_id);
      const recovery = audit.events.find((event) => event.actor === "startup_recovery");
      assert.ok(recovery);
      assert.equal(recovery.from_status, "executing");
      assert.equal(recovery.to_status, "pending");
      assert.deepEqual(recovery.details.source_signals, [{ type: "skill", id: "stale-skill-3" }]);
    } finally {
      recovered.close();
    }
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
