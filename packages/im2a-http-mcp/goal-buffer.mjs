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
    `);
  }

  put({
    goalId = null,
    driveId,
    proposedAt = null,
    intentSummary,
    sourceSignals,
    priorityScore,
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
