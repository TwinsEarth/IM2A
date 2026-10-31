/**
 * v2.6.3 restricted goal execution.
 *
 * Allowed inputs are read-only signals plus a draft-only patch artifact. No
 * skill package is written and no goal is crystallized.
 */
import { writeDraft } from "./drafts.mjs";

export const RECOMMENDED_NEXT = Object.freeze(["crystallize", "patch", "discard"]);

export class GoalExecutionError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "GoalExecutionError";
    this.code = code;
  }
}

function round(value) {
  return Math.round(value * 100) / 100;
}

export async function scheduleGoal({
  goalBuffer,
  goalId,
  agentDid,
  budgetTokens,
  skillStore,
  memoryHub,
  draftsDir,
  reservedAvailable = true,
  now = Date.now(),
} = {}) {
  const goal = goalBuffer.get(goalId);
  if (!goal) throw new GoalExecutionError("GOAL_NOT_FOUND");
  if (!["pending", "scheduled"].includes(goal.status)) throw new GoalExecutionError("INVALID_GOAL_STATE", `goal is ${goal.status}`);
  if (!reservedAvailable) throw new GoalExecutionError("RESERVED_POOL_UNAVAILABLE");
  if (goal.status === "pending") {
    goalBuffer.transition(goalId, "scheduled", { actor: "goal_schedule", reason: "reserved_pool_allocation" });
  }
  goalBuffer.transition(goalId, "executing", { actor: "goal_schedule", reason: "restricted_execution" });
  let execution;
  try {
    execution = await executeGoal({
      goal: goalBuffer.get(goalId),
      agentDid,
      budgetTokens,
      skillStore,
      memoryHub,
      draftsDir,
      now,
    });
  } catch (error) {
    try {
      goalBuffer.transition(goalId, "expired", { actor: "goal_schedule", reason: String(error.message).slice(0, 200) });
      goalBuffer.recordExecution({
        goalId,
        driveId: goal.drive_id,
        status: "expired",
        confidenceDelta: 0,
        reflectionNotes: `execution failed: ${String(error.message).slice(0, 200)}`,
        recommendedNext: "discard",
      });
    } catch { /* best effort */ }
    throw error;
  }
  const completed = goalBuffer.transition(goalId, "completed", { actor: "goal_schedule", reason: "execution_result_ready" });
  goalBuffer.recordExecution({
    goalId,
    driveId: completed.drive_id,
    status: "completed",
    confidenceDelta: execution.confidence_delta,
    draftPatch: execution.draft_patch,
    reflectionNotes: execution.reflection_notes,
    recommendedNext: execution.recommended_next,
  });
  return { goal: completed, execution_result: execution };
}

export async function executeGoal({
  goal,
  agentDid,
  budgetTokens,
  skillStore,
  memoryHub,
  draftsDir,
  now = Date.now(),
}) {
  if (!goal) throw new GoalExecutionError("GOAL_NOT_FOUND");
  if (!Number.isInteger(budgetTokens) || budgetTokens < 1) throw new GoalExecutionError("INVALID_BUDGET_TOKENS");
  if (!agentDid || typeof agentDid !== "string") throw new GoalExecutionError("INVALID_AGENT_DID");
  const skillSignal = (goal.source_signals ?? []).find((signal) => signal.type === "skill");
  const memorySignal = (goal.source_signals ?? []).find((signal) => signal.type === "memory_item");
  const notes = [];
  let matchedSkill = null;
  let memoryItems = [];
  if (skillSignal) {
    const audit = skillStore.curatorAudit({ agentDid, scope: "agent_owned", now });
    matchedSkill = audit.skills.find((skill) => skill.skill_id === skillSignal.id) ?? null;
    notes.push(matchedSkill
      ? `curator_audit matched ${skillSignal.id} (${matchedSkill.lifecycle_state}, ${matchedSkill.patch_count} patches).`
      : `curator_audit did not return ${skillSignal.id}; draft is evidence-only.`);
  }
  if (memorySignal) {
    const recalled = memoryHub.recall({
      query: goal.intent_summary,
      agentDid,
      budgetTokens,
      includeStale: true,
    });
    memoryItems = recalled.items;
    notes.push(`memory_recall_v2 returned ${memoryItems.length} read-only item(s).`);
  }
  if (!skillSignal && !memorySignal) notes.push("no read-only source signal was available.");
  const estimatedDraftBytes = Buffer.byteLength(goal.intent_summary, "utf8") + 512;
  if (Math.ceil(estimatedDraftBytes / 4) > budgetTokens) {
    throw new GoalExecutionError("EXECUTION_BUDGET_EXCEEDED", "budget too small for a draft");
  }
  let draft = null;
  if (skillSignal) {
    draft = await writeDraft({
      dir: draftsDir,
      goalId: goal.goal_id,
      targetSkillId: skillSignal.id,
      intentSummary: goal.intent_summary,
      sourceSignals: goal.source_signals,
      now,
    });
    notes.push(`draft_patch written to ${draft.path}; applied=false; no skill package write.`);
  }
  const signalBoost = matchedSkill ? 0.1 : skillSignal ? 0.03 : 0.02;
  const confidenceDelta = round((Number(goal.priority_score) - 0.55) * 0.8 + signalBoost);
  let recommendedNext;
  let recommendationReason;
  if (confidenceDelta < 0) {
    recommendedNext = "discard";
    recommendationReason = "confidence_delta is negative; do not crystallize.";
  } else if (draft && confidenceDelta >= 0.15) {
    recommendedNext = "crystallize";
    recommendationReason = "positive confidence_delta and a draft patch exist; still requires v2.7 ownership.";
  } else {
    recommendedNext = "patch";
    recommendationReason = "non-negative but below crystallize threshold; keep as patch candidate.";
  }
  const reflectionNotes = notes.join(" ");
  const used = Math.ceil((
    Buffer.byteLength(reflectionNotes, "utf8")
    + (draft ? draft.bytes : 0)
    + Buffer.byteLength(goal.intent_summary, "utf8")
  ) / 4);
  if (used > budgetTokens) throw new GoalExecutionError("EXECUTION_BUDGET_EXCEEDED", "execution exceeded reserved budget");
  return {
    goal_id: goal.goal_id,
    agent_did: agentDid,
    draft_patch: draft ? {
      draft_id: draft.draft_id,
      target_skill_id: draft.target_skill_id,
      path: draft.path,
      sha256: draft.sha256,
      bytes: draft.bytes,
      applied: false,
    } : null,
    reflection_notes: reflectionNotes,
    confidence_delta: confidenceDelta,
    recommended_next: recommendedNext,
    recommendation_reason: recommendationReason,
    budget: { requested: budgetTokens, used, source: "reserved_pool" },
    read_only_tools: ["curator_audit", "memory_recall_v2"],
    memory_items: memoryItems.length,
    drafted_skill_ids: draft ? [draft.target_skill_id] : [],
  };
}
