import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { GoalBuffer } from "../packages/im2a-http-mcp/goal-buffer.mjs";
import { scheduleGoal } from "../packages/im2a-http-mcp/goal-executor.mjs";
import { SqliteSkillStore } from "../packages/skill-runtime/src/sqlite-store.mjs";

const now = Date.UTC(2026, 10, 2, 0, 0, 0);
const agentDid = "did:im2a:v263-test";

function seedSkillStore() {
  const store = new SqliteSkillStore({ path: ":memory:", now: () => now });
  store.registerPackage({
    id: "stale-skill-a", version: "1.0.0", description: "stale build skill", tags: ["build"],
    files: { "SKILL.md": "# stale-skill-a\n\nstale build skill" },
  });
  store.setOwner("stale-skill-a", agentDid);
  store.db.prepare("UPDATE active SET updated_at = ? WHERE skill_id = ?").run(now - 45 * 24 * 60 * 60 * 1000, "stale-skill-a");
  return store;
}

function putGoal(buffer, priority, summary) {
  return buffer.put({
    driveId: "stale_skill_caretaker",
    intentSummary: summary,
    sourceSignals: [{ type: "skill", id: "stale-skill-a" }],
    priorityScore: priority,
  });
}

async function withContext(fn) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-goal-schedule-"));
  const skills = seedSkillStore();
  const buffer = new GoalBuffer({ path: path.join(dir, "goal-buffer.db"), now: () => now });
  try {
    return await fn({ dir, skills, buffer, draftsDir: path.join(dir, "drafts") });
  } finally {
    buffer.close();
    skills.close();
    await rm(dir, { recursive: true, force: true });
  }
}

test("goal_schedule transitions pending -> scheduled -> executing -> completed and writes only drafts", async () => {
  await withContext(async ({ skills, buffer, draftsDir }) => {
    const beforePackages = skills.db.prepare("SELECT COUNT(*) AS n FROM packages").get().n;
    const beforeSha = skills.activeSha("stale-skill-a");
    const goal = putGoal(buffer, 0.9, "Patch stale build skill.");
    const result = await scheduleGoal({
      goalBuffer: buffer,
      goalId: goal.goal_id,
      agentDid,
      budgetTokens: 2000,
      skillStore: skills,
      memoryHub: { recall: () => ({ items: [], budget_used: 0, suppressed_count: 0 }) },
      draftsDir,
      reservedAvailable: true,
      now,
    });
    assert.equal(result.goal.status, "completed");
    assert.equal(result.execution_result.recommended_next, "crystallize");
    assert.ok(result.execution_result.confidence_delta > 0);
    assert.ok(result.execution_result.draft_patch);
    assert.ok(result.execution_result.draft_patch.path.startsWith(draftsDir));
    assert.equal(existsSync(result.execution_result.draft_patch.path), true);
    const content = await readFile(result.execution_result.draft_patch.path, "utf8");
    assert.match(content, /Draft patch for stale-skill-a/);
    assert.match(content, /applied=false|not applied/);
    assert.equal(existsSync(path.join(draftsDir, "../skills")), false);
    assert.equal(skills.db.prepare("SELECT COUNT(*) AS n FROM packages").get().n, beforePackages);
    assert.equal(skills.activeSha("stale-skill-a"), beforeSha);
    const events = buffer.audit(goal.goal_id);
    assert.deepEqual(events.events.map((event) => event.to_status), ["pending", "scheduled", "executing", "completed"]);
  });
});

test("negative confidence_delta recommends discard, never crystallize", async () => {
  await withContext(async ({ skills, buffer, draftsDir }) => {
    const goal = putGoal(buffer, 0.2, "Patch a low-priority stale skill.");
    const result = await scheduleGoal({
      goalBuffer: buffer,
      goalId: goal.goal_id,
      agentDid,
      budgetTokens: 2000,
      skillStore: skills,
      memoryHub: { recall: () => ({ items: [], budget_used: 0, suppressed_count: 0 }) },
      draftsDir,
      reservedAvailable: true,
      now,
    });
    assert.ok(result.execution_result.confidence_delta < 0);
    assert.equal(result.execution_result.recommended_next, "discard");
    assert.equal(result.goal.status, "completed");
  });
});

test("unavailable reserved pool leaves the goal pending", async () => {
  await withContext(async ({ skills, buffer, draftsDir }) => {
    const goal = putGoal(buffer, 0.9, "Patch stale build skill.");
    await assert.rejects(scheduleGoal({
      goalBuffer: buffer,
      goalId: goal.goal_id,
      agentDid,
      budgetTokens: 2000,
      skillStore: skills,
      memoryHub: { recall: () => ({ items: [], budget_used: 0, suppressed_count: 0 }) },
      draftsDir,
      reservedAvailable: false,
      now,
    }), (error) => error.code === "RESERVED_POOL_UNAVAILABLE");
    assert.equal(buffer.get(goal.goal_id).status, "pending");
  });
});
