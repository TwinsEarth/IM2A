import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { GoalBuffer, GoalBufferError } from "../packages/im2a-http-mcp/goal-buffer.mjs";
import { loadDrives, loadDrive, parseDrive, writeDrive } from "../packages/im2a-http-mcp/drives.mjs";
import { PUBLIC_SESSION, SkillGuard } from "../packages/im2a-http-mcp/skill-guard.mjs";
import { validateSchema } from "../scripts/schema-validator.mjs";

const driveSchema = JSON.parse(await readFile(new URL("../docs/contracts/drive-declaration-1.0.schema.json", import.meta.url), "utf8"));
const goalSchema = JSON.parse(await readFile(new URL("../docs/contracts/goal-buffer-1.0.schema.json", import.meta.url), "utf8"));

const drive = {
  drive_id: "stale_skill_caretaker",
  description: "self-owned skills whose lifecycle_state has been stale for more than 14 days without a patch",
  priority: 0.7,
  trigger_hint: "curator_audit.lifecycle_state == stale",
  scope: "self_owned_skills",
  body: "# Stale skill caretaker\n\nReview stale self-owned skills.\n",
};

test("the checked-in drive sample matches the frozen declaration schema", async () => {
  const sample = await loadDrive(fileURLToPath(new URL("../packages/agent-plugins/source/drives/stale_skill_caretaker.md", import.meta.url)));
  const result = validateSchema(driveSchema, {
    drive_id: sample.drive_id,
    description: sample.description,
    priority: sample.priority,
    trigger_hint: sample.trigger_hint,
    scope: sample.scope,
  });
  assert.equal(result.ok, true, JSON.stringify(result.errors));
});

test("drive declarations parse, persist and load from drives/", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-drives-"));
  try {
    const written = await writeDrive(dir, drive);
    assert.equal(written.drive_id, drive.drive_id);
    assert.equal(written.priority, 0.7);
    assert.equal(written.scope, "self_owned_skills");
    const raw = await readFile(path.join(dir, `${drive.drive_id}.md`), "utf8");
    assert.match(raw, /^---\ndrive_id:/);
    const loaded = await loadDrive(path.join(dir, `${drive.drive_id}.md`));
    assert.deepEqual({ ...loaded, body: loaded.body }, { ...written, body: loaded.body });
    const all = await loadDrives(dir);
    assert.equal(all.length, 1);
    assert.equal(all[0].trigger_hint, drive.trigger_hint);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("drive declarations reject malformed or unsafe input", () => {
  const invalid = (fn) => assert.throws(fn, (error) => error.code === "DRIVE_INVALID");
  invalid(() => parseDrive("no frontmatter"));
  invalid(() => parseDrive(`---\ndrive_id: Bad-ID\ndescription: x\npriority: 0.5\ntrigger_hint: y\nscope: self_owned_skills\n---\n`));
  invalid(() => parseDrive(`---\ndrive_id: valid_id\ndescription: x\npriority: 2\ntrigger_hint: y\nscope: self_owned_skills\n---\n`));
  invalid(() => parseDrive(`---\ndrive_id: valid_id\ndescription: x\npriority: 0.5\ntrigger_hint: y\nscope: network\n---\n`));
  invalid(() => parseDrive(`---\ndrive_id: valid_id\ndescription: x\npriority: 0.5\ntrigger_hint: y\nscope: self_owned_skills\nextra: field\n---\n`));
});

test("goal buffer persists status transitions and an external audit trail", () => {
  const buffer = new GoalBuffer({ path: ":memory:", now: () => Date.UTC(2026, 10, 1, 0, 0, 0) });
  try {
    const goal = buffer.put({
      driveId: drive.drive_id,
      intentSummary: "Review stale self-owned skills.",
      sourceSignals: [{ type: "skill", id: "stale-skill-a" }],
      priorityScore: 0.8,
    });
    assert.equal(goal.status, "pending");
    assert.match(goal.goal_id, /^goal_[0-9a-f]{24}$/);
    assert.equal(goal.source_signals[0].id, "stale-skill-a");
    const goalSchemaResult = validateSchema(goalSchema, goal);
    assert.equal(goalSchemaResult.ok, true, JSON.stringify(goalSchemaResult.errors));
    const scheduled = buffer.transition(goal.goal_id, "scheduled", { actor: "harness", reason: "idle slot" });
    assert.equal(scheduled.status, "scheduled");
    const executing = buffer.transition(goal.goal_id, "executing", { actor: "harness" });
    assert.equal(executing.status, "executing");
    const completed = buffer.transition(goal.goal_id, "completed", { actor: "harness", reason: "done" });
    assert.equal(completed.status, "completed");
    assert.throws(() => buffer.transition(goal.goal_id, "executing"), (error) => error instanceof GoalBufferError && error.code === "INVALID_STATUS_TRANSITION");
    const audit = buffer.audit(goal.goal_id);
    assert.equal(audit.count, 4);
    assert.deepEqual(audit.events.map((event) => event.to_status), ["pending", "scheduled", "executing", "completed"]);
    assert.equal(buffer.query({ status: "completed" }).count, 1);
    assert.equal(buffer.query({ status: "pending" }).count, 0);
    const invalid = (fn) => assert.throws(fn, (error) => error.code === "GOAL_INVALID");
    invalid(() => buffer.put({ driveId: "x", intentSummary: "x", sourceSignals: [], priorityScore: 0.1 }));
    invalid(() => buffer.put({ driveId: "x", intentSummary: "x", sourceSignals: [{ type: "other", id: "x" }], priorityScore: 0.1 }));
  } finally {
    buffer.close();
  }
});

test("public sessions cannot pass the drive write gate", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-drive-gate-"));
  try {
    const guard = new SkillGuard({ auditPath: path.join(dir, "audit.jsonl"), now: () => Date.UTC(2026, 10, 1, 0, 0, 0) });
    const denied = await guard.check({
      session: PUBLIC_SESSION,
      agentDid: "did:im2a:public",
      operation: "patch",
      targetSkillId: drive.drive_id,
      targetOwnerDid: null,
    });
    assert.equal(denied.allowed, false);
    assert.equal(denied.denied_reason, "insufficient_scope");
    assert.match(denied.audit_event_id, /^evt_/);
    const audit = await readFile(path.join(dir, "audit.jsonl"), "utf8");
    assert.match(audit, new RegExp(`"target_skill_id":"${drive.drive_id}"`));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
