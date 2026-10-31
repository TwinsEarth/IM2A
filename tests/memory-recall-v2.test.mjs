import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { MemoryHub } from "../packages/memory-hub/src/index.mjs";

const DAY = 24 * 60 * 60 * 1000;
const base = Date.UTC(2026, 9, 31, 12, 0, 0);
const padded = "release memory detail ".repeat(18);

async function withHub(fn) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-recall-v2-"));
  const hub = new MemoryHub({ path: path.join(dir, "memory.db"), tenant: "namespace-50", now: () => base });
  try {
    return await fn(hub);
  } finally {
    hub.close();
    await rm(dir, { recursive: true, force: true });
  }
}

function seed(hub) {
  for (let i = 0; i < 15; i += 1) {
    hub.put({
      id: `skill-${i}`, layer: "skill_description", kind: "skill",
      title: `release skill ${i}`, body: `${padded} skill principle ${i}`,
      sourceTraceId: `trc_skill_${i}`, confidence: 0.85, updatedAt: base - i * DAY,
      metadata: { test: "v258" },
    });
  }
  for (let i = 0; i < 20; i += 1) {
    hub.put({
      id: `atom-${i}`, layer: "atom", kind: "fact",
      title: `release atom ${i}`, body: `${padded} atom fact ${i}`,
      sourceTraceId: `trc_atom_${i}`, confidence: 0.7, updatedAt: base - (i % 20) * DAY,
      metadata: { test: "v258" },
    });
  }
  for (let i = 0; i < 12; i += 1) {
    hub.put({
      id: `raw-${i}`, layer: "raw_event", kind: "episode",
      title: `release raw ${i}`, body: `${padded} raw event ${i}`,
      sourceTraceId: `trc_raw_${i}`, confidence: 0.6, updatedAt: base - i * DAY,
      metadata: { test: "v258" },
    });
  }
  for (let i = 0; i < 10; i += 1) {
    hub.put({
      id: `entity-${i}`, layer: "entity_page", kind: "entity",
      title: `release entity ${i}`, body: `${padded} entity page ${i}`,
      sourceTraceId: `trc_entity_${i}`, confidence: 0.8, updatedAt: base - i * DAY,
      metadata: { test: "v258" },
    });
  }
  hub.put({
    id: "atom-stale", layer: "atom", kind: "fact",
    title: "release stale atom", body: `${padded} stale release fact`,
    confidence: 0.9, updatedAt: base - 45 * DAY, metadata: { test: "v258" },
  });
  hub.put({
    id: "atom-conflict-low", layer: "atom", kind: "fact",
    title: "release deployment window", body: `${padded} conflicting lower source`,
    confidence: 0.3, conflictKey: "deployment-window", updatedAt: base - DAY, metadata: { test: "v258" },
  });
  hub.put({
    id: "atom-conflict-high", layer: "atom", kind: "fact",
    title: "release deployment window", body: `${padded} higher confidence source`,
    confidence: 0.95, conflictKey: "deployment-window", updatedAt: base - DAY, metadata: { test: "v258" },
  });
  hub.put({
    id: "atom-low", layer: "atom", kind: "fact",
    title: "release low confidence", body: `${padded} low confidence release`,
    confidence: 0.2, updatedAt: base - DAY, metadata: { test: "v258" },
  });
}

test("50+ memory namespace obeys budget and suppresses stale atoms", async () => {
  await withHub(async (hub) => {
    seed(hub);
    const total = hub.db.prepare("SELECT COUNT(*) AS n FROM memory").get().n;
    assert.ok(total >= 50, `memory count=${total}`);
    const before = total;
    const result = hub.recall({ query: "release", agentDid: "did:im2a:recall", budgetTokens: 2000 });
    assert.ok(result.budget_used <= 2000, `budget_used=${result.budget_used}`);
    assert.ok(result.items.length > 0);
    assert.ok(result.suppressed_count > 0, `suppressed=${result.suppressed_count}`);
    assert.equal(result.namespace, "namespace-50");
    assert.equal(result.agent_did, "did:im2a:recall");
    for (const item of result.items) {
      assert.ok(["raw_event", "atom", "skill_description", "entity_page"].includes(item.layer));
      assert.ok(["fresh", "stale", "conflicted", "low_confidence"].includes(item.quality_flag));
      assert.ok(item.score >= 0 && item.score <= 1);
      assert.ok(typeof item.content === "string" && item.content.length > 0);
      assert.ok(item.source_trace_id === null || typeof item.source_trace_id === "string");
    }
    const top3 = result.items.slice(0, 3);
    assert.ok(top3.every((item) => item.quality_flag !== "stale"), JSON.stringify(top3));
    assert.equal(hub.db.prepare("SELECT COUNT(*) AS n FROM memory").get().n, before);
  });
});

test("include_stale exposes stale quality flags; layer routing is honored", async () => {
  await withHub(async (hub) => {
    seed(hub);
    const all = hub.recall({ query: "release", agentDid: "did:im2a:recall", budgetTokens: 100000, includeStale: true });
    assert.ok(all.items.some((item) => item.quality_flag === "stale"), "stale should be visible when explicitly requested");
    assert.ok(all.items.some((item) => item.quality_flag === "conflicted"), "conflicted should be marked");
    assert.ok(all.items.some((item) => item.quality_flag === "low_confidence"), "low confidence should be marked");

    const onlySkills = hub.recall({
      query: "release", agentDid: "did:im2a:recall", budgetTokens: 2000, layers: ["skill_description"],
    });
    assert.ok(onlySkills.items.length > 0);
    assert.ok(onlySkills.items.every((item) => item.layer === "skill_description"));
    assert.ok(onlySkills.budget_used <= 2000);
  });
});

test("invalid budget/layers fail closed", async () => {
  await withHub(async (hub) => {
    seed(hub);
    assert.throws(() => hub.recall({ query: "release", budgetTokens: 0 }), /INVALID_BUDGET_TOKENS/);
    assert.throws(() => hub.recall({ query: "release", budgetTokens: 100, layers: ["unknown"] }), /INVALID_LAYERS/);
    assert.throws(() => hub.put({ title: "bad", body: "bad", layer: "unknown" }), /INVALID_LAYER/);
    assert.throws(() => hub.put({ title: "bad", body: "bad", confidence: 2 }), /INVALID_CONFIDENCE/);
  });
});
