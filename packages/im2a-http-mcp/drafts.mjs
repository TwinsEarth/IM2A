/**
 * v2.6.3 draft storage. A draft is a candidate patch artifact, never an active
 * skill. It is written under drafts/, not skills/.
 */
import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

export class DraftError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "DraftError";
    this.code = code;
  }
}

const GOAL_RE = /^goal_[0-9a-f]{24}$/;
const SKILL_RE = /^[A-Za-z0-9_.:-]{1,200}$/;

export async function writeDraft({
  dir,
  goalId,
  targetSkillId,
  intentSummary,
  sourceSignals = [],
  now = Date.now(),
}) {
  if (typeof dir !== "string" || dir.length === 0) throw new DraftError("DRAFT_INVALID", "drafts dir required");
  if (!GOAL_RE.test(String(goalId))) throw new DraftError("DRAFT_INVALID", "goal_id invalid");
  if (!SKILL_RE.test(String(targetSkillId))) throw new DraftError("DRAFT_INVALID", "target skill id invalid");
  if (typeof intentSummary !== "string" || intentSummary.length === 0) throw new DraftError("DRAFT_INVALID", "intent summary required");
  const draftId = `draft_${createHash("sha256").update(`${goalId}:${targetSkillId}:${intentSummary}`).digest("hex").slice(0, 24)}`;
  const folder = path.join(dir, goalId);
  const file = path.join(folder, `${targetSkillId}.patch.md`);
  const content = [
    `# Draft patch for ${targetSkillId}`,
    "",
    `- draft_id: ${draftId}`,
    `- goal_id: ${goalId}`,
    `- generated_at: ${new Date(now).toISOString()}`,
    `- status: draft (not applied)`,
    "",
    "## Intent",
    "",
    intentSummary,
    "",
    "## Source signals",
    "",
    ...sourceSignals.map((signal) => `- ${signal.type}:${signal.id}`),
    "",
    "## Proposed change",
    "",
    "Inspect the referenced stale material and prepare a bounded patch for operator review.",
    "This file is a candidate artifact only; no active skill package was modified.",
    "",
  ].join("\n");
  await mkdir(folder, { recursive: true, mode: 0o750 });
  await writeFile(file, content, { encoding: "utf8", mode: 0o640 });
  return {
    draft_id: draftId,
    target_skill_id: targetSkillId,
    path: file,
    bytes: Buffer.byteLength(content),
    sha256: createHash("sha256").update(content).digest("hex"),
    applied: false,
  };
}
