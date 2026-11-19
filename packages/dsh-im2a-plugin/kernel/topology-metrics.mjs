/**
 * WP-5 of the six-layer plan: topology metrics.
 *
 * The framework's claim is 协同效应 ≈ f(节点数 × 连接质量 × 归因精度). This module
 * computes the three factors from records the system ALREADY keeps — metering
 * records (WP-2), traces (v2.5.4) and contribution entries (v2.7.x) — so a
 * snapshot is a recomputation, not a new collection surface.
 *
 * Two honesty constraints are built into the shape rather than left to prose:
 *
 *   - `quality_index` is the observed execution success ratio, NOT a credit
 *     tier. The identity module (2.5) is still `planned`, so there is no credit
 *     data to read; the snapshot says so instead of inventing tiers.
 *   - `composite` is a relative indicator for comparing snapshots. It is not an
 *     intelligence measure and the module never claims "exponential emergence":
 *     the product is bounded by node_count and falls to 0 with either factor.
 */
import { createHash } from "node:crypto";
import { canonicalJson } from "./did-portability.mjs";

export const TOPOLOGY_METRICS_VERSION = "1.0";

/**
 * A patch counts only when it carries valid validation evidence AND a verifier
 * who is not the author.
 *
 * This mirrors the audit rule in the bridge's skill-claim.mjs on purpose: the
 * plugin package must not import across package boundaries, because the
 * published tarball contains this package only. The rule is small and stable,
 * so duplicating it here is cheaper than adding a runtime dependency on the
 * bridge — and `tests/package-boundary.test.mjs` keeps the duplication honest.
 */
export function isEffectivePatch(entry) {
  if (!entry || entry.action !== "patch" || !entry.validation_evidence) return false;
  const evidence = entry.validation_evidence;
  if (!Number.isInteger(evidence.post_patch_success_count) || evidence.post_patch_success_count < 0) return false;
  if (!Number.isInteger(evidence.post_patch_failure_count) || evidence.post_patch_failure_count < 0) return false;
  if (!Array.isArray(evidence.verifier_agents)) return false;
  return evidence.verifier_agents.some((did) => typeof did === "string" && did !== entry.agent_did);
}

export class TopologyMetricsError extends Error {
  constructor(code, message, detail = {}) {
    super(message);
    this.name = "TopologyMetricsError";
    this.code = code;
    this.detail = detail;
  }
}

function ratio(numerator, denominator) {
  return denominator > 0 ? Number((numerator / denominator).toFixed(4)) : null;
}

function requireArray(value, field) {
  if (!Array.isArray(value)) {
    throw new TopologyMetricsError("invalid_input", `${field} must be an array`, { field });
  }
  return value;
}

export function computeTopologyMetrics({
  meteringRecords = [],
  traces = [],
  contributionEntries = [],
  now = () => new Date(),
} = {}) {
  requireArray(meteringRecords, "meteringRecords");
  requireArray(traces, "traces");
  requireArray(contributionEntries, "contributionEntries");

  // node_count: any agent that left a record is a node, across all three sources.
  const nodes = new Set();
  for (const record of meteringRecords) {
    if (typeof record?.agent_did === "string" && record.agent_did.length > 0) nodes.add(record.agent_did);
  }
  for (const trace of traces) {
    if (typeof trace?.agent_did === "string" && trace.agent_did.length > 0) nodes.add(trace.agent_did);
  }
  for (const entry of contributionEntries) {
    if (typeof entry?.agent_did === "string" && entry.agent_did.length > 0) nodes.add(entry.agent_did);
  }

  // quality_index: execution reliability actually observed, not a credit tier.
  let totalEvents = 0;
  let successfulEvents = 0;
  for (const trace of traces) {
    for (const event of trace?.events ?? []) {
      if (typeof event?.success !== "boolean") continue;
      totalEvents += 1;
      if (event.success) successfulEvents += 1;
    }
  }
  const successRatio = ratio(successfulEvents, totalEvents);

  // attribution_precision: a patch only counts when it carries valid validation
  // evidence and a verifier who is not the author (same rule as the v2.7.2 audit).
  const patches = contributionEntries.filter((entry) => entry?.action === "patch");
  const effectivePatches = patches.filter(isEffectivePatch);
  const precision = ratio(effectivePatches.length, patches.length);

  const qualityAvailable = successRatio !== null;
  const precisionAvailable = precision !== null;
  const composite = nodes.size > 0 && qualityAvailable && precisionAvailable
    ? Number((nodes.size * successRatio * precision).toFixed(4))
    : null;

  return {
    schema_version: TOPOLOGY_METRICS_VERSION,
    generated_at: new Date(now()).toISOString(),
    sources: {
      metering_records: meteringRecords.length,
      traces: traces.length,
      contribution_entries: contributionEntries.length,
      trace_events: totalEvents,
      patches: patches.length,
    },
    node_count: {
      value: nodes.size,
      available: true,
      definition: "distinct agent_did seen in metering, trace or contribution records",
    },
    quality_index: {
      value: successRatio,
      available: qualityAvailable,
      definition: "observed execution success ratio over recorded trace events",
      credit_tier_coverage: 0,
      credit_source: "unavailable: the identity module (2.5) is planned; connection quality is proxied by execution reliability",
      events: { total: totalEvents, successful: successfulEvents },
    },
    attribution_precision: {
      value: precision,
      available: precisionAvailable,
      definition: "effective patches / all patches; effective means valid validation evidence plus an independent verifier",
      patches: { total: patches.length, effective: effectivePatches.length },
    },
    composite: {
      value: composite,
      available: composite !== null,
      formula: "node_count x quality_index x attribution_precision",
      note: "relative indicator for comparing snapshots; not an intelligence measure, not a claim of exponential emergence",
    },
  };
}

/**
 * Content digest of a snapshot, excluding `generated_at`, so two snapshots over
 * the same records compare equal regardless of when they were taken.
 */
export function digestTopologyMetrics(metrics) {
  if (!metrics || typeof metrics !== "object") {
    throw new TopologyMetricsError("invalid_snapshot", "metrics must be an object");
  }
  const { generated_at: _ignored, ...content } = metrics;
  return `sha256:${createHash("sha256").update(canonicalJson(content)).digest("hex")}`;
}
