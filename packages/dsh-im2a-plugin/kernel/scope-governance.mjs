/**
 * WP-6 of the six-layer plan: governance and physical authorization (C1).
 *
 * Two things are being protected here, and they are deliberately not the same
 * mechanism:
 *
 *   1. An Agent may PROPOSE expanding its own scope, but may never approve or
 *      self-grant. Only a human principal decides. This is the C1 rule that the
 *      scope ceiling belongs to governance, not to the agent.
 *
 *   2. A physical scope (motion, actuation) has NO agent-initiated path at all.
 *      `proposeScopeChange` refuses it outright: a proposal that can be created
 *      is a proposal that can be argued into existence. Physical authority is
 *      only issued through `grantScopeDirectly`, which requires a human
 *      principal and a recorded reason.
 *
 * Both refusals are errors, not silent downgrades, so a caller cannot mistake a
 * rejected request for a pending one.
 */
import { createHash, randomBytes } from "node:crypto";

export const SCOPE_GOVERNANCE_VERSION = "1.0";

const SCOPE_RE = /^[a-z][a-z0-9_-]{1,31}:[a-z][a-z0-9_-]{1,31}$/;
const PRINCIPAL_RE = /^(human|agent|system):[A-Za-z0-9._:%-]{1,200}$/;

/** Scopes an Agent may even ask for. Physical domains are absent by design. */
export const DEFAULT_PROPOSABLE_SCOPES = Object.freeze([
  "skill:read",
  "skill:write",
  "memory:read",
  "memory:write",
  "trace:write",
  "autonomy:drive",
  "credit:read",
  "economy:read",
  "fleet:coordinate",
  "governance:read",
  "observability:read",
]);

/** Domains whose authority is physical. They are never agent-proposable. */
export const PHYSICAL_SCOPE_DOMAINS = Object.freeze(["physical", "actuator", "bio"]);

/** Changes of this class must be decided by a human, never by an agent or a timer. */
export const HIGH_IMPACT_SCOPE_DOMAINS = Object.freeze(["governance", "physical", "actuator", "bio", "fleet"]);

export class ScopeGovernanceError extends Error {
  constructor(code, message, detail = {}) {
    super(message);
    this.name = "ScopeGovernanceError";
    this.code = code;
    this.detail = detail;
  }
}

export function parseScope(scope) {
  if (typeof scope !== "string" || !SCOPE_RE.test(scope)) {
    throw new ScopeGovernanceError("invalid_scope", `invalid scope ${scope}`, { scope });
  }
  const [domain, action] = scope.split(":");
  return { domain, action };
}

export function scopeDomain(scope) {
  return parseScope(scope).domain;
}

export function isPhysicalScope(scope) {
  return PHYSICAL_SCOPE_DOMAINS.includes(scopeDomain(scope));
}

export function isHighImpactScope(scope) {
  return HIGH_IMPACT_SCOPE_DOMAINS.includes(scopeDomain(scope));
}

export function principalKind(principal) {
  // An agent is identified by its DID; accept the bare DID as an agent principal
  // so a caller cannot accidentally turn "agent tries to approve" into
  // "invalid input" and miss the real refusal.
  if (typeof principal === "string" && /^did:[a-z0-9]+:[A-Za-z0-9._:%-]{3,200}$/.test(principal)) {
    return "agent";
  }
  if (typeof principal !== "string" || !PRINCIPAL_RE.test(principal)) {
    throw new ScopeGovernanceError("invalid_principal", `invalid principal ${principal}`, { principal });
  }
  return principal.split(":")[0];
}

function requireHuman(principal, action) {
  const kind = principalKind(principal);
  if (kind !== "human") {
    throw new ScopeGovernanceError(
      "human_approval_required",
      `${action} requires a human principal, got ${kind}`,
      { principal, kind },
    );
  }
  return principal;
}

function normalizeScopes(scopes, field) {
  if (!Array.isArray(scopes) || scopes.length === 0) {
    throw new ScopeGovernanceError("invalid_scopes", `${field} must be a non-empty array`);
  }
  const unique = [...new Set(scopes)];
  for (const scope of unique) parseScope(scope);
  return unique.sort();
}

function newId(prefix) {
  return `${prefix}_${randomBytes(12).toString("hex")}`;
}

export function createScopeGovernance({
  proposableScopes = DEFAULT_PROPOSABLE_SCOPES,
  requiredHumanApprovals = 1,
  clock = () => new Date(),
} = {}) {
  if (!Number.isInteger(requiredHumanApprovals) || requiredHumanApprovals < 1) {
    throw new ScopeGovernanceError("invalid_config", "requiredHumanApprovals must be a positive integer");
  }
  const proposable = new Set(proposableScopes);
  const proposals = new Map();
  const grants = [];

  function requireProposal(proposalId) {
    const proposal = proposals.get(proposalId);
    if (!proposal) {
      throw new ScopeGovernanceError("unknown_proposal", `no proposal ${proposalId}`, { proposalId });
    }
    return proposal;
  }

  function issueGrant({ agentDid, scopes, grantedBy, source, reason }) {
    const grant = {
      schema_version: SCOPE_GOVERNANCE_VERSION,
      grant_id: newId("grn"),
      agent_did: agentDid,
      scopes,
      granted_by: grantedBy,
      granted_at: new Date(clock()).toISOString(),
      source,
      reason,
    };
    grants.push(Object.freeze(structuredClone(grant)));
    return grant;
  }

  return {
    version: SCOPE_GOVERNANCE_VERSION,

    /**
     * Agent-initiated path. It can only ADD scope inside the proposable set, and
     * it never returns a grant; a human decision is always required.
     */
    proposeScopeChange({ agentDid, scopes, rationale } = {}) {
      if (typeof agentDid !== "string" || agentDid.length === 0) {
        throw new ScopeGovernanceError("invalid_agent", "agentDid is required");
      }
      const requested = normalizeScopes(scopes, "scopes");
      if (typeof rationale !== "string" || rationale.trim().length < 8) {
        throw new ScopeGovernanceError("rationale_required", "rationale must be at least 8 characters");
      }

      // Physical authority has no agent-initiated path at all: refuse before a
      // proposal object can exist.
      const physical = requested.filter(isPhysicalScope);
      if (physical.length > 0) {
        throw new ScopeGovernanceError(
          "physical_scope_requires_human_grant",
          `physical scope ${physical.join(", ")} cannot be proposed by an agent; it must be granted by a human`,
          { physical },
        );
      }

      const aboveCeiling = requested.filter((scope) => !proposable.has(scope));
      if (aboveCeiling.length > 0) {
        throw new ScopeGovernanceError(
          "scope_above_ceiling",
          `scope ${aboveCeiling.join(", ")} is outside the proposable set`,
          { aboveCeiling },
        );
      }

      const proposal = {
        schema_version: SCOPE_GOVERNANCE_VERSION,
        proposal_id: newId("scp"),
        agent_did: agentDid,
        requested_scopes: requested,
        high_impact: requested.some(isHighImpactScope),
        rationale: rationale.trim(),
        status: "pending",
        approvals: [],
        created_at: new Date(clock()).toISOString(),
        required_human_approvals: requiredHumanApprovals,
      };
      proposals.set(proposal.proposal_id, proposal);
      return structuredClone(proposal);
    },

    /**
     * Record one human decision. Agents cannot approve anything — including
     * their own proposal — and duplicate approvals by the same human do not
     * count twice.
     */
    recordHumanDecision(proposalId, { principal, decision } = {}) {
      const proposal = requireProposal(proposalId);
      if (proposal.status !== "pending") {
        throw new ScopeGovernanceError("proposal_closed", `proposal ${proposalId} is ${proposal.status}`, {
          proposalId,
          status: proposal.status,
        });
      }
      requireHuman(principal, "scope approval");
      if (decision !== "approve" && decision !== "reject") {
        throw new ScopeGovernanceError("invalid_decision", "decision must be approve or reject");
      }

      if (decision === "reject") {
        proposal.status = "rejected";
        proposal.approvals.push({ principal, decision, at: new Date(clock()).toISOString() });
        return { proposal: structuredClone(proposal), grant: null };
      }

      if (!proposal.approvals.some((approval) => approval.principal === principal && approval.decision === "approve")) {
        proposal.approvals.push({ principal, decision, at: new Date(clock()).toISOString() });
      }
      const approvals = proposal.approvals.filter((approval) => approval.decision === "approve").length;
      if (approvals < requiredHumanApprovals) {
        return { proposal: structuredClone(proposal), grant: null };
      }

      proposal.status = "approved";
      const grant = issueGrant({
        agentDid: proposal.agent_did,
        scopes: proposal.requested_scopes,
        grantedBy: principal,
        source: "proposal",
        reason: proposal.rationale,
      });
      return { proposal: structuredClone(proposal), grant };
    },

    /**
     * Human-initiated path. This is the ONLY way physical authority is issued.
     * It still records who granted it and why; nothing is granted silently.
     */
    grantScopeDirectly({ agentDid, scopes, grantedBy, reason } = {}) {
      if (typeof agentDid !== "string" || agentDid.length === 0) {
        throw new ScopeGovernanceError("invalid_agent", "agentDid is required");
      }
      requireHuman(grantedBy, "direct scope grant");
      const granted = normalizeScopes(scopes, "scopes");
      if (typeof reason !== "string" || reason.trim().length < 8) {
        throw new ScopeGovernanceError("reason_required", "reason must be at least 8 characters");
      }
      return issueGrant({
        agentDid,
        scopes: granted,
        grantedBy,
        source: "direct",
        reason: reason.trim(),
      });
    },

    /** Scope an agent actually holds: the union of every grant issued for it. */
    effectiveScopes(agentDid) {
      const held = new Set();
      for (const grant of grants) {
        if (grant.agent_did !== agentDid) continue;
        for (const scope of grant.scopes) held.add(scope);
      }
      return [...held].sort();
    },

    listProposals(agentDid) {
      const all = [...proposals.values()];
      const filtered = agentDid ? all.filter((proposal) => proposal.agent_did === agentDid) : all;
      return filtered.map((proposal) => structuredClone(proposal));
    },

    listGrants(agentDid) {
      return grants
        .filter((grant) => (agentDid ? grant.agent_did === agentDid : true))
        .map((grant) => structuredClone(grant));
    },
  };
}

/** Stable identity of one grant record, for audit evidence. */
export function grantDigest(grant) {
  const { granted_at: _ignored, ...content } = grant;
  const keys = Object.keys(content).sort();
  const canonical = `{${keys.map((key) => `${JSON.stringify(key)}:${JSON.stringify(content[key])}`).join(",")}}`;
  return `sha256:${createHash("sha256").update(canonical).digest("hex")}`;
}
