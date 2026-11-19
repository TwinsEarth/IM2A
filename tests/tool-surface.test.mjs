import assert from "node:assert/strict";
import test from "node:test";
import { createRegistry, loadPlugin } from "../packages/dsh-im2a-plugin/kernel/registry.mjs";

const ROOT = new URL("../packages/dsh-im2a-plugin/", import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1");

async function registry() {
  const { manifest, aliases } = await loadPlugin(ROOT);
  return createRegistry({ manifest, aliases });
}

test("the callable surface is the runtime list, not the design list", async () => {
  const { publicTools, declaredTools, runtimeTools, toolGap } = await registry();
  const gap = toolGap();

  assert.deepEqual(publicTools().map((t) => t.name).sort(), runtimeTools().map((t) => t.name).sort());
  assert.equal(gap.runtime, 32, "the bridge serves 32 tools");
  assert.equal(gap.declared, 39, "the manifest describes 39 canonical names");
  assert.equal(gap.runtime_only.length, gap.runtime - gap.both.length);
  assert.equal(gap.declared, gap.both.length + gap.declared_only.length);
  assert.equal(declaredTools().length, gap.declared);
});

test("every design name is either callable or reported as declared-only", async () => {
  const { declaredTools, toolGap } = await registry();
  const gap = toolGap();
  const declaredOnly = new Set(gap.declared_only);
  for (const tool of declaredTools()) {
    if (tool.callable) {
      assert.ok(gap.both.includes(tool.name), `${tool.name} claims callable but is not in the overlap`);
    } else {
      assert.ok(declaredOnly.has(tool.name), `${tool.name} is neither callable nor reported as declared-only`);
    }
  }
});

test("every runtime tool is attributable to a module", async () => {
  const { runtimeTools } = await registry();
  for (const tool of runtimeTools()) {
    assert.notEqual(tool.module, "unmapped", `${tool.name} has no owning module`);
  }
});

test("resolve reports the runtime name and the canonical target separately", async () => {
  const reg = await registry();
  const legacy = reg.resolve("memory_search");
  assert.equal(legacy.runtimeName, "memory_search");
  assert.equal(legacy.tool, "im2a_memory_search");
  assert.equal(legacy.deprecated, true);

  const canonical = reg.resolve("im2a_kernel_status");
  assert.equal(canonical.runtimeName, "im2a_kernel_status");
  assert.equal(canonical.deprecated, false);
});

test("a manifest without a runtime block is rejected", async () => {
  const { manifest } = await loadPlugin(ROOT);
  const broken = structuredClone(manifest);
  delete broken.runtime;
  assert.throws(() => createRegistry({ manifest: broken, aliases: { entries: [] } }), /runtime block is required/);
});
