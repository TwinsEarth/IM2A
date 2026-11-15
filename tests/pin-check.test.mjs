import assert from "node:assert/strict";
import test from "node:test";
import { EXIT, evaluatePinState, resolveDshVersion } from "../scripts/pin-check.mjs";

const PIN = "0.2.2-rc.3";

function state(overrides = {}) {
  return {
    runtime: PIN,
    matrixTarget: PIN,
    packagePin: PIN,
    pluginPin: PIN,
    pluginVersion: "3.3.1",
    ...overrides,
  };
}

test("everything matching is OK", () => {
  const verdict = evaluatePinState(state());
  assert.equal(verdict.status, "OK");
  assert.equal(verdict.exitCode, EXIT.OK);
  assert.deepEqual(verdict.differences, []);
});

test("a newer runtime asks for a release and suggests the next patch", () => {
  const verdict = evaluatePinState(state({ runtime: "0.2.2-rc.4" }));
  assert.equal(verdict.status, "NEEDS_RELEASE");
  assert.equal(verdict.exitCode, 1);
  assert.equal(verdict.suggestedVersion, "3.3.2");
  assert.match(verdict.reason, /0\.2\.2-rc\.4/);
  assert.ok(verdict.edit.some((line) => line.includes("version-matrix.json")));
  assert.ok(verdict.edit.some((line) => line.includes("RUNTIME_PIN")));
});

test("a runtime change outranks a repo inconsistency", () => {
  const verdict = evaluatePinState(state({ runtime: "0.2.2-rc.4", matrixTarget: "0.2.2-rc.2" }));
  assert.equal(verdict.status, "NEEDS_RELEASE");
  assert.equal(verdict.exitCode, 1);
  assert.ok(verdict.differences.length > 0, "the repo mismatch is still reported alongside");
});

test("matching runtime but a stale matrix is REPO_INCONSISTENT", () => {
  const verdict = evaluatePinState(state({ matrixTarget: "0.2.2-rc.2" }));
  assert.equal(verdict.status, "REPO_INCONSISTENT");
  assert.equal(verdict.exitCode, 2);
  assert.equal(verdict.differences.length, 1);
  assert.equal(verdict.differences[0].where, "version-matrix.json dsh-runtime.target");
});

test("the two manifests disagreeing is REPO_INCONSISTENT", () => {
  const verdict = evaluatePinState(state({ pluginPin: "0.2.2-rc.2" }));
  assert.equal(verdict.status, "REPO_INCONSISTENT");
  assert.equal(verdict.exitCode, 2);
  assert.equal(verdict.differences[0].where, "plugin.json");
});

test("an undeterminable runtime is UNKNOWN, not a false OK", () => {
  const verdict = evaluatePinState(state({ runtime: null }));
  assert.equal(verdict.status, "UNKNOWN");
  assert.equal(verdict.exitCode, 3);
  assert.match(verdict.hint, /--dsh-version/);
});

test("version resolution prefers explicit input, then env, then the command", () => {
  const run = () => "0.9.9-rc.9\n";
  assert.deepEqual(
    resolveDshVersion({ override: "1.2.3", env: { DSH_VERSION: "2.0.0" }, run }),
    { version: "1.2.3", source: "--dsh-version" },
  );
  assert.deepEqual(
    resolveDshVersion({ env: { DSH_VERSION: "2.0.0" }, run }),
    { version: "2.0.0", source: "DSH_VERSION" },
  );
  assert.deepEqual(resolveDshVersion({ env: {}, run }), { version: "0.9.9-rc.9", source: "dsh --version" });
});

test("a failing command without any file source resolves to null", () => {
  const run = () => { throw new Error("spawn failed"); };
  assert.deepEqual(resolveDshVersion({ env: {}, run }), { version: null, source: null });
});
