/**
 * Append-only execution-trace store for IM2A v2.5.4.
 *
 * A trace batch contains span metadata and SHA-256 digests only. Raw prompts,
 * tool arguments, tool results, credentials and free-form memory are rejected
 * structurally. The file is append-only; there is deliberately no update or
 * delete operation in this module.
 */
import { createHash, randomBytes } from "node:crypto";
import { mkdir, open, readFile } from "node:fs/promises";
import path from "node:path";
import { UiArtifactError, createUiArtifact } from "../dsh-im2a-plugin/kernel/ui-artifact.mjs";

export const TRACE_SCHEMA_VERSION = "1.0";
export const TRACE_MAX_EVENTS = 2000;
export const TRACE_MAX_UI_ARTIFACTS = 8;
export const TRACE_FILE_NAME = "trajectory.jsonl";
export const UI_ARTIFACT_FILE_NAME = "ui-artifacts.jsonl";

const SPAN_TYPES = new Set(["tool_call", "llm_call", "retrieval", "skill_load"]);
const DIGEST_RE = /^sha256:[0-9a-f]{64}$/;
const DID_RE = /^did:[a-z0-9]+:[A-Za-z0-9._:%-]{3,200}$/;
const TENANT_RE = /^[a-z0-9][a-z0-9._-]{2,63}$/;
const CONTROL_RE = /[\u0000-\u001f\u007f]/;

export class TraceValidationError extends Error {
  constructor(message) {
    super(message);
    this.name = "TraceValidationError";
    this.code = "TRACE_INVALID";
  }
}

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function requireString(value, field, { min = 1, max = 200, pattern } = {}) {
  if (typeof value !== "string" || value.length < min || value.length > max) {
    throw new TraceValidationError(`${field} must be a string of length ${min}..${max}`);
  }
  if (CONTROL_RE.test(value)) throw new TraceValidationError(`${field} contains control characters`);
  if (pattern && !pattern.test(value)) throw new TraceValidationError(`${field} has invalid format`);
  return value;
}

function requireDigest(value, field) {
  if (typeof value !== "string" || !DIGEST_RE.test(value)) {
    throw new TraceValidationError(`${field} must be sha256:<64 lowercase hex>`);
  }
  return value;
}

function requireNumber(value, field, { min, max, integer = false } = {}) {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new TraceValidationError(`${field} must be a finite number`);
  }
  if (integer && !Number.isInteger(value)) throw new TraceValidationError(`${field} must be an integer`);
  if (min !== undefined && value < min) throw new TraceValidationError(`${field} is below ${min}`);
  if (max !== undefined && value > max) throw new TraceValidationError(`${field} is above ${max}`);
  return value;
}

function requireObject(value, field) {
  if (!isPlainObject(value)) throw new TraceValidationError(`${field} must be an object`);
  return value;
}

function onlyKeys(value, allowed, field) {
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) throw new TraceValidationError(`${field} has unexpected property ${key}`);
  }
}

function isoNow(clock) {
  const now = clock ? clock() : new Date();
  if (!(now instanceof Date) || Number.isNaN(now.getTime())) {
    throw new TraceValidationError("clock must return a valid Date");
  }
  return now.toISOString();
}

function requireIso(value, field) {
  if (typeof value !== "string" || Number.isNaN(Date.parse(value))) {
    throw new TraceValidationError(`${field} must be a valid date-time`);
  }
  return new Date(value).toISOString();
}

/** Stable JSON: object keys are sorted so the digest is reproducible. */
export function canonicalJson(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (isPlainObject(value)) {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

export function sha256Hex(value) {
  return createHash("sha256").update(value).digest("hex");
}

function operationName(spanType) {
  if (spanType === "llm_call") return "chat";
  if (spanType === "retrieval") return "retrieval";
  return "execute_tool";
}

function normalizeEvent(raw, index, { agentDid, defaultOccurredAt }) {
  const field = `events[${index}]`;
  requireObject(raw, field);
  onlyKeys(raw, new Set([
    "span_type", "name", "input_digest", "output_digest", "duration_ms",
    "token_cost", "success", "error_class", "skill_version", "occurred_at",
  ]), field);

  const spanType = raw.span_type;
  if (!SPAN_TYPES.has(spanType)) {
    throw new TraceValidationError(`${field}.span_type must be one of ${[...SPAN_TYPES].join(", ")}`);
  }
  const name = requireString(raw.name, `${field}.name`, { max: 128 });
  const inputDigest = requireDigest(raw.input_digest, `${field}.input_digest`);
  const outputDigest = requireDigest(raw.output_digest, `${field}.output_digest`);
  const durationMs = requireNumber(raw.duration_ms, `${field}.duration_ms`, { min: 0, max: 3600000 });
  const tokenCost = requireNumber(raw.token_cost, `${field}.token_cost`, { min: 0, max: 1000000, integer: true });
  if (typeof raw.success !== "boolean") throw new TraceValidationError(`${field}.success must be boolean`);

  let errorClass = raw.error_class ?? null;
  if (raw.success === false) {
    if (errorClass === null || errorClass === "") errorClass = "unknown";
    requireString(errorClass, `${field}.error_class`, { max: 64 });
  } else if (errorClass !== null && errorClass !== "") {
    throw new TraceValidationError(`${field}.error_class must be null when success is true`);
  } else {
    errorClass = null;
  }

  let skillVersion = raw.skill_version ?? "none";
  requireString(skillVersion, `${field}.skill_version`, { max: 128 });
  if (spanType === "skill_load" && skillVersion === "none") {
    throw new TraceValidationError(`${field}.skill_version is required for skill_load`);
  }

  const occurredAt = raw.occurred_at === undefined
    ? defaultOccurredAt
    : requireIso(raw.occurred_at, `${field}.occurred_at`);

  const attributes = {
    "gen_ai.operation.name": operationName(spanType),
    "im2a.agent_did": agentDid,
    "im2a.skill_version": skillVersion,
  };
  if (spanType === "tool_call" || spanType === "skill_load") attributes["gen_ai.tool.name"] = name;
  if (tokenCost > 0) attributes["gen_ai.usage.total_tokens"] = tokenCost;

  return {
    seq: index + 1,
    span_id: randomBytes(8).toString("hex"),
    span_type: spanType,
    name,
    input_digest: inputDigest,
    output_digest: outputDigest,
    duration_ms: durationMs,
    token_cost: tokenCost,
    success: raw.success,
    error_class: errorClass,
    occurred_at: occurredAt,
    attributes,
  };
}

/**
 * Validate and normalize one ingest request. The returned record is the exact
 * object written to trajectory.jsonl.
 */
export function normalizeTraceBatch(input, { tenant, clock } = {}) {
  requireObject(input, "input");
  onlyKeys(input, new Set(["session_id", "agent_did", "events", "ui_artifacts"]), "input");
  const tenantValue = requireString(tenant, "tenant", { max: 64, pattern: TENANT_RE });
  const sessionId = requireString(input.session_id, "session_id", { max: 200 });
  const agentDid = requireString(input.agent_did, "agent_did", { max: 208, pattern: DID_RE });
  if (!Array.isArray(input.events) || input.events.length < 1 || input.events.length > TRACE_MAX_EVENTS) {
    throw new TraceValidationError(`events must contain 1..${TRACE_MAX_EVENTS} entries`);
  }

  const receivedAt = isoNow(clock);
  const events = input.events.map((event, index) =>
    normalizeEvent(event, index, { agentDid, defaultOccurredAt: receivedAt }));

  const record = {
    schema_version: TRACE_SCHEMA_VERSION,
    trace_id: randomBytes(16).toString("hex"),
    tenant: tenantValue,
    agent_did: agentDid,
    session_id: sessionId,
    received_at: receivedAt,
    events,
  };
  record.content_sha256 = `sha256:${sha256Hex(canonicalJson(record))}`;
  return record;
}

/**
 * WP-3a: normalize the optional ui_artifact declarations carried by one ingest
 * batch.
 *
 * The client cannot know the trace id (the server mints it) or the artifact id,
 * so the ingest payload carries only the product facts: kind, format, content
 * hash, size and optional provenance links. The stored product is a complete
 * `ui-artifact-1.0` document living in a sidecar file, which keeps the frozen
 * `execution-trace-1.0` contract untouched.
 */
export function normalizeUiArtifacts(rawList, { traceId, agentDid, clock } = {}) {
  if (rawList === undefined) return [];
  if (!Array.isArray(rawList) || rawList.length < 1 || rawList.length > TRACE_MAX_UI_ARTIFACTS) {
    throw new TraceValidationError(`ui_artifacts must contain 1..${TRACE_MAX_UI_ARTIFACTS} entries`);
  }
  const createdAt = isoNow(clock);
  return rawList.map((raw, index) => {
    const field = `ui_artifacts[${index}]`;
    requireObject(raw, field);
    onlyKeys(raw, new Set(["kind", "format", "sha256", "bytes", "goal_id", "parent_artifact_id"]), field);
    for (const key of ["kind", "format", "sha256", "bytes"]) {
      if (!Object.hasOwn(raw, key)) throw new TraceValidationError(`${field} is missing required ${key}`);
    }
    try {
      return createUiArtifact({
        traceId,
        agentDid,
        kind: raw.kind,
        format: raw.format,
        sha256: raw.sha256,
        bytes: raw.bytes,
        goalId: raw.goal_id,
        parentArtifactId: raw.parent_artifact_id,
        now: () => new Date(createdAt),
      });
    } catch (error) {
      if (error instanceof UiArtifactError) {
        throw new TraceValidationError(`${field}: ${error.message}`);
      }
      throw error;
    }
  });
}

export class TraceStore {
  #dir;
  #tenant;
  #fileName;
  #artifactFileName;
  #clock;
  #tail = Promise.resolve();

  constructor({ dir, tenant, fileName = TRACE_FILE_NAME, artifactFileName = UI_ARTIFACT_FILE_NAME, clock } = {}) {
    if (typeof dir !== "string" || dir.length === 0) throw new TypeError("dir is required");
    if (typeof tenant !== "string" || !TENANT_RE.test(tenant)) throw new TypeError("tenant is invalid");
    this.#dir = dir;
    this.#tenant = tenant;
    this.#fileName = fileName;
    this.#artifactFileName = artifactFileName;
    this.#clock = clock;
  }

  get tenantDir() {
    return path.join(this.#dir, this.#tenant);
  }

  get filePath() {
    return path.join(this.tenantDir, this.#fileName);
  }

  /** Sidecar for ui_artifact records; keeps execution-trace-1.0 frozen. */
  get artifactFilePath() {
    return path.join(this.tenantDir, this.#artifactFileName);
  }

  /**
   * Read-only lookup by trace id. This method never writes, rotates or repairs
   * the trajectory file; a missing file means "not found", not "create it".
   */
  async get(traceId) {
    if (typeof traceId !== "string" || !/^[0-9a-f]{32}$/.test(traceId)) {
      throw new TraceValidationError("trace_id must be 32 lowercase hex characters");
    }
    let raw;
    try {
      raw = await readFile(this.filePath, "utf8");
    } catch (error) {
      if (error.code === "ENOENT") return null;
      throw error;
    }
    for (const line of raw.split("\n")) {
      if (!line.trim()) continue;
      let record;
      try { record = JSON.parse(line); } catch { continue; }
      if (record?.trace_id !== traceId) continue;
      if (record.tenant !== this.#tenant) {
        throw new TraceValidationError("trace tenant does not match the store tenant");
      }
      return record;
    }
    return null;
  }

  /**
   * Read-only recent traces that loaded a specific skill. The skill id is the
   * skill_load span name, matching the v2.5.4 trace contract.
   */
  async recentForSkill(skillId, { limit = 5 } = {}) {
    if (typeof skillId !== "string" || skillId.length === 0 || skillId.length > 200) {
      throw new TraceValidationError("skill_id must be a non-empty string");
    }
    let raw;
    try { raw = await readFile(this.filePath, "utf8"); }
    catch (error) {
      if (error.code === "ENOENT") return [];
      throw error;
    }
    const records = [];
    for (const line of raw.split("\n")) {
      if (!line.trim()) continue;
      let record;
      try { record = JSON.parse(line); } catch { continue; }
      if (record?.tenant !== this.#tenant) continue;
      const matched = (record.events ?? []).some((event) =>
        event.span_type === "skill_load"
        && (event.name === skillId || event.attributes?.["gen_ai.tool.name"] === skillId));
      if (matched) records.push(record);
    }
    return records.slice(-Math.min(Math.max(Number(limit) || 5, 1), 50));
  }

  async ingest(input) {
    const record = normalizeTraceBatch(input, { tenant: this.#tenant, clock: this.#clock });
    const artifacts = normalizeUiArtifacts(input.ui_artifacts, {
      traceId: record.trace_id,
      agentDid: record.agent_did,
      clock: this.#clock,
    });
    const traceLine = `${JSON.stringify(record)}\n`;
    const artifactLines = artifacts.map((artifact) => `${JSON.stringify(artifact)}\n`);
    const run = this.#tail.then(async () => {
      await this.#appendLine(this.filePath, traceLine);
      for (const artifactLine of artifactLines) {
        await this.#appendLine(this.artifactFilePath, artifactLine);
      }
    });
    this.#tail = run.catch(() => {});
    await run;
    return {
      trace_id: record.trace_id,
      ingested_count: record.events.length,
      content_sha256: record.content_sha256,
      ui_artifact_ids: artifacts.map((artifact) => artifact.artifact_id),
    };
  }

  /**
   * Read-only sidecar lookup. A missing sidecar means "this trace declared no
   * ui_artifact", not "the trace is missing".
   */
  async getUiArtifacts(traceId) {
    if (typeof traceId !== "string" || !/^[0-9a-f]{32}$/.test(traceId)) {
      throw new TraceValidationError("trace_id must be 32 lowercase hex characters");
    }
    let raw;
    try {
      raw = await readFile(this.artifactFilePath, "utf8");
    } catch (error) {
      if (error.code === "ENOENT") return [];
      throw error;
    }
    const artifacts = [];
    for (const line of raw.split("\n")) {
      if (!line.trim()) continue;
      let artifact;
      try { artifact = JSON.parse(line); } catch { continue; }
      if (artifact?.trace_id === traceId) artifacts.push(artifact);
    }
    return artifacts;
  }

  async #appendLine(filePath, line) {
    await mkdir(this.tenantDir, { recursive: true, mode: 0o750 });
    const handle = await open(filePath, "a", 0o640);
    try {
      await handle.writeFile(line, "utf8");
      await handle.sync();
    } finally {
      await handle.close();
    }
  }
}
