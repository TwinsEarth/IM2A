import assert from "node:assert/strict";
import test from "node:test";
import { calibrateDrives } from "../packages/im2a-http-mcp/drive-calibrate.mjs";

const now = Date.UTC(2026, 10, 2, 0, 0, 0);
const drives = [
  { drive_id: "stale_skill_caretaker", description: "scope == self_owned_skills AND lifecycle_state == stale", trigger_hint: "curator_audit.lifecycle_state == stale" },
  { drive_id: "risky_drive", description: "scope == self_owned_skills AND lifecycle_state == stale", trigger_hint: "curator_audit.lifecycle_state == stale" },
];

function gid(prefix, i) {
  return `goal_${String(prefix).padStart(2, "0")}${String(i).padStart(22, "0")}`;
}
function goalsFor(driveId, prefix, completed, expired) {
  const goals = [];
  for (let i = 0; i < completed; i += 1) goals.push({ goal_id: gid(prefix, i), drive_id: driveId, status: "completed" });
  for (let i = 0; i < expired; i += 1) goals.push({ goal_id: gid(prefix, completed + i), drive_id: driveId, status: "expired" });
  return goals;
}
function execsFor(driveId, goals, deltas, yields) {
  return goals.map((goal, i) => ({
    goal_id: goal.goal_id,
    drive_id: driveId,
    confidence_delta: deltas[i] ?? 0,
    execution_yield: yields[i] ?? false,
  }));
}

function snapshot() {
  const healthyGoals = goalsFor("stale_skill_caretaker", "a", 8, 2);
  const riskyGoals = goalsFor("risky_drive", "b", 2, 5);
  const healthy = execsFor("stale_skill_caretaker", healthyGoals,
    [0.4, 0.35, 0.3, 0.25, 0.22, 0.2, 0.18, 0.15, -0.1, -0.2],
    [true, true, true, true, true, true, true, true, false, false]);
  const risky = execsFor("risky_drive", riskyGoals,
    [0.2, 0.1, -0.1, -0.2, -0.2, -0.3, -0.4],
    [true, true, false, false, false, false, false]);
  const proposals = [
    ...healthyGoals.map((goal, i) => ({ proposal_goal_id: gid("p", i), drive_id: goal.drive_id, adopted_goal_id: i < 8 ? goal.goal_id : null })),
    ...riskyGoals.map((goal, i) => ({ proposal_goal_id: gid("q", i), drive_id: goal.drive_id, adopted_goal_id: i < 1 ? goal.goal_id : null })),
  ];
  return { goals: [...healthyGoals, ...riskyGoals], executions: [...healthy, ...risky], proposals };
}

test("drive_calibrate returns a non-unchanged adjustment with goal-level evidence", () => {
  const result = calibrateDrives({ agentDid: "did:im2a:calibrate", lookbackDays: 14, drives, snapshot: snapshot(), now });
  assert.equal(result.read_only, true);
  assert.equal(result.drives_modified, false);
  assert.equal(result.drive_count, 2);
  assert.equal(result.calibration_suggestions.length, 2);
  const healthy = result.calibration_suggestions.find((suggestion) => suggestion.drive_id === "stale_skill_caretaker");
  const risky = result.calibration_suggestions.find((suggestion) => suggestion.drive_id === "risky_drive");
  assert.equal(healthy.priority_adjustment, "increase");
  assert.equal(risky.priority_adjustment, "decrease");
  assert.ok(result.calibration_suggestions.some((suggestion) => suggestion.priority_adjustment !== "unchanged"));
  assert.ok(healthy.metrics.proposal_acceptance_rate > 0.5);
  assert.ok(healthy.metrics.execution_yield > 0.5);
  assert.ok(healthy.evidence.length >= 10);
  assert.ok(healthy.evidence.every((item) => /^goal_/.test(item.goal_id)));
  assert.ok(healthy.evidence.some((item) => typeof item.confidence_delta === "number"));
  assert.ok(healthy.trigger_hint_refinement.length > 0);
});

test("invalid lookback and agent are rejected", () => {
  assert.throws(() => calibrateDrives({ agentDid: "did:im2a:x", lookbackDays: 0, drives, snapshot: snapshot(), now }), (error) => error.code === "INVALID_LOOKBACK_DAYS");
  assert.throws(() => calibrateDrives({ agentDid: "", lookbackDays: 14, drives, snapshot: snapshot(), now }), (error) => error.code === "INVALID_AGENT_DID");
});
