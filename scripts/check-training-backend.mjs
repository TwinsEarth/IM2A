/**
 * TrainingBackend contract checks (v2.5.0 draft).
 *
 * Proves the plug-in boundary fails closed: no default backend, registration is
 * not enablement, budget refusal happens before submission, cancellation is not
 * rollback, and collection is not verification.
 *
 * No GPU, no network, no credentials, no training code.
 *
 * Usage: node scripts/check-training-backend.mjs
 */

import { readFile } from "node:fs/promises";
import {
  BackendRefusal,
  assertPlan,
  computePlanId,
} from "../packages/training-backend/src/plan.mjs";
import {
  BackendRegistryError,
  disable,
  enable,
  list,
  listEnabled,
  register,
  resolve,
} from "../packages/training-backend/src/registry.mjs";
import { createFakeBackend } from "../packages/training-backend/src/fake-backend.mjs";

const results = [];
function check(name, ok, detail) {
  results.push({ name, ok });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${ok ? "" : `: ${JSON.stringify(detail)}`}`);
}

async function refusalCode(fn) {
  try {
    await fn();
    return null;
  } catch (error) {
    return error?.code ?? error?.name ?? "UNKNOWN";
  }
}

const contractSource = await readFile(new URL("../packages/training-backend/src/contract.ts", import.meta.url), "utf8");
for (const method of ["capabilities", "estimate", "submit", "poll", "cancel", "collect", "verify"]) {
  check(`contract declares ${method}()`, contractSource.includes(`${method}(`));
}

check("no backend is registered by default", list().length === 0, list());
check("an unknown backend cannot be resolved", (await refusalCode(() => resolve("fake"))) === "UNKNOWN_BACKEND");

const fake = createFakeBackend({ id: "fake" });
register(fake);
check("registration alone does not enable a backend", list().find((entry) => entry.id === "fake")?.enabled === false, list());
check("a registered but disabled backend cannot be resolved", (await refusalCode(() => resolve("fake"))) === "BACKEND_DISABLED");

check("a backend without estimate() is refused", (await refusalCode(() => {
  const { estimate: _removed, ...rest } = fake;
  register({ ...rest, id: "broken" });
})) === "BACKEND_INVALID");

check("duplicate ids are refused", (await refusalCode(() => register(createFakeBackend({ id: "fake" })))) === "DUPLICATE_BACKEND");

enable("fake");
check("explicit enablement resolves the backend", resolve("fake") === fake && listEnabled().length === 1, listEnabled());

const basePlan = {
  planId: "",
  method: "sft",
  datasetManifestSha256: `sha256:${"a".repeat(64)}`,
  baseCheckpointDigest: `sha256:${"b".repeat(64)}`,
  hyperparams: { loraRank: 16, epochs: 1 },
  seed: 20261030,
  budget: { gpuHoursMax: 1, costUsdMax: 10 },
  imageDigest: `sha256:${"c".repeat(64)}`,
};
const plan = { ...basePlan, planId: computePlanId(basePlan) };
check("planId is content-addressed", computePlanId(plan) === plan.planId, computePlanId(plan));

check("a tampered plan is refused", (() => {
  try {
    assertPlan({ ...plan, seed: 1 }, fake.capabilities());
    return false;
  } catch (error) {
    return error.code === "PLAN_ID_MISMATCH";
  }
})());

const estimate = await fake.estimate(plan);
check("estimate returns a bounded cost without spending", estimate.gpuHours > 0 && estimate.costUsdUpperBound > 0, estimate);

const tooCheap = { ...basePlan, budget: { gpuHoursMax: 1, costUsdMax: 0.5 } };
tooCheap.planId = computePlanId(tooCheap);
check("budget refusal happens before submission", (await refusalCode(() => fake.submit(tooCheap))) === "BUDGET_EXCEEDED");

const zeroBudget = { ...basePlan, budget: { gpuHoursMax: 0, costUsdMax: 0 } };
zeroBudget.planId = computePlanId(zeroBudget);
check("a plan without budget is refused", (() => {
  try {
    assertPlan(zeroBudget, fake.capabilities());
    return false;
  } catch (error) {
    return error.code === "BUDGET_REQUIRED";
  }
})());

const { jobId } = await fake.submit(plan);
const firstPoll = await fake.poll(jobId);
check("submitted job starts queued/running", ["queued", "running"].includes(firstPoll.state), firstPoll);
check("execution metrics are not quality metrics", Object.keys(firstPoll.metrics).every((key) => key.startsWith("fake_")), firstPoll.metrics);

const secondPoll = await fake.poll(jobId);
check("job reaches succeeded without claiming quality", secondPoll.state === "succeeded" && Object.keys(secondPoll.metrics).every((key) => key.startsWith("fake_")), secondPoll);

const collected = await fake.collect(jobId);
check("collection returns inventory but is not verification", collected.artifacts.length > 0, collected);

const verdict = await fake.verify(collected.artifacts);
check("fake checkpoint fails the clean-load probe", verdict.inventoryOk && verdict.hashOk && verdict.loadProbeOk === false, verdict);

const { jobId: cancelledJob } = await fake.submit(plan);
await fake.cancel(cancelledJob);
check("cancel is terminal and stops the job", (await fake.poll(cancelledJob)).state === "cancelled");
check("partial artifacts cannot be collected as a checkpoint", (await refusalCode(() => fake.collect(cancelledJob))) === "JOB_NOT_FINISHED");
check("a succeeded job cannot be cancelled into a rollback", (await refusalCode(() => fake.cancel(jobId))) === "JOB_TERMINAL");

disable("fake");
check("disablement is immediate and fails closed", (await refusalCode(() => resolve("fake"))) === "BACKEND_DISABLED");

const failed = results.filter((entry) => !entry.ok);
console.log(JSON.stringify({
  checks: results.length,
  passed: results.length - failed.length,
  backends: list(),
  backendKind: fake.kind,
  methods: fake.capabilities().methods,
}, null, 2));
console.log(failed.length === 0 ? "ALL BACKEND CHECKS PASS" : `${failed.length} BACKEND CHECK(S) FAILED`);
process.exit(failed.length === 0 ? 0 : 1);
