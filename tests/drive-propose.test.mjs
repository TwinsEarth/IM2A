import assert from "node:assert/strict";
import test from "node:test";
import { drivePropose, parseDrivePredicate, matchesDrive } from "../packages/im2a-http-mcp/drive-propose.mjs";

const now = Date.UTC(2026, 10, 1, 0, 0, 0);
const drive = {
  drive_id: "stale_skill_caretaker",
  description: "scope == self_owned_skills AND lifecycle_state == stale AND patch_count == 0 AND ageDays >= 14",
  priority: 0.7,
  trigger_hint: "curator_audit.lifecycle_state == stale",
  scope: "self_owned_skills",
};

function skillStore() {
  return {
    curatorAudit: ({ agentDid, scope, now: at }) => ({
      skills: [{
        skill_id: "windows-build",
        lifecycle_state: "stale",
        last_used_at: new Date(at - 45 * 24 * 60 * 60 * 1000).toISOString(),
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
  recall: () => ({
    items: [{
      content: "release note with low confidence",
      layer: "atom",
      score: 0.7,
      quality_flag: "low_confidence",
      source_trace_id: "0123456789abcdef0123456789abcdef",
    }],
    budget_used: 1938,
    suppressed_count: 3,
  }),
};

test("drive description predicates parse and match curator signals", () => {
  const predicates = parseDrivePredicate(drive.description);
  assert.equal(predicates.length, 4);
  const signal = { lifecycle_state: "stale", patch_count: 0, ageDays: 45 };
  assert.equal(matchesDrive(predicates, signal, drive), true);
  assert.equal(matchesDrive(predicates, { ...signal, patch_count: 2 }, drive), false);
  assert.throws(() => parseDrivePredicate("lifecycle_state stale"), (error) => error.code === "DRIVE_INVALID");
  assert.throws(() => parseDrivePredicate("evil == 1"), (error) => error.code === "DRIVE_INVALID");
});

test("curator-backed drive_propose returns a traceable proposed goal without buffer writes", () => {
  const result = drivePropose({
    drives: [drive],
    driveIds: ["stale_skill_caretaker"],
    agentDid: "did:im2a:propose-test",
    urgencyHint: "high",
    skillStore: skillStore(),
    memoryHub,
    estimatedTokenCost: 2000,
    now,
  });
  assert.equal(result.proposed_goals.length, 1);
  const goal = result.proposed_goals[0];
  assert.match(goal.goal_id, /^goal_[0-9a-f]{24}$/);
  assert.equal(goal.drive_id, "stale_skill_caretaker");
  assert.deepEqual(goal.source_signals, [{ type: "skill", id: "windows-build" }]);
  assert.equal(goal.estimated_token_cost, 2000);
  assert.ok(goal.confidence > 0.5 && goal.confidence <= 0.95);
  assert.match(goal.intent_summary, /skill:windows-build/);
  assert.equal(result.token_cost_basis, "recall_history_median");
  assert.equal(result.estimated_token_cost, 2000);
});

test("memory-backed drive_propose emits a memory_item signal", () => {
  const memoryDrive = {
    drive_id: "memory_confidence_guardian",
    description: "quality_flag == low_confidence AND layer == atom",
    priority: 0.6,
    trigger_hint: "memory_recall_v2.quality_flag == low_confidence",
    scope: "self_owned_skills",
  };
  const result = drivePropose({
    drives: [memoryDrive],
    driveIds: [memoryDrive.drive_id],
    agentDid: "did:im2a:propose-test",
    skillStore: skillStore(),
    memoryHub,
    estimatedTokenCost: 2000,
    now,
  });
  assert.equal(result.proposed_goals.length, 1);
  assert.deepEqual(result.proposed_goals[0].source_signals, [{ type: "memory_item", id: "0123456789abcdef0123456789abcdef" }]);
  assert.equal(result.proposed_goals[0].estimated_token_cost, 2000);
});

test("missing drives and invalid ids fail closed", () => {
  assert.throws(() => drivePropose({
    drives: [drive], driveIds: ["missing"], agentDid: "did:im2a:x", skillStore: skillStore(), memoryHub, estimatedTokenCost: 2000, now,
  }), (error) => error.code === "DRIVE_NOT_FOUND");
  assert.throws(() => drivePropose({
    drives: [drive], driveIds: [], agentDid: "did:im2a:x", skillStore: skillStore(), memoryHub, estimatedTokenCost: 2000, now,
  }), (error) => error.code === "DRIVE_INVALID");
});
