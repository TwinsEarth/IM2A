/**
 * WP-1 of the six-layer plan: L1 model capability registry.
 *
 * The registry consumes the frozen `model-capability-manifest-1.0` documents
 * (docs/contracts/) and answers three questions for L2:
 *   - which models are declared (list / get);
 *   - what one call costs (estimateCost), using only declared prices;
 *   - which endpoint to use for a region/protocol.
 *
 * It deliberately does NOT route. Choosing a model by cost/latency/capability is
 * the 3.4 commercial module's decision; the layer only states facts.
 */
import { createHash } from "node:crypto";

export const MODEL_REGISTRY_VERSION = "1.0";

export class ModelRegistryError extends Error {
  constructor(code, message, detail = {}) {
    super(message);
    this.name = "ModelRegistryError";
    this.code = code;
    this.detail = detail;
  }
}

function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Semantic and structural checks the registry relies on. JSON Schema is already
 * enforced by scripts/check-layer-contracts.mjs; this repeats only what would
 * otherwise produce a wrong answer at runtime.
 */
export function validateCapabilityManifest(manifest) {
  const errors = [];
  if (!isPlainObject(manifest)) return ["manifest must be an object"];
  if (manifest.schema_version !== "1.0") errors.push("schema_version must be 1.0");

  const provider = manifest.provider;
  if (!isPlainObject(provider) || !provider.provider_id) errors.push("provider.provider_id is required");
  if (!isPlainObject(provider) || !provider.deployment) errors.push("provider.deployment is required");

  const model = manifest.model;
  if (!isPlainObject(model) || !model.model_id) errors.push("model.model_id is required");

  if (isPlainObject(provider) && provider.deployment === "self_hosted" && !model?.weights_license) {
    errors.push("self_hosted requires weights_license");
  }

  const endpoints = Array.isArray(manifest.endpoints) ? manifest.endpoints : [];
  if (endpoints.length === 0) errors.push("endpoints must contain at least one entry");
  const protocols = endpoints.map((endpoint) => endpoint?.protocol);
  if (new Set(protocols).size !== protocols.length) errors.push("duplicate endpoint protocol");

  const limits = manifest.limits;
  if (!isPlainObject(limits)) {
    errors.push("limits is required");
  } else if (!Number.isInteger(limits.context_tokens) || limits.context_tokens < 1) {
    errors.push("limits.context_tokens must be a positive integer");
  } else if (!Number.isInteger(limits.max_output_tokens) || limits.max_output_tokens < 1) {
    errors.push("limits.max_output_tokens must be a positive integer");
  } else if (limits.max_output_tokens > limits.context_tokens) {
    errors.push("max_output_tokens must not exceed context_tokens");
  }

  const pricing = manifest.pricing;
  if (!isPlainObject(pricing)) {
    errors.push("pricing is required");
  } else {
    if (!Number.isFinite(pricing.input_per_mtok) || pricing.input_per_mtok < 0) {
      errors.push("pricing.input_per_mtok must be a non-negative number");
    }
    if (!Number.isFinite(pricing.output_per_mtok) || pricing.output_per_mtok < 0) {
      errors.push("pricing.output_per_mtok must be a non-negative number");
    }
    if (!pricing.currency) errors.push("pricing.currency is required");
  }

  return errors;
}

export function modelKeyOf(manifest) {
  return `${manifest.provider.provider_id}/${manifest.model.model_id}`;
}

function assertTokenCount(value, field) {
  if (!Number.isInteger(value) || value < 0) {
    throw new ModelRegistryError("invalid_tokens", `${field} must be a non-negative integer`, { field, value });
  }
}

export function createModelRegistry({ manifests = [] } = {}) {
  if (!Array.isArray(manifests)) throw new ModelRegistryError("invalid_manifest", "manifests must be an array");

  const byKey = new Map();
  for (const [index, manifest] of manifests.entries()) {
    const errors = validateCapabilityManifest(manifest);
    if (errors.length) {
      throw new ModelRegistryError("invalid_manifest", `manifest[${index}]: ${errors.join("; ")}`, { index, errors });
    }
    const key = modelKeyOf(manifest);
    if (byKey.has(key)) {
      throw new ModelRegistryError("duplicate_model", `model declared twice: ${key}`, { key });
    }
    byKey.set(key, Object.freeze(structuredClone(manifest)));
  }

  function requireManifest(key) {
    const manifest = byKey.get(key);
    if (!manifest) {
      throw new ModelRegistryError("model_not_declared", `no declared model ${key}`, { key });
    }
    return manifest;
  }

  return {
    version: MODEL_REGISTRY_VERSION,

    list() {
      return [...byKey.keys()].sort().map((key) => {
        const manifest = byKey.get(key);
        return {
          key,
          provider_id: manifest.provider.provider_id,
          model_id: manifest.model.model_id,
          deployment: manifest.provider.deployment,
          context_tokens: manifest.limits.context_tokens,
          max_output_tokens: manifest.limits.max_output_tokens,
          pricing: { ...manifest.pricing },
        };
      });
    },

    get(key) {
      return byKey.get(key) ?? null;
    },

    /**
     * Cost of one call, derived only from declared per-MTok prices. Rounded to
     * 8 decimals so callers never compare float dust.
     */
    estimateCost({ key, tokensIn = 0, tokensOut = 0 } = {}) {
      const manifest = requireManifest(key);
      assertTokenCount(tokensIn, "tokensIn");
      assertTokenCount(tokensOut, "tokensOut");
      const raw =
        (tokensIn / 1_000_000) * manifest.pricing.input_per_mtok +
        (tokensOut / 1_000_000) * manifest.pricing.output_per_mtok;
      return {
        key,
        currency: manifest.pricing.currency,
        tokens_in: tokensIn,
        tokens_out: tokensOut,
        amount: Number(raw.toFixed(8)),
        unit_prices: {
          input_per_mtok: manifest.pricing.input_per_mtok,
          output_per_mtok: manifest.pricing.output_per_mtok,
        },
      };
    },

    /**
     * Pick an endpoint by region and/or protocol. No fallback across models:
     * an unavailable endpoint is an explicit error, not a silent substitution.
     */
    endpointsFor(key, { region, protocol } = {}) {
      const manifest = requireManifest(key);
      let candidates = manifest.endpoints;
      if (region) candidates = candidates.filter((endpoint) => endpoint.region === region);
      if (protocol) candidates = candidates.filter((endpoint) => endpoint.protocol === protocol);
      if (candidates.length === 0) {
        throw new ModelRegistryError(
          "endpoint_unavailable",
          `no endpoint for ${key}${region ? ` in ${region}` : ""}${protocol ? ` speaking ${protocol}` : ""}`,
          { key, region, protocol },
        );
      }
      return candidates.map((endpoint) => ({ ...endpoint }));
    },

    /** Stable digest of the declared set, for freeze evidence. */
    digest() {
      const canonical = [...byKey.keys()]
        .sort()
        .map((key) => JSON.stringify(byKey.get(key)))
        .join("\n");
      return `sha256:${createHash("sha256").update(canonical).digest("hex")}`;
    },
  };
}
