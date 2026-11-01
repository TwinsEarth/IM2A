import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  IM2A_METADATA_KEYS,
  assertNoAgentMetadataEdit,
  claimSkill,
  parseSkillMarkdown,
} from "../packages/im2a-http-mcp/skill-metadata.mjs";

const goalId = "goal_0123456789abcdef01234567";
const draftId = "draft_0123456789abcdef01234567";

test("skill claim metadata is standard-client compatible and contribution log is external JSONL", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-skill-metadata-"));
  try {
    const draftPath = path.join(dir, "marker.patch.md");
    const marker = "# candidate skill body\n\nThis is the claimable body.";
    await writeFile(draftPath, marker, "utf8");
    const draftPatch = {
      draft_id: draftId,
      target_skill_id: "metadata-skill",
      path: draftPath,
      sha256: createHash("sha256").update(marker).digest("hex"),
      bytes: Buffer.byteLength(marker),
      applied: false,
    };
    let registered = null;
    const skillStore = {
      list: () => [],
      registerPackage: (input) => {
        registered = input;
        return { packageSha: "a".repeat(64) };
      },
    };
    const claimed = await claimSkill({
      skillStore,
      skillsDir: path.join(dir, "skills"),
      agentDid: "did:im2a:agent:metadata-test",
      goalId,
      draftPatch,
      description: "A skill claimed from a v2.6 autonomy draft.",
      body: marker,
      lineage: ["did:im2a:agent:metadata-test@v1"],
      patchDepth: 0,
      creditAtClaim: 0.72,
    });
    assert.equal(registered.id, "metadata-skill");
    assert.ok(registered.files["SKILL.md"]);
    assert.ok(registered.files[".im2a/contributions.jsonl"]);
    const skillMd = await readFile(claimed.skill_md_path, "utf8");
    const parsed = parseSkillMarkdown(skillMd);
    assert.equal(parsed.name, "metadata-skill");
    assert.equal(parsed.description, "A skill claimed from a v2.6 autonomy draft.");
    assert.match(parsed.body, /candidate skill body/);
    assert.deepEqual(Object.keys(parsed.metadata).sort(), [...IM2A_METADATA_KEYS].sort());
    assert.equal(parsed.metadata["im2a.origin_agent"], "did:im2a:agent:metadata-test");
    assert.equal(parsed.metadata["im2a.origin_credit_at_claim"], "0.72");
    assert.equal(parsed.metadata["im2a.contribution_log_ref"], "skills/metadata-skill/.im2a/contributions.jsonl");
    assert.equal(parsed.metadata["im2a.patch_depth"], "0");
    assert.equal(JSON.parse(parsed.metadata["im2a.skill_lineage"])[0], "did:im2a:agent:metadata-test@v1");

    const logText = await readFile(claimed.contribution_log_path, "utf8");
    const event = JSON.parse(logText.trim());
    assert.equal(event.event_type, "skill_claim");
    assert.equal(event.goal_id, goalId);
    assert.equal(event.draft_id, draftId);
    assert.equal(event.draft_sha256, draftPatch.sha256);

    assert.throws(
      () => assertNoAgentMetadataEdit(skillMd),
      (error) => error.code === "IM2A_METADATA_READ_ONLY",
    );
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
