import assert from "node:assert/strict";
import test from "node:test";
import {
  TOPOLOGY_METRICS_VERSION,
  TopologyMetricsError,
  computeTopologyMetrics,
  digestTopologyMetrics,
} from "../packages/dsh-im2a-plugin/kernel/topology-metrics.mjs";

const clock = () => new Date("2026-11-15T00:00:00Z");

const event = (success) => ({ span_type: "tool_call", name: "x", success });
const trace = (did, results) => ({ trace_id: "0".repeat(32), agent_did: did, events: results.map(event) });
const meter = (did) => ({ agent_did: did, record_id: "meter_0123456789abcdef01234567" });

const effectivePatch = (author) => ({
  action: "patch",
  agent_did: author,
  validation_evidence: {
    post_patch_success_count: 3,
    post_patch_failure_count: 0,
    verifier_agents: ["did:im2a:reviewer"],
  },
});
const selfVerifiedPatch = (author) => ({
  action: "patch",
  agent_did: author,
  validation_evidence: {
    post_patch_success_count: 1,
    post_patch_failure_count: 0,
    verifier_agents: [author],
  },
});

test("an empty snapshot is explicit about unavailable factors, not zero", () => {
  const metrics = computeTopologyMetrics({ now: clock });
  assert.equal(metrics.schema_version, TOPOLOGY_METRICS_VERSION);
  assert.equal(metrics.node_count.value, 0);
  assert.equal(metrics.quality_index.value, null);
  assert.equal(metrics.quality_index.available, false);
  assert.equal(metrics.attribution_precision.value, null);
  assert.equal(metrics.attribution_precision.available, false);
  assert.equal(metrics.composite.value, null);
  assert.equal(metrics.composite.available, false);
});

test("nodes are deduplicated across all three record sources", () => {
  const metrics = computeTopologyMetrics({
    meteringRecords: [meter("did:im2a:a"), meter("did:im2a:a")],
    traces: [trace("did:im2a:a", [true]), trace("did:im2a:b", [true])],
    contributionEntries: [{ action: "create", agent_did: "did:im2a:c" }],
    now: clock,
  });
  assert.equal(metrics.node_count.value, 3);
  assert.equal(metrics.sources.metering_records, 2);
  assert.equal(metrics.sources.traces, 2);
  assert.equal(metrics.sources.contribution_entries, 1);
});

test("quality is the observed execution success ratio", () => {
  const metrics = computeTopologyMetrics({
    traces: [trace("did:im2a:a", [true, true, false]), trace("did:im2a:b", [true])],
    now: clock,
  });
  assert.equal(metrics.quality_index.value, 0.75);
  assert.equal(metrics.quality_index.available, true);
  assert.deepEqual(metrics.quality_index.events, { total: 4, successful: 3 });
});

test("attribution precision counts only independently verified patches", () => {
  const metrics = computeTopologyMetrics({
    contributionEntries: [
      { action: "create", agent_did: "did:im2a:a" },
      effectivePatch("did:im2a:a"),
      selfVerifiedPatch("did:im2a:b"),
    ],
    now: clock,
  });
  assert.equal(metrics.attribution_precision.value, 0.5);
  assert.deepEqual(metrics.attribution_precision.patches, { total: 2, effective: 1 });
});

test("the composite appears only when every factor is available", () => {
  const complete = computeTopologyMetrics({
    meteringRecords: [meter("did:im2a:a")],
    traces: [trace("did:im2a:a", [true, true, true, false])],
    contributionEntries: [effectivePatch("did:im2a:a"), selfVerifiedPatch("did:im2a:b")],
    now: clock,
  });
  assert.equal(complete.composite.available, true);
  // 2 nodes (a from metering/trace/patch, b from the self-verified patch)
  // x 0.75 success x 0.5 precision
  assert.equal(complete.node_count.value, 2);
  assert.equal(complete.composite.value, 0.75);
  assert.match(complete.composite.formula, /node_count/);

  const missingPrecision = computeTopologyMetrics({
    meteringRecords: [meter("did:im2a:a")],
    traces: [trace("did:im2a:a", [true])],
    now: clock,
  });
  assert.equal(missingPrecision.attribution_precision.available, false);
  assert.equal(missingPrecision.composite.value, null);
});

test("the snapshot never claims credit tiers it does not have", () => {
  const metrics = computeTopologyMetrics({ traces: [trace("did:im2a:a", [true])], now: clock });
  assert.equal(metrics.quality_index.credit_tier_coverage, 0);
  assert.match(metrics.quality_index.credit_source, /identity module .* planned/);
});

test("the digest ignores generation time but tracks content", () => {
  const records = {
    meteringRecords: [meter("did:im2a:a")],
    traces: [trace("did:im2a:a", [true, false])],
    contributionEntries: [effectivePatch("did:im2a:a")],
  };
  const first = computeTopologyMetrics({ ...records, now: clock });
  const second = computeTopologyMetrics({ ...records, now: () => new Date("2027-03-03T03:03:03Z") });
  assert.notEqual(first.generated_at, second.generated_at);
  assert.equal(digestTopologyMetrics(first), digestTopologyMetrics(second));
  assert.match(digestTopologyMetrics(first), /^sha256:[0-9a-f]{64}$/);

  const changed = computeTopologyMetrics({
    ...records,
    traces: [trace("did:im2a:a", [true, true])],
    now: clock,
  });
  assert.notEqual(digestTopologyMetrics(first), digestTopologyMetrics(changed));
});

test("bad input is rejected instead of silently coerced", () => {
  assert.throws(
    () => computeTopologyMetrics({ traces: "not-an-array" }),
    (error) => error instanceof TopologyMetricsError && error.code === "invalid_input",
  );
  assert.throws(
    () => digestTopologyMetrics(null),
    (error) => error.code === "invalid_snapshot",
  );
});
