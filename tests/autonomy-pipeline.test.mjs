import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runAutonomyPipelineDryRun } from "../packages/im2a-http-mcp/autonomy-pipeline.mjs";

const frozen = JSON.parse(await readFile(new URL("../docs/contracts/v2.7-interface-freeze-1.0.json", import.meta.url), "utf8"));
const frozenHashes = JSON.parse(await readFile(new URL("../docs/contracts/frozen-v2.6.9-interfaces.json", import.meta.url), "utf8"));

const now = Date.UTC(2026, 10, 2, 0, 0, 0);
const drive = {
  drive_id: "stale_skill_caretaker",
  description: "scope == self_owned_skills AND lifecycle_state == stale AND patch_count == 0 AND ageDays >= 14",
  priority: 0.7,
  trigger_hint: "curator_audit.lifecycle_state == stale",
  scope: "self_owned_skills",
};

function skillStore() {
  return {
    curatorAudit: ({ agentDid, scope }) => ({
      skills: [{
        skill_id: "windows-build",
        lifecycle_state: "stale",
        last_used_at: new Date(now - 45 * 24 * 60 * 60 * 1000).toISOString(),
        use_count_30d: 0,
        patch_count: 0,
        estimated_context_cost_tokens: 42,
        recommended_action: "flag_for_review",
        owner: agentDid,
        scope,
      }],
      audit: { scope },
    }),
  };
}

const memoryHub = {
  recall: () => ({ items: [], budget_used: 10, suppressed_count: 0 }),
};

const traceStore = {
  recentForSkill: async () => [],
};

test("autonomy dry-run completes in under 45s with full signal traceability and frozen enums", async () => {
  const report = await runAutonomyPipelineDryRun({
    agentDid: "did:im2a:autonomy-test",
    drives: [drive],
    driveIds: [drive.drive_id],
    skillStore: skillStore(),
    memoryHub,
    traceStore,
    budgetTokens: 5000,
    now,
  });
  assert.equal(report.ok, true);
  assert.equal(report.under_45_seconds, true);
  assert.ok(report.duration_ms < 45000);
  assert.equal(report.signal_traceability, "full");
  assert.deepEqual(report.missing_signals, []);
  assert.equal(report.steps.length, 3);
  assert.ok(report.steps.every((step) => step.ok));
  assert.equal(report.skill_claim.ok, true);
  assert.equal(report.execution_result.draft_patch.applied, false);
  assert.equal(report.execution_result.draft_patch.target_skill_id, "windows-build");
  assert.deepEqual(report.goal_status_enum, frozen.goal_buffer.status_enum);
  assert.deepEqual(report.recommended_next_enum, frozen.execution_result.recommended_next_enum);
  assert.equal(report.frozen.contribution_log_ref, "goal_id");
  assert.match(report.goal_buffer.goal_id, /^goal_[0-9a-f]{24}$/);
  for (const interfaceInfo of Object.values(frozenHashes.interfaces)) {
    const body = await readFile(new URL(`../${interfaceInfo.schema}`, import.meta.url));
    assert.equal(createHash("sha256").update(body).digest("hex"), interfaceInfo.sha256, interfaceInfo.schema);
  }
});
