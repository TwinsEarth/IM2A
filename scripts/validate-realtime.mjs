/**
 * Validates the v2.5.0 realtime contracts against their fixtures.
 *
 * Two schemas, five positive fixtures, three negative fixtures:
 *   realtime-session-profile.schema.json  (constants: channels, resume, backoff, caps)
 *   realtime-message.schema.json          (D/P/C message shape; P and C cannot carry content)
 *
 * Read-only: never regenerates fixtures, never contacts a network endpoint.
 * The structural coupling check at the end is the point of this file: the profile
 * *declares* that presence and control may not carry content, and the message
 * schema must *make that impossible*, not merely quote a constant.
 */
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { assertValid, validateSchema } from "./schema-validator.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
export const defaultRoot = path.resolve(here, "..");

const PROFILES = "packages/protocol";
const CONTENT_BEARING = ["payload", "content", "text"];

export async function validateRealtime({ root = defaultRoot } = {}) {
  const read = async (rel) => JSON.parse(await readFile(path.join(root, rel), "utf8"));

  const profile = await read(`${PROFILES}/schemas/realtime-session-profile.schema.json`);
  const message = await read(`${PROFILES}/schemas/realtime-message.schema.json`);

  assertValid(profile, await read(`${PROFILES}/fixtures/realtime-session-profile.valid.json`), "realtime profile fixture");
  const profileNegative = validateSchema(profile, await read(`${PROFILES}/fixtures/realtime-session-profile.negative.json`));
  if (profileNegative.ok) throw new Error("realtime profile negative fixture was accepted");

  const channels = ["durable", "presence", "control"];
  for (const channel of channels) {
    assertValid(message, await read(`${PROFILES}/fixtures/realtime-message.${channel}.json`), `realtime message ${channel}`);
  }

  const negatives = ["realtime-message.negative.json", "realtime-message.negative-idempotency.json"];
  for (const fixture of negatives) {
    const verdict = validateSchema(message, await read(`${PROFILES}/fixtures/${fixture}`));
    if (verdict.ok) throw new Error(`${fixture} was accepted by realtime-message.schema.json`);
    if (!/oneOf matched 0 branches|unexpected property|missing required/.test(verdict.errors.join(" "))) {
      throw new Error(`${fixture} was rejected for an unexpected reason: ${verdict.errors.join("; ")}`);
    }
  }

  // The profile and the message schema must agree on which classes carry content.
  // Profile channel entries are $refs into #/$defs, so resolve them before reading.
  const resolveLocal = (node) => {
    for (let hop = 0; node?.$ref && hop < 8; hop += 1) node = profile.$defs?.[node.$ref.replace("#/$defs/", "")];
    return node;
  };
  const declared = profile.properties.channels.properties;
  if (resolveLocal(declared.durable).properties.mayCarryContent.const !== true) {
    throw new Error("profile: durable must carry content");
  }
  for (const channel of ["presence", "control"]) {
    if (resolveLocal(declared[channel]).properties.mayCarryContent.const !== false) {
      throw new Error(`profile: ${channel} must not carry content`);
    }
    const variant = message.$defs[channel];
    if (variant.additionalProperties !== false) throw new Error(`message: ${channel} must reject unknown properties`);
    for (const key of CONTENT_BEARING) {
      if (variant.properties?.[key]) throw new Error(`message: ${channel} exposes ${key}`);
    }
  }

  return {
    ok: true,
    profile: profile.properties.profileVersion.const,
    message: "1.0",
    channels: channels.length,
    positives: channels.length + 1,
    negatives: negatives.length + 1,
  };
}

if (process.argv[1] && import.meta.url === (await import("node:url")).pathToFileURL(process.argv[1]).href) {
  validateRealtime()
    .then((result) => console.log(JSON.stringify(result, null, 2)))
    .catch((error) => {
      console.error(`REALTIME CONTRACT FAILED: ${error.message}`);
      process.exit(1);
    });
}
