/**
 * IM2A v2.5.0 TrainingBackend contract (draft, types only).
 *
 * This is deliberately NOT the harness-adapter `apply`/`rollback` contract.
 * A training job is a remote, costly operation whose failure mode is "partial
 * artifacts + money already spent". It can be cancelled and quarantined, never
 * rolled back. Reusing the adapter contract would hide that failure mode.
 *
 * No backend is registered by default. Registration and enablement are two
 * separate decisions; see src/registry.mjs.
 */

export type BackendKind = "local" | "managed" | "batch-api" | "fake";

export type TrainingMethod = "sft" | "dpo" | "grpo";

export interface TrainingPlan {
  /** Content address: sha256 over the plan excluding planId. */
  planId: string;
  method: TrainingMethod;
  datasetManifestSha256: string;
  baseCheckpointDigest: string;
  hyperparams: Record<string, string | number | boolean>;
  seed: number;
  budget: { gpuHoursMax: number; costUsdMax: number };
  imageDigest: string;
}

export interface BackendCapabilities {
  methods: TrainingMethod[];
  lora: boolean;
  multiNode: boolean;
  resume: boolean;
  maxContextTokens: number;
  /** Hard ceiling the backend will refuse to exceed, independent of the plan. */
  maxGpuHours: number;
  maxCostUsd: number;
}

export type JobState = "queued" | "running" | "failed" | "cancelled" | "succeeded";

export interface JobStatus {
  state: JobState;
  /** Execution telemetry only. Never a quality claim. */
  metrics: Record<string, number>;
}

export interface CollectedArtifacts {
  artifacts: string[];
  manifestSha256: string;
}

export interface ArtifactVerdict {
  inventoryOk: boolean;
  hashOk: boolean;
  loadProbeOk: boolean;
}

export interface TrainingBackend {
  readonly id: string;
  readonly kind: BackendKind;

  /** Pure: no submission, no spend. */
  capabilities(): BackendCapabilities;

  /** Pure: no submission, no spend. A backend without a cost upper bound is refused. */
  estimate(plan: TrainingPlan): Promise<{ gpuHours: number; costUsdUpperBound: number }>;

  /** Refuses when the estimate exceeds the plan budget or the backend ceiling. */
  submit(plan: TrainingPlan): Promise<{ jobId: string }>;

  poll(jobId: string): Promise<JobStatus>;

  /** Cancellation stops spend. It cannot restore spent budget or remove partial artifacts. */
  cancel(jobId: string): Promise<void>;

  /** Collect is not verification: a returned directory is not a complete checkpoint. */
  collect(jobId: string): Promise<CollectedArtifacts>;

  /** Promotion requires all three booleans true. */
  verify(artifacts: string[]): Promise<ArtifactVerdict>;
}
