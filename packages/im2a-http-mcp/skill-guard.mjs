/**
 * v2.5.7 skill-write gate and append-only audit chain.
 *
 * The gate is deliberately a library seam used by the MCP handlers. It does
 * not live in a Harness process and it never trusts a caller-supplied scope.
 * The requested scope is derived from the operation, while the granted scope
 * and level live in a server-verified session object.
 */
import { createHmac, randomBytes, timingSafeEqual } from "node:crypto";
import { mkdir, open, readFile } from "node:fs/promises";
import path from "node:path";
import { canonicalJson, sha256Hex } from "./trace-store.mjs";

export const SKILL_GUARD_POLICY_VERSION = "skill-guard-1.0";
export const SKILL_WRITE_SCOPE = "skill:write";
export const SKILL_READ_SCOPE = "skill:read";
export const SKILL_GUARD_LEVELS = Object.freeze({ L0: 0, L1: 1, L2: 2, L3: 3 });
export const DENIED_REASONS = Object.freeze([
  "insufficient_scope",
  "scope_ceiling_exceeded",
  "cross_agent_write_blocked",
]);
export const PUBLIC_SESSION = Object.freeze({
  agent_did: null,
  authorization_level: "L0",
  scopes: Object.freeze([]),
  session_id: "public",
  subject_kind: "public_code",
});

export class SkillGuardError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "SkillGuardError";
    this.code = code;
  }
}

function base64url(value) {
  return Buffer.from(value).toString("base64url");
}

function safeLevel(value) {
  return typeof value === "string" && Object.hasOwn(SKILL_GUARD_LEVELS, value);
}

function normalizeSession(session) {
  if (!session || typeof session !== "object" || Array.isArray(session)) {
    throw new SkillGuardError("SESSION_INVALID", "session must be an object");
  }
  if (!safeLevel(session.authorization_level)) throw new SkillGuardError("SESSION_INVALID", "invalid authorization level");
  if (!Array.isArray(session.scopes) || session.scopes.some((scope) => typeof scope !== "string")) {
    throw new SkillGuardError("SESSION_INVALID", "invalid scopes");
  }
  return {
    agent_did: session.agent_did ?? null,
    authorization_level: session.authorization_level,
    scopes: [...new Set(session.scopes)],
    session_id: String(session.session_id ?? "unknown").slice(0, 200),
    subject_kind: String(session.subject_kind ?? "agent").slice(0, 32),
    expires_at: Number(session.expires_at ?? 0),
  };
}

export function signSession(session, key, now = Date.now()) {
  if (typeof key !== "string" || key.length < 16) throw new SkillGuardError("SESSION_KEY_INVALID");
  const normalized = normalizeSession({
    ...session,
    expires_at: session.expires_at ?? now + 5 * 60 * 1000,
  });
  const payload = base64url(JSON.stringify(normalized));
  const signature = createHmac("sha256", key).update(payload).digest("base64url");
  return `${payload}.${signature}`;
}

export function verifySession(token, key, now = Date.now()) {
  if (typeof token !== "string" || !token.includes(".") || typeof key !== "string" || key.length < 16) {
    throw new SkillGuardError("SESSION_INVALID");
  }
  const [payload, signature] = token.split(".");
  const expected = createHmac("sha256", key).update(payload).digest("base64url");
  const got = Buffer.from(signature ?? "");
  const want = Buffer.from(expected);
  if (got.length !== want.length || !timingSafeEqual(got, want)) throw new SkillGuardError("SESSION_INVALID");
  let session;
  try { session = normalizeSession(JSON.parse(Buffer.from(payload, "base64url").toString("utf8"))); }
  catch { throw new SkillGuardError("SESSION_INVALID"); }
  if (!session.expires_at || session.expires_at <= now) throw new SkillGuardError("SESSION_EXPIRED");
  return session;
}

function requiredOperation(value) {
  if (!["create", "patch", "archive"].includes(value)) {
    throw new SkillGuardError("INVALID_OPERATION", "operation must be create, patch or archive");
  }
  return value;
}

function decide(session, { agentDid, operation, targetOwnerDid }) {
  if (session.subject_kind === "public_code" || session.authorization_level === "L0" || session.scopes.length === 0) {
    return { allowed: false, denied_reason: "insufficient_scope" };
  }
  if (!session.scopes.includes(SKILL_WRITE_SCOPE)) {
    return { allowed: false, denied_reason: "insufficient_scope" };
  }
  if (SKILL_GUARD_LEVELS[session.authorization_level] < SKILL_GUARD_LEVELS.L2) {
    return { allowed: false, denied_reason: "scope_ceiling_exceeded" };
  }
  if (session.agent_did !== agentDid) {
    return { allowed: false, denied_reason: "cross_agent_write_blocked" };
  }
  if (targetOwnerDid && targetOwnerDid !== agentDid) {
    return { allowed: false, denied_reason: "cross_agent_write_blocked" };
  }
  return { allowed: true, denied_reason: null };
}

export class SkillGuard {
  #auditPath;
  #now;
  #tail = Promise.resolve();
  #lastHash = null;

  constructor({ auditPath, now = () => Date.now() } = {}) {
    if (typeof auditPath !== "string" || auditPath.length === 0) throw new TypeError("auditPath is required");
    this.#auditPath = auditPath;
    this.#now = now;
  }

  get auditPath() { return this.#auditPath; }

  async #loadLastHash() {
    if (this.#lastHash !== null) return this.#lastHash;
    let raw;
    try { raw = await readFile(this.#auditPath, "utf8"); }
    catch (error) {
      if (error.code === "ENOENT") { this.#lastHash = "0".repeat(64); return this.#lastHash; }
      throw new SkillGuardError("GUARD_AUDIT_FAILED", error.message);
    }
    const lines = raw.trim().split("\n").filter(Boolean);
    if (lines.length === 0) { this.#lastHash = "0".repeat(64); return this.#lastHash; }
    let last;
    try { last = JSON.parse(lines.at(-1)); } catch { throw new SkillGuardError("GUARD_AUDIT_FAILED", "audit tail is not JSON"); }
    if (typeof last.hash !== "string" || !/^[0-9a-f]{64}$/.test(last.hash)) {
      throw new SkillGuardError("GUARD_AUDIT_FAILED", "audit tail has no valid hash");
    }
    this.#lastHash = last.hash;
    return this.#lastHash;
  }

  async check({ session, agentDid, operation, targetSkillId, targetOwnerDid = null } = {}) {
    const normalized = normalizeSession(session);
    const op = requiredOperation(operation);
    if (typeof agentDid !== "string" || agentDid.length === 0) throw new SkillGuardError("INVALID_AGENT_DID");
    if (typeof targetSkillId !== "string" || targetSkillId.length === 0) throw new SkillGuardError("INVALID_TARGET_SKILL");
    const decision = decide(normalized, { agentDid, operation: op, targetOwnerDid });
    const at = new Date(this.#now()).toISOString();
    const run = this.#tail.then(async () => {
      const prevHash = await this.#loadLastHash();
      const body = {
        schema_version: "1.0",
        audit_event_id: `evt_${randomBytes(12).toString("hex")}`,
        request: {
          agent_did: agentDid,
          operation: op,
          target_skill_id: targetSkillId,
          requested_scope: SKILL_WRITE_SCOPE,
          session_id: normalized.session_id,
          subject_kind: normalized.subject_kind,
          authorization_level: normalized.authorization_level,
        },
        decision: {
          allowed: decision.allowed,
          denied_reason: decision.denied_reason,
          policy_version: SKILL_GUARD_POLICY_VERSION,
        },
        approval: { required: false, status: "not_required" },
        result: { status: decision.allowed ? "allowed" : "denied", at },
        prev_hash: prevHash,
      };
      const event = { ...body, hash: sha256Hex(canonicalJson(body)) };
      await mkdir(path.dirname(this.#auditPath), { recursive: true, mode: 0o750 });
      const handle = await open(this.#auditPath, "a", 0o640);
      try {
        await handle.writeFile(`${JSON.stringify(event)}\n`, "utf8");
        await handle.sync();
      } finally {
        await handle.close();
      }
      this.#lastHash = event.hash;
      return event;
    });
    this.#tail = run.catch(() => {});
    let event;
    try { event = await run; }
    catch (error) {
      if (error instanceof SkillGuardError) throw error;
      throw new SkillGuardError("GUARD_AUDIT_FAILED", error.message);
    }
    return {
      allowed: event.decision.allowed,
      denied_reason: event.decision.denied_reason,
      audit_event_id: event.audit_event_id,
    };
  }
}

export async function verifyAuditChain(auditPath) {
  let raw;
  try { raw = await readFile(auditPath, "utf8"); }
  catch (error) {
    if (error.code === "ENOENT") return { ok: true, count: 0, last_hash: "0".repeat(64), errors: [] };
    return { ok: false, count: 0, last_hash: null, errors: [error.message] };
  }
  const lines = raw.trim().split("\n").filter(Boolean);
  const errors = [];
  let prev = "0".repeat(64);
  for (let index = 0; index < lines.length; index += 1) {
    let event;
    try { event = JSON.parse(lines[index]); }
    catch { errors.push(`line ${index + 1}: invalid JSON`); break; }
    if (event.prev_hash !== prev) errors.push(`line ${index + 1}: prev_hash mismatch`);
    const { hash, ...body } = event;
    const expected = sha256Hex(canonicalJson(body));
    if (hash !== expected) errors.push(`line ${index + 1}: hash mismatch`);
    prev = hash;
  }
  return {
    ok: errors.length === 0,
    count: lines.length,
    last_hash: lines.length ? prev : "0".repeat(64),
    errors,
  };
}
