/**
 * Draft v2.5.0 bridge/plugin contract checks.
 *
 * Read-only: validates the two draft contracts against their fixtures and
 * proves that every negative control is rejected. Nothing here runs at
 * publish time yet; see docs/design/IM2A-v2.5.0-MCP-BRIDGE-AND-PLUGINS2-PACKAGING.md §6.4
 * for the exact gate wiring.
 */
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { validateSchema } from "./schema-validator.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const defaultRoot = path.resolve(here, "..");

/** The bridge tool surface is closed. Adding a tool must edit this list on purpose. */
const EXPECTED_TOOLS = [
  "im2a_harness_list", "im2a_send", "im2a_recv", "im2a_presence", "im2a_whoami",
  "im2a_history", "skills_list", "skill_view", "skill_manage", "curator",
  "memory_search", "memory_get", "memory_propose"
];

/**
 * Name → scope is a semantic rule JSON Schema cannot express, and a tool with
 * the wrong scope is worse than a missing tool: it is a capability nobody meant
 * to grant. The curator scope must never appear on an ordinary tool.
 */
const SCOPE_BY_TOOL = {
  im2a_harness_list: ["bus:read"],
  im2a_send: ["bus:send"],
  im2a_recv: ["bus:read"],
  im2a_presence: ["bus:read"],
  im2a_whoami: ["bus:read"],
  im2a_history: ["bus:read"],
  skills_list: ["skill:read"],
  skill_view: ["skill:read"],
  skill_manage: ["skill:propose"],
  curator: ["skill:curate"],
  memory_search: ["memory:read"],
  memory_get: ["memory:read"],
  memory_propose: ["memory:propose"]
};

async function readJson(root, rel) {
  return JSON.parse(await readFile(path.join(root, rel), "utf8"));
}

function mutate(doc, patch = {}) {
  for (const [dotted, value] of Object.entries(patch.set ?? {})) {
    const keys = dotted.split(".");
    const last = keys.pop();
    keys.reduce((node, key) => node[key], doc)[last] = value;
  }
  for (const dotted of patch.unset ?? []) {
    const keys = dotted.split(".");
    const last = keys.pop();
    delete keys.reduce((node, key) => node[key], doc)[last];
  }
  return doc;
}

export async function checkBridgeContracts({ root = defaultRoot } = {}) {
  const contracts = [
    {
      id: "mcp-bridge-tool-catalog",
      schema: "docs/contracts/mcp-bridge-tool-catalog-2.0.schema.json",
      fixture: "docs/contracts/fixtures/mcp-bridge-tool-catalog.valid.json",
      negatives: "docs/contracts/fixtures/mcp-bridge-tool-catalog.negatives.json",
      surfaceCheck: true
    },
    {
      id: "agent-plugin-2.0",
      schema: "docs/contracts/agent-plugin-2.0.schema.json",
      fixture: "docs/contracts/fixtures/agent-plugin-2.0.valid.json",
      negatives: "docs/contracts/fixtures/agent-plugin-2.0.negatives.json",
      surfaceCheck: false
    }
  ];

  const failures = [];
  const summary = [];

  for (const contract of contracts) {
    const schema = await readJson(root, contract.schema);
    const base = await readJson(root, contract.fixture);
    const negativeSet = await readJson(root, contract.negatives);

    const positive = validateSchema(schema, base);
    if (!positive.ok) {
      failures.push({ contract: contract.id, case: "valid fixture", errors: positive.errors });
    }

    if (contract.surfaceCheck) {
      const names = base.tools.map((tool) => tool.name);
      const missing = EXPECTED_TOOLS.filter((name) => !names.includes(name));
      const extra = names.filter((name) => !EXPECTED_TOOLS.includes(name));
      if (names.length !== new Set(names).size) failures.push({ contract: contract.id, case: "duplicate tool name" });
      if (missing.length || extra.length) {
        failures.push({ contract: contract.id, case: "tool surface drift", missing, extra });
      }
      for (const tool of base.tools) {
        const expectedScope = SCOPE_BY_TOOL[tool.name];
        if (!expectedScope) continue;
        if (tool.scope.join(",") !== expectedScope.join(",")) {
          failures.push({ contract: contract.id, case: `${tool.name} scope`, expected: expectedScope, actual: tool.scope });
        }
      }
    }

    let rejected = 0;
    for (const testCase of negativeSet.cases) {
      const result = validateSchema(schema, mutate(structuredClone(base), testCase));
      const joined = result.errors.join("; ");
      if (result.ok) {
        failures.push({ contract: contract.id, case: testCase.id, error: "negative control was accepted" });
      } else if (!joined.includes(testCase.expect)) {
        failures.push({ contract: contract.id, case: testCase.id, error: `expected "${testCase.expect}", got "${joined}"` });
      } else {
        rejected += 1;
      }
    }

    let accepted = 0;
    for (const testCase of negativeSet.validMutations ?? []) {
      const result = validateSchema(schema, mutate(structuredClone(base), testCase));
      if (!result.ok) {
        failures.push({ contract: contract.id, case: testCase.id, error: result.errors.join("; ") });
      } else {
        accepted += 1;
      }
    }

    summary.push({
      contract: contract.id,
      schema: contract.schema,
      validFixture: positive.ok,
      negativesRejected: `${rejected}/${negativeSet.cases.length}`,
      validMutationsAccepted: `${accepted}/${(negativeSet.validMutations ?? []).length}`
    });
  }

  return { ok: failures.length === 0, contracts: summary, failures };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  checkBridgeContracts()
    .then((result) => {
      console.log(JSON.stringify(result, null, 2));
      if (!result.ok) process.exitCode = 1;
    })
    .catch((error) => {
      console.error(`BRIDGE CONTRACT CHECK FAILED: ${error.message}`);
      process.exit(1);
    });
}
