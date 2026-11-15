/**
 * WP-2 of the six-layer plan: L6 resource metering.
 *
 * Two responsibilities, deliberately kept together because they are the same
 * economic decision seen from two ends:
 *   1. one immutable record per consumption event (L6 -> L2 contract);
 *   2. the marginal budget split that decides how much of that spend is reserved
 *      for self-improvement and maintenance instead of external work.
 *
 * The 10% reserved floor is institutional (C2), not a tunable efficiency knob:
 * a split that lets external work push reserved below 10% of itself is rejected,
 * not clamped.
 */
import { randomBytes } from "node:crypto";

export const METERING_VERSION = "1.0";
export const RESERVED_FLOOR_RATIO = 0.1;
const EPSILON = 1e-9;

export const LAYERS = Object.freeze(["compute", "storage", "network", "bandwidth", "data", "account"]);
export const UNITS = Object.freeze([
  "tokens",
  "gpu_seconds",
  "bytes",
  "requests",
  "bytes_per_second",
  "records",
  "calls",
]);
export const PURPOSES = Object.freeze(["external", "self_improvement", "maintenance"]);
export const CURRENCIES = Object.freeze(["USD", "CNY", "CREDIT"]);

export class MeterError extends Error {
  constructor(code, message, detail = {}) {
    super(message);
    this.name = "MeterError";
    this.code = code;
    this.detail = detail;
  }
}

function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Reserved work is internal by definition; only external work can be billed. */
export function purposePool(purpose) {
  if (!PURPOSES.includes(purpose)) {
    throw new MeterError("invalid_purpose", `unknown purpose ${purpose}`, { purpose });
  }
  return purpose === "external" ? "external_pool" : "reserved_pool";
}

/**
 * Split a token budget into the three pools the floor is defined on, rejecting
 * any split that pushes reserved work below 10% of external work.
 */
export function splitMarginalBudget(total, shares = {}) {
  if (!Number.isFinite(total) || total < 0) {
    throw new MeterError("invalid_budget", "total must be a non-negative number", { total });
  }
  const external = shares.external ?? 0;
  const selfImprovement = shares.self_improvement ?? 0;
  const maintenance = shares.maintenance ?? 0;
  for (const [name, share] of Object.entries({ external, self_improvement: selfImprovement, maintenance })) {
    if (!Number.isFinite(share) || share < 0) {
      throw new MeterError("invalid_share", `${name} share must be a non-negative number`, { name, share });
    }
  }
  const sum = external + selfImprovement + maintenance;
  if (Math.abs(sum - 1) > EPSILON) {
    throw new MeterError("shares_must_sum_to_one", `shares sum to ${sum}`, { external, self_improvement: selfImprovement, maintenance });
  }
  const reserved = selfImprovement + maintenance;
  if (reserved + EPSILON < RESERVED_FLOOR_RATIO * external) {
    throw new MeterError(
      "reserved_floor_violated",
      `reserved share ${reserved} is below ${RESERVED_FLOOR_RATIO} x external ${external}`,
      { external, reserved, floor_ratio: RESERVED_FLOOR_RATIO },
    );
  }
  return {
    total,
    external_pool: Number((total * external).toFixed(8)),
    reserved_pool: Number((total * reserved).toFixed(8)),
    self_improvement_pool: Number((total * selfImprovement).toFixed(8)),
    maintenance_pool: Number((total * maintenance).toFixed(8)),
    reserved_floor_ratio: RESERVED_FLOOR_RATIO,
  };
}

/**
 * Structural + semantic checks mirroring resource-metering-record-1.0. JSON
 * Schema is enforced by the layer contract gate; this keeps a bad record out of
 * the economy module even when it arrives from code rather than a file.
 */
export function validateMeteringRecord(record) {
  const errors = [];
  if (!isPlainObject(record)) return ["record must be an object"];
  if (record.schema_version !== "1.0") errors.push("schema_version must be 1.0");
  if (record.meter_version !== "1.0") errors.push("meter_version must be 1.0");
  if (typeof record.record_id !== "string" || !/^meter_[0-9a-f]{24}$/.test(record.record_id)) {
    errors.push("record_id must match meter_<24 hex>");
  }
  if (typeof record.at !== "string" || Number.isNaN(Date.parse(record.at))) errors.push("at must be a timestamp");
  if (typeof record.agent_did !== "string" || record.agent_did.length === 0) errors.push("agent_did is required");
  if (!LAYERS.includes(record.layer)) errors.push(`unknown layer ${record.layer}`);

  const resource = record.resource;
  if (!isPlainObject(resource)) {
    errors.push("resource is required");
  } else {
    if (typeof resource.kind !== "string" || resource.kind.length === 0) errors.push("resource.kind is required");
    if (!Number.isFinite(resource.quantity) || resource.quantity < 0) errors.push("resource.quantity must be a non-negative number");
    if (!UNITS.includes(resource.unit)) errors.push(`unknown unit ${resource.unit}`);
  }

  const task = record.task;
  if (!isPlainObject(task)) {
    errors.push("task is required");
  } else if (!PURPOSES.includes(task.purpose)) {
    errors.push(`unknown purpose ${task.purpose}`);
  }

  const cost = record.cost;
  if (!isPlainObject(cost)) {
    errors.push("cost is required");
  } else {
    if (!Number.isFinite(cost.amount) || cost.amount < 0) errors.push("cost.amount must be a non-negative number");
    if (!CURRENCIES.includes(cost.currency)) errors.push(`unknown currency ${cost.currency}`);
  }

  if (typeof record.billable !== "boolean") errors.push("billable must be a boolean");

  if (errors.length === 0) {
    // Billing implies real cost, but the converse is false: internal work can
    // consume a large amount and still be non-billable.
    if (record.billable && !(cost.amount > 0)) errors.push("billable must match cost.amount > 0");
    if (task.purpose !== "external" && record.billable) errors.push("internal purpose must not be billable");
  }

  return errors;
}

function newRecordId() {
  return `meter_${randomBytes(12).toString("hex")}`;
}

export function createResourceMeter({ agentDid, providerId, region, clock = () => new Date() } = {}) {
  if (typeof agentDid !== "string" || agentDid.length === 0) {
    throw new MeterError("invalid_agent", "agentDid is required");
  }
  const records = [];

  return {
    version: METERING_VERSION,

    /** Record one consumption event. Billing is derived, never asserted by the caller. */
    record({ layer, resource, task, cost } = {}) {
      if (!isPlainObject(task) || !PURPOSES.includes(task.purpose)) {
        throw new MeterError("invalid_purpose", "task.purpose must be one of the three purposes", { purpose: task?.purpose });
      }
      const billable = task.purpose === "external" && Number(cost?.amount) > 0;
      const record = {
        schema_version: "1.0",
        meter_version: METERING_VERSION,
        record_id: newRecordId(),
        at: new Date(clock()).toISOString(),
        agent_did: agentDid,
        layer,
        resource,
        ...(providerId ? { provider: { provider_id: providerId, ...(region ? { region } : {}) } } : {}),
        task,
        cost,
        billable,
      };
      const errors = validateMeteringRecord(record);
      if (errors.length) {
        throw new MeterError("invalid_record", errors.join("; "), { errors });
      }
      records.push(Object.freeze(structuredClone(record)));
      return record;
    },

    records() {
      return records.map((record) => structuredClone(record));
    },

    /** Sum of billable cost only; internal work is tracked but never charged. */
    totalBillableCost(currency = "USD") {
      return records
        .filter((record) => record.billable && record.cost.currency === currency)
        .reduce((sum, record) => sum + record.cost.amount, 0);
    },

    /** Cost grouped by pool, so the reserved floor can be audited after the fact. */
    costByPool(currency = "USD") {
      const pools = { external_pool: 0, reserved_pool: 0 };
      for (const record of records) {
        if (record.cost.currency !== currency) continue;
        pools[purposePool(record.task.purpose)] += record.cost.amount;
      }
      return pools;
    },
  };
}
