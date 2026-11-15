/**
 * IM2A six-layer contracts: L1 -> L2, L6 -> L2, L5.2 -> L2.
 *
 * Read-only. Each contract is checked three ways:
 *   1. the valid fixture must be accepted;
 *   2. every schema negative must be rejected with the expected reason;
 *   3. every semantic negative must be rejected by the contract's own rules,
 *      for constraints JSON Schema cannot express.
 *
 * Positive controls (validMutations) prove the gate is not simply "reject
 * everything unusual": a legitimate variant must still pass both schema and
 * semantics.
 *
 * See docs/design/IM2A-SYSTEM-FRAMEWORK-6L.md and
 * docs/design/IM2A-6L-EXECUTION-PLAN.md (WP-0).
 */
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { validateSchema } from "./schema-validator.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
export const defaultRoot = path.resolve(here, "..");

function setPath(target, dotted, value) {
  const keys = dotted.split(".");
  let node = target;
  for (let i = 0; i < keys.length - 1; i += 1) {
    const key = keys[i];
    node = Array.isArray(node) ? node[Number(key)] : node[key];
    if (node === undefined || node === null) throw new Error(`cannot traverse ${dotted}`);
  }
  const last = keys[keys.length - 1];
  if (Array.isArray(node)) node[Number(last)] = value;
  else node[last] = value;
}

function unsetPath(target, dotted) {
  const keys = dotted.split(".");
  let node = target;
  for (let i = 0; i < keys.length - 1; i += 1) {
    const key = keys[i];
    node = Array.isArray(node) ? node[Number(key)] : node[key];
    if (node === undefined || node === null) return;
  }
  const last = keys[keys.length - 1];
  if (Array.isArray(node)) node.splice(Number(last), 1);
  else delete node[last];
}

export function mutate(base, testCase) {
  const value = structuredClone(base);
  for (const [dotted, next] of Object.entries(testCase.set ?? {})) setPath(value, dotted, next);
  for (const dotted of testCase.unset ?? []) unsetPath(value, dotted);
  return value;
}

/** The endpoint set is declared once, the output budget must fit the window,
 *  and a self-hosted model must name its weights license. */
function capabilitySemantics(value) {
  const errors = [];
  const protocols = value.endpoints.map((endpoint) => endpoint.protocol);
  if (new Set(protocols).size !== protocols.length) errors.push("duplicate endpoint protocol");
  if (value.limits.max_output_tokens > value.limits.context_tokens) {
    errors.push("max_output_tokens must not exceed context_tokens");
  }
  if (value.provider.deployment === "self_hosted" && !value.model.weights_license) {
    errors.push("self_hosted requires weights_license");
  }
  return errors;
}

/** Billing implies real cost, and only external work may be billed. The converse
 *  is deliberately false: internal work can consume a large amount and stay free. */
function meteringSemantics(value) {
  const errors = [];
  if (value.billable && !(value.cost.amount > 0)) errors.push("billable must match cost.amount > 0");
  if (value.task.purpose !== "external" && value.billable) {
    errors.push("internal purpose must not be billable");
  }
  return errors;
}

/** An instant-world UI is a trace product until promotion, and promotion is an
 *  event with a target skill. */
function uiArtifactSemantics(value) {
  const errors = [];
  if (value.status === "promoted") {
    if (typeof value.promoted_skill_id !== "string" || value.promoted_skill_id.length === 0) {
      errors.push("promoted requires promoted_skill_id");
    }
    if (!value.promoted_at) errors.push("promoted requires promoted_at");
  } else if (value.promoted_skill_id !== undefined && value.promoted_skill_id !== null) {
    errors.push("ephemeral must not carry promoted_skill_id");
  }
  return errors;
}

const CONTRACTS = [
  {
    id: "model-capability-manifest-1.0",
    direction: "L1 -> L2",
    schema: "docs/contracts/model-capability-manifest-1.0.schema.json",
    valid: "docs/contracts/fixtures/model-capability-manifest-1.0.valid.json",
    negatives: "docs/contracts/fixtures/model-capability-manifest-1.0.negatives.json",
    semantics: capabilitySemantics,
  },
  {
    id: "resource-metering-record-1.0",
    direction: "L6 -> L2",
    schema: "docs/contracts/resource-metering-record-1.0.schema.json",
    valid: "docs/contracts/fixtures/resource-metering-record-1.0.valid.json",
    negatives: "docs/contracts/fixtures/resource-metering-record-1.0.negatives.json",
    semantics: meteringSemantics,
  },
  {
    id: "ui-artifact-1.0",
    direction: "L5.2 -> L2",
    schema: "docs/contracts/ui-artifact-1.0.schema.json",
    valid: "docs/contracts/fixtures/ui-artifact-1.0.valid.json",
    negatives: "docs/contracts/fixtures/ui-artifact-1.0.negatives.json",
    semantics: uiArtifactSemantics,
  },
];

async function readJson(root, rel) {
  return JSON.parse(await readFile(path.join(root, rel), "utf8"));
}

export async function checkLayerContracts({ root = defaultRoot } = {}) {
  const failures = [];
  const summary = [];

  for (const contract of CONTRACTS) {
    const schema = await readJson(root, contract.schema);
    const base = await readJson(root, contract.valid);
    const negatives = await readJson(root, contract.negatives);

    const positive = validateSchema(schema, base);
    if (!positive.ok) {
      failures.push({ contract: contract.id, case: "valid fixture", error: positive.errors.join("; ") });
    }
    for (const error of contract.semantics(base)) {
      failures.push({ contract: contract.id, case: "valid fixture semantics", error });
    }

    let rejected = 0;
    for (const testCase of negatives.cases ?? []) {
      const result = validateSchema(schema, mutate(base, testCase));
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
    for (const testCase of negatives.validMutations ?? []) {
      const value = mutate(base, testCase);
      const result = validateSchema(schema, value);
      if (!result.ok) {
        failures.push({ contract: contract.id, case: testCase.id, error: `valid mutation rejected: ${result.errors.join("; ")}` });
        continue;
      }
      const semanticErrors = contract.semantics(value);
      if (semanticErrors.length) {
        failures.push({ contract: contract.id, case: testCase.id, error: `valid mutation failed semantics: ${semanticErrors.join("; ")}` });
      } else {
        accepted += 1;
      }
    }

    let semanticRejected = 0;
    for (const testCase of negatives.semanticCases ?? []) {
      const errors = contract.semantics(mutate(base, testCase));
      const joined = errors.join("; ");
      if (errors.length === 0) {
        failures.push({ contract: contract.id, case: testCase.id, error: "semantic negative was accepted" });
      } else if (!joined.includes(testCase.expect)) {
        failures.push({ contract: contract.id, case: testCase.id, error: `expected "${testCase.expect}", got "${joined}"` });
      } else {
        semanticRejected += 1;
      }
    }

    summary.push({
      contract: contract.id,
      direction: contract.direction,
      schema: contract.schema,
      validFixture: positive.ok,
      negativesRejected: `${rejected}/${(negatives.cases ?? []).length}`,
      semanticNegativesRejected: `${semanticRejected}/${(negatives.semanticCases ?? []).length}`,
      validMutationsAccepted: `${accepted}/${(negatives.validMutations ?? []).length}`,
    });
  }

  return { ok: failures.length === 0, contracts: summary, failures };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  checkLayerContracts()
    .then((result) => {
      console.log(JSON.stringify(result, null, 2));
      if (!result.ok) process.exitCode = 1;
    })
    .catch((error) => {
      console.error(`LAYER CONTRACT CHECK FAILED: ${error.message}`);
      process.exit(1);
    });
}
