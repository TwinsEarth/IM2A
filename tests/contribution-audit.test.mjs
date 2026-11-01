import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { claimDraftSkill } from "../packages/im2a-http-mcp/skill-claim.mjs";
import { auditContributionLog } from "../packages/im2a-http-mcp/skill-audit.mjs";

const goalId = "goal_0123456789abcdef01234567";
const draftId = "draft_0123456789abcdef01234567";
const skillId = "audit-chain-skill";

function fakeStore() {
  const active = new Map();
  return {
    list: () => [...active.values()].map((item) => ({ id: item.id, version: item.version })),
    registerPackage: ({ id, version }) => {
      const packageSha = createHash("sha256").update(`${id}:${version}`).digest("hex");
      active.set(id, { id, version, packageSha });
      return { packageSha };
    },
    setActive: (id, packageSha) => {
      const item = active.get(id);
      if (item) item.packageSha = packageSha;
    },
  };
}

function draftText() {
  return `# Draft patch for ${skillId}\n\n- draft_id: ${draftId}\n- goal_id: ${goalId}\n- status: draft (not applied)\n\n## Intent\n\nAudit chain fixture.\n`;
}

test("contribution_audit verifies three patches and reports the tampered sequence", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-contribution-audit-"));
  try {
    const draftsDir = path.join(dir, "drafts");
    const skillsDir = path.join(dir, "skills");
    const draftPath = path.join(draftsDir, goalId, `${skillId}.patch.md`);
    await mkdir(path.dirname(draftPath), { recursive: true });
    await writeFile(draftPath, draftText(), "utf8");
    const skillStore = fakeStore();
    const justification = {
      goal_id: goalId,
      source_signals: [{ type: "skill", id: "audit-source" }],
      description: "Audit fixture.",
    };
    const claimed = await claimDraftSkill({
      skillStore,
      skillsDir,
      draftsDir,
      agentDid: "did:im2a:agent:author",
      draftId,
      newSkillName: skillId,
      claimJustification: justification,
      now: Date.UTC(2026, 10, 2, 0, 0, 0),
    });
    for (let i = 1; i <= 3; i += 1) {
      await claimDraftSkill({
        skillStore,
        skillsDir,
        draftsDir,
        agentDid: "did:im2a:agent:author",
        draftId,
        targetSkillId: skillId,
        claimJustification: { ...justification, description: `Patch ${i}` },
        changedSections: [`references/toolchain-${i}.md`],
        validationEvidence: {
          post_patch_success_count: i,
          post_patch_failure_count: i === 1 ? 1 : 0,
          verifier_agents: ["did:im2a:agent:verifier"],
        },
        now: Date.UTC(2026, 10, 2, i, 0, 0),
      });
    }
    const verified = await auditContributionLog({ skillsDir, skillId });
    assert.equal(verified.integrity_status, "verified");
    assert.equal(verified.entries.length, 4);
    assert.equal(verified.entries[3].version_after, "0.4.0");
    assert.equal(verified.unique_contributor_count, 1);
    assert.equal(verified.total_patch_count, 3);
    assert.equal(verified.effective_patch_count, 3);
    assert.ok(verified.effective_patch_count <= verified.total_patch_count);

    const logPath = claimed.contribution_log_path;
    const original = await readFile(logPath, "utf8");
    const lines = original.trim().split("\n");
    const second = JSON.parse(lines[1]);
    second.agent_did = "did:im2a:agent:tampered";
    lines[1] = JSON.stringify(second);
    await writeFile(logPath, `${lines.join("\n")}\n`, "utf8");
    const broken = await auditContributionLog({ skillsDir, skillId });
    assert.equal(broken.integrity_status, "broken_at_seq_2");
    assert.equal(broken.broken_at_seq, 2);
    assert.ok(broken.chain_errors.some((error) => error.includes("entry_hash mismatch")));
    assert.ok(broken.effective_patch_count <= broken.total_patch_count);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
