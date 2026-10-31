import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import test from "node:test";
import { runEvolutionPipelineDryRun } from "../packages/im2a-http-mcp/evolution-pipeline.mjs";
import { validateSchema } from "../scripts/schema-validator.mjs";

const root = new URL("../", import.meta.url);
const read = (path) => readFileSync(new URL(path, root), "utf8");
const freeze = JSON.parse(read("docs/contracts/frozen-evolution-tools-2.5.9.json"));
const manifest = JSON.parse(read("docs/contracts/FROZEN-EVOLUTION-TOOLS-2.5.9.sha256.json"));
const reportSchema = JSON.parse(read("docs/contracts/evolution-pipeline-1.0.schema.json"));
const hash = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");

const names = ["trace_ingest", "reflect_trigger", "curator_audit", "skill_guard_check", "memory_recall_v2"];

test("v2.5.9 freezes exactly five interfaces and hashes match", () => {
  assert.equal(freeze.release, "2.5.9");
  assert.equal(freeze.frozen, true);
  assert.deepEqual(freeze.tools.map((tool) => tool.name), names);
  assert.equal(hash(freeze), manifest.freeze_sha256);
  assert.equal(hash(reportSchema), manifest.pipeline_report_schema_sha256);
  for (const tool of freeze.tools) {
    assert.equal(hash(tool.input_schema), manifest.tools[tool.name].input_schema_sha256);
    assert.equal(hash(tool.output_schema), manifest.tools[tool.name].output_schema_sha256);
  }
});

test("dry-run outputs validate against every frozen output schema", () => {
  const report = runEvolutionPipelineDryRun();
  const reportResult = validateSchema(reportSchema, report);
  assert.equal(reportResult.ok, true, JSON.stringify(reportResult.errors));
  assert.equal(report.id_traceability, "full");
  assert.equal(report.schema_valid, true);
  assert.equal(report.writes, 0);
  assert.equal(report.would_write_count, 2);
  for (const tool of freeze.tools) {
    const step = report.steps.find((item) => item.step === tool.name);
    assert.ok(step, `missing step ${tool.name}`);
    const result = validateSchema(tool.output_schema, step.output);
    assert.equal(result.ok, true, `${tool.name}: ${JSON.stringify(result.errors)}`);
  }
  const reflect = report.steps.find((step) => step.step === "reflect_trigger");
  const candidate = reflect.output.distilled_candidates[0];
  assert.match(candidate.candidate_id, /^cand_[0-9a-f]{24}$/);
  assert.equal(reflect.output.assessment.trace_id, report.steps[0].ids.trace_id);
  assert.ok(report.steps[4].ids.source_trace_ids.includes(report.steps[0].ids.trace_id));
});

test("freeze file is additive-only and rejects a forged schema hash", () => {
  const forged = JSON.parse(JSON.stringify(freeze));
  forged.tools[0].output_schema.required.push("not_a_real_field");
  assert.notEqual(hash(forged), manifest.freeze_sha256);
  assert.equal(freeze.tools.length, 5);
  assert.ok(freeze.policy.includes("additive-only"));
});
