/**
 * WP-3 of the six-layer plan: ui_artifact, the L5.2 "instant world" trace product.
 *
 * An Agent can render a task-scoped interface at runtime. That interface is
 * recorded with the trace but is NOT an asset: it stays `ephemeral` and stays out
 * of the contribution chain. Promotion into a skill is an explicit event with a
 * target skill id and a timestamp, and it is idempotent so a retried promotion
 * cannot fork the contribution chain.
 *
 * Contract: docs/contracts/ui-artifact-1.0.schema.json
 */
import { randomBytes } from "node:crypto";

export const UI_ARTIFACT_VERSION = "1.0";
export const UI_ARTIFACT_KINDS = Object.freeze(["layout", "interactive", "document", "scene"]);
export const UI_ARTIFACT_FORMATS = Object.freeze(["html", "svg", "json", "markdown"]);

export class UiArtifactError extends Error {
  constructor(code, message, detail = {}) {
    super(message);
    this.name = "UiArtifactError";
    this.code = code;
    this.detail = detail;
  }
}

function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

const TRACE_ID_RE = /^[0-9a-f]{32}$/;
const ARTIFACT_ID_RE = /^uiart_[0-9a-f]{24}$/;
const SHA256_RE = /^[0-9a-f]{64}$/;

/** Structural + semantic checks mirroring ui-artifact-1.0. */
export function validateUiArtifact(value) {
  const errors = [];
  if (!isPlainObject(value)) return ["artifact must be an object"];
  if (value.schema_version !== "1.0") errors.push("schema_version must be 1.0");
  if (typeof value.artifact_id !== "string" || !ARTIFACT_ID_RE.test(value.artifact_id)) {
    errors.push("artifact_id must match uiart_<24 hex>");
  }
  if (typeof value.trace_id !== "string" || !TRACE_ID_RE.test(value.trace_id)) {
    errors.push("trace_id must be 32 lowercase hex characters");
  }
  if (typeof value.created_at !== "string" || Number.isNaN(Date.parse(value.created_at))) {
    errors.push("created_at must be a timestamp");
  }
  if (!UI_ARTIFACT_KINDS.includes(value.kind)) errors.push(`unknown kind ${value.kind}`);
  if (!UI_ARTIFACT_FORMATS.includes(value.format)) errors.push(`unknown format ${value.format}`);
  if (typeof value.sha256 !== "string" || !SHA256_RE.test(value.sha256)) {
    errors.push("sha256 must be 64 lowercase hex characters");
  }
  if (!Number.isInteger(value.bytes) || value.bytes < 1) errors.push("bytes must be a positive integer");
  if (!["ephemeral", "promoted"].includes(value.status)) errors.push(`unknown status ${value.status}`);

  const provenance = value.provenance;
  if (!isPlainObject(provenance) || typeof provenance.agent_did !== "string" || provenance.agent_did.length === 0) {
    errors.push("provenance.agent_did is required");
  }

  if (value.status === "promoted") {
    if (typeof value.promoted_skill_id !== "string" || value.promoted_skill_id.length === 0) {
      errors.push("promoted requires promoted_skill_id");
    }
    if (typeof value.promoted_at !== "string" || Number.isNaN(Date.parse(value.promoted_at))) {
      errors.push("promoted requires promoted_at");
    }
  } else if (value.promoted_skill_id !== undefined && value.promoted_skill_id !== null) {
    errors.push("ephemeral must not carry promoted_skill_id");
  }

  return errors;
}

/** Build a fresh ephemeral artifact. Content itself lives in a content-addressed store. */
export function createUiArtifact({
  traceId,
  agentDid,
  kind = "layout",
  format = "html",
  sha256,
  bytes,
  goalId,
  parentArtifactId,
  now = () => new Date(),
} = {}) {
  const artifact = {
    schema_version: "1.0",
    artifact_id: `uiart_${randomBytes(12).toString("hex")}`,
    trace_id: traceId,
    created_at: new Date(now()).toISOString(),
    kind,
    format,
    sha256,
    bytes,
    status: "ephemeral",
    promoted_skill_id: null,
    provenance: {
      agent_did: agentDid,
      ...(goalId ? { goal_id: goalId } : {}),
      ...(parentArtifactId ? { parent_artifact_id: parentArtifactId } : {}),
    },
  };
  const errors = validateUiArtifact(artifact);
  if (errors.length) {
    throw new UiArtifactError("invalid_artifact", errors.join("; "), { errors });
  }
  return artifact;
}

/**
 * Promote an ephemeral artifact into a skill. Idempotent for the same target:
 * promoting twice returns the same shape instead of appending a second event.
 * A different target is a conflict, not an overwrite.
 */
export function promoteUiArtifact(artifact, { skillId, now = () => new Date() } = {}) {
  const errors = validateUiArtifact(artifact);
  if (errors.length) {
    throw new UiArtifactError("invalid_artifact", errors.join("; "), { errors });
  }
  if (typeof skillId !== "string" || skillId.length === 0 || skillId.length > 200) {
    throw new UiArtifactError("invalid_skill_id", "skillId must be a non-empty string of at most 200 characters");
  }

  if (artifact.status === "promoted") {
    if (artifact.promoted_skill_id === skillId) return structuredClone(artifact);
    throw new UiArtifactError(
      "artifact_already_promoted",
      `artifact ${artifact.artifact_id} is already promoted to ${artifact.promoted_skill_id}`,
      { artifact_id: artifact.artifact_id, promoted_skill_id: artifact.promoted_skill_id, requested: skillId },
    );
  }

  const promoted = {
    ...structuredClone(artifact),
    status: "promoted",
    promoted_skill_id: skillId,
    promoted_at: new Date(now()).toISOString(),
  };
  const promotedErrors = validateUiArtifact(promoted);
  if (promotedErrors.length) {
    throw new UiArtifactError("invalid_artifact", promotedErrors.join("; "), { errors: promotedErrors });
  }
  return promoted;
}

/** True only while the artifact is still a trace product rather than an asset. */
export function isPromotable(artifact) {
  return artifact?.status === "ephemeral" && validateUiArtifact(artifact).length === 0;
}
