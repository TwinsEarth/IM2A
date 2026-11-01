/**
 * v2.7.2 read-only contribution audit.
 */
import { readFile } from "node:fs/promises";
import path from "node:path";
import { isEffectivePatch, verifyContributionChain } from "./skill-claim.mjs";

export const CONTRIBUTION_AUDIT_VERSION = "1.0";
const SKILL_RE = /^[a-z0-9][a-z0-9-]{1,63}$/;

export class ContributionAuditError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "ContributionAuditError";
    this.code = code;
  }
}

export async function auditContributionLog({ skillsDir, skillId, agentDid = null } = {}) {
  if (!SKILL_RE.test(String(skillId ?? ""))) throw new ContributionAuditError("SKILL_METADATA_INVALID", "skill_id invalid");
  if (agentDid !== null && typeof agentDid !== "string") throw new ContributionAuditError("INVALID_AGENT_DID");
  const logPath = path.join(skillsDir, skillId, ".im2a", "contributions.jsonl");
  let raw;
  try { raw = await readFile(logPath, "utf8"); }
  catch (error) {
    if (error.code === "ENOENT") throw new ContributionAuditError("SKILL_METADATA_MISSING", skillId);
    throw error;
  }
  const entries = [];
  let parseBrokenAt = null;
  const lines = raw.split("\n").filter((line) => line.trim().length > 0);
  for (let index = 0; index < lines.length; index += 1) {
    try { entries.push(JSON.parse(lines[index])); }
    catch {
      parseBrokenAt = index + 1;
      break;
    }
  }
  const chain = verifyContributionChain(entries);
  const brokenAt = parseBrokenAt ?? chain.broken_at_seq;
  const integrityStatus = chain.ok && parseBrokenAt === null ? "verified" : `broken_at_seq_${brokenAt ?? "unknown"}`;
  const filtered = agentDid === null ? entries : entries.filter((entry) => entry.agent_did === agentDid);
  const contributors = new Set(entries.map((entry) => entry.agent_did).filter((did) => typeof did === "string"));
  const patchCount = entries.filter((entry) => entry.action === "patch").length;
  const effectivePatchCount = entries.filter(isEffectivePatch).length;
  return {
    schema_version: CONTRIBUTION_AUDIT_VERSION,
    skill_id: skillId,
    log_ref: `skills/${skillId}/.im2a/contributions.jsonl`,
    entries: filtered,
    integrity_status: integrityStatus,
    broken_at_seq: brokenAt,
    unique_contributor_count: contributors.size,
    effective_patch_count: effectivePatchCount,
    total_patch_count: patchCount,
    chain_errors: chain.errors,
  };
}
