import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { validateSchema } from "../scripts/schema-validator.mjs";
import {
  TraceStore,
  TraceValidationError,
  canonicalJson,
  sha256Hex,
} from "../packages/im2a-http-mcp/trace-store.mjs";

const schema = JSON.parse(await readFile(
  new URL("../docs/contracts/execution-trace-1.0.schema.json", import.meta.url),
  "utf8",
));

const digest = (label) => `sha256:${sha256Hex(label)}`;
const did = "did:im2a:agent-v254-test";

function event(overrides = {}) {
  return {
    span_type: "tool_call",
    name: "im2a_send",
    input_digest: digest("input"),
    output_digest: digest("output"),
    duration_ms: 12.5,
    token_cost: 7,
    success: true,
    error_class: null,
    ...overrides,
  };
}

function batch(events, overrides = {}) {
  return {
    session_id: "hs_v254_trace_test",
    agent_did: did,
    events,
    ...overrides,
  };
}

function twelveEvents() {
  return [
    event({ span_type: "skill_load", name: "release-gate", skill_version: "1.0.0" }),
    event({ span_type: "retrieval", name: "memory_search", token_cost: 0 }),
    event({ span_type: "llm_call", name: "planner", token_cost: 120 }),
    ...Array.from({ length: 8 }, (_, index) =>
      event({ name: `tool_${index + 1}`, duration_ms: 5 + index })),
    event({
      span_type: "tool_call",
      name: "deploy",
      success: false,
      error_class: "exit_1",
      output_digest: digest("failed-output"),
    }),
  ];
}

async function withStore(fn) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-trace-"));
  try {
    return await fn({ dir, store: new TraceStore({ dir, tenant: "tencent-dev" }) });
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

test("12-event trace passes the JSON Schema and carries DID plus skill version", async () => {
  await withStore(async ({ store }) => {
    const result = await store.ingest(batch(twelveEvents()));
    assert.match(result.trace_id, /^[0-9a-f]{32}$/);
    assert.equal(result.ingested_count, 12);
    assert.match(result.content_sha256, /^sha256:[0-9a-f]{64}$/);

    const lines = (await readFile(store.filePath, "utf8")).trim().split("\n");
    assert.equal(lines.length, 1);
    const record = JSON.parse(lines[0]);
    const validation = validateSchema(schema, record);
    assert.equal(validation.ok, true, validation.errors.join("; "));
    assert.equal(record.events.length, 12);
    for (const item of record.events) {
      assert.equal(item.attributes["im2a.agent_did"], did);
      assert.ok(item.attributes["im2a.skill_version"]);
    }
    assert.equal(record.events[0].attributes["im2a.skill_version"], "1.0.0");
    assert.equal(record.events[1].attributes["im2a.skill_version"], "none");

    const { content_sha256: recorded, ...body } = record;
    assert.equal(recorded, `sha256:${sha256Hex(canonicalJson(body))}`);
  });
});

test("the trajectory file is append-only and two ingests preserve both lines", async () => {
  await withStore(async ({ store }) => {
    const first = await store.ingest(batch([event()]));
    const second = await store.ingest(batch([event({ name: "im2a_recv" })]));
    assert.notEqual(first.trace_id, second.trace_id);
    const lines = (await readFile(store.filePath, "utf8")).trim().split("\n");
    assert.equal(lines.length, 2);
    assert.equal(JSON.parse(lines[0]).trace_id, first.trace_id);
    assert.equal(JSON.parse(lines[1]).trace_id, second.trace_id);
  });
});

test("raw content and unknown fields are rejected structurally", async () => {
  await withStore(async ({ store }) => {
    await assert.rejects(
      store.ingest(batch([event({ prompt: "raw secret text" })])),
      (error) => error instanceof TraceValidationError && error.message.includes("prompt"),
    );
    await assert.rejects(
      store.ingest(batch([event()], { tenant: "other" })),
      (error) => error instanceof TraceValidationError && error.message.includes("tenant"),
    );
  });
});

test("digest, DID, span type and size bounds are enforced", async () => {
  await withStore(async ({ store }) => {
    await assert.rejects(store.ingest(batch([event({ input_digest: "sha256:xyz" })])));
    await assert.rejects(store.ingest(batch([event()], { agent_did: "not-a-did" })));
    await assert.rejects(store.ingest(batch([event({ span_type: "browser" })])));
    await assert.rejects(store.ingest(batch(Array.from({ length: 2001 }, () => event()))));
    await assert.rejects(store.ingest(batch([event({ success: true, error_class: "must-not-exist" })])));
  });
});

test("skill_load requires an explicit skill_version", async () => {
  await withStore(async ({ store }) => {
    await assert.rejects(
      store.ingest(batch([event({ span_type: "skill_load", name: "release-gate" })])),
      (error) => error instanceof TraceValidationError && error.message.includes("skill_version"),
    );
  });
});

test("tenants write to different trajectory files", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-trace-"));
  try {
    const a = new TraceStore({ dir, tenant: "tenant-a" });
    const b = new TraceStore({ dir, tenant: "tenant-b" });
    await a.ingest(batch([event()]));
    await b.ingest(batch([event()]));
    assert.notEqual(a.filePath, b.filePath);
    assert.equal((await readFile(a.filePath, "utf8")).trim().split("\n").length, 1);
    assert.equal((await readFile(b.filePath, "utf8")).trim().split("\n").length, 1);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
