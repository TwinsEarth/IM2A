/**
 * TrainingPlan content addressing and pre-submit validation.
 *
 * A plan whose planId does not match its content is refused: otherwise two
 * different runs can share an id and the experiment record becomes unusable.
 */

import { createHash } from "node:crypto";

const SHA256 = /^sha256:[0-9a-f]{64}$/;
const METHODS = new Set(["sft", "dpo", "grpo"]);

export function canonicalize(value) {
  if (value === null || typeof value !== "object") return value;
  if (Array.isArray(value)) return value.map(canonicalize);
  const out = {};
  for (const key of Object.keys(value).sort()) out[key] = canonicalize(value[key]);
  return out;
}

export function canonicalJson(value) {
  return JSON.stringify(canonicalize(value));
}

export function computePlanId(plan) {
  const { planId: _ignored, ...rest } = plan;
  return `sha256:${createHash("sha256").update(canonicalJson(rest), "utf8").digest("hex")}`;
}

export class BackendRefusal extends Error {
  constructor(code, message) {
    super(`${code}: ${message}`);
    this.name = "BackendRefusal";
    this.code = code;
  }
}

/** Throws BackendRefusal on the first violated plan rule. */
export function assertPlan(plan, capabilities) {
  if (!plan || typeof plan !== "object") throw new BackendRefusal("PLAN_INVALID", "plan is not an object");
  if (!METHODS.has(plan.method)) throw new BackendRefusal("PLAN_INVALID", `unknown method ${plan.method}`);
  if (!SHA256.test(plan.datasetManifestSha256 ?? "")) {
    throw new BackendRefusal("PLAN_INVALID", "datasetManifestSha256 must be a sha256 digest");
  }
  if (!SHA256.test(plan.baseCheckpointDigest ?? "")) {
    throw new BackendRefusal("PLAN_INVALID", "baseCheckpointDigest must be a sha256 digest");
  }
  if (!SHA256.test(plan.imageDigest ?? "")) {
    throw new BackendRefusal("PLAN_INVALID", "imageDigest must be a sha256 digest");
  }
  if (!Number.isInteger(plan.seed) || plan.seed < 0) {
    throw new BackendRefusal("PLAN_INVALID", "seed must be a non-negative integer");
  }
  if (!plan.budget || !(plan.budget.gpuHoursMax > 0) || !(plan.budget.costUsdMax > 0)) {
    throw new BackendRefusal("BUDGET_REQUIRED", "plan needs positive gpuHoursMax and costUsdMax");
  }
  if (computePlanId(plan) !== plan.planId) {
    throw new BackendRefusal("PLAN_ID_MISMATCH", "planId is not the content hash of the plan");
  }
  if (capabilities) {
    if (!capabilities.methods.includes(plan.method)) {
      throw new BackendRefusal("METHOD_UNSUPPORTED", `backend does not support ${plan.method}`);
    }
    if (typeof capabilities.maxContextTokens !== "number" || capabilities.maxContextTokens <= 0) {
      throw new BackendRefusal("CAPABILITIES_INVALID", "maxContextTokens must be positive");
    }
  }
}
