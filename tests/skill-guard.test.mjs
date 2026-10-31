import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  PUBLIC_SESSION,
  SkillGuard,
  SkillGuardError,
  signSession,
  verifyAuditChain,
  verifySession,
} from "../packages/im2a-http-mcp/skill-guard.mjs";

const NOW = Date.UTC(2026, 9, 31, 12, 0, 0);
const KEY = "test-skill-guard-key-32-bytes-long";

const l2 = {
  agent_did: "did:im2a:guard-agent",
  authorization_level: "L2",
  scopes: ["skill:read", "skill:write"],
  session_id: "hs_guard_test",
  subject_kind: "agent",
};

async function withGuard(fn) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-skill-guard-"));
  const auditPath = path.join(dir, "audit.jsonl");
  const guard = new SkillGuard({ auditPath, now: () => NOW });
  try {
    return await fn({ dir, auditPath, guard });
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

test("public code is denied insufficient_scope and the audit chain verifies", async () => {
  await withGuard(async ({ auditPath, guard }) => {
    const result = await guard.check({
      session: PUBLIC_SESSION,
      agentDid: "did:im2a:public-caller",
      operation: "create",
      targetSkillId: "new-skill",
    });
    assert.equal(result.allowed, false);
    assert.equal(result.denied_reason, "insufficient_scope");
    assert.match(result.audit_event_id, /^evt_[0-9a-f]{24}$/);
    const lines = (await readFile(auditPath, "utf8")).trim().split("\n");
    assert.equal(lines.length, 1);
    const event = JSON.parse(lines[0]);
    assert.equal(event.request.requested_scope, "skill:write");
    assert.equal(event.request.subject_kind, "public_code");
    assert.equal(event.decision.allowed, false);
    assert.equal(event.decision.denied_reason, "insufficient_scope");
    assert.equal(event.approval.status, "not_required");
    assert.equal(event.result.status, "denied");
    assert.equal((await verifyAuditChain(auditPath)).ok, true);
  });
});

test("L2 skill:write is allowed and returns a traceable audit event", async () => {
  await withGuard(async ({ auditPath, guard }) => {
    const result = await guard.check({
      session: l2,
      agentDid: l2.agent_did,
      operation: "patch",
      targetSkillId: "existing-skill",
    });
    assert.equal(result.allowed, true);
    assert.equal(result.denied_reason, null);
    assert.match(result.audit_event_id, /^evt_[0-9a-f]{24}$/);
    const event = JSON.parse((await readFile(auditPath, "utf8")).trim());
    assert.equal(event.request.authorization_level, "L2");
    assert.equal(event.request.session_id, l2.session_id);
    assert.equal(event.decision.allowed, true);
    assert.equal(event.result.status, "allowed");
    assert.equal(event.hash, event.hash.toLowerCase());
    assert.equal((await verifyAuditChain(auditPath)).ok, true);
  });
});

test("read-only, level ceiling and cross-agent rules fail closed", async () => {
  await withGuard(async ({ guard }) => {
    const readOnly = await guard.check({
      session: { ...l2, scopes: ["skill:read"] },
      agentDid: l2.agent_did,
      operation: "create",
      targetSkillId: "new-skill",
    });
    assert.equal(readOnly.denied_reason, "insufficient_scope");

    const lowLevel = await guard.check({
      session: { ...l2, authorization_level: "L1" },
      agentDid: l2.agent_did,
      operation: "create",
      targetSkillId: "new-skill",
    });
    assert.equal(lowLevel.denied_reason, "scope_ceiling_exceeded");

    const otherAgent = await guard.check({
      session: { ...l2, agent_did: "did:im2a:other" },
      agentDid: l2.agent_did,
      operation: "archive",
      targetSkillId: "existing-skill",
    });
    assert.equal(otherAgent.denied_reason, "cross_agent_write_blocked");

    const foreignTarget = await guard.check({
      session: l2,
      agentDid: l2.agent_did,
      operation: "patch",
      targetSkillId: "existing-skill",
      targetOwnerDid: "did:im2a:foreign-owner",
    });
    assert.equal(foreignTarget.denied_reason, "cross_agent_write_blocked");
  });
});

test("signed sessions cannot be forged or used after expiry", () => {
  const token = signSession(l2, KEY, NOW);
  const verified = verifySession(token, KEY, NOW + 1000);
  assert.equal(verified.agent_did, l2.agent_did);
  assert.deepEqual(verified.scopes, l2.scopes);
  const [payload, signature] = token.split(".");
  const flipped = signature.endsWith("A") ? "B" : "A";
  assert.throws(() => verifySession(`${payload}.${signature.slice(0, -1)}${flipped}`, KEY, NOW), (error) => error.code === "SESSION_INVALID");
  assert.throws(() => verifySession(token, "wrong-key-32-bytes-long", NOW), (error) => error.code === "SESSION_INVALID");
  const short = signSession({ ...l2, session_id: "short", expires_at: NOW + 10 }, KEY, NOW);
  assert.throws(() => verifySession(short, KEY, NOW + 100), (error) => error.code === "SESSION_EXPIRED");
});

test("audit chain detects tampering and preserves two-event ordering", async () => {
  await withGuard(async ({ auditPath, guard }) => {
    await guard.check({ session: PUBLIC_SESSION, agentDid: "did:im2a:public", operation: "create", targetSkillId: "a" });
    await guard.check({ session: l2, agentDid: l2.agent_did, operation: "create", targetSkillId: "b" });
    const chain = await verifyAuditChain(auditPath);
    assert.equal(chain.ok, true);
    assert.equal(chain.count, 2);
    const lines = (await readFile(auditPath, "utf8")).trim().split("\n");
    const first = JSON.parse(lines[0]);
    const second = JSON.parse(lines[1]);
    assert.equal(second.prev_hash, first.hash);
    const tampered = `${JSON.stringify({ ...first, decision: { ...first.decision, allowed: true } })}\n${lines[1]}\n`;
    const tamperPath = `${auditPath}.tampered`;
    await writeFile(tamperPath, tampered);
    const bad = await verifyAuditChain(tamperPath);
    assert.equal(bad.ok, false);
    assert.ok(bad.errors.some((error) => error.includes("hash mismatch")));
  });
});

test("invalid operations are rejected before audit append", async () => {
  await withGuard(async ({ auditPath, guard }) => {
    await assert.rejects(
      guard.check({ session: l2, agentDid: l2.agent_did, operation: "delete", targetSkillId: "a" }),
      (error) => error instanceof SkillGuardError && error.code === "INVALID_OPERATION",
    );
    const chain = await verifyAuditChain(auditPath);
    assert.equal(chain.count, 0);
  });
});
