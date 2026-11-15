import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { validateSchema } from "../scripts/schema-validator.mjs";
import {
  TraceStore,
  TraceValidationError,
  UI_ARTIFACT_FILE_NAME,
  sha256Hex,
} from "../packages/im2a-http-mcp/trace-store.mjs";

const traceSchema = JSON.parse(await readFile(
  new URL("../docs/contracts/execution-trace-1.0.schema.json", import.meta.url),
  "utf8",
));
const artifactSchema = JSON.parse(await readFile(
  new URL("../docs/contracts/ui-artifact-1.0.schema.json", import.meta.url),
  "utf8",
));

const DID = "did:im2a:agent-wp3a-test";
const CONTENT_HASH = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
const GOAL_ID = "goal_0123456789abcdef01234567";

const digest = (label) => `sha256:${sha256Hex(label)}`;

function event() {
  return {
    span_type: "tool_call",
    name: "im2a_send",
    input_digest: digest("input"),
    output_digest: digest("output"),
    duration_ms: 12.5,
    token_cost: 7,
    success: true,
    error_class: null,
  };
}

function batch(overrides = {}) {
  return {
    session_id: "hs_wp3a_sidecar",
    agent_did: DID,
    events: [event()],
    ...overrides,
  };
}

function declared(hash = CONTENT_HASH) {
  return { kind: "interactive", format: "html", sha256: hash, bytes: 4096, goal_id: GOAL_ID };
}

async function withStore(run) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-wp3a-"));
  const store = new TraceStore({ dir, tenant: "wp3a" });
  try {
    return await run(store, dir);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

test("declared artifacts land in a sidecar with server-minted ids", async () => {
  await withStore(async (store) => {
    const result = await store.ingest(batch({ ui_artifacts: [declared()] }));
    assert.equal(result.ui_artifact_ids.length, 1);

    const [artifact] = await store.getUiArtifacts(result.trace_id);
    assert.equal(artifact.trace_id, result.trace_id);
    assert.equal(artifact.status, "ephemeral");
    assert.equal(artifact.promoted_skill_id, null);
    assert.equal(artifact.artifact_id, result.ui_artifact_ids[0]);
    assert.equal(artifact.provenance.agent_did, DID);
    assert.equal(artifact.provenance.goal_id, GOAL_ID);
    assert.equal(validateSchema(artifactSchema, artifact).ok, true);

    // The frozen trace record must not grow a new property.
    const trace = await store.get(result.trace_id);
    assert.equal(validateSchema(traceSchema, trace).ok, true);
    assert.equal(Object.hasOwn(trace, "ui_artifacts"), false);
  });
});

test("a batch without artifacts writes no sidecar and stays valid", async () => {
  await withStore(async (store, dir) => {
    const result = await store.ingest(batch());
    assert.deepEqual(result.ui_artifact_ids, []);
    assert.deepEqual(await store.getUiArtifacts(result.trace_id), []);
    await assert.rejects(
      readFile(path.join(dir, "wp3a", UI_ARTIFACT_FILE_NAME), "utf8"),
      (error) => error.code === "ENOENT",
    );
  });
});

test("an invalid artifact rejects the batch before the trace is written", async () => {
  await withStore(async (store) => {
    await assert.rejects(
      store.ingest(batch({ ui_artifacts: [{ kind: "hologram", format: "html", sha256: CONTENT_HASH, bytes: 4096 }] })),
      (error) => error instanceof TraceValidationError && error.message.includes("kind"),
    );
    // No trace may be persisted when the batch is rejected.
    const lines = await readFile(store.filePath, "utf8").catch(() => "");
    assert.equal(lines, "");
  });
});

test("artifacts stay attached to their own trace", async () => {
  await withStore(async (store) => {
    const first = await store.ingest(batch({ ui_artifacts: [declared()] }));
    const second = await store.ingest(batch({
      ui_artifacts: [declared("f".repeat(64))],
    }));

    assert.notEqual(first.ui_artifact_ids[0], second.ui_artifact_ids[0]);

    const a = await store.getUiArtifacts(first.trace_id);
    const b = await store.getUiArtifacts(second.trace_id);
    assert.equal(a.length, 1);
    assert.equal(b.length, 1);
    assert.equal(a[0].trace_id, first.trace_id);
    assert.equal(b[0].trace_id, second.trace_id);
    assert.equal(a[0].sha256, CONTENT_HASH);
    assert.equal(b[0].sha256, "f".repeat(64));
  });
});

test("the sidecar is append-only, one JSON line per artifact", async () => {
  await withStore(async (store, dir) => {
    const first = await store.ingest(batch({ ui_artifacts: [declared(), declared("e".repeat(64))] }));
    assert.equal(first.ui_artifact_ids.length, 2);

    const raw = await readFile(path.join(dir, "wp3a", UI_ARTIFACT_FILE_NAME), "utf8");
    const lines = raw.split("\n").filter((line) => line.trim().length > 0);
    assert.equal(lines.length, 2);
    for (const line of lines) {
      const parsed = JSON.parse(line);
      assert.equal(parsed.trace_id, first.trace_id);
      assert.equal(validateSchema(artifactSchema, parsed).ok, true);
    }
  });
});

test("unsupported or extra artifact fields are rejected, not ignored", async () => {
  await withStore(async (store) => {
    await assert.rejects(
      store.ingest(batch({ ui_artifacts: [{ ...declared(), rendered_html: "<html/>" }] })),
      (error) => error instanceof TraceValidationError && error.message.includes("unexpected property"),
    );
    await assert.rejects(
      store.ingest(batch({ ui_artifacts: [{ kind: "layout", format: "html", sha256: CONTENT_HASH }] })),
      (error) => error instanceof TraceValidationError && error.message.includes("bytes"),
    );
  });
});
