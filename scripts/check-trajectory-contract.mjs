/**
 * Trajectory contract check (v2.5.0 draft).
 *
 * Validates the trajectory record against its schema AND its cross-field
 * semantic rules, plus the dataset manifest. Structural validation alone would
 * accept a record whose splitKey points at another subject, which is exactly the
 * train/eval leak this contract exists to prevent.
 *
 * This script is intentionally NOT wired into scripts/validate.mjs: the contract
 * is still a draft and must not add a release gate before ADR 0005 is accepted.
 *
 * Usage: node scripts/check-trajectory-contract.mjs
 */

import { readFile } from "node:fs/promises";
import {
  computeContentHash,
  sha256Canonical,
  validateDatasetManifest,
  validateTrajectoryRecord,
} from "./lib/trajectory-contract.mjs";

const load = async (rel) => JSON.parse(await readFile(new URL(rel, import.meta.url), "utf8"));

const recordSchema = await load("../docs/contracts/trajectory-record.schema.json");
const manifestSchema = await load("../docs/contracts/trajectory-dataset-manifest.schema.json");
const base = await load("../docs/contracts/fixtures/trajectory-record.synthetic.json");

const cases = [];

function add(name, expectOk, mutate, { rehash = true } = {}) {
  const value = structuredClone(base);
  mutate(value);
  if (rehash) value.contentHash = computeContentHash(value);
  const result = validateTrajectoryRecord(value, recordSchema);
  cases.push({ name, expectOk, ok: result.ok, errors: result.ok ? [] : result.errors.slice(0, 2) });
}

// Structural controls.
add("valid synthetic fixture", true, () => {});
add("reject extra authCode field", false, (v) => { v.authCode = "REDACTED-IN-FIXTURE"; });
add("reject token field in provenance", false, (v) => { v.provenance.token = "redacted"; });
add("reject gate as trainable role", false, (v) => { v.labels.trainableRoles = ["gate"]; });
add("reject missing masked role", false, (v) => { v.labels.maskedRoles = ["system", "human", "tool", "gate"]; });
add("reject trajectory-level split key", false, (v) => { v.split.splitKey = v.trajectoryId; });
add("reject tool turn without toolName", false, (v) => { delete v.conversations[5].toolName; });
add("reject unknown consent basis", false, (v) => { v.consent.basis = "terms_of_service"; });
add("reject malformed trajectory id", false, (v) => { v.trajectoryId = "trj_lowercase"; });
add("reject phone count missing", false, (v) => { delete v.redaction.counts.phone; });

// Semantic controls: these all pass JSON Schema and must still be rejected.
add("reject splitKey pointing at another subject", false, (v) => {
  v.split.splitKey = "sub_aaaaaaaaaaaaaaaa";
});
add("reject revoked subject in train", false, (v) => {
  v.consent.revokedAt = "2026-10-30T13:00:00Z";
});
add("reject split outside consent scope", false, (v) => {
  v.consent.scope = ["eval"];
});
add("reject accepted record with authCode hit", false, (v) => {
  v.redaction.counts.authCode = 1;
});
add("reject accepted record with token hit", false, (v) => {
  v.redaction.counts.token = 1;
});
add("reject turns/count mismatch", false, (v) => {
  v.quality.turns = 6;
});
add("reject decreasing turn timestamps", false, (v) => {
  v.conversations[4].at = 100;
});
add("reject truncated turn without partial status", false, (v) => {
  v.conversations[6].truncated = true;
});
add("reject completed trajectory ending on tool", false, (v) => {
  v.conversations.push({ from: "tool", value: "{\"ok\":true}", at: 6200, toolName: "calendar.update", toolStatus: "ok" });
  v.quality.turns = v.conversations.length;
  v.conversations[6].truncated = false;
  v.terminal.status = "completed";
});
add("reject partial trajectory without reason", false, (v) => {
  v.terminal.status = "partial";
  v.terminal.reason = null;
});
add("reject deleteAfter before retainUntil", false, (v) => {
  v.retention.deleteAfter = "2027-01-01T00:00:00Z";
});
add("reject unverifiable contentHash", false, (v) => {
  v.conversations[1].value = "tampered after hashing";
}, { rehash: false });

const manifest = {
  schemaVersion: "1.0",
  dataset: {
    name: "im2a-synth-gate-1",
    version: "0.1.0",
    createdAt: "2026-10-30T12:00:00Z",
    sourceQuery: "synthetic scripted approval scenarios",
    filters: ["approval-only", "no-user-data"],
    collectorVersion: "traj-collector-0.1.0",
    recordSchemaId: "https://im2a.dev/schemas/trajectory-record-1.0.json",
  },
  counts: { records: 5000, train: 4000, eval: 800, holdout: 200, quarantined: 0, subjects: 250 },
  splits: {
    unit: "subject",
    seed: 20261030,
    frozenAt: "2026-10-30T12:00:00Z",
    evalWindow: { from: "2026-11-15T00:00:00Z", to: "2026-11-30T00:00:00Z" },
  },
  redaction: {
    policyVersion: "redact-1.0",
    residualRisk: "low",
    counts: {
      authCode: 0, token: 0, phone: 0, email: 0, idCard: 0,
      wxid: 0, deviceId: 0, ip: 0, sessionRef: 0, url: 0, filePath: 0,
    },
  },
  license: {
    codeLicense: "MIT",
    dataBasis: ["synthetic"],
    modelOutputTerms: "synthetic-scripted-no-upstream-model-output",
    ledgerRef: "ledger:synthetic:0.1.0",
  },
  consent: { requiredBasis: ["synthetic"], coverageRatio: 1, withdrawnSubjects: 0 },
  integrity: {
    recordSchemaSha256: sha256Canonical(recordSchema),
    shards: [
      { path: "shards/train-00000.jsonl", sha256: "sha256:6666666666666666666666666666666666666666666666666666666666666666", records: 5000 },
    ],
    dedupe: { exactDupes: 0, nearDupes: 3, nearDupeThreshold: 0.8 },
  },
  frozen: {
    frozenAt: "2026-10-30T12:00:00Z",
    frozenBy: "gate-preflight",
    hashManifestSha256: "sha256:7777777777777777777777777777777777777777777777777777777777777777",
  },
};

const manifestCases = [];
function addManifest(name, expectOk, mutate) {
  const value = structuredClone(manifest);
  mutate(value);
  const result = validateDatasetManifest(value, manifestSchema, { recordSchema });
  manifestCases.push({ name, expectOk, ok: result.ok, errors: result.ok ? [] : result.errors.slice(0, 2) });
}

addManifest("valid manifest", true, () => {});
addManifest("reject trajectory-unit split", false, (m) => { m.splits.unit = "trajectory"; });
addManifest("reject split counts that do not add up", false, (m) => { m.counts.train = 3999; });
addManifest("reject incomplete consent coverage", false, (m) => { m.consent.coverageRatio = 0.99; });
addManifest("reject frozen dataset with withdrawn subjects", false, (m) => { m.consent.withdrawnSubjects = 1; });
addManifest("reject dataset-level credential hits", false, (m) => { m.redaction.counts.authCode = 1; });
addManifest("reject shard record mismatch", false, (m) => { m.integrity.shards[0].records = 4999; });
addManifest("reject record schema hash mismatch", false, (m) => { m.integrity.recordSchemaSha256 = `sha256:${"0".repeat(64)}`; });
addManifest("reject reversed eval window", false, (m) => {
  m.splits.evalWindow = { from: "2026-11-30T00:00:00Z", to: "2026-11-15T00:00:00Z" };
});

let failed = 0;
for (const c of [...cases, ...manifestCases]) {
  if (c.ok !== c.expectOk) {
    failed += 1;
    console.log(`FAIL ${c.name}: ok=${c.ok} expected=${c.expectOk} ${JSON.stringify(c.errors)}`);
  } else {
    console.log(`ok   ${c.name}`);
  }
}

const passed = cases.filter((c) => c.ok === c.expectOk).length + manifestCases.filter((c) => c.ok === c.expectOk).length;
console.log(JSON.stringify({
  recordCases: cases.length,
  recordPassed: cases.filter((c) => c.ok === c.expectOk).length,
  manifestCases: manifestCases.length,
  manifestPassed: manifestCases.filter((c) => c.ok === c.expectOk).length,
  totalPassed: passed,
  totalCases: cases.length + manifestCases.length,
  recordSchemaSha256: sha256Canonical(recordSchema),
  manifestSchemaSha256: sha256Canonical(manifestSchema),
  fixtureSha256: sha256Canonical(base),
}, null, 2));

console.log(failed === 0 ? "ALL CONTRACT CHECKS PASS" : `${failed} CHECK(S) FAILED`);
process.exit(failed === 0 ? 0 : 1);
