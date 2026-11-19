import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { validateSchema } from "../scripts/schema-validator.mjs";
import {
  MeterError,
  RESERVED_FLOOR_RATIO,
  createResourceMeter,
  purposePool,
  restoreResourceMeter,
  splitMarginalBudget,
  validateMeteringRecord,
} from "../packages/dsh-im2a-plugin/kernel/meter.mjs";

const schema = JSON.parse(await readFile(
  new URL("../docs/contracts/resource-metering-record-1.0.schema.json", import.meta.url),
  "utf8",
));

const clock = () => new Date("2026-11-15T00:00:00Z");

function newMeter() {
  return createResourceMeter({
    agentDid: "did:im2a:lead",
    providerId: "twinsearth-gpu-01",
    region: "cn-east",
    clock,
  });
}

function computeCall(meter, purpose = "external", amount = 0.0052) {
  return meter.record({
    layer: "compute",
    resource: { kind: "model_inference", quantity: 18_432, unit: "tokens" },
    task: { purpose },
    cost: { amount, currency: "USD" },
  });
}

test("one external compute call produces one billable record", () => {
  const meter = newMeter();
  const record = computeCall(meter);
  assert.equal(meter.records().length, 1);
  assert.equal(record.billable, true);
  assert.equal(record.at, "2026-11-15T00:00:00.000Z");
  assert.match(record.record_id, /^meter_[0-9a-f]{24}$/);
  assert.equal(validateSchema(schema, record).ok, true);
  assert.deepEqual(validateMeteringRecord(record), []);
});

test("internal work is recorded but never billed", () => {
  const meter = newMeter();
  const selfImprovement = computeCall(meter, "self_improvement", 0.01);
  const maintenance = computeCall(meter, "maintenance", 0);
  assert.equal(selfImprovement.billable, false);
  assert.equal(maintenance.billable, false);
  assert.equal(meter.totalBillableCost("USD"), 0);
  assert.deepEqual(meter.costByPool("USD"), { external_pool: 0, reserved_pool: 0.01 });
});

test("billable totals and pools separate external from reserved spend", () => {
  const meter = newMeter();
  computeCall(meter, "external", 0.4);
  computeCall(meter, "external", 0.6);
  computeCall(meter, "self_improvement", 0.05);
  assert.equal(meter.totalBillableCost("USD"), 1);
  assert.deepEqual(meter.costByPool("USD"), { external_pool: 1, reserved_pool: 0.05 });
});

test("bad records are rejected before they can reach the economy module", () => {
  const meter = newMeter();
  assert.throws(
    () => meter.record({
      layer: "compute",
      resource: { kind: "model_inference", quantity: 1, unit: "widgets" },
      task: { purpose: "external" },
      cost: { amount: 0.1, currency: "USD" },
    }),
    (error) => error instanceof MeterError && error.code === "invalid_record",
  );
  assert.throws(
    () => meter.record({
      layer: "quantum",
      resource: { kind: "model_inference", quantity: 1, unit: "tokens" },
      task: { purpose: "external" },
      cost: { amount: 0.1, currency: "USD" },
    }),
    (error) => error.code === "invalid_record",
  );
  assert.equal(meter.records().length, 0);
});

test("the three-purpose split keeps the reserved floor", () => {
  const ok = splitMarginalBudget(1000, { external: 0.9, self_improvement: 0.05, maintenance: 0.05 });
  assert.equal(ok.external_pool, 900);
  assert.equal(ok.reserved_pool, 100);
  assert.equal(ok.reserved_floor_ratio, RESERVED_FLOOR_RATIO);
  assert.equal(ok.external_pool + ok.reserved_pool, 1000);

  // Exactly at the floor: reserved 0.1 >= 0.1 x external 0.9.
  assert.doesNotThrow(() => splitMarginalBudget(1000, { external: 0.9, self_improvement: 0.1 }));

  assert.throws(
    () => splitMarginalBudget(1000, { external: 0.95, self_improvement: 0.025, maintenance: 0.025 }),
    (error) => error instanceof MeterError && error.code === "reserved_floor_violated",
  );
  assert.throws(
    () => splitMarginalBudget(1000, { external: 0.7, self_improvement: 0.2 }),
    (error) => error.code === "shares_must_sum_to_one",
  );
});

test("purpose maps to the pool the floor is audited on", () => {
  assert.equal(purposePool("external"), "external_pool");
  assert.equal(purposePool("self_improvement"), "reserved_pool");
  assert.equal(purposePool("maintenance"), "reserved_pool");
  assert.throws(() => purposePool("billing"), (error) => error.code === "invalid_purpose");
});

test("a snapshot survives a restart and keeps the totals", () => {
  const first = createResourceMeter({ agentDid: "did:im2a:meter", clock });
  first.record({
    layer: "compute",
    resource: { kind: "gpu", quantity: 2, unit: "gpu_seconds" },
    task: { purpose: "external" },
    cost: { amount: 0.5, currency: "USD" },
  });
  first.record({
    layer: "storage",
    resource: { kind: "disk", quantity: 10, unit: "bytes" },
    task: { purpose: "maintenance" },
    cost: { amount: 0.25, currency: "USD" },
  });

  const snapshot = first.snapshot();
  assert.equal(snapshot.record_count, 2);
  assert.equal(snapshot.agent_did, "did:im2a:meter");

  const restarted = restoreResourceMeter(JSON.parse(JSON.stringify(snapshot)), { clock });
  assert.equal(restarted.records().length, 2);
  assert.equal(restarted.totalBillableCost("USD"), 0.5, "internal work is still not billable");
  assert.deepEqual(restarted.costByPool("USD"), { external_pool: 0.5, reserved_pool: 0.25 });
});

test("a tampered snapshot is rejected instead of skewing totals", () => {
  const meter = createResourceMeter({ agentDid: "did:im2a:meter", clock });
  meter.record({
    layer: "compute",
    resource: { kind: "gpu", quantity: 1, unit: "gpu_seconds" },
    task: { purpose: "external" },
    cost: { amount: 1, currency: "USD" },
  });
  const snapshot = JSON.parse(JSON.stringify(meter.snapshot()));
  snapshot.records[0].cost.amount = -5;
  assert.throws(() => restoreResourceMeter(snapshot, { clock }), (error) => error.code === "invalid_record");
  assert.throws(() => restoreResourceMeter({ records: [] }, { clock }), (error) => error.code === "invalid_snapshot");
});