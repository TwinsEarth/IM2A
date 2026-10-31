/**
 * v2.6.4 read-only drive calibration.
 *
 * Reads goal history, execution deltas and proposal adoption telemetry and
 * returns suggestions. It never edits drives/ and never calls skill_guard.
 */
export const DRIVE_CALIBRATION_VERSION = "1.0";

export class DriveCalibrateError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "DriveCalibrateError";
    this.code = code;
  }
}

function ratio(numerator, denominator) {
  return denominator > 0 ? Math.round((numerator / denominator) * 1000) / 1000 : null;
}

function mean(values) {
  if (values.length === 0) return null;
  return Math.round((values.reduce((sum, value) => sum + value, 0) / values.length) * 1000) / 1000;
}

function suggestionFor(drive, goals, executions, proposals) {
  const completed = goals.filter((goal) => goal.status === "completed").length;
  const expired = goals.filter((goal) => goal.status === "expired").length;
  const total = goals.length;
  const adopted = proposals.filter((proposal) => proposal.adopted_goal_id !== null).length;
  const proposalAcceptanceRate = ratio(adopted, proposals.length);
  const executionYield = ratio(executions.filter((execution) => execution.execution_yield).length, executions.length);
  const expiryRate = ratio(expired, total);
  const deltas = executions.map((execution) => execution.confidence_delta).filter((value) => Number.isFinite(value));
  const meanDelta = mean(deltas);
  let priorityAdjustment = "unchanged";
  let priorityDelta = 0;
  if (total > 0 && (expiryRate >= 0.4 || (meanDelta !== null && meanDelta < 0) || (proposalAcceptanceRate !== null && proposalAcceptanceRate < 0.3))) {
    priorityAdjustment = "decrease";
    priorityDelta = -0.05;
  } else if (total > 0 && completed >= 3 && (executionYield ?? 0) >= 0.5 && meanDelta !== null && meanDelta >= 0.15 && (proposalAcceptanceRate === null || proposalAcceptanceRate >= 0.3)) {
    priorityAdjustment = "increase";
    priorityDelta = 0.05;
  }
  const triggerHintRefinement = priorityAdjustment === "decrease"
    ? `Add an expiry guard and require confidence_delta >= 0 before proposing; observed expiry_rate=${expiryRate ?? 0}.`
    : priorityAdjustment === "increase"
      ? `Narrow to high-yield execution evidence; observed execution_yield=${executionYield ?? 0} and mean_delta=${meanDelta ?? 0}.`
      : "Keep the current trigger wording; the observed sample does not justify a change.";
  const evidence = goals.slice(0, 20).map((goal) => {
    const execution = executions.find((item) => item.goal_id === goal.goal_id);
    return {
      goal_id: goal.goal_id,
      status: goal.status,
      confidence_delta: execution?.confidence_delta ?? null,
      execution_yield: execution?.execution_yield ?? null,
    };
  });
  return {
    drive_id: drive.drive_id,
    priority_adjustment: priorityAdjustment,
    priority_delta: priorityDelta,
    trigger_hint_refinement: triggerHintRefinement,
    evidence,
    metrics: {
      goal_count: total,
      completed_count: completed,
      expired_count: expired,
      execution_count: executions.length,
      proposal_acceptance_rate: proposalAcceptanceRate,
      execution_yield: executionYield,
      expiry_rate: expiryRate,
      mean_confidence_delta: meanDelta,
      confidence_delta_distribution: deltas,
    },
  };
}

export function calibrateDrives({ agentDid, lookbackDays = 14, drives, snapshot, now = Date.now() } = {}) {
  if (!agentDid || typeof agentDid !== "string") throw new DriveCalibrateError("INVALID_AGENT_DID");
  const days = Number(lookbackDays);
  if (!Number.isInteger(days) || days < 1 || days > 90) throw new DriveCalibrateError("INVALID_LOOKBACK_DAYS");
  if (!Array.isArray(drives)) throw new DriveCalibrateError("DRIVE_INVALID");
  if (!snapshot || !Array.isArray(snapshot.goals) || !Array.isArray(snapshot.executions) || !Array.isArray(snapshot.proposals)) {
    throw new DriveCalibrateError("CALIBRATION_SNAPSHOT_INVALID");
  }
  const from = now - days * 24 * 60 * 60 * 1000;
  const suggestions = drives.map((drive) => suggestionFor(
    drive,
    snapshot.goals.filter((goal) => goal.drive_id === drive.drive_id),
    snapshot.executions.filter((execution) => execution.drive_id === drive.drive_id),
    snapshot.proposals.filter((proposal) => proposal.drive_id === drive.drive_id),
  ));
  return {
    schema_version: DRIVE_CALIBRATION_VERSION,
    agent_did: agentDid,
    lookback_days: days,
    window: { from: new Date(from).toISOString(), to: new Date(now).toISOString() },
    drive_count: drives.length,
    calibration_suggestions: suggestions,
    read_only: true,
    drives_modified: false,
    generated_at: new Date(now).toISOString(),
  };
}
