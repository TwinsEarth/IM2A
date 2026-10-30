/**
 * v2.5.0 boundary checks (draft): capability defaults, metric catalog, and the
 * gate record's own promotion guard.
 *
 * The gate-record check exists because a record can be edited by hand minutes
 * before a release. If it claims PASS while still containing NOT STARTED items
 * or unfilled <placeholders>, the promotion is rejected here.
 *
 * Usage: node scripts/check-v250-boundaries.mjs
 */

import { readFile } from "node:fs/promises";
import { sha256Canonical } from "./lib/trajectory-contract.mjs";

const load = async (rel) => JSON.parse(await readFile(new URL(rel, import.meta.url), "utf8"));
const read = async (rel) => readFile(new URL(rel, import.meta.url), "utf8");

const defaults = await load("../docs/contracts/capability-defaults-1.0.json");
const metrics = await load("../docs/contracts/eval-metrics-1.0.json");
const policy = await load("../docs/contracts/redaction-policy-1.0.json");
const recordSchema = await load("../docs/contracts/trajectory-record.schema.json");
const gateRecord = await read("../docs/design/GATE-RECORD-v2.5.0-SFT-SYNTHETIC.md");

const results = [];
function check(name, ok, detail) {
  results.push({ name, ok });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${ok ? "" : `: ${JSON.stringify(detail)}`}`);
}

/* Capability defaults: absence of a decision is off. */
const caps = defaults.capabilities;
const on = caps.filter((cap) => cap.default === "on").map((cap) => cap.id);
const off = caps.filter((cap) => cap.default === "off").map((cap) => cap.id);
check("exactly 12 v2.5.0 capabilities are enumerated", caps.length === 12, caps.length);
check("capability ids are unique", new Set(caps.map((cap) => cap.id)).size === caps.length);
check("exactly two capabilities are on by default", on.length === 2, on);
check("the default-on set is synthetic generation plus offline eval", on.sort().join(",") === ["offline_eval_harness", "synthetic_trajectory_generation"].join(","), on);
check("all other capabilities are off", off.length === 10, off);
for (const cap of caps) {
  check(`capability ${cap.id} names a gate, owner, evidence, and stop condition`,
    Boolean(cap.gate && cap.owner && cap.evidenceRef && cap.stopCondition && cap.reason));
}
for (const required of ["production_trajectory_capture", "consented_sft", "dpo_preference_pairs", "grpo_rlvr", "auto_promote_to_reply", "checkpoint_auto_register_or_resume"]) {
  check(`high-risk capability ${required} is off`, off.includes(required));
}

/* Metrics: behaviour is the quality claim; RL is defined but disabled. */
const metricIds = metrics.metrics.map((metric) => metric.id);
check("metric ids are unique", new Set(metricIds).size === metricIds.length);
check("metric catalog is not frozen yet", metrics.frozen === false);
check("comparator names evaluator, prompt set, decoding, checkpoint", Boolean(
  metrics.comparator.evaluatorVersion && metrics.comparator.promptSetHash &&
  metrics.comparator.decoding && metrics.comparator.checkpointDigest));

const requiredBehavior = [
  "gate_violation_rate", "deny_retry_rate", "tool_argument_validity", "grounded_claim_rate",
  "cross_user_refusal_rate", "pii_leak_rate", "degraded_honesty_rate", "task_success_delta",
];
for (const id of requiredBehavior) {
  const metric = metrics.metrics.find((entry) => entry.id === id);
  check(`behaviour metric ${id} is enabled on the sample`, Boolean(metric && metric.enabled && metric.layer === "behavior"));
}

const rl = metrics.metrics.filter((metric) => metric.layer === "rl");
check("RL metrics are defined but disabled in v2.5.0", rl.length > 0 && rl.every((metric) => metric.enabled === false && metric.disabledReason), rl.map((metric) => metric.id));

const allowedOps = new Set(["=", ">=", "<=", ">", "<", "delta>=", "report"]);
for (const metric of metrics.metrics) {
  check(`metric ${metric.id} has a gate and a valid threshold op`,
    ["preflight", "smoke", "signal", "controlled"].includes(metric.gate) && allowedOps.has(metric.threshold.op) && (metric.threshold.op === "report" || metric.threshold.value !== null));
}

/* Redaction policy must not drift from the record schema's count keys. */
const countKeys = new Set(Object.keys(recordSchema.$defs.redaction.properties.counts.properties));
for (const entry of policy.classes) {
  if (entry.countKey) check(`redaction class ${entry.id} maps to a schema count key`, countKeys.has(entry.countKey), entry.countKey);
  check(`redaction class ${entry.id} has an explicit action`, typeof entry.action === "string" && entry.action.length > 0);
}
check("policy keeps free-text re-identification under manual review", policy.classes.some((entry) => entry.id === "freeText" && entry.action === "manual_review"));

/* Gate record: a PASS claim with NOT STARTED items or placeholders is refused. */
const requiredSections = [
  "## Behavior Target and Non-Goals",
  "## Fixed Comparator Contract",
  "## Gate: Preflight",
  "## Single Change Under Test",
  "## Required Measurements",
  "## Promotion or Stop Decision",
  "## Preserved Evidence",
];
for (const section of requiredSections) {
  const occurrences = gateRecord.split("\n").filter((line) => line.trim() === section).length;
  check(`gate record contains ${section} exactly once`, occurrences === 1, occurrences);
}
const statusMatch = gateRecord.match(/门禁状态[：:]\s*([^\n（(]+)/);
const status = statusMatch ? statusMatch[1].trim() : "UNKNOWN";
check("gate record declares a status", status !== "UNKNOWN", status);
if (status.startsWith("PASS")) {
  check("PASS gate record contains no NOT STARTED item", !/NOT STARTED/.test(gateRecord));
  check("PASS gate record contains no unfilled placeholder", !/<sha256:\.\.\.>|<approved>|<path>|<base-checkpoint-sha256>/.test(gateRecord));
}

const failed = results.filter((entry) => !entry.ok);
console.log(JSON.stringify({
  checks: results.length,
  passed: results.length - failed.length,
  gateStatus: status,
  defaultOn: on.sort(),
  defaultOff: off.length,
  metrics: metrics.metrics.length,
  enabledMetrics: metrics.metrics.filter((metric) => metric.enabled).length,
  disabledRlMetrics: rl.length,
  policySha256: sha256Canonical(policy),
  metricsSha256: sha256Canonical(metrics),
  defaultsSha256: sha256Canonical(defaults),
}, null, 2));
console.log(failed.length === 0 ? "ALL BOUNDARY CHECKS PASS" : `${failed.length} BOUNDARY CHECK(S) FAILED`);
process.exit(failed.length === 0 ? 0 : 1);
