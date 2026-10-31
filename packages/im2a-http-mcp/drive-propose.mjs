/**
 * v2.6.1 read-only drive proposal generation.
 *
 * This module reads drives and current signals, matches each drive's
 * description predicate, and returns proposed goals. It never writes the goal
 * buffer and never writes skills, memory or traces.
 */
import { randomBytes } from "node:crypto";

const CURATOR_FIELDS = new Set([
  "scope", "lifecycle_state", "patch_count", "ageDays", "use_count_30d",
  "recommended_action", "estimated_context_cost_tokens",
]);
const MEMORY_FIELDS = new Set(["quality_flag", "layer", "score", "source_trace_id", "confidence"]);
const OPERATORS = ["==", "!=", ">=", "<=", ">", "<"];

export class DriveProposeError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "DriveProposeError";
    this.code = code;
  }
}

function parseValue(raw) {
  const value = raw.trim();
  if ((value.startsWith("\"") && value.endsWith("\"")) || (value.startsWith("'") && value.endsWith("'"))) {
    return value.slice(1, -1);
  }
  if (/^-?\d+(?:\.\d+)?$/.test(value)) return Number(value);
  return value;
}

export function parseDrivePredicate(description) {
  if (typeof description !== "string" || description.trim() === "") throw new DriveProposeError("DRIVE_INVALID", "description is required");
  const pieces = description.split(/\s+AND\s+/i);
  const predicates = [];
  for (const piece of pieces) {
    const text = piece.trim();
    const match = /^([A-Za-z_][A-Za-z0-9_]*)\s*(==|!=|>=|<=|>|<)\s*(.+)$/.exec(text);
    if (!match) throw new DriveProposeError("DRIVE_INVALID", `cannot parse drive condition: ${text}`);
    const [, field, operator, rawValue] = match;
    if (!CURATOR_FIELDS.has(field) && !MEMORY_FIELDS.has(field)) {
      throw new DriveProposeError("DRIVE_INVALID", `unsupported drive field: ${field}`);
    }
    predicates.push({ field, operator, value: parseValue(rawValue) });
  }
  return predicates;
}

function compare(actual, operator, expected) {
  if (operator === "==") return actual === expected || String(actual) === String(expected);
  if (operator === "!=") return !(actual === expected || String(actual) === String(expected));
  const left = Number(actual);
  const right = Number(expected);
  if (!Number.isFinite(left) || !Number.isFinite(right)) return false;
  if (operator === ">=") return left >= right;
  if (operator === "<=") return left <= right;
  if (operator === ">") return left > right;
  if (operator === "<") return left < right;
  return false;
}

export function matchesDrive(predicates, signal, drive) {
  return predicates.every(({ field, operator, value }) => {
    if (field === "scope") return compare(drive.scope, operator, value);
    return compare(signal[field], operator, value);
  });
}

function routeDrive(drive) {
  const text = `${drive.description} ${drive.trigger_hint}`;
  const memory = /memory_recall|quality_flag|low_confidence|conflicted|source_trace_id/.test(text);
  const curator = /curator_audit|lifecycle_state|patch_count|ageDays|recommended_action|use_count_30d/.test(text);
  if (memory && !curator) return "memory";
  return "curator";
}

function confidenceFor(drive, urgencyHint, signal) {
  const urgency = String(urgencyHint ?? "").toLowerCase();
  const boost = /urgent|high|critical/.test(urgency) ? 0.08 : /low/.test(urgency) ? -0.05 : 0;
  const staleBoost = signal.lifecycle_state === "stale" ? 0.05 : 0;
  return Math.round(Math.min(0.95, Math.max(0.1, 0.55 + drive.priority * 0.3 + boost + staleBoost)) * 100) / 100;
}

function curatorIntent(skill, drive) {
  return `patch skill:${skill.skill_id} because drive ${drive.drive_id} matched ${skill.lifecycle_state} lifecycle with ${skill.patch_count} patches and ${skill.use_count_30d} uses in 30d.`;
}

function memoryIntent(itemId, item, drive) {
  return `review memory item ${itemId} because drive ${drive.drive_id} matched ${item.quality_flag} at layer ${item.layer} with score ${item.score}.`;
}

function memoryQuery(drive) {
  const text = String(drive.trigger_hint ?? "")
    .replace(/[A-Za-z_][A-Za-z0-9_.]*\s*(?:==|!=|>=|<=|>|<)/g, " ")
    .replace(/["']/g, " ")
    .replace(/[^A-Za-z0-9_\u4e00-\u9fff\s]/g, " ");
  const words = text.split(/\s+/).filter((word) => word.length > 2 && !["memory", "recall", "curator", "audit", "quality", "flag", "low", "confidence", "conflicted", "stale", "score", "layer"].includes(word.toLowerCase()));
  return words.slice(0, 4).join(" ") || "memory";
}

/**
 * @param {object} input
 * @param {object[]} input.drives loaded drive declarations
 * @param {string[]} input.driveIds requested drive ids
 * @param {string} input.agentDid caller DID
 * @param {string} [input.urgencyHint]
 * @param {object} input.skillStore read-only curator_audit provider
 * @param {object} input.memoryHub read-only memory_recall provider
 * @param {number|null} input.estimatedTokenCost median recall budget history
 * @param {number} [input.now]
 */
export function drivePropose({
  drives,
  driveIds,
  agentDid,
  urgencyHint,
  skillStore,
  memoryHub,
  estimatedTokenCost,
  now = Date.now(),
}) {
  if (!Array.isArray(driveIds) || driveIds.length === 0 || driveIds.some((id) => typeof id !== "string")) {
    throw new DriveProposeError("DRIVE_INVALID", "drive_ids must be a non-empty string array");
  }
  const uniqueIds = [...new Set(driveIds)];
  const selected = uniqueIds.map((id) => {
    const drive = drives.find((candidate) => candidate.drive_id === id);
    if (!drive) throw new DriveProposeError("DRIVE_NOT_FOUND", `drive not found: ${id}`);
    return drive;
  });
  const tokenCost = Number.isInteger(estimatedTokenCost) && estimatedTokenCost > 0 ? estimatedTokenCost : null;
  const proposed = [];
  const evaluated = [];
  for (const drive of selected) {
    const predicates = parseDrivePredicate(drive.description);
    const route = routeDrive(drive);
    evaluated.push({ drive_id: drive.drive_id, route, matched: 0 });
    if (route === "curator") {
      const audit = skillStore.curatorAudit({ agentDid, scope: "agent_owned", now });
      for (const skill of audit.skills) {
        const lastUsed = Date.parse(skill.last_used_at);
        const signal = {
          scope: drive.scope,
          lifecycle_state: skill.lifecycle_state,
          patch_count: skill.patch_count,
          ageDays: Number.isFinite(lastUsed) ? Math.max(0, (now - lastUsed) / (24 * 60 * 60 * 1000)) : 0,
          use_count_30d: skill.use_count_30d,
          recommended_action: skill.recommended_action,
          estimated_context_cost_tokens: skill.estimated_context_cost_tokens,
          skill_id: skill.skill_id,
        };
        if (!matchesDrive(predicates, signal, drive)) continue;
        const sourceSignals = [{ type: "skill", id: skill.skill_id }];
        proposed.push({
          goal_id: `goal_${randomBytes(12).toString("hex")}`,
          drive_id: drive.drive_id,
          intent_summary: curatorIntent(skill, drive),
          source_signals: sourceSignals,
          estimated_token_cost: tokenCost ?? Math.max(1, Math.ceil(Buffer.byteLength(curatorIntent(skill, drive), "utf8") / 4)),
          confidence: confidenceFor(drive, urgencyHint, signal),
        });
        evaluated[evaluated.length - 1].matched += 1;
      }
    } else {
      const query = memoryQuery(drive);
      const recalled = memoryHub.recall({
        query,
        agentDid,
        budgetTokens: tokenCost ?? 2000,
        includeStale: true,
      });
      for (const item of recalled.items) {
        const itemId = item.source_trace_id ?? `mem_${randomBytes(8).toString("hex")}`;
        const signal = {
          quality_flag: item.quality_flag,
          layer: item.layer,
          score: item.score,
          source_trace_id: item.source_trace_id,
          confidence: item.score,
        };
        if (!matchesDrive(predicates, signal, drive)) continue;
        proposed.push({
          goal_id: `goal_${randomBytes(12).toString("hex")}`,
          drive_id: drive.drive_id,
          intent_summary: memoryIntent(itemId, item, drive),
          source_signals: [{ type: "memory_item", id: itemId }],
          estimated_token_cost: tokenCost ?? Math.max(1, recalled.budget_used || Math.ceil(Buffer.byteLength(item.content, "utf8") / 4)),
          confidence: confidenceFor(drive, urgencyHint, signal),
        });
        evaluated[evaluated.length - 1].matched += 1;
      }
    }
  }
  return {
    proposed_goals: proposed.slice(0, 20),
    evaluated_drives: evaluated,
    estimated_token_cost: tokenCost,
    token_cost_basis: tokenCost === null ? "no_recall_history_fallback" : "recall_history_median",
    generated_at: new Date(now).toISOString(),
    agent_did: agentDid,
  };
}
