/**
 * v2.6.9 autonomy pipeline dry-run and v2.7 interface freeze proof.
 *
 * drive_propose -> goal_schedule (temporary buffer + temp drafts)
 * -> simulated skill_claim schema validation.
 *
 * The production goal buffer, skills and drafts are never written here.
 */
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { GoalBuffer, GOAL_STATUSES } from "./goal-buffer.mjs";
import { RECOMMENDED_NEXT, scheduleGoal } from "./goal-executor.mjs";
import { drivePropose } from "./drive-propose.mjs";

export const AUTONOMY_PIPELINE_VERSION = "1.0";
export const AUTONOMY_STATUS_ENUM = Object.freeze([...GOAL_STATUSES]);
export const AUTONOMY_RECOMMENDED_ENUM = Object.freeze([...RECOMMENDED_NEXT]);

const SIGNAL_TYPES = new Set(["skill", "memory_item"]);

function nowIso(ms) { return new Date(ms).toISOString(); }

function validSignals(value) {
  return Array.isArray(value) && value.length > 0 && value.every((signal) =>
    signal && typeof signal === "object" && SIGNAL_TYPES.has(signal.type)
    && typeof signal.id === "string" && signal.id.length > 0);
}

function validateGoalBuffer(goal) {
  const errors = [];
  for (const key of ["goal_id", "drive_id", "proposed_at", "intent_summary", "priority_score", "status", "created_at", "updated_at"]) {
    if (goal?.[key] === undefined || goal[key] === null) errors.push(`${key} is required`);
  }
  if (!/^goal_[0-9a-f]{24}$/.test(goal?.goal_id ?? "")) errors.push("goal_id must be goal_<24 hex>");
  if (!validSignals(goal?.source_signals)) errors.push("source_signals must be a non-empty typed list");
  if (!Number.isFinite(goal?.priority_score) || goal.priority_score < 0 || goal.priority_score > 1) errors.push("priority_score must be 0..1");
  if (!AUTONOMY_STATUS_ENUM.includes(goal?.status)) errors.push(`status must be one of ${AUTONOMY_STATUS_ENUM.join("|")}`);
  return { ok: errors.length === 0, errors, enum: AUTONOMY_STATUS_ENUM };
}

function validateExecutionResult(result) {
  const errors = [];
  for (const key of ["goal_id", "agent_did", "reflection_notes", "confidence_delta", "recommended_next", "recommendation_reason", "budget", "read_only_tools"]) {
    if (result?.[key] === undefined || result[key] === null) errors.push(`${key} is required`);
  }
  if (!/^goal_[0-9a-f]{24}$/.test(result?.goal_id ?? "")) errors.push("goal_id must be goal_<24 hex>");
  if (!Number.isFinite(result?.confidence_delta)) errors.push("confidence_delta must be a number");
  if (!AUTONOMY_RECOMMENDED_ENUM.includes(result?.recommended_next)) errors.push(`recommended_next must be one of ${AUTONOMY_RECOMMENDED_ENUM.join("|")}`);
  if (result?.draft_patch !== null && result?.draft_patch !== undefined) {
    const draft = result.draft_patch;
    for (const key of ["draft_id", "target_skill_id", "path", "sha256", "bytes", "applied"]) {
      if (draft[key] === undefined) errors.push(`draft_patch.${key} is required`);
    }
    if (!/^draft_[0-9a-f]{24}$/.test(draft.draft_id ?? "")) errors.push("draft_id invalid");
    if (typeof draft.path !== "string" || !draft.path.includes("drafts")) errors.push("draft path must be a drafts/ artifact");
    if (draft.applied !== false) errors.push("draft_patch.applied must be false");
  }
  return { ok: errors.length === 0, errors, enum: AUTONOMY_RECOMMENDED_ENUM };
}

function validateSkillClaim(input) {
  const errors = [];
  for (const key of ["goal_id", "agent_did", "draft_patch", "source_signals", "recommended_next"]) {
    if (input?.[key] === undefined || input[key] === null) errors.push(`${key} is required`);
  }
  if (!/^goal_[0-9a-f]{24}$/.test(input?.goal_id ?? "")) errors.push("goal_id must be goal_<24 hex>");
  if (!validSignals(input?.source_signals)) errors.push("source_signals must be a non-empty typed list");
  if (!AUTONOMY_RECOMMENDED_ENUM.includes(input?.recommended_next)) errors.push("recommended_next is not in the frozen enum");
  if (!input?.draft_patch || typeof input.draft_patch !== "object") errors.push("draft_patch is required for skill_claim");
  else {
    if (!/^draft_[0-9a-f]{24}$/.test(input.draft_patch.draft_id ?? "")) errors.push("draft_patch.draft_id invalid");
    if (typeof input.draft_patch.target_skill_id !== "string" || input.draft_patch.target_skill_id.length === 0) errors.push("target_skill_id required");
    if (!/^[0-9a-f]{64}$/.test(input.draft_patch.sha256 ?? "")) errors.push("draft_patch.sha256 invalid");
    if (!Number.isInteger(input.draft_patch.bytes) || input.draft_patch.bytes < 1) errors.push("draft_patch.bytes invalid");
    if (input.draft_patch.applied !== false) errors.push("draft_patch.applied must be false");
  }
  return { ok: errors.length === 0, errors, inputSchema: "skill-claim-1.0" };
}

export async function runAutonomyPipelineDryRun({
  agentDid,
  drives = [],
  driveIds = [],
  skillStore,
  memoryHub,
  traceStore = null,
  budgetTokens = 5000,
  now = Date.now(),
} = {}) {
  if (typeof agentDid !== "string" || agentDid.length === 0) throw new Error("agent_did is required");
  if (!Array.isArray(drives) || drives.length === 0) throw new Error("drives are required");
  if (!Number.isInteger(budgetTokens) || budgetTokens < 1) throw new Error("budget_tokens must be a positive integer");
  const startedAt = now;
  const started = performance.now();
  const steps = [];
  const work = await mkdtemp(path.join(os.tmpdir(), "im2a-autonomy-"));
  let buffer = null;
  try {
    const proposeStarted = performance.now();
    const proposal = await drivePropose({
      drives,
      driveIds,
      agentDid,
      skillStore,
      memoryHub,
      traceStore,
      estimatedTokenCost: budgetTokens,
      recentIntentCount: {},
      now,
    });
    const candidates = proposal.proposed_goals ?? [];
    const selected = candidates.find((goal) => goal.source_signals?.some((signal) => signal.type === "skill")) ?? candidates[0];
    if (!selected) throw new Error("drive_propose returned no proposal");
    steps.push({
      name: "drive_propose",
      duration_ms: Math.max(0, Math.round((performance.now() - proposeStarted) * 1000) / 1000),
      ok: true,
      proposed_goal_id: selected.goal_id,
      signal_count: selected.source_signals.length,
      priority_score: selected.priority_score,
      schema_validation: { ok: true, candidate_count: candidates.length },
    });

    const scheduleStarted = performance.now();
    buffer = new GoalBuffer({ path: path.join(work, "goal-buffer.db") });
    const admitted = buffer.admit({
      driveId: selected.drive_id,
      intentSummary: selected.intent_summary,
      sourceSignals: selected.source_signals,
      priorityScore: selected.priority_score,
    });
    const goal = admitted.goal;
    const scheduled = await scheduleGoal({
      goalBuffer: buffer,
      goalId: goal.goal_id,
      agentDid,
      budgetTokens,
      skillStore,
      memoryHub,
      draftsDir: path.join(work, "drafts"),
      reservedAvailable: true,
      now,
    });
    const execution = scheduled.execution_result;
    const finalGoal = buffer.get(goal.goal_id) ?? goal;
    const draftBody = execution.draft_patch ? await readFile(execution.draft_patch.path, "utf8") : "";
    steps.push({
      name: "goal_schedule",
      duration_ms: Math.max(0, Math.round((performance.now() - scheduleStarted) * 1000) / 1000),
      ok: true,
      goal_id: goal.goal_id,
      status: scheduled.goal.status,
      draft_patch: execution.draft_patch,
      draft_body_sha256: execution.draft_patch ? requireHash(draftBody) : null,
      schema_validation: {
        goal_buffer: validateGoalBuffer(finalGoal),
        execution_result: validateExecutionResult(execution),
      },
    });

    const claimStarted = performance.now();
    const claimInput = {
      goal_id: finalGoal.goal_id,
      agent_did: agentDid,
      intent_summary: finalGoal.intent_summary,
      source_signals: finalGoal.source_signals,
      draft_patch: execution.draft_patch,
      recommended_next: execution.recommended_next,
      contribution_log_ref: { goal_id: finalGoal.goal_id, drive_id: finalGoal.drive_id },
    };
    const claimValidation = validateSkillClaim(claimInput);
    steps.push({
      name: "skill_claim(simulated)",
      duration_ms: Math.max(0, Math.round((performance.now() - claimStarted) * 1000) / 1000),
      ok: claimValidation.ok,
      goal_id: goal.goal_id,
      schema_validation: claimValidation,
    });

    const proposalSignals = selected.source_signals ?? [];
    const goalSignals = finalGoal.source_signals ?? [];
    const claimSignals = claimInput.source_signals ?? [];
    const missing = proposalSignals.filter((signal) =>
      !goalSignals.some((item) => item.type === signal.type && item.id === signal.id)
      || !claimSignals.some((item) => item.type === signal.type && item.id === signal.id));
    const traceability = missing.length === 0 ? "full" : "partial";
    const durationMs = Math.max(0, Math.round((performance.now() - started) * 1000) / 1000);
    const goalValidation = validateGoalBuffer(finalGoal);
    const report = {
      schema_version: AUTONOMY_PIPELINE_VERSION,
      agent_did: agentDid,
      started_at: nowIso(startedAt),
      finished_at: new Date(now + durationMs).toISOString(),
      duration_ms: durationMs,
      under_45_seconds: durationMs < 45000,
      ok: goalValidation.ok && steps.every((item) => item.ok) && traceability === "full",
      steps,
      signal_traceability: traceability,
      missing_signals: missing,
      goal_buffer: finalGoal,
      execution_result: execution,
      skill_claim: claimValidation,
      schema_validation: {
        goal_buffer: goalValidation,
        execution_result: validateExecutionResult(execution),
        skill_claim: claimValidation,
      },
      goal_status_enum: AUTONOMY_STATUS_ENUM,
      recommended_next_enum: AUTONOMY_RECOMMENDED_ENUM,
      frozen: {
        goal_buffer_schema: "goal-buffer-1.0",
        execution_result_schema: "execution-result-1.0",
        skill_claim_schema: "skill-claim-1.0",
        contribution_log_ref: "goal_id",
      },
    };
    return report;
  } finally {
    if (buffer) buffer.close();
    await rm(work, { recursive: true, force: true });
  }
}

function requireHash(text) {
  return `sha256:${createHash("sha256").update(text).digest("hex")}`;
}
