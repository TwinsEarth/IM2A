/**
 * v2.6.0 local goal buffer.
 *
 * This SQLite database is deliberately separate from Memory Hub: unvalidated
 * intentions must not become network-level memory. The buffer only stores
 * intent objects and their status transitions.
 */
import { randomBytes } from "node:crypto";
import { DatabaseSync } from "node:sqlite";

export const GOAL_STATUSES = Object.freeze(["pending", "scheduled", "executing", "completed", "expired"]);
export const GOAL_TRANSITIONS = Object.freeze({
  pending: ["scheduled", "expired"],
  scheduled: ["executing", "expired"],
  executing: ["completed", "expired"],
  completed: [],
  expired: [],
});

export class GoalBufferError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "GoalBufferError";
    this.code = code;
  }
}

const SIGNAL_TYPES = new Set(["skill", "memory_item"]);
const ID_RE = /^[A-Za-z0-9_.:-]{1,200}$/;

function nowIso(now) { return new Date(now).toISOString(); }

function assertSignal(signal) {
  if (!signal || typeof signal !== "object" || Array.isArray(signal)) throw new GoalBufferError("GOAL_INVALID", "source_signal must be an object");
  if (!SIGNAL_TYPES.has(signal.type)) throw new GoalBufferError("GOAL_INVALID", "source_signal.type must be skill or memory_item");
  if (typeof signal.id !== "string" || !ID_RE.test(signal.id)) throw new GoalBufferError("GOAL_INVALID", "source_signal.id is invalid");
  return { type: signal.type, id: signal.id };
}

export class GoalBuffer {
  constructor({ path, now = () => Date.now() } = {}) {
    if (typeof path !== "string" || path.length === 0) throw new TypeError("path is required");
    this.now = now;
    this.db = new DatabaseSync(path);
    this.db.exec(`
      PRAGMA foreign_keys = ON;
      CREATE TABLE IF NOT EXISTS goals (
        goal_id TEXT PRIMARY KEY,
        drive_id TEXT NOT NULL,
        proposed_at TEXT NOT NULL,
        intent_summary TEXT NOT NULL,
        source_signals_json TEXT NOT NULL,
        priority_score REAL NOT NULL,
        status TEXT NOT NULL,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS goal_events (
        event_id TEXT PRIMARY KEY,
        goal_id TEXT NOT NULL,
        from_status TEXT,
        to_status TEXT NOT NULL,
        acted_at TEXT NOT NULL,
        actor TEXT NOT NULL,
        reason TEXT
      );
      CREATE TABLE IF NOT EXISTS recall_history (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        at TEXT NOT NULL,
        budget_used INTEGER NOT NULL,
        budget_tokens INTEGER NOT NULL,
        item_count INTEGER NOT NULL,
        suppressed_count INTEGER NOT NULL,
        agent_did TEXT,
        query_hash TEXT
      );
      CREATE TABLE IF NOT EXISTS goal_proposals (
        proposal_goal_id TEXT PRIMARY KEY,
        drive_id TEXT NOT NULL,
        proposed_at TEXT NOT NULL,
        intent_summary TEXT NOT NULL,
        source_signals_json TEXT NOT NULL,
        adopted_goal_id TEXT
      );
      CREATE TABLE IF NOT EXISTS goal_executions (
        execution_id TEXT PRIMARY KEY,
        goal_id TEXT NOT NULL,
        drive_id TEXT NOT NULL,
        at TEXT NOT NULL,
        status TEXT NOT NULL,
        confidence_delta REAL NOT NULL,
        draft_patch_json TEXT,
        reflection_notes TEXT,
        recommended_next TEXT,
        execution_yield INTEGER NOT NULL
      );
    `);
  }

  put({
    goalId = null,
    driveId,
    proposedAt = null,
    intentSummary,
    sourceSignals,
    priorityScore,
    proposalId = null,
  } = {}) {
    if (typeof driveId !== "string" || driveId.length === 0) throw new GoalBufferError("GOAL_INVALID", "drive_id is required");
    if (typeof intentSummary !== "string" || intentSummary.length === 0 || intentSummary.length > 2000) throw new GoalBufferError("GOAL_INVALID", "intent_summary is required (max 2000)");
    if (!Array.isArray(sourceSignals) || sourceSignals.length === 0) throw new GoalBufferError("GOAL_INVALID", "source_signals must be a non-empty array");
    const signals = sourceSignals.map(assertSignal);
    const priority = Number(priorityScore);
    if (!Number.isFinite(priority) || priority < 0 || priority > 1) throw new GoalBufferError("GOAL_INVALID", "priority_score must be 0..1");
    const at = nowIso(this.now());
    const goalIdValue = goalId ?? `goal_${randomBytes(12).toString("hex")}`;
    if (!/^goal_[0-9a-f]{24}$/.test(goalIdValue)) throw new GoalBufferError("GOAL_INVALID", "goal_id must be goal_<24 hex>");
    try {
      this.db.exec("BEGIN IMMEDIATE");
      this.db.prepare(`INSERT INTO goals
        (goal_id, drive_id, proposed_at, intent_summary, source_signals_json, priority_score, status, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`)
        .run(goalIdValue, driveId, proposedAt ?? at, intentSummary, JSON.stringify(signals), priority, at, at);
      this.db.prepare(`INSERT INTO goal_events
        (event_id, goal_id, from_status, to_status, acted_at, actor, reason)
        VALUES (?, ?, NULL, 'pending', ?, 'system', 'created')`)
        .run(`gevt_${randomBytes(12).toString("hex")}`, goalIdValue, at);
      if (proposalId) {
        if (!/^goal_[0-9a-f]{24}$/.test(String(proposalId))) throw new GoalBufferError("GOAL_INVALID", "proposal_id must be goal_<24 hex>");
        this.db.prepare("UPDATE goal_proposals SET adopted_goal_id = ? WHERE proposal_goal_id = ?").run(goalIdValue, proposalId);
      }
      this.db.exec("COMMIT");
    } catch (error) {
      try { this.db.exec("ROLLBACK"); } catch { /* best effort */ }
      if (String(error.message).includes("UNIQUE")) throw new GoalBufferError("GOAL_EXISTS");
      throw error;
    }
    return this.get(goalIdValue);
  }

  get(goalId) {
    const row = this.db.prepare("SELECT * FROM goals WHERE goal_id = ?").get(goalId);
    return row ? this.#goal(row) : null;
  }

  transition(goalId, toStatus, { actor = "operator", reason = null } = {}) {
    const goal = this.get(goalId);
    if (!goal) throw new GoalBufferError("GOAL_NOT_FOUND");
    if (!GOAL_STATUSES.includes(toStatus)) throw new GoalBufferError("GOAL_INVALID", "invalid status");
    if (!GOAL_TRANSITIONS[goal.status].includes(toStatus)) {
      throw new GoalBufferError("INVALID_STATUS_TRANSITION", `${goal.status} -> ${toStatus} is not allowed`);
    }
    const at = nowIso(this.now());
    this.db.prepare("UPDATE goals SET status = ?, updated_at = ? WHERE goal_id = ?").run(toStatus, at, goalId);
    this.db.prepare(`INSERT INTO goal_events
      (event_id, goal_id, from_status, to_status, acted_at, actor, reason)
      VALUES (?, ?, ?, ?, ?, ?, ?)`)
      .run(`gevt_${randomBytes(12).toString("hex")}`, goalId, goal.status, toStatus, at, String(actor).slice(0, 200), reason ? String(reason).slice(0, 500) : null);
    return this.get(goalId);
  }

  nextPending() {
    const row = this.db.prepare("SELECT * FROM goals WHERE status = 'pending' ORDER BY priority_score DESC, proposed_at ASC LIMIT 1").get();
    return row ? this.#goal(row) : null;
  }

  query({ status = null, driveId = null, limit = 100 } = {}) {
    const clauses = [];
    const params = [];
    if (status) {
      if (!GOAL_STATUSES.includes(status)) throw new GoalBufferError("GOAL_INVALID", "invalid status");
      clauses.push("status = ?");
      params.push(status);
    }
    if (driveId) {
      clauses.push("drive_id = ?");
      params.push(driveId);
    }
    const sql = `SELECT * FROM goals ${clauses.length ? `WHERE ${clauses.join(" AND ")}` : ""} ORDER BY proposed_at DESC LIMIT ?`;
    params.push(Math.min(Math.max(Number(limit) || 100, 1), 500));
    const goals = this.db.prepare(sql).all(...params).map((row) => this.#goal(row));
    return { goals, count: goals.length };
  }

  audit(goalId = null) {
    const rows = goalId
      ? this.db.prepare("SELECT * FROM goal_events WHERE goal_id = ? ORDER BY rowid ASC").all(goalId)
      : this.db.prepare("SELECT * FROM goal_events ORDER BY rowid ASC LIMIT 1000").all();
    return {
      events: rows.map((row) => ({
        event_id: row.event_id,
        goal_id: row.goal_id,
        from_status: row.from_status,
        to_status: row.to_status,
        acted_at: row.acted_at,
        actor: row.actor,
        reason: row.reason,
      })),
      count: rows.length,
    };
  }

  recordRecall({ budgetUsed, budgetTokens, itemCount = 0, suppressedCount = 0, agentDid = null, queryHash = null } = {}) {
    for (const [name, value] of [["budget_used", budgetUsed], ["budget_tokens", budgetTokens], ["item_count", itemCount], ["suppressed_count", suppressedCount]]) {
      if (!Number.isInteger(value) || value < 0) throw new GoalBufferError("GOAL_INVALID", `${name} must be a non-negative integer`);
    }
    this.db.prepare(`INSERT INTO recall_history
      (at, budget_used, budget_tokens, item_count, suppressed_count, agent_did, query_hash)
      VALUES (?, ?, ?, ?, ?, ?, ?)`)
      .run(nowIso(this.now()), budgetUsed, budgetTokens, itemCount, suppressedCount, agentDid ? String(agentDid).slice(0, 200) : null, queryHash ? String(queryHash).slice(0, 128) : null);
  }

  medianRecallBudget({ limit = 50 } = {}) {
    const rows = this.db.prepare("SELECT budget_used FROM recall_history ORDER BY id DESC LIMIT ?").all(Math.min(Math.max(Number(limit) || 50, 1), 500));
    if (rows.length === 0) return null;
    const values = rows.map((row) => Number(row.budget_used)).sort((a, b) => a - b);
    const mid = Math.floor(values.length / 2);
    return values.length % 2 ? values[mid] : Math.round((values[mid - 1] + values[mid]) / 2);
  }

  recallHistoryCount() {
    return this.db.prepare("SELECT COUNT(*) AS n FROM recall_history").get().n;
  }

  recordProposal({ proposalGoalId, driveId, intentSummary, sourceSignals = [], at = null } = {}) {
    if (!/^goal_[0-9a-f]{24}$/.test(String(proposalGoalId))) throw new GoalBufferError("GOAL_INVALID", "proposal goal id invalid");
    if (typeof driveId !== "string" || driveId.length === 0) throw new GoalBufferError("GOAL_INVALID", "drive_id required");
    if (typeof intentSummary !== "string" || intentSummary.length === 0) throw new GoalBufferError("GOAL_INVALID", "intent_summary required");
    const signals = sourceSignals.map(assertSignal);
    this.db.prepare(`INSERT OR IGNORE INTO goal_proposals
      (proposal_goal_id, drive_id, proposed_at, intent_summary, source_signals_json, adopted_goal_id)
      VALUES (?, ?, ?, ?, ?, NULL)`)
      .run(proposalGoalId, driveId, at ?? nowIso(this.now()), intentSummary, JSON.stringify(signals));
  }

  recordExecution({
    goalId,
    driveId,
    status,
    confidenceDelta,
    draftPatch = null,
    reflectionNotes = "",
    recommendedNext = null,
    at = null,
  } = {}) {
    if (!/^goal_[0-9a-f]{24}$/.test(String(goalId))) throw new GoalBufferError("GOAL_INVALID", "goal_id invalid");
    if (typeof driveId !== "string" || driveId.length === 0) throw new GoalBufferError("GOAL_INVALID", "drive_id required");
    if (!["completed", "expired"].includes(status)) throw new GoalBufferError("GOAL_INVALID", "execution status must be completed or expired");
    if (!Number.isFinite(confidenceDelta)) throw new GoalBufferError("GOAL_INVALID", "confidence_delta invalid");
    const yieldValue = draftPatch ? 1 : reflectionNotes ? 1 : 0;
    this.db.prepare(`INSERT INTO goal_executions
      (execution_id, goal_id, drive_id, at, status, confidence_delta, draft_patch_json, reflection_notes, recommended_next, execution_yield)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
      .run(
        `gexec_${randomBytes(12).toString("hex")}`,
        goalId,
        driveId,
        at ?? nowIso(this.now()),
        status,
        confidenceDelta,
        draftPatch ? JSON.stringify(draftPatch) : null,
        reflectionNotes ? String(reflectionNotes).slice(0, 4000) : null,
        recommendedNext ? String(recommendedNext) : null,
        yieldValue,
      );
  }

  calibrationSnapshot({ sinceMs } = {}) {
    const since = new Date(Number(sinceMs)).toISOString();
    const goals = this.db.prepare("SELECT * FROM goals WHERE created_at >= ? ORDER BY created_at ASC LIMIT 5000").all(since).map((row) => this.#goal(row));
    const executions = this.db.prepare("SELECT * FROM goal_executions WHERE at >= ? ORDER BY at ASC LIMIT 5000").all(since).map((row) => ({
      execution_id: row.execution_id,
      goal_id: row.goal_id,
      drive_id: row.drive_id,
      at: row.at,
      status: row.status,
      confidence_delta: row.confidence_delta,
      draft_patch: row.draft_patch_json ? JSON.parse(row.draft_patch_json) : null,
      reflection_notes: row.reflection_notes,
      recommended_next: row.recommended_next,
      execution_yield: row.execution_yield === 1,
    }));
    const proposals = this.db.prepare("SELECT * FROM goal_proposals WHERE proposed_at >= ? ORDER BY proposed_at ASC LIMIT 5000").all(since).map((row) => ({
      proposal_goal_id: row.proposal_goal_id,
      drive_id: row.drive_id,
      proposed_at: row.proposed_at,
      intent_summary: row.intent_summary,
      source_signals: JSON.parse(row.source_signals_json),
      adopted_goal_id: row.adopted_goal_id,
    }));
    return { goals, executions, proposals };
  }

  #goal(row) {
    return {
      goal_id: row.goal_id,
      drive_id: row.drive_id,
      proposed_at: row.proposed_at,
      intent_summary: row.intent_summary,
      source_signals: JSON.parse(row.source_signals_json),
      priority_score: row.priority_score,
      status: row.status,
      created_at: row.created_at,
      updated_at: row.updated_at,
    };
  }

  close() { this.db.close(); }
}
