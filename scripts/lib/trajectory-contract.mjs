/**
 * Trajectory contract semantic checks (v2.5.0 draft).
 *
 * JSON Schema cannot express the cross-field rules that actually prevent a
 * train/eval leak or a label-mask error:
 *   - split.splitKey must equal consent.subjectRef
 *   - a revoked subject may only appear in quarantine
 *   - an accepted record may not carry credential hits
 *   - contentHash must be reproducible from the record itself
 *
 * The schema stays the structural contract. This module is the semantic layer
 * and is deliberately dependency-free so it can run in CI without a package
 * install.
 */

import { createHash } from "node:crypto";
import { validateSchema } from "../schema-validator.mjs";

export const NON_ASSISTANT_ROLES = ["system", "human", "tool", "gate", "approval"];
export const CREDENTIAL_CLASSES = ["authCode", "token"];

export function canonicalize(value) {
  if (value === null || typeof value !== "object") return value;
  if (Array.isArray(value)) return value.map(canonicalize);
  const out = {};
  for (const key of Object.keys(value).sort()) out[key] = canonicalize(value[key]);
  return out;
}

export function canonicalJson(value) {
  return JSON.stringify(canonicalize(value));
}

export function sha256Canonical(value) {
  return `sha256:${createHash("sha256").update(canonicalJson(value), "utf8").digest("hex")}`;
}

/** contentHash covers the whole record except itself, under canonical JSON. */
export function computeContentHash(record) {
  const { contentHash: _ignored, ...rest } = record;
  return sha256Canonical(rest);
}

function parseTime(value) {
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? null : ms;
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/**
 * Validate one trajectory record: schema first, then the semantic invariants
 * that the schema cannot express.
 */
export function validateTrajectoryRecord(record, schema) {
  const schemaResult = validateSchema(schema, record);
  const errors = schemaResult.ok ? [] : [`schema: ${schemaResult.errors[0]}`];
  const semantic = [];

  const consent = isObject(record?.consent) ? record.consent : null;
  const split = isObject(record?.split) ? record.split : null;
  const labels = isObject(record?.labels) ? record.labels : null;
  const turns = Array.isArray(record?.conversations) ? record.conversations : null;
  const quality = isObject(record?.quality) ? record.quality : null;
  const terminal = isObject(record?.terminal) ? record.terminal : null;
  const retention = isObject(record?.retention) ? record.retention : null;
  const provenance = isObject(record?.provenance) ? record.provenance : null;
  const redaction = isObject(record?.redaction) ? record.redaction : null;

  if (consent && split && split.splitKey !== consent.subjectRef) {
    semantic.push("semantic: split.splitKey must equal consent.subjectRef (subject-level split)");
  }

  if (consent && consent.revokedAt != null && split?.name !== "quarantine") {
    semantic.push("semantic: revoked consent may only appear in the quarantine split");
  }

  if (consent && split && ["train", "eval"].includes(split.name) && Array.isArray(consent.scope) && !consent.scope.includes(split.name)) {
    semantic.push(`semantic: consent.scope does not cover split ${split.name}`);
  }

  if (consent) {
    const granted = parseTime(consent.grantedAt);
    const expires = parseTime(consent.expiresAt);
    if (granted !== null && expires !== null && expires <= granted) {
      semantic.push("semantic: consent.expiresAt must be after consent.grantedAt");
    }
  }

  if (retention && provenance) {
    const retainUntil = parseTime(retention.retainUntil);
    const deleteAfter = parseTime(retention.deleteAfter);
    const recordedAt = parseTime(provenance.recordedAt);
    if (retainUntil !== null && deleteAfter !== null && deleteAfter < retainUntil) {
      semantic.push("semantic: retention.deleteAfter must be >= retention.retainUntil");
    }
    if (retainUntil !== null && recordedAt !== null && retainUntil <= recordedAt) {
      semantic.push("semantic: retention.retainUntil must be after provenance.recordedAt");
    }
  }

  if (redaction?.counts) {
    for (const key of CREDENTIAL_CLASSES) {
      if (Number(redaction.counts[key]) > 0) {
        semantic.push(`semantic: accepted record carries ${key} hits; discard the record instead of redacting it`);
      }
    }
  }

  if (labels && Array.isArray(labels.trainableRoles)) {
    if (labels.trainableRoles.some((role) => NON_ASSISTANT_ROLES.includes(role))) {
      semantic.push("semantic: gate/human/tool/system/approval must never be trainable");
    }
  }

  if (quality && turns && quality.turns !== turns.length) {
    semantic.push("semantic: quality.turns must equal conversations.length");
  }

  if (turns) {
    let previous = -1;
    for (const turn of turns) {
      if (typeof turn?.at === "number" && turn.at < previous) {
        semantic.push("semantic: conversations[].at must be non-decreasing");
        break;
      }
      if (typeof turn?.at === "number") previous = turn.at;
    }
    const truncated = turns.some((turn) => turn?.truncated === true);
    if (truncated && terminal?.status !== "partial") {
      semantic.push("semantic: a truncated turn requires terminal.status=partial");
    }
    if (terminal?.status === "completed" && turns.at(-1)?.from !== "gpt") {
      semantic.push("semantic: completed trajectory must end with an assistant turn (no loss-bearing tail truncation)");
    }
    if (terminal && terminal.status !== "completed" && !terminal.reason) {
      semantic.push("semantic: non-completed trajectory must name a terminal.reason");
    }
  }

  if (record && record.contentHash) {
    const expected = computeContentHash(record);
    if (record.contentHash !== expected) {
      semantic.push(`semantic: contentHash mismatch (expected ${expected})`);
    }
  }

  errors.push(...semantic);
  return {
    ok: schemaResult.ok && semantic.length === 0,
    schemaOk: schemaResult.ok,
    errors: [...errors, ...(schemaResult.ok ? [] : schemaResult.errors.slice(1))],
  };
}

/**
 * Dataset manifest invariants: counts must add up, the frozen set must be
 * consent-complete, and credential hits must be zero at dataset level.
 */
export function validateDatasetManifest(manifest, schema, { recordSchema } = {}) {
  const schemaResult = validateSchema(schema, manifest);
  const errors = schemaResult.ok ? [] : [`schema: ${schemaResult.errors[0]}`];
  const semantic = [];

  const counts = isObject(manifest?.counts) ? manifest.counts : null;
  if (counts) {
    const sum = counts.train + counts.eval + counts.holdout + counts.quarantined;
    if (sum !== counts.records) {
      semantic.push(`semantic: split counts sum to ${sum}, expected records=${counts.records}`);
    }
    if (counts.subjects > counts.records) {
      semantic.push("semantic: counts.subjects cannot exceed counts.records");
    }
    if (counts.records > 0 && counts.subjects === 0) {
      semantic.push("semantic: a non-empty dataset must report counted subjects");
    }
  }

  const shards = Array.isArray(manifest?.integrity?.shards) ? manifest.integrity.shards : null;
  if (shards && counts) {
    const shardRecords = shards.reduce((total, shard) => total + (Number(shard?.records) || 0), 0);
    if (shardRecords !== counts.records) {
      semantic.push(`semantic: shard records sum to ${shardRecords}, expected records=${counts.records}`);
    }
    const paths = shards.map((shard) => shard?.path);
    if (new Set(paths).size !== paths.length) {
      semantic.push("semantic: duplicate shard path");
    }
  }

  const window = manifest?.splits?.evalWindow;
  if (window) {
    const from = parseTime(window.from);
    const to = parseTime(window.to);
    if (from !== null && to !== null && from >= to) {
      semantic.push("semantic: splits.evalWindow.from must be before splits.evalWindow.to");
    }
  }

  const createdAt = parseTime(manifest?.dataset?.createdAt);
  const frozenAt = parseTime(manifest?.splits?.frozenAt);
  const manifestFrozenAt = parseTime(manifest?.frozen?.frozenAt);
  if (createdAt !== null && frozenAt !== null && frozenAt < createdAt) {
    semantic.push("semantic: splits.frozenAt cannot precede dataset.createdAt");
  }
  if (manifestFrozenAt !== null && frozenAt !== null && manifestFrozenAt < frozenAt) {
    semantic.push("semantic: frozen.frozenAt cannot precede splits.frozenAt");
  }

  if (manifest?.consent) {
    if (manifest.consent.coverageRatio !== 1) {
      semantic.push("semantic: a frozen dataset requires consent.coverageRatio=1");
    }
    if (manifest.consent.withdrawnSubjects !== 0) {
      semantic.push("semantic: a frozen dataset with withdrawn subjects must be rebuilt");
    }
  }

  if (manifest?.redaction?.counts) {
    for (const key of CREDENTIAL_CLASSES) {
      if (Number(manifest.redaction.counts[key]) > 0) {
        semantic.push(`semantic: dataset reports ${key} hits; those records must be discarded`);
      }
    }
  }

  if (recordSchema && manifest?.integrity?.recordSchemaSha256) {
    const expected = sha256Canonical(recordSchema);
    if (manifest.integrity.recordSchemaSha256 !== expected) {
      semantic.push("semantic: integrity.recordSchemaSha256 does not match the frozen record schema");
    }
  }

  errors.push(...semantic);
  return {
    ok: schemaResult.ok && semantic.length === 0,
    schemaOk: schemaResult.ok,
    errors: [...errors, ...(schemaResult.ok ? [] : schemaResult.errors.slice(1))],
  };
}

/**
 * Split purity for a set of records. The split unit is the subject, never the
 * trajectory. Returns errors instead of throwing so the caller can report all
 * violations at once.
 */
export function checkSplitPurity(records, { evalWindow, maxSubjectShare } = {}) {
  const errors = [];
  const subjectSplits = new Map();
  const hashSplits = new Map();
  let evalFrom = null;
  let evalTo = null;

  if (evalWindow) {
    evalFrom = parseTime(evalWindow.from);
    evalTo = parseTime(evalWindow.to);
    if (evalFrom === null || evalTo === null || evalFrom >= evalTo) {
      errors.push("semantic: evalWindow is missing, unparsable, or empty");
    }
  }

  for (const record of records) {
    const subject = record?.consent?.subjectRef;
    const name = record?.split?.name;
    if (!subject || !name) {
      errors.push("semantic: record is missing consent.subjectRef or split.name");
      continue;
    }
    if (record.split.splitKey !== subject) {
      errors.push(`semantic: ${subject} has splitKey ${record.split.splitKey}`);
    }
    if (!subjectSplits.has(subject)) subjectSplits.set(subject, new Set());
    subjectSplits.get(subject).add(name);

    const hash = record.contentHash;
    if (hash) {
      if (!hashSplits.has(hash)) hashSplits.set(hash, new Set());
      hashSplits.get(hash).add(name);
    }

    const recordedAt = parseTime(record?.provenance?.recordedAt);
    if (evalFrom !== null && evalTo !== null && recordedAt !== null) {
      if (name === "train" && recordedAt >= evalFrom && recordedAt <= evalTo) {
        errors.push(`semantic: train record ${record.trajectoryId} falls inside the eval window`);
      }
      if (name === "eval" && (recordedAt < evalFrom || recordedAt > evalTo)) {
        errors.push(`semantic: eval record ${record.trajectoryId} falls outside the eval window`);
      }
    }
  }

  for (const [subject, names] of subjectSplits) {
    if (names.size > 1) {
      errors.push(`semantic: subject ${subject} appears in multiple splits: ${[...names].sort().join(",")}`);
    }
  }
  for (const [hash, names] of hashSplits) {
    if (names.size > 1) {
      errors.push(`semantic: contentHash ${hash} appears in multiple splits`);
    }
  }

  const total = records.length;
  const perSubject = new Map();
  for (const record of records) {
    const subject = record?.consent?.subjectRef;
    if (!subject) continue;
    perSubject.set(subject, (perSubject.get(subject) ?? 0) + 1);
  }
  let maxShare = 0;
  let maxSubject = null;
  for (const [subject, count] of perSubject) {
    const share = total === 0 ? 0 : count / total;
    if (share > maxShare) {
      maxShare = share;
      maxSubject = subject;
    }
  }
  if (typeof maxSubjectShare === "number" && maxShare > maxSubjectShare) {
    errors.push(`semantic: subject ${maxSubject} holds ${(maxShare * 100).toFixed(1)}% of records (limit ${(maxSubjectShare * 100).toFixed(1)}%)`);
  }

  return {
    ok: errors.length === 0,
    errors,
    stats: {
      records: total,
      subjects: subjectSplits.size,
      splits: [...new Set(records.map((record) => record?.split?.name).filter(Boolean))].sort(),
      maxSubjectShare: Number(maxShare.toFixed(4)),
    },
  };
}

export const REQUIRED_STOP_CODES = [
  "deny_list_hit",
  "mask_violation",
  "split_leak",
  "manifest_hash_mismatch",
  "load_probe_failed",
];

/**
 * A capture job must be explicitly enabled and fully preflighted. An absent
 * flag is not a pass: every preflight boolean must be true before a job can run.
 */
export function validateCollectionJob(job, schema) {
  const schemaResult = validateSchema(schema, job);
  const errors = schemaResult.ok ? [] : [`schema: ${schemaResult.errors[0]}`];
  const semantic = [];

  if (job?.activation?.enabled === true) {
    if (!job.activation.approvedBy || !job.activation.approvalRef) {
      semantic.push("semantic: an enabled capture job needs approvedBy and approvalRef");
    }
    for (const [flag, value] of Object.entries(job.preflight ?? {})) {
      if (value !== true) semantic.push(`semantic: preflight.${flag} is not satisfied`);
    }
  }

  if (job?.mode === "consented") {
    if (job.dataBasis === "synthetic") {
      semantic.push("semantic: consented mode cannot use a synthetic data basis");
    }
    if (!job.consentPolicyVersion) {
      semantic.push("semantic: consented mode needs consentPolicyVersion");
    }
  }

  if (job?.mode === "synthetic" && job.dataBasis !== "synthetic") {
    semantic.push("semantic: synthetic mode requires dataBasis=synthetic");
  }

  const stopOn = new Set(job?.stopOn ?? []);
  for (const code of REQUIRED_STOP_CODES) {
    if (!stopOn.has(code)) semantic.push(`semantic: stopOn is missing ${code}`);
  }

  if (job?.budget) {
    if (job.budget.maxRecords > 5000) {
      semantic.push("semantic: first-gate capture is capped at 5000 records");
    }
    if (!(job.budget.maxGpuHours > 0) || !(job.budget.costUsdMax > 0)) {
      semantic.push("semantic: budget needs a positive GPU-hour and cost upper bound");
    }
  }

  errors.push(...semantic);
  return {
    ok: schemaResult.ok && semantic.length === 0,
    schemaOk: schemaResult.ok,
    errors: [...errors, ...(schemaResult.ok ? [] : schemaResult.errors.slice(1))],
  };
}

/** Convenience: validate a JSONL text blob as records. */
export function parseJsonl(text) {  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .map((line, index) => {
      try {
        return JSON.parse(line);
      } catch (error) {
        throw new Error(`invalid JSONL at line ${index + 1}: ${error.message}`);
      }
    });
}
