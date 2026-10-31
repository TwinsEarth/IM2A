/**
 * v2.5.5 read-only reflection trigger.
 *
 * This module reads an existing trace and returns suggestions. It never writes
 * a skill, memory, proposal or trace. Candidate generation is deliberately
 * abstract: source traces provide evidence, not steps to copy.
 */
import { loadReflectConfig } from "./reflect-config.mjs";
import { canonicalJson, sha256Hex } from "./trace-store.mjs";

export const FOCUS_VALUES = Object.freeze([
  "failure_pattern",
  "success_pattern",
  "bottleneck",
  "coordination",
]);

export class ReflectError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "ReflectError";
    this.code = code;
  }
}

function round(value, digits = 3) {
  const factor = 10 ** digits;
  return Math.round(value * factor) / factor;
}

function clamp(value, min, max) {
  return Math.min(max, Math.max(min, value));
}

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function assertTrace(record) {
  if (!isPlainObject(record) || !Array.isArray(record.events) || record.events.length === 0) {
    throw new ReflectError("TRACE_INVALID", "trace has no events");
  }
}

function candidateConfidence(config, { toolNorm, uniqueNorm, recovery, coordination }) {
  const raw = config.confidence.base
    + config.confidence.toolCalls * toolNorm
    + config.confidence.uniqueSpans * uniqueNorm
    + config.confidence.failureRecovery * (recovery ? 1 : 0)
    + config.confidence.coordination * (coordination ? 1 : 0);
  return round(clamp(raw, 0.05, Math.min(0.99, config.confidence.max)));
}

/**
 * Assess one trace. Exported for unit tests and for future v2.6 consumers.
 */
export function assessTrace(record, { config, focus } = {}) {
  if (!config) throw new ReflectError("REFLECT_CONFIG_INVALID", "reflect config is required");
  if (focus !== undefined && !FOCUS_VALUES.includes(focus)) {
    throw new ReflectError("INVALID_FOCUS", `focus must be one of ${FOCUS_VALUES.join(", ")}`);
  }
  assertTrace(record);

  const events = record.events;
  const toolCalls = events.filter((event) => event.span_type === "tool_call").length;
  const uniqueNames = new Set(events.map((event) => event.name)).size;
  const totalTokens = events.reduce((sum, event) => sum + (Number(event.token_cost) || 0), 0);
  const maxDuration = events.reduce((max, event) => Math.max(max, Number(event.duration_ms) || 0), 0);
  const finalSuccess = events[events.length - 1]?.success === true;
  const failureRecovery = events.some((event, index) =>
    event.success === false && events.slice(index + 1).some((later) => later.success === true));
  // v2.5.5 has no peer-agent field in the 1.0 trace contract. Bus send/recv
  // spans are the available evidence of a cross-agent sequence; v2.6 will add
  // explicit peer attribution.
  const coordination = events.some((event) =>
    event.name === "im2a_send" || event.name === "im2a_recv" || event.name.startsWith("agent:"));

  const toolNorm = Math.min(toolCalls / config.minToolCalls, 1);
  const uniqueNorm = Math.min(uniqueNames / Math.max(config.minUniqueSpans, 1), 1);
  const tokenNorm = Math.min(totalTokens / 1000, 1);
  const complexityScore = round(
    config.weights.toolCalls * toolNorm
    + config.weights.uniqueSpans * uniqueNorm
    + config.weights.failureRecovery * (failureRecovery ? 1 : 0)
    + config.weights.coordination * (coordination ? 1 : 0)
    + config.weights.tokenCost * tokenNorm,
  );

  const reasons = [];
  const enoughCalls = toolCalls >= config.minToolCalls;
  if (enoughCalls) reasons.push("tool_calls_at_or_above_min");
  else reasons.push("insufficient_tool_calls");
  if (failureRecovery && config.enableFailureRecovery) reasons.push("failure_recovery_present");
  if (coordination && config.enableCoordination) reasons.push("coordination_present");
  if (!failureRecovery && config.enableFailureRecovery) reasons.push("no_failure_recovery");
  if (!coordination && config.enableCoordination) reasons.push("no_coordination");
  if (finalSuccess) reasons.push("successful_terminal_span");
  if (!finalSuccess) reasons.push("terminal_span_not_successful");
  if (focus) reasons.push(`focus_${focus}`);

  const hasSignal = enoughCalls
    || (failureRecovery && config.enableFailureRecovery)
    || (coordination && config.enableCoordination);
  const successGate = !config.requireSuccess || finalSuccess;
  const aboveFloor = complexityScore >= config.complexityFloor;
  if (!aboveFloor) reasons.push("complexity_below_floor");
  const worthReflecting = Boolean(hasSignal && successGate && aboveFloor);

  const confidence = candidateConfidence(config, {
    toolNorm,
    uniqueNorm,
    recovery: failureRecovery,
    coordination,
  });
  const candidates = [];
  const add = (candidateType, summary) => {
    if (candidates.some((candidate) => candidate.summary === summary)) return;
    candidates.push({
      candidate_id: `cand_${sha256Hex(canonicalJson({
        trace_id: record.trace_id,
        candidate_type: candidateType,
        summary,
      })).slice(0, 24)}`,
      candidate_type: candidateType,
      summary,
      source_trace_ids: [record.trace_id],
      confidence,
    });
  };

  if (worthReflecting && failureRecovery) {
    const hasSkill = events.some((event) => event.attributes?.["im2a.skill_version"] && event.attributes["im2a.skill_version"] !== "none");
    add(hasSkill ? "skill_patch" : "principle_extract",
      "Recover from a failed tool step by switching the retrieval path and verifying the replacement result before continuing.");
  }
  if (worthReflecting && coordination) {
    add("principle_extract",
      "For cross-agent work, persist the handoff artifact and require an explicit acknowledgement before continuing.");
  }
  if (worthReflecting && enoughCalls) {
    add(uniqueNames >= Math.max(3, config.minUniqueSpans) ? "skill_create" : "principle_extract",
      "For multi-step tasks, checkpoint intermediate results and validate the slowest step before adding more calls.");
  }
  if (worthReflecting && (focus === "bottleneck" || maxDuration >= 1000)) {
    add("principle_extract",
      "When latency concentrates in one step, measure and batch that step before adding parallel calls.");
  }

  return {
    assessment: {
      trace_id: record.trace_id,
      worth_reflecting: worthReflecting,
      reason_codes: [...new Set(reasons)],
      complexity_score: complexityScore,
    },
    distilled_candidates: worthReflecting ? candidates.slice(0, config.maxCandidates) : [],
  };
}

/**
 * Read-only entrypoint used by the MCP tool.
 */
export async function reflectOnTrace({ traceStore, traceId, agentDid, focus, config, env } = {}) {
  if (!traceStore || typeof traceStore.get !== "function") {
    throw new ReflectError("TRACE_STORE_UNAVAILABLE", "trace store is unavailable");
  }
  if (typeof traceId !== "string" || !/^[0-9a-f]{32}$/.test(traceId)) {
    throw new ReflectError("TRACE_INVALID", "trace_id must be 32 lowercase hex characters");
  }
  if (typeof agentDid !== "string" || agentDid.length === 0) {
    throw new ReflectError("TRACE_INVALID", "agent_did is required");
  }
  const resolvedConfig = config ?? loadReflectConfig({ env });
  const record = await traceStore.get(traceId);
  if (!record) throw new ReflectError("TRACE_NOT_FOUND", `trace not found: ${traceId}`);
  if (record.agent_did !== agentDid) {
    throw new ReflectError("TRACE_AGENT_MISMATCH", "agent_did does not own this trace");
  }
  return assessTrace(record, { config: resolvedConfig, focus });
}
