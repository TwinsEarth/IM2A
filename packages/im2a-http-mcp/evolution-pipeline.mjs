/**
 * v2.5.9 end-to-end evolution-pipeline dry run.
 *
 * This orchestrator proves that the v2.5.4-v2.5.8 interfaces compose without
 * touching durable state. Trace and guard writes are represented as
 * would_write summaries; the other steps are read-only. All fixtures live in
 * process memory.
 */
import { randomBytes } from "node:crypto";
import { normalizeTraceBatch, sha256Hex } from "./trace-store.mjs";
import { assessTrace } from "./reflect.mjs";
import { loadReflectConfig } from "./reflect-config.mjs";
import { evaluateGuard } from "./skill-guard.mjs";
import { SqliteSkillStore } from "../skill-runtime/src/sqlite-store.mjs";
import { MemoryHub } from "../memory-hub/src/index.mjs";

export const EVOLUTION_PIPELINE_VERSION = "1.0";
const DRY_AGENT = "did:im2a:dryrun-agent";
const DRY_SESSION = "hs_dryrun_evolution";
const DAY = 24 * 60 * 60 * 1000;

function digest(value) {
  return `sha256:${sha256Hex(value)}`;
}

function event(name, overrides = {}) {
  return {
    span_type: "tool_call",
    name,
    input_digest: digest(`in-${name}`),
    output_digest: digest(`out-${name}`),
    duration_ms: 10,
    token_cost: 1,
    success: true,
    error_class: null,
    ...overrides,
  };
}

function traceFixture() {
  return {
    session_id: DRY_SESSION,
    agent_did: DRY_AGENT,
    events: [
      event("search"),
      event("read"),
      event("search", { success: false, error_class: "NOT_FOUND", duration_ms: 40 }),
      event("read"),
      event("plan"),
      event("send"),
      event("verify"),
    ],
  };
}

function validateTrace(output) {
  const errors = [];
  if (typeof output?.trace_id !== "string" || !/^[0-9a-f]{32}$/.test(output.trace_id)) errors.push("trace_id must be 32 lowercase hex");
  if (!Number.isInteger(output?.ingested_count) || output.ingested_count < 1) errors.push("ingested_count must be positive");
  if (typeof output?.content_sha256 !== "string" || !/^sha256:[0-9a-f]{64}$/.test(output.content_sha256)) errors.push("content_sha256 must be sha256:<64 hex>");
  return errors;
}

function validateReflect(output) {
  const errors = [];
  if (!output?.assessment || typeof output.assessment !== "object") errors.push("assessment is required");
  if (typeof output?.assessment?.trace_id !== "string" || !/^[0-9a-f]{32}$/.test(output.assessment.trace_id)) errors.push("assessment.trace_id must be 32 lowercase hex");
  if (typeof output?.assessment?.worth_reflecting !== "boolean") errors.push("assessment.worth_reflecting must be boolean");
  if (!Array.isArray(output?.assessment?.reason_codes)) errors.push("assessment.reason_codes must be array");
  if (!Array.isArray(output?.distilled_candidates)) errors.push("distilled_candidates must be array");
  for (const candidate of output?.distilled_candidates ?? []) {
    if (!/^cand_[0-9a-f]{24}$/.test(candidate.candidate_id ?? "")) errors.push("candidate_id invalid");
    if (!["skill_create", "skill_patch", "principle_extract"].includes(candidate.candidate_type)) errors.push("candidate_type invalid");
    if (typeof candidate.summary !== "string" || candidate.summary.length === 0) errors.push("candidate.summary required");
    if (!Array.isArray(candidate.source_trace_ids) || candidate.source_trace_ids.some((id) => !/^[0-9a-f]{32}$/.test(id))) errors.push("candidate.source_trace_ids invalid");
    if (!(candidate.confidence > 0 && candidate.confidence < 1)) errors.push("candidate.confidence out of range");
  }
  return errors;
}

function validateCurator(output) {
  const errors = [];
  if (!Array.isArray(output?.skills)) errors.push("skills must be array");
  if (!output?.audit || typeof output.audit !== "object") errors.push("audit is required");
  for (const skill of output?.skills ?? []) {
    if (typeof skill.skill_id !== "string" || skill.skill_id.length === 0) errors.push("skill_id required");
    if (!["active", "stale", "archived"].includes(skill.lifecycle_state)) errors.push("lifecycle_state invalid");
    if (!["keep", "flag_for_review", "merge_candidate", "archive_candidate"].includes(skill.recommended_action)) errors.push("recommended_action invalid");
  }
  return errors;
}

function validateGuard(output) {
  const errors = [];
  if (typeof output?.allowed !== "boolean") errors.push("allowed must be boolean");
  if (output?.allowed === true && output?.denied_reason !== null) errors.push("allowed=true requires denied_reason=null");
  if (output?.allowed === false && !["insufficient_scope", "scope_ceiling_exceeded", "cross_agent_write_blocked"].includes(output?.denied_reason)) errors.push("denied_reason invalid");
  if (typeof output?.audit_event_id !== "string" || output.audit_event_id.length === 0) errors.push("audit_event_id required");
  return errors;
}

function validateRecall(output, budget) {
  const errors = [];
  if (!Array.isArray(output?.items)) errors.push("items must be array");
  if (!Number.isInteger(output?.budget_used) || output.budget_used < 0 || output.budget_used > budget) errors.push("budget_used exceeds hard budget");
  if (!Number.isInteger(output?.suppressed_count) || output.suppressed_count < 0) errors.push("suppressed_count invalid");
  for (const item of output?.items ?? []) {
    if (typeof item.content !== "string" || item.content.length === 0) errors.push("item.content required");
    if (!["raw_event", "atom", "skill_description", "entity_page"].includes(item.layer)) errors.push("item.layer invalid");
    if (!(item.score >= 0 && item.score <= 1)) errors.push("item.score out of range");
    if (!["fresh", "stale", "conflicted", "low_confidence"].includes(item.quality_flag)) errors.push("item.quality_flag invalid");
    if (!(item.source_trace_id === null || /^[0-9a-f]{32}$/.test(item.source_trace_id))) errors.push("item.source_trace_id invalid");
  }
  return errors;
}

function timed(fn) {
  const start = performance.now();
  const value = fn();
  return { value, duration_ms: Math.round((performance.now() - start) * 1000) / 1000 };
}

export function runEvolutionPipelineDryRun({ now = Date.now() } = {}) {
  const started = now;
  const began = performance.now();
  const steps = [];
  const schemaChecks = [];
  const trace = timed(() => {
    const record = normalizeTraceBatch(traceFixture(), { tenant: "dryrun", clock: () => new Date(started) });
    return {
      output: {
        trace_id: record.trace_id,
        ingested_count: record.events.length,
        content_sha256: record.content_sha256,
      },
      record,
      would_write: {
        path: "traces/dryrun/trajectory.jsonl",
        trace_id: record.trace_id,
        events: record.events.length,
        bytes: Buffer.byteLength(JSON.stringify(record)) + 1,
      },
    };
  });
  const traceOutput = trace.value.output;
  const traceErrors = validateTrace(traceOutput);
  steps.push({
    step: "trace_ingest", index: 1, status: traceErrors.length ? "fail" : "pass",
    duration_ms: trace.duration_ms, schema_valid: traceErrors.length === 0, schema_errors: traceErrors,
    input_from: "fixture", output: traceOutput, would_write: trace.value.would_write,
    ids: { trace_id: traceOutput.trace_id },
  });
  schemaChecks.push({ step: "trace_ingest", ok: traceErrors.length === 0, errors: traceErrors });

  const reflect = timed(() => assessTrace(trace.value.record, { config: loadReflectConfig({ env: {} }) }));
  const reflectErrors = validateReflect(reflect.value);
  const candidate = reflect.value.distilled_candidates[0] ?? null;
  steps.push({
    step: "reflect_trigger", index: 2, status: reflectErrors.length ? "fail" : "pass",
    duration_ms: reflect.duration_ms, schema_valid: reflectErrors.length === 0, schema_errors: reflectErrors,
    input_from: "trace_ingest.trace_id", output: reflect.value, would_write: null,
    ids: {
      trace_id: reflect.value.assessment.trace_id,
      candidate_ids: reflect.value.distilled_candidates.map((item) => item.candidate_id),
    },
  });
  schemaChecks.push({ step: "reflect_trigger", ok: reflectErrors.length === 0, errors: reflectErrors });

  const curator = timed(() => {
    const store = new SqliteSkillStore({ path: ":memory:", now: () => started });
    try {
      const ids = ["dryrun-skill-a", "dryrun-skill-b", "dryrun-skill-c", "dryrun-skill-d", "dryrun-skill-e"];
      for (const id of ids) {
        store.registerPackage({
          id, version: "1.0.0", description: `dry-run skill ${id}`,
          tags: ["dryrun"], files: { "SKILL.md": `# ${id}\n\ndry-run fixture` },
        });
      }
      store.setOwner(ids[0], DRY_AGENT);
      return store.curatorAudit({ scope: "network", now: started });
    } finally {
      store.close();
    }
  });
  const curatorErrors = validateCurator(curator.value);
  const targetSkill = curator.value.skills[0]?.skill_id ?? null;
  steps.push({
    step: "curator_audit", index: 3, status: curatorErrors.length ? "fail" : "pass",
    duration_ms: curator.duration_ms, schema_valid: curatorErrors.length === 0, schema_errors: curatorErrors,
    input_from: "read-only fixture", output: curator.value, would_write: null,
    ids: { skill_ids: curator.value.skills.map((skill) => skill.skill_id) },
  });
  schemaChecks.push({ step: "curator_audit", ok: curatorErrors.length === 0, errors: curatorErrors });

  const guard = timed(() => {
    const session = {
      agent_did: DRY_AGENT,
      authorization_level: "L2",
      scopes: ["skill:read", "skill:write"],
      session_id: DRY_SESSION,
      subject_kind: "dryrun_agent",
      expires_at: started + 5 * 60 * 1000,
    };
    const decision = evaluateGuard(session, {
      agentDid: DRY_AGENT,
      operation: "patch",
      targetOwnerDid: DRY_AGENT,
    });
    const auditEventId = `evt_dryrun_${randomBytes(12).toString("hex")}`;
    return {
      output: { allowed: decision.allowed, denied_reason: decision.denied_reason, audit_event_id: auditEventId },
      would_write: {
        path: "skill-guard/audit.jsonl",
        audit_event_id: auditEventId,
        decision: decision.allowed ? "allowed" : "denied",
        action: "patch",
        target_skill_id: targetSkill,
        persisted: false,
      },
    };
  });
  const guardErrors = validateGuard(guard.value.output);
  steps.push({
    step: "skill_guard_check", index: 4, status: guardErrors.length ? "fail" : "pass",
    duration_ms: guard.duration_ms, schema_valid: guardErrors.length === 0, schema_errors: guardErrors,
    input_from: "curator_audit.skill_id", output: guard.value.output, would_write: guard.value.would_write,
    ids: { skill_id: targetSkill, audit_event_id: guard.value.output.audit_event_id },
  });
  schemaChecks.push({ step: "skill_guard_check", ok: guardErrors.length === 0, errors: guardErrors });

  const budget = 2000;
  const recall = timed(() => {
    const hub = new MemoryHub({ path: ":memory:", tenant: "dryrun", now: () => started });
    try {
      const summary = candidate?.summary ?? "dry-run candidate summary";
      const candidateId = candidate?.candidate_id ?? "cand_dryrun";
      for (let i = 0; i < 8; i += 1) {
        hub.put({
          id: `dryrun-memory-${i}`,
          layer: i === 0 ? "skill_description" : "atom",
          kind: i === 0 ? "skill" : "fact",
          title: i === 0 ? "dry-run candidate recall" : `dry-run memory ${i}`,
          body: i === 0 ? `${candidateId} ${summary}` : `dry-run recall fixture ${i}`,
          sourceTraceId: i === 0 ? traceOutput.trace_id : null,
          confidence: 0.8,
          updatedAt: started - i * DAY,
          metadata: { dryrun: true },
        });
      }
      const query = summary.replace(/[^\p{L}\p{N}\s]/gu, " ").trim().split(/\s+/).slice(0, 3).join(" ") || "dry-run";
      const result = hub.recall({ query, agentDid: DRY_AGENT, budgetTokens: budget });
      return { result, query, candidateId };
    } finally {
      hub.close();
    }
  });
  const recallErrors = validateRecall(recall.value.result, budget);
  steps.push({
    step: "memory_recall_v2", index: 5, status: recallErrors.length ? "fail" : "pass",
    duration_ms: recall.duration_ms, schema_valid: recallErrors.length === 0, schema_errors: recallErrors,
    input_from: "reflect_trigger.distilled_candidates", output: recall.value.result, would_write: null,
    ids: {
      query: recall.value.query,
      source_trace_ids: recall.value.result.items.map((item) => item.source_trace_id).filter(Boolean),
    },
  });
  schemaChecks.push({ step: "memory_recall_v2", ok: recallErrors.length === 0, errors: recallErrors });

  const traceToReflect = traceErrors.length === 0
    && reflectErrors.length === 0
    && reflect.value.assessment.trace_id === traceOutput.trace_id
    && reflect.value.distilled_candidates.every((item) => item.source_trace_ids.includes(traceOutput.trace_id));
  const curatorToGuard = curatorErrors.length === 0
    && guardErrors.length === 0
    && typeof targetSkill === "string"
    && targetSkill.length > 0
    && guard.value.would_write.target_skill_id === targetSkill;
  const reflectToRecall = reflectErrors.length === 0
    && recallErrors.length === 0
    && candidate !== null
    && recall.value.result.items.some((item) =>
      item.source_trace_id === traceOutput.trace_id && item.content.includes(candidate.candidate_id));
  const guardTerminal = guardErrors.length === 0 && /^evt_/.test(guard.value.output.audit_event_id);
  const traceabilityChecks = [
    { from: "trace_ingest", to: "reflect_trigger", ok: traceToReflect, id: traceOutput.trace_id },
    { from: "reflect_trigger", to: "memory_recall_v2", ok: reflectToRecall, id: candidate?.candidate_id ?? null },
    { from: "curator_audit", to: "skill_guard_check", ok: curatorToGuard, id: targetSkill },
    { from: "skill_guard_check", to: "pipeline_report", ok: guardTerminal, id: guard.value.output.audit_event_id },
  ];
  const idTraceability = traceabilityChecks.every((check) => check.ok) ? "full" : "partial";
  const completed = Date.now();
  const failed = steps.some((step) => step.status !== "pass");
  return {
    schema_version: EVOLUTION_PIPELINE_VERSION,
    run_id: `dryrun_${randomBytes(12).toString("hex")}`,
    mode: "dryrun",
    status: failed ? "fail" : "pass",
    started_at: new Date(started).toISOString(),
    completed_at: new Date(completed).toISOString(),
    duration_ms: Math.round((performance.now() - began) * 1000) / 1000,
    dry_run: true,
    writes: 0,
    would_write_count: steps.filter((step) => step.would_write).length,
    steps,
    schema_checks: schemaChecks,
    schema_valid: schemaChecks.every((check) => check.ok),
    id_traceability: idTraceability,
    id_traceability_checks: traceabilityChecks,
    recall: { budget_tokens: budget, budget_used: recall.value.result.budget_used, suppressed_count: recall.value.result.suppressed_count },
  };
}
