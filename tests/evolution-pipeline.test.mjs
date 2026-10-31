import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runEvolutionPipelineDryRun } from "../packages/im2a-http-mcp/evolution-pipeline.mjs";
import { validateSchema } from "../scripts/schema-validator.mjs";

const reportSchema = JSON.parse(readFileSync(new URL("../docs/contracts/evolution-pipeline-1.0.schema.json", import.meta.url), "utf8"));

test("evolution pipeline dry run covers five steps, zero writes and full ID traceability", () => {
  const report = runEvolutionPipelineDryRun();
  assert.equal(report.schema_version, "1.0");
  assert.equal(report.mode, "dryrun");
  assert.equal(report.status, "pass");
  assert.equal(report.dry_run, true);
  assert.equal(report.writes, 0);
  assert.equal(report.would_write_count, 2);
  assert.equal(report.schema_valid, true);
  assert.equal(report.id_traceability, "full");
  assert.ok(report.duration_ms < 30000, `duration_ms=${report.duration_ms}`);
  assert.equal(report.steps.length, 5);
  assert.deepEqual(report.steps.map((step) => step.step), [
    "trace_ingest",
    "reflect_trigger",
    "curator_audit",
    "skill_guard_check",
    "memory_recall_v2",
  ]);
  assert.ok(report.steps.every((step) => step.status === "pass" && step.schema_valid));
  assert.ok(report.schema_checks.every((check) => check.ok));
  assert.ok(report.id_traceability_checks.every((check) => check.ok));
  const trace = report.steps[0];
  const reflect = report.steps[1];
  const guard = report.steps[3];
  const recall = report.steps[4];
  assert.match(trace.ids.trace_id, /^[0-9a-f]{32}$/);
  assert.equal(reflect.ids.trace_id, trace.ids.trace_id);
  assert.ok(reflect.ids.candidate_ids.length > 0);
  assert.ok(reflect.ids.candidate_ids.every((id) => /^cand_[0-9a-f]{24}$/.test(id)));
  assert.ok(guard.ids.audit_event_id.startsWith("evt_dryrun_"));
  assert.ok(recall.ids.source_trace_ids.includes(trace.ids.trace_id));
  assert.ok(trace.would_write && guard.would_write);
  assert.ok(trace.would_write.persisted === undefined);
  assert.ok(guard.would_write.persisted === false);
  assert.equal(guard.output.allowed, true);
  assert.equal(guard.output.denied_reason, null);
  assert.ok(report.recall.budget_used <= report.recall.budget_tokens);
  const schemaResult = validateSchema(reportSchema, report);
  assert.equal(schemaResult.ok, true, JSON.stringify(schemaResult.errors));
});
