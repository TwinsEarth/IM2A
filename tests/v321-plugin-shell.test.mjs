import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import {
  TOOL_NAME_RE,
  createRegistry,
  computeLoadPlan,
  computeUnloadOrder,
  effectiveStatus,
  loadPlugin,
  validateManifest,
} from "../packages/dsh-im2a-plugin/kernel/registry.mjs";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "packages", "dsh-im2a-plugin");
const LEGACY_TOOLS = [
  "autonomy_pipeline_dryrun",
  "contribution_audit",
  "curator",
  "curator_audit",
  "drive_calibrate",
  "drive_list",
  "drive_manage",
  "drive_propose",
  "evolution_pipeline_dryrun",
  "goal_buffer",
  "goal_schedule",
  "im2a_harness_list",
  "im2a_history",
  "im2a_presence",
  "im2a_recv",
  "im2a_send",
  "im2a_whoami",
  "memory_get",
  "memory_propose",
  "memory_recall_v2",
  "memory_search",
  "reflect_trigger",
  "scheduler_audit",
  "scheduler_cycle",
  "skill_claim",
  "skill_guard_check",
  "skill_manage",
  "skill_view",
  "skills_list",
  "trace_ingest",
];
const PLANNED = ["identity", "economy", "governance"];

test("manifest is a valid 3.2.1 single-package shell", async () => {
  const { manifest } = await loadPlugin(ROOT);
  const guard = validateManifest(manifest);
  assert.deepEqual(guard.errors, []);
  assert.equal(manifest.name, "dsh-im2a-plugin");
  assert.equal(manifest.version, "3.2.1");
  assert.equal(manifest.premium.license_implemented, false);
  for (const id of PLANNED) {
    assert.equal(manifest.modules[id].status, "planned");
    assert.deepEqual(manifest.modules[id].tools, [], `${id} must not expose tools`);
  }
  for (const [id, mod] of Object.entries(manifest.premium.modules)) {
    assert.equal(mod.status, "planned", `pro/${id} must stay planned in 3.2.1`);
    assert.equal(mod.requires_signature, true);
  }
});

test("alias table covers all 30 legacy tools with unique public names", async () => {
  const aliases = JSON.parse(
    await readFile(join(ROOT, "migrations", "3.2.1-tool-aliases.json"), "utf8"),
  );
  assert.equal(aliases.entries.length, 30);
  const olds = aliases.entries.map((e) => e.old_name).sort();
  assert.deepEqual(olds, LEGACY_TOOLS);
  for (const entry of aliases.entries) {
    assert.match(entry.new_name, TOOL_NAME_RE, `${entry.new_name} is not a valid MCP tool name`);
    assert.match(entry.schema_hash, /^sha256:[0-9a-f]{64}$/);
    assert.equal(entry.since, "3.2.1");
    assert.equal(entry.deprecated_after, "3.4.0");
    assert.ok(!PLANNED.includes(entry.module), `${entry.old_name} points at a planned module`);
  }
  for (const tool of LEGACY_TOOLS) assert.ok(aliasIndex(aliases, tool), `${tool} unmapped`);
});

function aliasIndex(aliases, name) {
  return aliases.entries.find((e) => e.old_name === name);
}

test("two legacy dry-runs merge onto one public tool", async () => {
  const aliases = JSON.parse(
    await readFile(join(ROOT, "migrations", "3.2.1-tool-aliases.json"), "utf8"),
  );
  const merged = aliases.entries.filter((e) => e.new_name === "im2a_observability_pipeline_dryrun");
  assert.equal(merged.length, 2);
  assert.deepEqual(merged.map((e) => e.fixed_args.pipeline).sort(), ["autonomy", "evolution"]);
});

test("load order is topological and unload order is its reverse", async () => {
  const { manifest } = await loadPlugin(ROOT);
  const { order, errors } = computeLoadPlan(manifest);
  assert.deepEqual(errors, []);
  assert.deepEqual(order, [
    "memory",
    "evolution",
    "asset",
    "identity",
    "autonomy",
    "economy",
    "fleet",
    "governance",
    "observability",
  ]);
  for (const id of order) {
    for (const dep of manifest.modules[id].deps ?? []) {
      assert.ok(order.indexOf(dep) < order.indexOf(id), `${dep} must precede ${id}`);
    }
  }
  assert.deepEqual(computeUnloadOrder(order), [...order].reverse());
});

test("soft deps never mark a working module unavailable", async () => {
  const { manifest } = await loadPlugin(ROOT);
  assert.deepEqual(manifest.modules.fleet.soft_deps, ["economy"]);
  assert.equal(effectiveStatus(manifest, "fleet"), "available");
  assert.equal(effectiveStatus(manifest, "economy"), "planned");
});

test("registry hides planned modules and reports them honestly", async () => {
  const { manifest, aliases } = await loadPlugin(ROOT);
  const registry = createRegistry({ manifest, aliases });
  const names = registry.publicTools().map((t) => t.name);
  assert.equal(new Set(names).size, names.length, "tool names must be unique");
  assert.ok(names.includes("im2a_memory_recall"));
  assert.ok(names.includes("memory_recall_v2"), "legacy alias must stay callable");
  for (const id of PLANNED) {
    assert.ok(!names.some((n) => n.startsWith(`im2a_${id}_`)), `${id} leaked a tool`);
  }
  const report = registry.statusReport();
  assert.equal(report.premium.license_implemented, false);
  const economy = report.modules.find((m) => m.id === "economy");
  assert.equal(economy.effective, "planned");
  assert.equal(economy.tools, 0);
});

test("bus returns error values, never fake success", async () => {
  const { manifest, aliases } = await loadPlugin(ROOT);
  const registry = createRegistry({ manifest, aliases });

  const unknown = await registry.call("not_a_tool");
  assert.equal(unknown.isError, true);
  assert.equal(unknown.errorCode, "unknown_tool");

  const plannedCall = await registry.call("im2a_governance_propose");
  assert.equal(plannedCall.isError, true);
  assert.equal(plannedCall.errorCode, "not_implemented");
  assert.equal(plannedCall.module, "governance");

  const noHandler = await registry.call("im2a_memory_recall", { query: "x" });
  assert.equal(noHandler.errorCode, "not_implemented");

  const viaAlias = registry.resolve("memory_recall_v2");
  assert.equal(viaAlias.name, "im2a_memory_recall");
  assert.equal(viaAlias.deprecated, true);

  const merged = registry.resolve("autonomy_pipeline_dryrun");
  assert.deepEqual(merged.fixedArgs, { pipeline: "autonomy" });
});

test("disabling a module with dependants is refused with the dependency chain", async () => {
  const { manifest, aliases } = await loadPlugin(ROOT);
  const registry = createRegistry({ manifest, aliases });
  const refused = registry.setModule("asset", false);
  assert.equal(refused.isError, true);
  assert.equal(refused.errorCode, "dependency_conflict");
  assert.ok(refused.dependants.includes("autonomy"));
  assert.equal(registry.setModule("memory", false).errorCode, "dependency_conflict");
  assert.equal(registry.setModule("observability", false).ok, true);
  assert.equal(registry.setModule("identity", true).errorCode, "not_implemented");
});
