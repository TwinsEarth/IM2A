import assert from "node:assert/strict";
import test from "node:test";
import {
  DEFAULT_PROPOSABLE_SCOPES,
  ScopeGovernanceError,
  createScopeGovernance,
  grantDigest,
  isHighImpactScope,
  isPhysicalScope,
  parseScope,
  principalKind,
} from "../packages/dsh-im2a-plugin/kernel/scope-governance.mjs";

const AGENT = "did:im2a:agent-wp6";
const HUMAN = "human:alice";
const OTHER_HUMAN = "human:bob";
const clock = () => new Date("2026-11-15T00:00:00Z");

function gov(options = {}) {
  return createScopeGovernance({ clock, ...options });
}

test("an agent can propose but receives no grant by itself", () => {
  const g = gov();
  const proposal = g.proposeScopeChange({
    agentDid: AGENT,
    scopes: ["skill:write"],
    rationale: "needs write access for the release task",
  });
  assert.match(proposal.proposal_id, /^scp_[0-9a-f]{24}$/);
  assert.equal(proposal.status, "pending");
  assert.deepEqual(proposal.requested_scopes, ["skill:write"]);
  assert.deepEqual(g.effectiveScopes(AGENT), [], "a pending proposal must not grant anything");
});

test("only a human decision turns a proposal into a grant", () => {
  const g = gov();
  const { proposal_id: id } = g.proposeScopeChange({
    agentDid: AGENT,
    scopes: ["skill:write", "memory:read"],
    rationale: "needs write access for the release task",
  });
  const { proposal, grant } = g.recordHumanDecision(id, { principal: HUMAN, decision: "approve" });
  assert.equal(proposal.status, "approved");
  assert.equal(grant.granted_by, HUMAN);
  assert.equal(grant.source, "proposal");
  assert.deepEqual(grant.scopes, ["memory:read", "skill:write"]);
  assert.deepEqual(g.effectiveScopes(AGENT), ["memory:read", "skill:write"]);
});

test("an agent cannot approve anything, including its own proposal", () => {
  const g = gov();
  const { proposal_id: id } = g.proposeScopeChange({
    agentDid: AGENT,
    scopes: ["skill:write"],
    rationale: "needs write access for the release task",
  });
  assert.throws(
    () => g.recordHumanDecision(id, { principal: AGENT, decision: "approve" }),
    (error) => error instanceof ScopeGovernanceError && error.code === "human_approval_required",
  );
  assert.deepEqual(g.effectiveScopes(AGENT), []);
});

test("a physical scope has no agent-initiated path at all", () => {
  const g = gov();
  assert.throws(
    () => g.proposeScopeChange({
      agentDid: AGENT,
      scopes: ["physical:motion"],
      rationale: "please let me move the robot arm",
    }),
    (error) => error.code === "physical_scope_requires_human_grant",
  );
  // The refusal must not leave a pending proposal behind either.
  assert.deepEqual(g.listProposals(AGENT), []);
  assert.equal(isPhysicalScope("physical:motion"), true);
  assert.equal(isPhysicalScope("skill:write"), false);
});

test("a human grants physical authority directly, with a recorded reason", () => {
  const g = gov();
  const grant = g.grantScopeDirectly({
    agentDid: AGENT,
    scopes: ["physical:motion"],
    grantedBy: HUMAN,
    reason: "supervised demo on the test bench",
  });
  assert.equal(grant.source, "direct");
  assert.equal(grant.granted_by, HUMAN);
  assert.equal(grant.reason, "supervised demo on the test bench");
  assert.match(grantDigest(grant), /^sha256:[0-9a-f]{64}$/);
  assert.deepEqual(g.effectiveScopes(AGENT), ["physical:motion"]);
});

test("a direct grant still requires a human and a reason", () => {
  const g = gov();
  assert.throws(
    () => g.grantScopeDirectly({ agentDid: AGENT, scopes: ["physical:motion"], grantedBy: AGENT, reason: "self service" }),
    (error) => error.code === "human_approval_required",
  );
  assert.throws(
    () => g.grantScopeDirectly({ agentDid: AGENT, scopes: ["physical:motion"], grantedBy: HUMAN, reason: "ok" }),
    (error) => error.code === "reason_required",
  );
  assert.deepEqual(g.listGrants(AGENT), []);
});

test("a proposal cannot exceed the proposable ceiling", () => {
  const g = gov();
  assert.throws(
    () => g.proposeScopeChange({
      agentDid: AGENT,
      scopes: ["economy:settle"],
      rationale: "needs settlement rights for payouts",
    }),
    (error) => error.code === "scope_above_ceiling",
  );
  assert.ok(DEFAULT_PROPOSABLE_SCOPES.includes("skill:write"));
  assert.ok(!DEFAULT_PROPOSABLE_SCOPES.includes("economy:settle"));
});

test("a rejected proposal grants nothing and cannot be reopened", () => {
  const g = gov();
  const { proposal_id: id } = g.proposeScopeChange({
    agentDid: AGENT,
    scopes: ["skill:write"],
    rationale: "needs write access for the release task",
  });
  const { proposal, grant } = g.recordHumanDecision(id, { principal: HUMAN, decision: "reject" });
  assert.equal(proposal.status, "rejected");
  assert.equal(grant, null);
  assert.deepEqual(g.effectiveScopes(AGENT), []);
  assert.throws(
    () => g.recordHumanDecision(id, { principal: OTHER_HUMAN, decision: "approve" }),
    (error) => error.code === "proposal_closed",
  );
});

test("duplicate approvals by the same human do not count twice", () => {
  const g = gov({ requiredHumanApprovals: 2 });
  const { proposal_id: id } = g.proposeScopeChange({
    agentDid: AGENT,
    scopes: ["skill:write"],
    rationale: "needs write access for the release task",
  });
  const first = g.recordHumanDecision(id, { principal: HUMAN, decision: "approve" });
  assert.equal(first.grant, null);
  const repeat = g.recordHumanDecision(id, { principal: HUMAN, decision: "approve" });
  assert.equal(repeat.grant, null, "the same human approving twice is still one approval");
  assert.equal(repeat.proposal.approvals.filter((approval) => approval.decision === "approve").length, 1);

  const second = g.recordHumanDecision(id, { principal: OTHER_HUMAN, decision: "approve" });
  assert.equal(second.proposal.status, "approved");
  assert.ok(second.grant);
});

test("high-impact domains are classified, and physical is never proposable", () => {
  assert.equal(isHighImpactScope("governance:propose"), true);
  assert.equal(isHighImpactScope("skill:write"), false);
  assert.deepEqual(parseScope("skill:write"), { domain: "skill", action: "write" });
  assert.throws(() => parseScope("Skill:Write"), (error) => error.code === "invalid_scope");
  assert.throws(() => parseScope("skillwrite"), (error) => error.code === "invalid_scope");
  assert.equal(principalKind("human:alice"), "human");
  assert.throws(() => principalKind("alice"), (error) => error.code === "invalid_principal");
});
