/**
 * WP-4 of the six-layer plan: DID-bound portability (L4).
 *
 * Skills, goals and credit belong to a DID, never to a device. Moving to a new
 * terminal exports one bundle and imports it under the SAME DID; the bundle's
 * digest is stable across machines so "did the assets survive the move" is a
 * comparison, not a claim.
 *
 * Two invariants are enforced here rather than left to callers:
 *   - a bundle can only be imported into its own DID (C4: lineage is not
 *     overwritten by importing it somewhere else);
 *   - import never appends to the contribution chain. It carries a read-only
 *     proof (count + head hash); restoring history is not rewriting it.
 */
import { createHash } from "node:crypto";

export const DID_BUNDLE_VERSION = "1.0";
export const VERIFICATION_TIERS = Object.freeze(["T0", "T1", "T2"]);

const DID_RE = /^did:[a-z0-9]+:[A-Za-z0-9._:%-]{3,200}$/;
const SHA256_RE = /^sha256:[0-9a-f]{64}$/;
const GOAL_ID_RE = /^goal_[0-9a-f]{24}$/;

export class DidPortabilityError extends Error {
  constructor(code, message, detail = {}) {
    super(message);
    this.name = "DidPortabilityError";
    this.code = code;
    this.detail = detail;
  }
}

/** Deterministic JSON: object keys sorted, arrays as given (callers pre-sort). */
export function canonicalJson(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (value && typeof value === "object") {
    const keys = Object.keys(value).sort();
    return `{${keys.map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

function digestOf(payload) {
  return `sha256:${createHash("sha256").update(canonicalJson(payload)).digest("hex")}`;
}

function normalizeSkills(skills) {
  if (!Array.isArray(skills)) throw new DidPortabilityError("invalid_bundle", "skills must be an array");
  const seen = new Set();
  return skills
    .map((skill) => {
      if (!skill || typeof skill.id !== "string" || skill.id.length === 0) {
        throw new DidPortabilityError("invalid_bundle", "each skill needs an id");
      }
      if (seen.has(skill.id)) throw new DidPortabilityError("duplicate_skill", `skill ${skill.id} appears twice`);
      seen.add(skill.id);
      return {
        id: skill.id,
        version: typeof skill.version === "string" ? skill.version : null,
        sha256: skill.sha256 ?? null,
      };
    })
    .sort((a, b) => a.id.localeCompare(b.id));
}

function normalizeGoals(goals) {
  if (!Array.isArray(goals)) throw new DidPortabilityError("invalid_bundle", "goals must be an array");
  const seen = new Set();
  return goals
    .map((goal) => {
      if (!goal || typeof goal.goal_id !== "string" || !GOAL_ID_RE.test(goal.goal_id)) {
        throw new DidPortabilityError("invalid_bundle", "each goal needs a goal_<24 hex> id");
      }
      if (seen.has(goal.goal_id)) throw new DidPortabilityError("duplicate_goal", `goal ${goal.goal_id} appears twice`);
      seen.add(goal.goal_id);
      return { goal_id: goal.goal_id, status: typeof goal.status === "string" ? goal.status : null };
    })
    .sort((a, b) => a.goal_id.localeCompare(b.goal_id));
}

function normalizeCredit(credit) {
  if (credit === null || credit === undefined) return null;
  if (!VERIFICATION_TIERS.includes(credit.verification_tier)) {
    throw new DidPortabilityError("invalid_bundle", `unknown verification tier ${credit.verification_tier}`);
  }
  if (typeof credit.score !== "number" || credit.score < 0 || credit.score > 1) {
    throw new DidPortabilityError("invalid_bundle", "credit.score must be within [0, 1]");
  }
  return { verification_tier: credit.verification_tier, score: credit.score };
}

function normalizeContribution(contribution) {
  if (contribution === null || contribution === undefined) return null;
  if (!Number.isInteger(contribution.count) || contribution.count < 0) {
    throw new DidPortabilityError("invalid_bundle", "contribution.count must be a non-negative integer");
  }
  if (contribution.count > 0 && !SHA256_RE.test(contribution.head_hash ?? "")) {
    throw new DidPortabilityError("invalid_bundle", "contribution.head_hash must be sha256:<64 hex> when count > 0");
  }
  return { count: contribution.count, head_hash: contribution.head_hash ?? null };
}

/** The content the digest commits to: everything except the export timestamp. */
function digestPayload(bundle) {
  return {
    schema_version: bundle.schema_version,
    did: bundle.did,
    assets: bundle.assets,
    contribution: bundle.contribution,
  };
}

export function exportDidBundle({ did, skills = [], goals = [], credit = null, contribution = null, clock = () => new Date() } = {}) {
  if (typeof did !== "string" || !DID_RE.test(did)) {
    throw new DidPortabilityError("invalid_did", `invalid DID ${did}`, { did });
  }
  const bundle = {
    schema_version: DID_BUNDLE_VERSION,
    did,
    exported_at: new Date(clock()).toISOString(),
    assets: {
      skills: normalizeSkills(skills),
      goals: normalizeGoals(goals),
      credit: normalizeCredit(credit),
    },
    contribution: normalizeContribution(contribution),
  };
  bundle.digest = digestOf(digestPayload(bundle));
  return bundle;
}

export function verifyDidBundle(bundle) {
  const errors = [];
  if (!bundle || typeof bundle !== "object") return { ok: false, errors: ["bundle must be an object"], digest: null };
  if (bundle.schema_version !== DID_BUNDLE_VERSION) errors.push("schema_version must be 1.0");
  if (typeof bundle.did !== "string" || !DID_RE.test(bundle.did)) errors.push("did is invalid");
  if (typeof bundle.exported_at !== "string" || Number.isNaN(Date.parse(bundle.exported_at))) {
    errors.push("exported_at must be a timestamp");
  }
  let expected = null;
  try {
    expected = digestOf(digestPayload(bundle));
  } catch (error) {
    errors.push(`bundle is not digestible: ${error.message}`);
  }
  if (expected && bundle.digest !== expected) errors.push("bundle digest does not match its content");
  return { ok: errors.length === 0, errors, digest: expected };
}

/**
 * Plan an import. Pure: it decides what would be written and proves it appends
 * nothing to the contribution chain. A bundle may only return to its own DID.
 */
export function planDidImport(bundle, { targetDid } = {}) {
  const check = verifyDidBundle(bundle);
  if (!check.ok) {
    throw new DidPortabilityError("invalid_bundle", check.errors.join("; "), { errors: check.errors });
  }
  if (targetDid !== bundle.did) {
    throw new DidPortabilityError(
      "did_mismatch",
      `bundle belongs to ${bundle.did}; importing into ${targetDid} would rewrite lineage (C4)`,
      { bundleDid: bundle.did, targetDid },
    );
  }
  return {
    did: bundle.did,
    digest: bundle.digest,
    assets: structuredClone(bundle.assets),
    contribution: bundle.contribution
      ? { ...bundle.contribution, mode: "read_only_proof" }
      : null,
    contributions_appended: 0,
    writes: [
      ...bundle.assets.skills.map((skill) => `skills/${skill.id}`),
      "goal_buffer",
      ...(bundle.assets.credit ? ["credit_profile"] : []),
    ],
  };
}
