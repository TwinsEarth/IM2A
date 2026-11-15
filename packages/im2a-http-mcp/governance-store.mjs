/**
 * Durable store for scope proposals (WP-6 / six-layer plan).
 *
 * The MCP surface deliberately exposes only two things: an agent may PROPOSE a
 * scope change, and anyone may READ the pending proposals. Human decisions are
 * recorded outside this surface — an MCP tool cannot be trusted with a
 * self-declared "human" principal, because an agent could pass that string.
 *
 * Proposals are append-only JSONL: one line per proposal, replayed into the
 * governance kernel on load. A corrupt line is skipped rather than crashing the
 * bridge, and the file is never rewritten or truncated.
 */
import { mkdir, open, readFile } from "node:fs/promises";
import path from "node:path";
import { createScopeGovernance } from "../dsh-im2a-plugin/kernel/scope-governance.mjs";

export const GOVERNANCE_FILE_NAME = "proposals.jsonl";

export class GovernanceStore {
  #dir;
  #fileName;
  #clock;
  #tail = Promise.resolve();
  #ready = null;
  #governance = null;

  constructor({ dir, fileName = GOVERNANCE_FILE_NAME, clock } = {}) {
    if (typeof dir !== "string" || dir.length === 0) throw new TypeError("dir is required");
    this.#dir = dir;
    this.#fileName = fileName;
    this.#clock = clock;
  }

  get filePath() {
    return path.join(this.#dir, this.#fileName);
  }

  #ensure() {
    if (!this.#ready) this.#ready = this.#load();
    return this.#ready;
  }

  async #load() {
    await mkdir(this.#dir, { recursive: true, mode: 0o750 });
    let raw = "";
    try {
      raw = await readFile(this.filePath, "utf8");
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
    }
    const seeded = [];
    for (const line of raw.split("\n")) {
      if (!line.trim()) continue;
      try {
        const proposal = JSON.parse(line);
        if (proposal && typeof proposal.proposal_id === "string") seeded.push(proposal);
      } catch {
        // A torn or hand-edited line is ignored; append-only history stays intact.
      }
    }
    this.#governance = createScopeGovernance({ seedProposals: seeded, clock: this.#clock });
    return this.#governance;
  }

  /** Agent-initiated path. Returns a pending proposal; never a grant. */
  async propose(input) {
    const governance = await this.#ensure();
    const proposal = governance.proposeScopeChange(input);
    const run = this.#tail.then(() => this.#appendLine(`${JSON.stringify(proposal)}\n`));
    this.#tail = run.catch(() => {});
    await run;
    return proposal;
  }

  /** Read-only: pending/decided proposals plus the scope the agent already holds. */
  async status(agentDid) {
    const governance = await this.#ensure();
    return {
      proposals: governance.listProposals(agentDid),
      effective_scopes: agentDid ? governance.effectiveScopes(agentDid) : [],
      decisions: "human decisions are recorded outside this MCP surface",
    };
  }

  async #appendLine(line) {
    await mkdir(this.#dir, { recursive: true, mode: 0o750 });
    const handle = await open(this.filePath, "a", 0o640);
    try {
      await handle.writeFile(line, "utf8");
      await handle.sync();
    } finally {
      await handle.close();
    }
  }
}
