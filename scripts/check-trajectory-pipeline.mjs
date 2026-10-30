/**
 * Trajectory pipeline checks (v2.5.0 draft): redaction policy, split purity,
 * and capture-job activation.
 *
 * Everything here runs on synthetic, in-memory samples. No user data, no
 * network, no GPU, no credentials. The point is to prove the *checks* fail
 * closed before any pipeline is allowed near real conversations.
 *
 * Usage: node scripts/check-trajectory-pipeline.mjs
 */

import { readFile } from "node:fs/promises";
import {
  checkSplitPurity,
  computeContentHash,
  validateCollectionJob,
} from "./lib/trajectory-contract.mjs";
import { applyPolicy, compilePolicy, scanText, scanValue } from "./lib/redaction.mjs";

const load = async (rel) => JSON.parse(await readFile(new URL(rel, import.meta.url), "utf8"));

const base = await load("../docs/contracts/fixtures/trajectory-record.synthetic.json");
const policy = await load("../docs/contracts/redaction-policy-1.0.json");
const jobSchema = await load("../docs/contracts/trajectory-collection-job.schema.json");

const results = [];
function record(name, ok, detail) {
  results.push({ name, ok, detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${ok || !detail ? "" : `: ${JSON.stringify(detail)}`}`);
}

/* ------------------------------------------------------------------ */
/* 1. Redaction: every regex class must detect its synthetic sample.   */
/* ------------------------------------------------------------------ */

const compiled = compilePolicy(policy);
const samples = [
  ["authCode", "IM2A_" + "20260101" + "1".repeat(22) + "A".repeat(93)],
  ["publicUid", "IM2A_" + "20260101" + "1".repeat(22)],
  ["token", "sk-" + "TESTONLY".repeat(4)],
  ["userCode", "IM2A_" + "20260101" + "123456789012"],
  ["im2aNumber", "9".repeat(12)],
  ["phone", "19" + "0".repeat(9)],
  ["email", "nobody@example.invalid"],
  ["idCard", "9".repeat(17) + "X"],
  ["wxid", "wxid_testonly0001"],
  ["deviceId", "deviceId: dev_test_0001"],
  ["ip", "192.0.2.10"],
  ["url", "http://10.0.0.5/internal"],
  ["filePath", "C:\\Users\\test\\notes.txt"],
  ["sessionRef", "dsh-session:test-0001"],
];

for (const [classId, sample] of samples) {
  const scan = scanText(sample, compiled);
  const hit = scan.hits.some((entry) => entry.classId === classId);
  const leaked = JSON.stringify(scan).includes(sample);
  record(`redaction detects ${classId}`, hit && !leaked, { hit, leaked });
}

const discardClasses = ["authCode", "token"];
for (const classId of discardClasses) {
  const sample = samples.find(([id]) => id === classId)[1];
  const applied = applyPolicy(sample, compiled);
  record(`redaction discards record for ${classId}`, applied.discard === true, applied.counts);
}

const missingKey = applyPolicy(samples.find(([id]) => id === "userCode")[1], compiled);
record("pseudonymization fails closed without a dataset key", missingKey.blocked === true, missingKey);

const keyed = applyPolicy(samples.find(([id]) => id === "userCode")[1], compiled, {
  pseudonymize: (_classId, value) => `sub_test_${value.length}`,
});
record("pseudonymization uses the dataset-key callback", keyed.text.includes("sub_test_") && !keyed.text.includes("IM2A_"), keyed.text);

const placeholderChecks = [
  ["phone", "<PHONE>"],
  ["idCard", "<ID_CARD>"],
  ["url", "<URL>"],
];
for (const [classId, placeholder] of placeholderChecks) {
  const sample = samples.find(([id]) => id === classId)[1];
  const applied = applyPolicy(sample, compiled);
  record(`redaction replaces ${classId} with a typed placeholder`, applied.text.includes(placeholder), applied.text.slice(0, 80));
}

// The redacted fixture must scan clean: no residual direct identifiers.
const fixtureScan = scanValue(base, compiled);
record("redacted fixture scans clean", fixtureScan.hits.length === 0, fixtureScan.hits);
record("contextual re-identification is not auto-cleared", fixtureScan.manualReview.includes("freeText"), fixtureScan.manualReview);

/* ------------------------------------------------------------------ */
/* 2. Split purity: subject-level split, hash-free cross-split, window. */
/* ------------------------------------------------------------------ */

function variant(index, { subject, split, recordedAt }) {
  const copy = structuredClone(base);
  copy.trajectoryId = `${base.trajectoryId.slice(0, -1)}${index % 10}`;
  copy.consent.subjectRef = subject;
  copy.split.splitKey = subject;
  copy.split.name = split;
  copy.provenance.recordedAt = recordedAt;
  if (split === "eval") copy.consent.scope = ["eval"];
  if (split === "train") copy.consent.scope = ["train"];
  copy.contentHash = computeContentHash(copy);
  return copy;
}

const evalWindow = { from: "2026-11-15T00:00:00Z", to: "2026-11-30T00:00:00Z" };
const clean = [
  variant(1, { subject: "sub_1111111111111111", split: "train", recordedAt: "2026-10-01T10:00:00Z" }),
  variant(2, { subject: "sub_1111111111111111", split: "train", recordedAt: "2026-10-02T10:00:00Z" }),
  variant(3, { subject: "sub_2222222222222222", split: "eval", recordedAt: "2026-11-20T10:00:00Z" }),
  variant(4, { subject: "sub_3333333333333333", split: "eval", recordedAt: "2026-11-21T10:00:00Z" }),
  variant(5, { subject: "sub_4444444444444444", split: "holdout", recordedAt: "2026-10-05T10:00:00Z" }),
];

const cleanResult = checkSplitPurity(clean, { evalWindow });
record("split purity accepts a subject-disjoint frozen set", cleanResult.ok, cleanResult.errors);

const leak = structuredClone(clean);
leak[2].split.name = "train";
leak[2].consent.scope = ["train"];
leak[2].contentHash = computeContentHash(leak[2]);
const leakResult = checkSplitPurity(leak, { evalWindow });
record("split purity rejects a subject spanning train and eval", !leakResult.ok, leakResult.errors);

const duplicate = structuredClone(clean);
duplicate[2].contentHash = duplicate[0].contentHash;
const duplicateResult = checkSplitPurity(duplicate, { evalWindow });
record("split purity rejects a cross-split contentHash duplicate", !duplicateResult.ok, duplicateResult.errors);

const windowLeak = structuredClone(clean);
windowLeak[0].provenance.recordedAt = "2026-11-20T10:00:00Z";
windowLeak[0].contentHash = computeContentHash(windowLeak[0]);
const windowResult = checkSplitPurity(windowLeak, { evalWindow });
record("split purity rejects a train record inside the eval window", !windowResult.ok, windowResult.errors);

const keyLeak = structuredClone(clean);
keyLeak[0].split.splitKey = "sub_9999999999999999";
keyLeak[0].contentHash = computeContentHash(keyLeak[0]);
const keyResult = checkSplitPurity(keyLeak, { evalWindow });
record("split purity rejects a splitKey that is not the subject", !keyResult.ok, keyResult.errors);

const concentrated = checkSplitPurity(clean, { evalWindow, maxSubjectShare: 0.3 });
record("split purity enforces subject concentration", !concentrated.ok, concentrated.stats);

/* ------------------------------------------------------------------ */
/* 3. Capture job: explicit activation, preflight, budget, stop codes. */
/* ------------------------------------------------------------------ */

const job = {
  schemaVersion: "1.0",
  jobId: "job_01J9Z8K3QF7M2C5V8B1N4T6R0X",
  datasetRef: { name: "im2a-synth-gate-1", version: "0.1.0" },
  mode: "synthetic",
  dataBasis: "synthetic",
  owner: "gate-preflight",
  consentPolicyVersion: "consent-1.0",
  preflight: {
    contractFrozen: true,
    denyListClean: true,
    splitPurity: true,
    consentCoverageComplete: true,
    budgetApproved: true,
    rollbackDrillDone: true,
    evalIdentityFrozen: true,
  },
  budget: { maxRecords: 5000, maxGpuHours: 4, costUsdMax: 20 },
  activation: { enabled: true, approvedBy: "gate-preflight", approvalRef: "apr_gate:synthetic:0001" },
  cursor: null,
  stopOn: [
    "deny_list_hit",
    "mask_violation",
    "split_leak",
    "manifest_hash_mismatch",
    "load_probe_failed",
  ],
  output: { format: "jsonl", shardMaxRecords: 1000 },
};

const jobCases = [];
function jobCase(name, expectOk, mutate) {
  const value = structuredClone(job);
  mutate(value);
  const result = validateCollectionJob(value, jobSchema);
  jobCases.push({ name, expectOk, ok: result.ok, errors: result.ok ? [] : result.errors.slice(0, 2) });
}
jobCase("valid enabled synthetic capture job", true, () => {});
jobCase("reject enabled job without approval reference", false, (j) => { j.activation.approvalRef = null; });
jobCase("reject enabled job with an unsatisfied preflight flag", false, (j) => { j.preflight.rollbackDrillDone = false; });
jobCase("reject consented mode using synthetic basis", false, (j) => { j.mode = "consented"; });
jobCase("reject missing stop code", false, (j) => {
  j.stopOn = j.stopOn.map((code) => (code === "split_leak" ? "eval_contamination" : code));
});
jobCase("reject first-gate capture above 5000 records", false, (j) => { j.budget.maxRecords = 6000; });
jobCase("reject zero cost upper bound", false, (j) => { j.budget.costUsdMax = 0; });

for (const c of jobCases) {
  record(c.name, c.ok === c.expectOk, c.ok === c.expectOk ? undefined : c.errors);
}

/* ------------------------------------------------------------------ */

const failed = results.filter((entry) => !entry.ok);
const summary = {
  checks: results.length,
  passed: results.length - failed.length,
  splitStats: cleanResult.stats,
  redactionPolicyVersion: compiled.policyVersion,
  redactionClasses: compiled.classes.length,
  jobCases: jobCases.length,
  jobPassed: jobCases.filter((c) => c.ok === c.expectOk).length,
};
console.log(JSON.stringify(summary, null, 2));
console.log(failed.length === 0 ? "ALL PIPELINE CHECKS PASS" : `${failed.length} PIPELINE CHECK(S) FAILED`);
process.exit(failed.length === 0 ? 0 : 1);
