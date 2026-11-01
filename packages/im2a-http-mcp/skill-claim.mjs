/**
 * v2.7.1 skill_claim: draft_patch -> owned skill + initial/successive
 * contribution-chain entry.
 */
import { createHash, randomBytes } from "node:crypto";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { parseSkillMarkdown, renderSkillMarkdown } from "./skill-metadata.mjs";

export const SKILL_CLAIM_VERSION = "1.0";
const GOAL_RE = /^goal_[0-9a-f]{24}$/;
const DRAFT_RE = /^draft_[0-9a-f]{24}$/;
const SKILL_RE = /^[a-z0-9][a-z0-9-]{1,63}$/;
const SHA_RE = /^[0-9a-f]{64}$/;
const TRACE_RE = /^[0-9a-f]{32}$/;
const SIGNAL_TYPES = new Set(["skill", "memory_item"]);

export class SkillClaimError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "SkillClaimError";
    this.code = code;
  }
}

function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value && typeof value === "object") {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

export function contributionEntryHash(entry) {
  const copy = { ...entry };
  delete copy.entry_hash;
  return createHash("sha256").update(canonical(copy)).digest("hex");
}

export function verifyContributionChain(entries) {
  const errors = [];
  let previous = null;
  for (let index = 0; index < entries.length; index += 1) {
    const entry = entries[index];
    if (entry.prev_hash !== previous) errors.push(`entry ${index}: prev_hash mismatch`);
    if (!SHA_RE.test(String(entry.entry_hash ?? ""))) errors.push(`entry ${index}: entry_hash invalid`);
    if (contributionEntryHash(entry) !== entry.entry_hash) errors.push(`entry ${index}: entry_hash mismatch`);
    previous = entry.entry_hash;
  }
  return { ok: errors.length === 0, count: entries.length, last_hash: previous, errors };
}

function validSignals(signals) {
  return Array.isArray(signals) && signals.length > 0 && signals.every((signal) =>
    signal && SIGNAL_TYPES.has(signal.type) && typeof signal.id === "string" && signal.id.length > 0);
}

function bumpMinor(version) {
  const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(String(version ?? ""));
  if (!match) throw new SkillClaimError("SKILL_VERSION_INVALID", version);
  return `${Number(match[1])}.${Number(match[2]) + 1}.0`;
}

async function findDraft(dir, draftId, goalId) {
  let names;
  try { names = await readdir(dir, { withFileTypes: true }); }
  catch (error) {
    if (error.code === "ENOENT") throw new SkillClaimError("DRAFT_NOT_FOUND", draftId);
    throw error;
  }
  const matches = [];
  const walk = async (folder) => {
    for (const entry of await readdir(folder, { withFileTypes: true })) {
      const full = path.join(folder, entry.name);
      if (entry.isDirectory()) { await walk(full); continue; }
      if (!entry.name.endsWith(".md")) continue;
      const text = await readFile(full, "utf8");
      if (!new RegExp(`^[-*] draft_id: ${draftId}$`, "m").test(text)) continue;
      if (!new RegExp(`^[-*] goal_id: ${goalId}$`, "m").test(text)) continue;
      const target = /^[-*] target_skill_id: ([A-Za-z0-9_.:-]+)$/m.exec(text)?.[1] ?? entry.name.replace(/\.patch\.md$/, "");
      matches.push({ path: full, text, target_skill_id: target });
    }
  };
  await walk(dir);
  if (matches.length === 0) throw new SkillClaimError("DRAFT_NOT_FOUND", draftId);
  return matches[0];
}

async function readLog(logPath) {
  let raw;
  try { raw = await readFile(logPath, "utf8"); }
  catch (error) {
    if (error.code === "ENOENT") return [];
    throw error;
  }
  return raw.split("\n").filter(Boolean).map((line, index) => {
    try { return JSON.parse(line); }
    catch { throw new SkillClaimError("CONTRIBUTION_LOG_INVALID", `line ${index + 1}`); }
  });
}

export async function claimDraftSkill({
  skillStore,
  skillsDir,
  draftsDir,
  agentDid,
  draftId,
  targetSkillId = null,
  newSkillName = null,
  claimJustification,
  traceId = null,
  now = Date.now(),
} = {}) {
  if (typeof agentDid !== "string" || agentDid.length === 0) throw new SkillClaimError("INVALID_AGENT_DID");
  if (!DRAFT_RE.test(String(draftId ?? ""))) throw new SkillClaimError("DRAFT_INVALID");
  if (!claimJustification || typeof claimJustification !== "object") throw new SkillClaimError("CLAIM_JUSTIFICATION_REQUIRED");
  const goalId = String(claimJustification.goal_id ?? "");
  if (!GOAL_RE.test(goalId)) throw new SkillClaimError("GOAL_INVALID");
  if (!validSignals(claimJustification.source_signals)) throw new SkillClaimError("SOURCE_SIGNALS_INVALID");
  if (traceId !== null && !TRACE_RE.test(String(traceId))) throw new SkillClaimError("TRACE_INVALID");
  if (targetSkillId && newSkillName) throw new SkillClaimError("CLAIM_TARGET_AMBIGUOUS");
  if (!targetSkillId && !newSkillName) throw new SkillClaimError("CLAIM_TARGET_REQUIRED");
  if (newSkillName && !SKILL_RE.test(String(newSkillName))) throw new SkillClaimError("SKILL_METADATA_INVALID", "new_skill_name invalid");
  if (targetSkillId && !SKILL_RE.test(String(targetSkillId))) throw new SkillClaimError("SKILL_METADATA_INVALID", "target_skill_id invalid");

  const draft = await findDraft(draftsDir, draftId, goalId);
  const draftSha = createHash("sha256").update(draft.text).digest("hex");
  const create = Boolean(newSkillName);
  const skillId = create ? newSkillName : targetSkillId;
  const existing = skillStore.list().find((item) => item.id === skillId) ?? null;
  if (create && existing) throw new SkillClaimError("SKILL_EXISTS", skillId);
  if (!create && !existing) throw new SkillClaimError("SKILL_NOT_FOUND", skillId);
  if (draft.target_skill_id !== skillId && create && draft.target_skill_id !== "unknown") {
    // A renamed new skill is allowed, but the draft must still be the same artifact.
  }

  const root = path.join(skillsDir, skillId);
  const logPath = path.join(root, ".im2a", "contributions.jsonl");
  const skillPath = path.join(root, "SKILL.md");
  let metadata;
  let versionBefore = null;
  let versionAfter = "0.1.0";
  let entries = [];
  if (create) {
    metadata = {
      "im2a.origin_agent": agentDid,
      "im2a.origin_credit_at_claim": String(claimJustification.origin_credit_at_claim ?? 0.72),
      "im2a.contribution_log_ref": `skills/${skillId}/.im2a/contributions.jsonl`,
      "im2a.skill_lineage": JSON.stringify([`${agentDid}@0.1.0`]),
      "im2a.patch_depth": "0",
    };
  } else {
    let existingMd;
    try { existingMd = await readFile(skillPath, "utf8"); }
    catch { throw new SkillClaimError("SKILL_METADATA_MISSING", skillId); }
    const parsed = parseSkillMarkdown(existingMd);
    metadata = { ...parsed.metadata };
    if (!metadata["im2a.origin_agent"]) throw new SkillClaimError("SKILL_METADATA_MISSING", "origin_agent");
    versionBefore = existing.version;
    versionAfter = bumpMinor(existing.version);
    entries = await readLog(logPath);
    const chain = verifyContributionChain(entries);
    if (!chain.ok) throw new SkillClaimError("CONTRIBUTION_CHAIN_INVALID", chain.errors.join("; "));
    metadata["im2a.patch_depth"] = String(Number(metadata["im2a.patch_depth"] ?? 0) + 1);
    let lineage = [];
    try { lineage = JSON.parse(metadata["im2a.skill_lineage"] ?? "[]"); } catch { lineage = []; }
    if (!Array.isArray(lineage)) lineage = [];
    lineage.push(`${agentDid}@${versionAfter}`);
    metadata["im2a.skill_lineage"] = JSON.stringify(lineage);
    metadata["im2a.contribution_log_ref"] = `skills/${skillId}/.im2a/contributions.jsonl`;
  }
  const body = draft.text.trim();
  const description = claimJustification.description ?? (create ? `Claimed skill ${skillId}` : `Patch claim for ${skillId} version ${versionAfter}`);
  const skillMd = renderSkillMarkdown({ name: skillId, description, metadata, body });
  const prevHash = entries.length ? entries[entries.length - 1].entry_hash : null;
  const entry = {
    entry_id: `contrib_${randomBytes(12).toString("hex")}`,
    claim_id: `claim_${randomBytes(12).toString("hex")}`,
    agent_did: agentDid,
    action: create ? "create" : "patch",
    version_before: versionBefore,
    version_after: versionAfter,
    goal_id: goalId,
    trace_id: traceId,
    timestamp: new Date(now).toISOString(),
    draft_id: draftId,
    draft_sha256: draftSha,
    source_signals: claimJustification.source_signals,
    prev_hash: prevHash,
    entry_hash: null,
  };
  entry.entry_hash = contributionEntryHash(entry);
  const nextEntries = [...entries, entry];
  const chain = verifyContributionChain(nextEntries);
  if (!chain.ok) throw new SkillClaimError("CONTRIBUTION_CHAIN_INVALID", chain.errors.join("; "));

  await mkdir(path.dirname(logPath), { recursive: true, mode: 0o750 });
  await writeFile(skillPath, skillMd, { encoding: "utf8", mode: 0o640 });
  await writeFile(logPath, `${nextEntries.map((item) => JSON.stringify(item)).join("\n")}\n`, { encoding: "utf8", mode: 0o640 });
  const files = { "SKILL.md": skillMd, ".im2a/contributions.jsonl": `${nextEntries.map((item) => JSON.stringify(item)).join("\n")}\n` };
  const registered = skillStore.registerPackage({ id: skillId, version: versionAfter, description, tags: create ? ["claimed"] : ["patched"], files });
  if (!create) skillStore.setActive(skillId, registered.packageSha, "skill_claim_patch");
  return {
    schema_version: SKILL_CLAIM_VERSION,
    claim_id: entry.claim_id,
    skill_id: skillId,
    action: entry.action,
    version: versionAfter,
    version_before: versionBefore,
    origin_agent: metadata["im2a.origin_agent"],
    patch_depth: Number(metadata["im2a.patch_depth"]),
    contribution_log_ref: metadata["im2a.contribution_log_ref"],
    contribution_log_path: logPath,
    skill_md_path: skillPath,
    entry,
    chain,
    packageSha: registered.packageSha,
  };
}
