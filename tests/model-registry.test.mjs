import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { validateSchema } from "../scripts/schema-validator.mjs";
import {
  ModelRegistryError,
  createModelRegistry,
  modelKeyOf,
  validateCapabilityManifest,
} from "../packages/dsh-im2a-plugin/kernel/model-registry.mjs";

const schema = JSON.parse(await readFile(
  new URL("../docs/contracts/model-capability-manifest-1.0.schema.json", import.meta.url),
  "utf8",
));
const fixture = JSON.parse(await readFile(
  new URL("../docs/contracts/fixtures/model-capability-manifest-1.0.valid.json", import.meta.url),
  "utf8",
));

const KEY = "twinsearth-gpu-01/deepseek-v4-1-flash-261017";

function clone() {
  return structuredClone(fixture);
}

test("the valid fixture satisfies the frozen contract and the registry", () => {
  assert.equal(validateSchema(schema, fixture).ok, true);
  assert.deepEqual(validateCapabilityManifest(fixture), []);
  const registry = createModelRegistry({ manifests: [fixture] });
  assert.deepEqual(registry.list().map((entry) => entry.key), [KEY]);
  assert.equal(registry.get(KEY).model.model_id, "deepseek-v4-1-flash-261017");
  assert.equal(registry.get("nope/none"), null);
});

test("an undeclared model is an explicit error, never a silent default", () => {
  const registry = createModelRegistry({ manifests: [fixture] });
  assert.throws(
    () => registry.estimateCost({ key: "other/model", tokensIn: 1, tokensOut: 1 }),
    (error) => error instanceof ModelRegistryError && error.code === "model_not_declared",
  );
});

test("cost is derived only from declared per-MTok prices", () => {
  const registry = createModelRegistry({ manifests: [fixture] });
  const cost = registry.estimateCost({ key: KEY, tokensIn: 1_000_000, tokensOut: 500_000 });
  // 0.14 per MTok input + 0.28 per MTok output
  assert.equal(cost.amount, 0.28);
  assert.equal(cost.currency, "USD");
  assert.deepEqual(cost.unit_prices, { input_per_mtok: 0.14, output_per_mtok: 0.28 });

  const tiny = registry.estimateCost({ key: KEY, tokensIn: 18_432, tokensOut: 0 });
  assert.equal(tiny.amount, Number(((18_432 / 1_000_000) * 0.14).toFixed(8)));
  assert.throws(
    () => registry.estimateCost({ key: KEY, tokensIn: -1 }),
    (error) => error.code === "invalid_tokens",
  );
});

test("endpoint selection filters by region and protocol", () => {
  const registry = createModelRegistry({ manifests: [fixture] });
  assert.equal(registry.endpointsFor(KEY, { region: "cn-east" }).length, 2);
  assert.equal(registry.endpointsFor(KEY, { protocol: "anthropic_messages" })[0].region, "cn-east");
  assert.throws(
    () => registry.endpointsFor(KEY, { region: "eu-west" }),
    (error) => error.code === "endpoint_unavailable",
  );
});

test("declaring the same model twice is rejected instead of silently overriding", () => {
  assert.throws(
    () => createModelRegistry({ manifests: [fixture, clone()] }),
    (error) => error.code === "duplicate_model",
  );
});

test("a semantically invalid manifest never reaches the registry", () => {
  const broken = clone();
  broken.limits.max_output_tokens = broken.limits.context_tokens * 4;
  assert.throws(
    () => createModelRegistry({ manifests: [broken] }),
    (error) => error.code === "invalid_manifest" && error.message.includes("max_output_tokens"),
  );

  const unpriced = clone();
  unpriced.pricing.input_per_mtok = -1;
  assert.throws(() => createModelRegistry({ manifests: [unpriced] }), (error) => error.code === "invalid_manifest");
});

test("a third-party model without a weights license is accepted", () => {
  const thirdParty = clone();
  thirdParty.provider.deployment = "third_party";
  delete thirdParty.model.weights_license;
  assert.deepEqual(validateCapabilityManifest(thirdParty), []);
  const registry = createModelRegistry({ manifests: [thirdParty] });
  assert.equal(registry.list()[0].deployment, "third_party");
});

test("the registry digest ignores declaration order", () => {
  const other = clone();
  other.provider.provider_id = "twinsearth-gpu-02";
  const a = createModelRegistry({ manifests: [fixture, other] }).digest();
  const b = createModelRegistry({ manifests: [other, fixture] }).digest();
  assert.equal(a, b);
  assert.match(a, /^sha256:[0-9a-f]{64}$/);
  assert.equal(modelKeyOf(other), "twinsearth-gpu-02/deepseek-v4-1-flash-261017");
});
