import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { TraceStore } from "../packages/im2a-http-mcp/trace-store.mjs";
import { assessTrace, reflectOnTrace } from "../packages/im2a-http-mcp/reflect.mjs";
import { mergeReflectConfig } from "../packages/im2a-http-mcp/reflect-config.mjs";

const did = "did:im2a:agent-reflect-test";
const digest = (text) => `sha256:${createHash("sha256").update(text).digest("hex")}`;
const config = mergeReflectConfig();

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

function batch(events) {
  return { session_id: "hs_reflect_test", agent_did: did, events };
}

async function withStore(fn) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-reflect-"));
  try {
    return await fn({ dir, store: new TraceStore({ dir, tenant: "tencent-dev" }) });
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

test("5+ successful tool calls are worth reflecting and yield a candidate", async () => {
  await withStore(async ({ store }) => {
    const events = ["search", "read", "plan", "send", "verify"].map((name) => event(name));
    const trace = await store.ingest(batch(events));
    const result = await reflectOnTrace({ traceStore: store, traceId: trace.trace_id, agentDid: did, config });
    assert.equal(result.assessment.worth_reflecting, true);
    assert.ok(result.assessment.complexity_score >= 0.25);
    assert.ok(result.distilled_candidates.length >= 1);
    for (const candidate of result.distilled_candidates) {
      assert.ok(["skill_create", "skill_patch", "principle_extract"].includes(candidate.candidate_type));
      assert.deepEqual(candidate.source_trace_ids, [trace.trace_id]);
      assert.ok(candidate.confidence > 0 && candidate.confidence < 1, `confidence=${candidate.confidence}`);
    }
  });
});

test("a 2-step simple query is not worth reflecting and returns no candidates", async () => {
  await withStore(async ({ store }) => {
    const trace = await store.ingest(batch([event("lookup"), event("answer")]));
    const result = await reflectOnTrace({ traceStore: store, traceId: trace.trace_id, agentDid: did, config });
    assert.equal(result.assessment.worth_reflecting, false);
    assert.deepEqual(result.distilled_candidates, []);
    assert.ok(result.assessment.reason_codes.includes("insufficient_tool_calls"));
  });
});

test("failure recovery and cross-agent evidence are both reflected", async () => {
  await withStore(async ({ store }) => {
    const recovered = await store.ingest(batch([
      { span_type: "skill_load", name: "release-gate", skill_version: "1.0.0", input_digest: digest("s-in"), output_digest: digest("s-out"), duration_ms: 1, token_cost: 0, success: true, error_class: null },
      event("search"),
      event("failed-write", { success: false, error_class: "tool_error" }),
      event("retry-write"),
      event("verify"),
      event("send"),
    ]));
    const recoveredResult = await reflectOnTrace({ traceStore: store, traceId: recovered.trace_id, agentDid: did, config });
    assert.equal(recoveredResult.assessment.worth_reflecting, true);
    assert.ok(recoveredResult.assessment.reason_codes.includes("failure_recovery_present"));
    assert.ok(recoveredResult.distilled_candidates.some((c) => c.candidate_type === "skill_patch"));

    const coordinated = await store.ingest(batch([
      event("im2a_send"), event("plan"), event("search"), event("verify"), event("answer"),
    ]));
    const coordinatedResult = await reflectOnTrace({ traceStore: store, traceId: coordinated.trace_id, agentDid: did, config });
    assert.equal(coordinatedResult.assessment.worth_reflecting, true);
    assert.ok(coordinatedResult.assessment.reason_codes.includes("coordination_present"));
    assert.ok(coordinatedResult.distilled_candidates.some((c) => c.summary.includes("cross-agent")));
  });
});

test("thresholds come from config, not from the agent loop", async () => {
  await withStore(async ({ store }) => {
    const trace = await store.ingest(batch(["a", "b", "c", "d", "e", "f"].map((name) => event(name))));
    const record = await store.get(trace.trace_id);
    const strict = mergeReflectConfig({ minToolCalls: 10 });
    const result = assessTrace(record, { config: strict });
    assert.equal(result.assessment.worth_reflecting, false);
    assert.ok(result.assessment.reason_codes.includes("insufficient_tool_calls"));
  });
});

test("reflection is read-only, tenant-scoped and refuses an agent mismatch", async () => {
  await withStore(async ({ store }) => {
    const trace = await store.ingest(batch(["a", "b", "c", "d", "e"].map((name) => event(name))));
    const before = await readFile(store.filePath, "utf8");
    await reflectOnTrace({ traceStore: store, traceId: trace.trace_id, agentDid: did, config });
    const after = await readFile(store.filePath, "utf8");
    assert.equal(after, before);
    await assert.rejects(
      reflectOnTrace({ traceStore: store, traceId: trace.trace_id, agentDid: "did:im2a:other", config }),
      (error) => error.code === "TRACE_AGENT_MISMATCH",
    );
    await assert.rejects(
      reflectOnTrace({ traceStore: store, traceId: "0".repeat(32), agentDid: did, config }),
      (error) => error.code === "TRACE_NOT_FOUND",
    );
    await assert.rejects(
      reflectOnTrace({ traceStore: store, traceId: trace.trace_id, agentDid: did, focus: "unknown", config }),
      (error) => error.code === "INVALID_FOCUS",
    );
  });
});
