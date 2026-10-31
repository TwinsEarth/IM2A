import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { SqliteSkillStore } from "../packages/skill-runtime/src/sqlite-store.mjs";

const DAY = 24 * 60 * 60 * 1000;
const base = Date.UTC(2026, 9, 31, 12, 0, 0);
let clock = base;

async function withStore(fn) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-curator-audit-"));
  const store = new SqliteSkillStore({
    path: path.join(dir, "skills.db"),
    evalFloor: 0,
    now: () => clock,
  });
  try {
    return await fn(store);
  } finally {
    store.close();
    await rm(dir, { recursive: true, force: true });
  }
}

function register(store, id, description, at) {
  clock = at;
  return store.registerPackage({
    id,
    version: "1.0.0",
    description,
    tags: ["test"],
    files: { "SKILL.md": `# ${id}\n\n${description}` },
  });
}

test("curator_audit reports the 5+ skill lifecycle distribution and L0 cost", async () => {
  await withStore(async (store) => {
    register(store, "active-skill", "route customer refund requests safely", base - 1 * DAY);
    register(store, "stale-skill", "summarize weekly deployment reports", base - 40 * DAY);
    register(store, "archive-skill", "convert old invoices into archived ledgers", base - 100 * DAY);
    register(store, "archived-skill", "legacy vendor onboarding checklist", base - 50 * DAY);
    register(store, "merge-a", "triage production incident alerts with severity rubric", base - 20 * DAY);
    register(store, "merge-b", "triage production incident alerts with severity rubric", base - 20 * DAY);

    const activeSha = store.activeSha("archived-skill");
    clock = base - 49 * DAY;
    const archiveProposal = store.manage({
      action: "archive",
      id: "archived-skill",
      baseSha: activeSha,
      content: "# archived-skill\n\narchived body",
      proposer: "curator-agent",
    });
    store.stage(archiveProposal.proposalId, { evidence: ["operator-request"], evalCases: ["eval-1"] });
    store.promote({
      proposalId: archiveProposal.proposalId,
      approvalId: "approval-1",
      expectedActiveSha: activeSha,
      approvedBy: "operator",
    });

    store.recordUsage("active-skill", { agentRef: "did:im2a:owner", at: base - 5 * DAY });
    store.recordUsage("stale-skill", { at: base - 35 * DAY });
    store.setOwner("active-skill", "did:im2a:owner");

    const patchBase = store.activeSha("active-skill");
    store.manage({
      action: "patch",
      id: "active-skill",
      baseSha: patchBase,
      content: "# active-skill\n\npatched body",
      proposer: "agent",
    });

    const before = {
      active: store.list().map((item) => item.id).sort(),
      proposals: store.db.prepare("SELECT COUNT(*) AS n FROM proposals").get().n,
      ledger: store.db.prepare("SELECT COUNT(*) AS n FROM ledger").get().n,
    };
    const result = store.curatorAudit({ scope: "network", now: base });
    const after = {
      active: store.list().map((item) => item.id).sort(),
      proposals: store.db.prepare("SELECT COUNT(*) AS n FROM proposals").get().n,
      ledger: store.db.prepare("SELECT COUNT(*) AS n FROM ledger").get().n,
    };
    assert.deepEqual(after, before, "audit must not write active/proposals/ledger");

    const states = Object.fromEntries(result.skills.map((skill) => [skill.skill_id, skill.lifecycle_state]));
    assert.equal(result.skills.length, 6);
    assert.equal(states["active-skill"], "active");
    assert.equal(states["stale-skill"], "stale");
    assert.equal(states["archive-skill"], "stale");
    assert.equal(states["archived-skill"], "archived");
    assert.equal(states["merge-a"], "active");
    assert.equal(states["merge-b"], "active");

    const byId = Object.fromEntries(result.skills.map((skill) => [skill.skill_id, skill]));
    assert.equal(byId["active-skill"].use_count_30d, 1);
    assert.equal(byId["stale-skill"].use_count_30d, 0);
    assert.equal(byId["active-skill"].patch_count, 1);
    assert.equal(byId["archive-skill"].recommended_action, "archive_candidate");
    assert.equal(byId["stale-skill"].recommended_action, "flag_for_review");
    assert.equal(byId["merge-a"].recommended_action, "merge_candidate");
    assert.equal(byId["merge-b"].recommended_action, "merge_candidate");
    assert.equal(byId["archived-skill"].recommended_action, "keep");

    for (const item of store.list()) {
      const actual = Math.ceil(Buffer.byteLength(JSON.stringify(item), "utf8") / 4);
      const estimate = byId[item.id].estimated_context_cost_tokens;
      assert.ok(Math.abs(actual - estimate) / actual <= 0.10, `${item.id}: estimate=${estimate} actual=${actual}`);
    }

    const owned = store.curatorAudit({ scope: "agent_owned", agentDid: "did:im2a:owner", now: base });
    assert.deepEqual(owned.skills.map((skill) => skill.skill_id), ["active-skill"]);

    const recent = store.curatorAudit({ scope: "network", since: new Date(base - 7 * DAY).toISOString(), now: base });
    assert.deepEqual(recent.skills.map((skill) => skill.skill_id), ["active-skill"]);
  });
});

test("curator_audit fails closed on invalid scope/since", async () => {
  await withStore(async (store) => {
    register(store, "only-skill", "a single test skill", base - DAY);
    assert.throws(() => store.curatorAudit({ scope: "agent_owned" }), (error) => error.code === "AGENT_DID_REQUIRED");
    assert.throws(() => store.curatorAudit({ scope: "bogus" }), (error) => error.code === "INVALID_SCOPE");
    assert.throws(() => store.curatorAudit({ scope: "network", since: "not-a-date" }), (error) => error.code === "INVALID_SINCE");
  });
});
