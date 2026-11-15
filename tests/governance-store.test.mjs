import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { GovernanceStore, GOVERNANCE_FILE_NAME } from "../packages/im2a-http-mcp/governance-store.mjs";
import { ScopeGovernanceError } from "../packages/dsh-im2a-plugin/kernel/scope-governance.mjs";

const AGENT = "did:im2a:agent-gov";
const clock = () => new Date("2026-11-15T00:00:00Z");

async function withStore(run) {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-gov-"));
  try {
    return await run(dir);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

test("propose records a pending proposal and grants nothing", async () => {
  await withStore(async (dir) => {
    const store = new GovernanceStore({ dir, clock });
    const proposal = await store.propose({
      agentDid: AGENT,
      scopes: ["skill:write"],
      rationale: "needs write access for the release task",
    });
    assert.match(proposal.proposal_id, /^scp_[0-9a-f]{24}$/);
    assert.equal(proposal.status, "pending");

    const status = await store.status(AGENT);
    assert.equal(status.proposals.length, 1);
    assert.deepEqual(status.effective_scopes, [], "a proposal must not grant scope");
    assert.match(status.decisions, /outside this MCP surface/);
  });
});

test("a proposal survives a bridge restart", async () => {
  await withStore(async (dir) => {
    const first = new GovernanceStore({ dir, clock });
    const created = await first.propose({
      agentDid: AGENT,
      scopes: ["memory:read"],
      rationale: "needs recall access across sessions",
    });

    const restarted = new GovernanceStore({ dir, clock });
    const status = await restarted.status(AGENT);
    assert.equal(status.proposals.length, 1);
    assert.equal(status.proposals[0].proposal_id, created.proposal_id);
    assert.deepEqual(status.proposals[0].requested_scopes, ["memory:read"]);
  });
});

test("a physical scope is refused before anything reaches disk", async () => {
  await withStore(async (dir) => {
    const store = new GovernanceStore({ dir, clock });
    await assert.rejects(
      store.propose({
        agentDid: AGENT,
        scopes: ["physical:motion"],
        rationale: "please let me move the arm",
      }),
      (error) => error instanceof ScopeGovernanceError && error.code === "physical_scope_requires_human_grant",
    );
    const status = await store.status(AGENT);
    assert.deepEqual(status.proposals, []);
    await assert.rejects(
      readFile(path.join(dir, GOVERNANCE_FILE_NAME), "utf8"),
      (error) => error.code === "ENOENT",
    );
  });
});

test("the proposal log is append-only, one JSON line per proposal", async () => {
  await withStore(async (dir) => {
    const store = new GovernanceStore({ dir, clock });
    await store.propose({ agentDid: AGENT, scopes: ["skill:write"], rationale: "first proposal reason" });
    await store.propose({ agentDid: AGENT, scopes: ["memory:read"], rationale: "second proposal reason" });

    const raw = await readFile(path.join(dir, GOVERNANCE_FILE_NAME), "utf8");
    const lines = raw.split("\n").filter((line) => line.trim().length > 0);
    assert.equal(lines.length, 2);
    for (const line of lines) {
      const parsed = JSON.parse(line);
      assert.match(parsed.proposal_id, /^scp_[0-9a-f]{24}$/);
      assert.equal(parsed.status, "pending");
    }
  });
});

test("status without an agent lists every proposal", async () => {
  await withStore(async (dir) => {
    const store = new GovernanceStore({ dir, clock });
    await store.propose({ agentDid: "did:im2a:a", scopes: ["skill:write"], rationale: "reason for agent a" });
    await store.propose({ agentDid: "did:im2a:b", scopes: ["memory:read"], rationale: "reason for agent b" });

    const all = await store.status(null);
    assert.equal(all.proposals.length, 2);
    assert.deepEqual(all.effective_scopes, []);

    const scoped = await store.status("did:im2a:a");
    assert.equal(scoped.proposals.length, 1);
    assert.equal(scoped.proposals[0].agent_did, "did:im2a:a");
  });
});
