import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { claimDraftSkill, contributionEntryHash, verifyContributionChain } from "../packages/im2a-http-mcp/skill-claim.mjs";

const goalId = "goal_0123456789abcdef01234567";
const draftId = "draft_0123456789abcdef01234567";

function draftText(target) {
  return [
    `# Draft patch for ${target}`,
    "",
    `- draft_id: ${draftId}`,
    `- goal_id: ${goalId}`,
    "- status: draft (not applied)",
    "",
    "## Intent",
    "",
    "Claimed from an autonomous improvement intent.",
    "",
  ].join("\n");
}

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

test("skill_claim creates and patches an owned skill with a verifiable contribution chain", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-skill-claim-"));
  try {
    const target = "claimed-skill";
    const draftsDir = path.join(dir, "drafts");
    const skillsDir = path.join(dir, "skills");
    const draftPath = path.join(draftsDir, goalId, `${target}.patch.md`);
    await mkdir(path.dirname(draftPath), { recursive: true });
    await writeFile(draftPath, draftText(target), "utf8");
    await writeFile(path.join(dir, "unused"), "unused", "utf8");
    const store = fakeStore();
    const justification = {
      goal_id: goalId,
      source_signals: [{ type: "skill", id: "windows-build" }],
      description: "Owned claim fixture.",
    };
    const created = await claimDraftSkill({
      skillStore: store,
      skillsDir,
      draftsDir,
      agentDid: "did:im2a:agent:origin",
      draftId,
      newSkillName: target,
      claimJustification: justification,
      now: Date.UTC(2026, 10, 2, 0, 0, 0),
    });
    assert.equal(created.action, "create");
    assert.equal(created.version, "0.1.0");
    assert.equal(created.origin_agent, "did:im2a:agent:origin");
    assert.equal(created.entry.version_before, null);
    assert.equal(created.entry.version_after, "0.1.0");
    assert.equal(created.entry.goal_id, goalId);
    assert.equal(created.entry.prev_hash, null);
    assert.equal(contributionEntryHash(created.entry), created.entry.entry_hash);
    assert.equal(created.chain.ok, true);
    assert.equal(created.chain.count, 1);

    const patched = await claimDraftSkill({
      skillStore: store,
      skillsDir,
      draftsDir,
      agentDid: "did:im2a:agent:origin",
      draftId,
      targetSkillId: target,
      claimJustification: { ...justification, description: "Second claim fixture." },
      now: Date.UTC(2026, 10, 2, 1, 0, 0),
    });
    assert.equal(patched.action, "patch");
    assert.equal(patched.version, "0.2.0");
    assert.equal(patched.origin_agent, "did:im2a:agent:origin");
    assert.equal(patched.entry.prev_hash, created.entry.entry_hash);
    assert.equal(contributionEntryHash(patched.entry), patched.entry.entry_hash);
    assert.equal(patched.chain.ok, true);
    assert.equal(patched.chain.count, 2);
    assert.equal(patched.patch_depth, 1);
    const log = await readFile(patched.contribution_log_path, "utf8");
    const entries = log.trim().split("\n").map((line) => JSON.parse(line));
    assert.equal(entries.length, 2);
    assert.equal(entries[1].version_before, "0.1.0");
    assert.equal(entries[1].version_after, "0.2.0");
    assert.equal(verifyContributionChain(entries).ok, true);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
