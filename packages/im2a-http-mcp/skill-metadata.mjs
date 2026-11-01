/**
 * v2.7.0 agentskills.io metadata ownership namespace.
 *
 * IM2A fields live inside the standard metadata map as namespaced string keys.
 * The contribution chain stays in a companion JSONL file and never in the
 * frontmatter.
 */
import { createHash, randomBytes } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

export const SKILL_METADATA_VERSION = "1.0";
export const IM2A_METADATA_KEYS = Object.freeze([
  "im2a.origin_agent",
  "im2a.origin_credit_at_claim",
  "im2a.contribution_log_ref",
  "im2a.skill_lineage",
  "im2a.patch_depth",
]);
const SKILL_ID_RE = /^[a-z0-9][a-z0-9-]{1,63}$/;
const GOAL_RE = /^goal_[0-9a-f]{24}$/;
const DRAFT_RE = /^draft_[0-9a-f]{24}$/;
const SHA_RE = /^[0-9a-f]{64}$/;

export class SkillMetadataError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "SkillMetadataError";
    this.code = code;
  }
}

function yamlString(value) {
  return JSON.stringify(String(value));
}

export function parseSkillMarkdown(text) {
  if (typeof text !== "string") throw new SkillMetadataError("SKILL_METADATA_INVALID", "skill must be text");
  const match = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?([\s\S]*)$/.exec(text);
  if (!match) throw new SkillMetadataError("SKILL_METADATA_INVALID", "frontmatter is required");
  const result = { name: null, description: null, metadata: {}, body: match[2] };
  let metadataIndent = null;
  for (const raw of match[1].split(/\r?\n/)) {
    const line = raw.replace(/\s+$/, "");
    if (!line.trim() || line.trim().startsWith("#")) continue;
    const indent = line.match(/^\s*/)[0].length;
    const trimmed = line.trim();
    const split = trimmed.indexOf(":");
    if (split <= 0) throw new SkillMetadataError("SKILL_METADATA_INVALID", `invalid frontmatter line: ${trimmed}`);
    const key = trimmed.slice(0, split).trim();
    const value = trimmed.slice(split + 1).trim();
    if (metadataIndent !== null && indent > metadataIndent) {
      const metadataValue = value.startsWith("\"") ? JSON.parse(value) : value;
      result.metadata[key] = String(metadataValue);
      continue;
    }
    metadataIndent = null;
    if (key === "metadata") {
      if (value !== "") throw new SkillMetadataError("SKILL_METADATA_INVALID", "metadata must be a map");
      metadataIndent = indent;
    } else if (key === "name") {
      result.name = value.startsWith("\"") ? JSON.parse(value) : value;
    } else if (key === "description") {
      result.description = value.startsWith("\"") ? JSON.parse(value) : value;
    }
  }
  return result;
}

export function renderSkillMarkdown({ name, description, metadata, body }) {
  if (!SKILL_ID_RE.test(String(name ?? ""))) throw new SkillMetadataError("SKILL_METADATA_INVALID", "invalid skill name");
  if (typeof description !== "string" || description.length < 1 || description.length > 1024) {
    throw new SkillMetadataError("SKILL_METADATA_INVALID", "description must be 1..1024 characters");
  }
  const lines = ["---", `name: ${yamlString(name)}`, `description: ${yamlString(description)}`, "metadata:"];
  for (const key of IM2A_METADATA_KEYS) {
    if (metadata[key] === undefined) throw new SkillMetadataError("SKILL_METADATA_INVALID", `missing ${key}`);
    lines.push(`  ${key}: ${yamlString(metadata[key])}`);
  }
  lines.push("---", "", String(body ?? "").trim(), "");
  return lines.join("\n");
}

export function assertNoAgentMetadataEdit(contentOrFiles) {
  const texts = [];
  if (typeof contentOrFiles === "string") texts.push(contentOrFiles);
  else if (contentOrFiles && typeof contentOrFiles === "object") {
    for (const [name, body] of Object.entries(contentOrFiles)) {
      if (name === "SKILL.md" || name.startsWith(".im2a/")) texts.push(String(body));
    }
  }
  for (const text of texts) {
    if (/^metadata:\s*$/m.test(text) && /^\s+im2a\./m.test(text)) {
      throw new SkillMetadataError("IM2A_METADATA_READ_ONLY", "im2a.* metadata is written only by skill_claim/skill_patch");
    }
  }
}

async function readDraftBody(draftPatch) {
  if (!draftPatch?.path) return "";
  try { return await readFile(draftPatch.path, "utf8"); }
  catch { return ""; }
}

export async function claimSkill({
  skillStore,
  skillsDir,
  agentDid,
  goalId,
  draftPatch,
  description = null,
  body = null,
  lineage = null,
  patchDepth = 0,
  creditAtClaim = 0.72,
  now = Date.now(),
} = {}) {
  if (typeof agentDid !== "string" || agentDid.length < 1) throw new SkillMetadataError("INVALID_AGENT_DID");
  if (!GOAL_RE.test(String(goalId ?? ""))) throw new SkillMetadataError("GOAL_INVALID");
  if (!draftPatch || typeof draftPatch !== "object") throw new SkillMetadataError("DRAFT_INVALID", "draft_patch is required");
  if (!DRAFT_RE.test(String(draftPatch.draft_id ?? ""))) throw new SkillMetadataError("DRAFT_INVALID", "draft_id invalid");
  if (!SHA_RE.test(String(draftPatch.sha256 ?? ""))) throw new SkillMetadataError("DRAFT_INVALID", "draft sha256 invalid");
  if (draftPatch.applied !== false) throw new SkillMetadataError("DRAFT_INVALID", "draft must be unapplied");
  const skillId = String(draftPatch.target_skill_id ?? "");
  if (!SKILL_ID_RE.test(skillId)) throw new SkillMetadataError("SKILL_METADATA_INVALID", "target_skill_id invalid");
  if (!Number.isInteger(patchDepth) || patchDepth < 0 || patchDepth > 1000) throw new SkillMetadataError("SKILL_METADATA_INVALID", "patch_depth invalid");
  if (!Number.isFinite(creditAtClaim) || creditAtClaim < 0 || creditAtClaim > 1) throw new SkillMetadataError("SKILL_METADATA_INVALID", "origin_credit_at_claim must be 0..1");
  if (skillStore.list().some((item) => item.id === skillId)) throw new SkillMetadataError("SKILL_EXISTS", skillId);

  const logRef = `skills/${skillId}/.im2a/contributions.jsonl`;
  const lineageValue = JSON.stringify(lineage ?? [`${agentDid}@v1`]);
  const metadata = {
    "im2a.origin_agent": agentDid,
    "im2a.origin_credit_at_claim": String(creditAtClaim),
    "im2a.contribution_log_ref": logRef,
    "im2a.skill_lineage": lineageValue,
    "im2a.patch_depth": String(patchDepth),
  };
  const draftText = body === null ? await readDraftBody(draftPatch) : "";
  const resolvedBody = body ?? (draftText.trim() || `# ${skillId}\n\nClaimed from draft ${draftPatch.draft_id}.`);
  const resolvedDescription = description ?? `Skill claimed from autonomous draft ${draftPatch.draft_id}`;
  const skillMd = renderSkillMarkdown({ name: skillId, description: resolvedDescription, metadata, body: resolvedBody });
  const event = {
    event_id: `contrib_${randomBytes(12).toString("hex")}`,
    at: new Date(now).toISOString(),
    event_type: "skill_claim",
    goal_id: goalId,
    agent_did: agentDid,
    draft_id: draftPatch.draft_id,
    draft_sha256: draftPatch.sha256,
    target_skill_id: skillId,
    credit_at_claim: creditAtClaim,
    patch_depth: patchDepth,
  };
  const logText = `${JSON.stringify(event)}\n`;
  const root = path.join(skillsDir, skillId);
  const logPath = path.join(root, ".im2a", "contributions.jsonl");
  const files = { "SKILL.md": skillMd, ".im2a/contributions.jsonl": logText };
  const registered = skillStore.registerPackage({ id: skillId, version: "0.1.0", description: resolvedDescription, tags: ["claimed"], files });
  await mkdir(path.dirname(logPath), { recursive: true, mode: 0o750 });
  await writeFile(path.join(root, "SKILL.md"), skillMd, { encoding: "utf8", mode: 0o640 });
  await writeFile(logPath, logText, { encoding: "utf8", mode: 0o640 });
  return {
    schema_version: SKILL_METADATA_VERSION,
    skill_id: skillId,
    packageSha: registered.packageSha,
    metadata,
    contribution_log_ref: logRef,
    contribution_log_path: logPath,
    skill_md_path: path.join(root, "SKILL.md"),
    contribution_event: event,
    standard_fields: { name: skillId, description: resolvedDescription, body: resolvedBody },
  };
}

export function metadataHash(metadata) {
  return `sha256:${createHash("sha256").update(JSON.stringify(metadata)).digest("hex")}`;
}
