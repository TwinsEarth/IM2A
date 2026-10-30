import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { generate } from "../packages/agent-plugins/generator/src/generate.mjs";
import { assertValid, validateSchema } from "./schema-validator.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
export const defaultRoot = path.resolve(here, "..");

export const GATES = ["commit", "tag", "Release", "npm", "CI", "Compat", "Release workflow", "npm-publish", "healthz"];

async function readJson(root, rel) {
  return JSON.parse(await readFile(path.join(root, rel), "utf8"));
}

export async function validateP0({ root = defaultRoot } = {}) {
  const matrix = await readJson(root, "version-matrix.json");
  const manifest = await readJson(root, "release-manifest.json");
  const manifestSchema = await readJson(root, "release-manifest.schema.json");
  const versions = await readJson(root, "packages/protocol/versions.json");
  const a2aSource = await readJson(root, "packages/protocol/vendor/a2a/SOURCE.json");
  const a2aSchema = await readJson(root, "packages/protocol/schemas/a2a-profile.schema.json");
  const a2aFixture = await readJson(root, "packages/protocol/fixtures/a2a-agent-card.v1.0.1.json");
  const a2aNegative = await readJson(root, "packages/protocol/fixtures/a2a-agent-card.negative.json");
  const mcpSchema = await readJson(root, "packages/protocol/schemas/mcp-profile.schema.json");
  const pluginSchema = await readJson(root, "packages/protocol/schemas/agent-plugin.schema.json");
  const sessionSchema = await readJson(root, "packages/protocol/schemas/session-ref.schema.json");
  const sessionFixture = await readJson(root, "packages/protocol/fixtures/session-ref.valid.json");
  const adapterSchema = await readJson(root, "packages/harness-adapters/schemas/adapter.schema.json");
  const adapters = await readJson(root, "packages/harness-adapters/adapters.json");
  const targets = await readJson(root, "packages/agent-plugins/generator/targets.json");
  const plugin = await readJson(root, "packages/agent-plugins/source/plugin.json");
  const mcp = await readJson(root, "packages/agent-plugins/source/mcp.json");
  const dshPackage = await readJson(root, "packages/im2a-dsh-plugin/package.json");
  const dshPatch = await readFile(path.join(root, "packages/im2a-dsh-plugin/cordis.patch.yml"), "utf8");
  const identPackage = await readJson(root, "packages/ident-code/package.json");
  const identSchema = await readJson(root, "packages/protocol/schemas/ident-code.schema.json");

  if (matrix.platform.version !== "2.4.6") throw new Error("platform version mismatch");
  if (matrix.go.version !== "2.3.26") throw new Error("go version mismatch");
  if (matrix.releaseGates.length !== GATES.length) throw new Error("release gates must be nine");

  // A2A pin checks. The spec URL must not be a floating /latest/ alias.
  if (versions.protocols.a2a.patchRelease !== "v1.0.1") throw new Error("A2A release must be v1.0.1");
  if (versions.protocols.a2a.releaseCommit !== a2aSource.releaseCommit) throw new Error("A2A commit pin mismatch");
  if (versions.protocols.a2a.spec.includes("/latest/")) throw new Error("A2A spec must not use /latest/");
  if (versions.protocols.a2a.protocolVersion !== "1.0") throw new Error("A2A protocol version must be 1.0");
  if (versions.protocols["ident-code"]?.protocolVersion !== "1.0") throw new Error("ident-code protocol must be 1.0");
  if (versions.protocols["ident-code"]?.status !== "pinned") throw new Error("ident-code must be pinned");
  if (identPackage.name !== "@twinsearth/im2a-ident-code") throw new Error("ident-code package name mismatch");
  if (identSchema.$id !== "https://im2a.dev/schemas/ident-code-1.0.json") throw new Error("ident-code schema id mismatch");
  if (!identSchema.$defs?.authCode || !identSchema.$defs?.userCode) throw new Error("ident-code schema definitions missing");
  if (versions.protocols.mcp.protocolVersion !== "2026-07-28") throw new Error("MCP pin mismatch");
  if (versions.protocols.mcp.status !== "pinned") throw new Error("MCP must be pinned");

  // Execute all local schemas against fixtures/source. This is the check that
  // the previous P0 report falsely claimed by only comparing strings.
  assertValid(a2aSchema, a2aFixture, "A2A fixture");
  const negative = validateSchema(a2aSchema, a2aNegative);
  if (negative.ok) throw new Error("A2A negative fixture was accepted");
  assertValid(mcpSchema, mcp, "MCP source");
  assertValid(pluginSchema, plugin, "Agent plugin source");
  assertValid(sessionSchema, sessionFixture, "session-ref fixture");
  for (const adapter of adapters) assertValid(adapterSchema, adapter, `adapter ${adapter.id}`);

  // P0-1: the DSH host bundle must be an insert patch, not a bare id override,
  // and must pin the fixed DSH build.
  if (dshPackage.name !== "@twinsearth/im2a-dsh-plugin") throw new Error("dsh host plugin name mismatch");
  if (dshPackage.engines?.dsh !== "0.2.1-rc.5") throw new Error("dsh host plugin must pin engines.dsh=0.2.1-rc.5");
  if (dshPackage.dsh?.bundle?.patch !== "./cordis.patch.yml") throw new Error("dsh host plugin patch entry missing");
  if (!/^- insert:$/m.test(dshPatch)) throw new Error("dsh host patch must use insert:");
  if (/^- id: im2a-mcp$/m.test(dshPatch)) throw new Error("dsh host patch must not use a bare id override");
  if (!/serverName: im2a/.test(dshPatch)) throw new Error("dsh host patch missing serverName");
  if (!/@deepseek-ai\/dsh-mcp-client/.test(dshPatch)) throw new Error("dsh host patch missing mcp-client");

  // Release manifest: draft must be structurally valid; non-draft must be
  // backed by evidence and artifacts. This is deliberately not a self-test.
  assertValid(manifestSchema, manifest, "release manifest");
  for (const gate of GATES) {
    if (!manifest.nineGates?.[gate]) throw new Error(`missing gate: ${gate}`);
    if (!manifestSchema.properties.nineGates.required.includes(gate)) throw new Error(`schema missing gate: ${gate}`);
  }
  if (manifest.platformVersion !== matrix.platform.version || manifest.goVersion !== matrix.go.version) {
    throw new Error("manifest version does not match version matrix");
  }
  if (manifest.status !== "draft") {
    if (!manifest.commit || manifest.components.length === 0) throw new Error("non-draft manifest needs commit/components");
    for (const gate of GATES) {
      if (manifest.nineGates[gate].status !== "passed") throw new Error(`gate not passed: ${gate}`);
      if (manifest.nineGates[gate].evidence.length === 0) throw new Error(`gate has no evidence: ${gate}`);
    }
  }

  // A single canonical harness set. Every copy must be set-equal, not just
  // length-equal; a phantom adapter must fail.
  const adapterIds = new Set(adapters.map((a) => a.id));
  const targetIds = new Set(targets.targets.map((t) => t.id));
  const pluginTargets = new Set(plugin.targets);
  if (adapters.length !== 12 || targets.targets.length !== 12 || plugin.targets.length !== 12) {
    throw new Error("harness count must be 12");
  }
  if (adapterIds.size !== 12 || targetIds.size !== 12 || pluginTargets.size !== 12) {
    throw new Error("duplicate harness id");
  }
  for (const id of adapterIds) {
    if (!targetIds.has(id) || !pluginTargets.has(id)) throw new Error(`harness set mismatch: ${id}`);
  }

  // Install method and generator format must agree for every target.
  const formatByTarget = new Map(targets.targets.map((t) => [t.id, t.format]));
  const methodByFormat = {
    "agent-plugins": "agent-plugins",
    "bundle": "bundle",
    "mcp-config": "mcp-config",
    "remote-mcp": "remote-mcp"
  };
  for (const adapter of adapters) {
    const expected = methodByFormat[formatByTarget.get(adapter.id)];
    if (adapter.install.method !== expected) {
      throw new Error(`install/format mismatch: ${adapter.id} ${adapter.install.method} vs ${formatByTarget.get(adapter.id)}`);
    }
  }

  // dist must already exist; validate is read-only and never rebuilds it.
  let checked;
  try {
    checked = await generate({ root, check: true });
  } catch (error) {
    throw new Error(`BLOCKED: generated dist missing or stale; run npm run generate (${error.message})`);
  }

  return {
    ok: true,
    platform: matrix.platform.version,
    go: matrix.go.version,
    adapters: adapters.length,
    targets: targets.targets.length,
    skills: plugin.skills.length,
    gates: GATES.length,
    a2a: `${versions.protocols.a2a.protocolVersion}/${versions.protocols.a2a.patchRelease}`,
    mcp: versions.protocols.mcp.protocolVersion,
    identCode: versions.protocols["ident-code"].protocolVersion,
    sourceHash: checked.sourceHash,
    generatedTargets: checked.targets,
    dshHostPlugin: `${dshPackage.name}@${dshPackage.version}`,
    schemaChecks: 6
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  validateP0().then((result) => {
    console.log(JSON.stringify(result, null, 2));
  }).catch((error) => {
    console.error(`P0 VALIDATION FAILED: ${error.message}`);
    process.exit(1);
  });
}
