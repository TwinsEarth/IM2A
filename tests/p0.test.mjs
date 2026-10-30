import assert from "node:assert/strict";
import { cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { validateP0 } from "../scripts/validate.mjs";
import { validateSchema } from "../scripts/schema-validator.mjs";

test("P0 version matrix, schemas, and generated dist are consistent", async () => {
  const result = await validateP0();
  assert.equal(result.ok, true);
  assert.equal(result.platform, "2.4.6");
  assert.equal(result.go, "2.3.26");
  assert.equal(result.adapters, 12);
  assert.equal(result.targets, 12);
  assert.equal(result.skills, 4);
  assert.equal(result.gates, 9);
  assert.equal(result.a2a, "1.0/v1.0.1");
  assert.equal(result.mcp, "2026-07-28");
  assert.match(result.sourceHash, /^[0-9a-f]{64}$/);
  assert.equal(result.generatedTargets, 12);
  assert.equal(result.schemaChecks, 6);
});

test("A2A negative fixture is rejected", async () => {
  const schema = JSON.parse(await readFile(
    new URL("../packages/protocol/schemas/a2a-profile.schema.json", import.meta.url), "utf8"));
  const negative = JSON.parse(await readFile(
    new URL("../packages/protocol/fixtures/a2a-agent-card.negative.json", import.meta.url), "utf8"));
  const result = validateSchema(schema, negative);
  assert.equal(result.ok, false);
});

test("tampered source cannot self-heal during validation", async () => {
  const sourceRoot = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "im2a-p0-"));
  try {
    await cp(sourceRoot, tempRoot, { recursive: true });
    const pluginPath = path.join(tempRoot, "packages", "agent-plugins", "source", "plugin.json");
    const plugin = JSON.parse(await readFile(pluginPath, "utf8"));
    plugin.description = `${plugin.description} tampered`;
    await writeFile(pluginPath, JSON.stringify(plugin, null, 2));
    await assert.rejects(() => validateP0({ root: tempRoot }), /BLOCKED: generated dist missing or stale/);
  } finally {
    await rm(tempRoot, { recursive: true, force: true });
  }
});
