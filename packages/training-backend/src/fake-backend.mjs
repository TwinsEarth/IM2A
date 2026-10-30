/**
 * Deterministic fake TrainingBackend.
 *
 * What it proves: the contract shape, budget refusal, cancellation semantics,
 * and that collection is not verification. What it does NOT prove: that a model
 * trains, that a checkpoint is complete, or that quality improved.
 *
 * `verify()` always reports loadProbeOk=false because there is no model loader
 * here. A promotion path that accepts this backend would be a bug, which the
 * contract check asserts.
 */

import { createHash } from "node:crypto";
import { BackendRefusal, assertPlan } from "./plan.mjs";

const FAKE_CEILING = { maxGpuHours: 8, maxCostUsd: 100 };

export function createFakeBackend({ id = "fake", kind = "fake" } = {}) {
  if (kind !== "fake") throw new Error("createFakeBackend only produces kind=fake backends");
  const jobs = new Map();
  let counter = 0;

  const backend = {
    id,
    kind,

    capabilities() {
      return {
        methods: ["sft"],
        lora: true,
        multiNode: false,
        resume: false,
        maxContextTokens: 4096,
        maxGpuHours: FAKE_CEILING.maxGpuHours,
        maxCostUsd: FAKE_CEILING.maxCostUsd,
      };
    },

    async estimate(plan) {
      assertPlan(plan, backend.capabilities());
      // Deterministic placeholder: 0.05 GPU-h and USD 1 per job. Not a quote.
      return { gpuHours: 0.05, costUsdUpperBound: 1 };
    },

    async submit(plan) {
      assertPlan(plan, backend.capabilities());
      const estimate = await backend.estimate(plan);
      if (estimate.costUsdUpperBound > plan.budget.costUsdMax) {
        throw new BackendRefusal("BUDGET_EXCEEDED", `cost upper bound ${estimate.costUsdUpperBound} exceeds plan budget`);
      }
      if (estimate.gpuHours > plan.budget.gpuHoursMax) {
        throw new BackendRefusal("BUDGET_EXCEEDED", `GPU-hour estimate ${estimate.gpuHours} exceeds plan budget`);
      }
      if (estimate.gpuHours > FAKE_CEILING.maxGpuHours) {
        throw new BackendRefusal("BACKEND_CEILING", "estimate exceeds backend ceiling");
      }
      counter += 1;
      const jobId = `job_fake_${counter.toString().padStart(4, "0")}`;
      jobs.set(jobId, { plan, state: "queued", polls: 0, artifacts: null });
      return { jobId };
    },

    async poll(jobId) {
      const job = jobs.get(jobId);
      if (!job) throw new BackendRefusal("UNKNOWN_JOB", jobId);
      if (job.state === "queued") {
        job.state = "running";
        return { state: "running", metrics: {} };
      }
      if (job.state === "running") {
        job.polls += 1;
        job.state = "succeeded";
        return { state: "succeeded", metrics: { fake_completion: 1 } };
      }
      return { state: job.state, metrics: {} };
    },

    async cancel(jobId) {
      const job = jobs.get(jobId);
      if (!job) throw new BackendRefusal("UNKNOWN_JOB", jobId);
      if (job.state === "succeeded") {
        throw new BackendRefusal("JOB_TERMINAL", "a succeeded job cannot be cancelled; quarantine its artifacts instead");
      }
      job.state = "cancelled";
    },

    async collect(jobId) {
      const job = jobs.get(jobId);
      if (!job) throw new BackendRefusal("UNKNOWN_JOB", jobId);
      if (job.state !== "succeeded") {
        throw new BackendRefusal("JOB_NOT_FINISHED", `job is ${job.state}; collection of partial artifacts would masquerade as a checkpoint`);
      }
      if (!job.artifacts) {
        const manifestSha256 = `sha256:${createHash("sha256").update(`${jobId}:${job.plan.planId}`).digest("hex")}`;
        job.artifacts = {
          artifacts: [`fake://${jobId}/checkpoint/manifest.json`],
          manifestSha256,
        };
      }
      return { ...job.artifacts, artifacts: [...job.artifacts.artifacts] };
    },

    async verify(artifacts) {
      const list = Array.isArray(artifacts) ? artifacts : [];
      const inventoryOk = list.length > 0 && list.every((path) => typeof path === "string" && path.startsWith("fake://"));
      const hashOk = inventoryOk;
      // There is no loader in a fake backend. This must stay false so callers
      // cannot register or resume a fake checkpoint.
      const loadProbeOk = false;
      return { inventoryOk, hashOk, loadProbeOk };
    },
  };

  return backend;
}
