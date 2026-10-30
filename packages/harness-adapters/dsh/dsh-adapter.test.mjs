import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { createDshAdapter, DEFAULT_PROFILE, MARKER_BEGIN } from "./adapter.mjs";

const sha = (value) => createHash("sha256").update(value).digest("hex");

async function sandboxProfile() {
  const dir = await mkdtemp(path.join(os.tmpdir(), "im2a-dsh-sandbox-"));
  await writeFile(path.join(dir, "cordis.patch.yml"), "[]\n", "utf8");
  await writeFile(path.join(dir, "cordis.yml"), "[]\n", "utf8");
  await writeFile(path.join(dir, "package.json"), JSON.stringify({ name: "sandbox", private: true }), "utf8");
  return dir;
}

test("DSH: detect reads live profile without changing it", async () => {
  const adapter = createDshAdapter();
  const livePatch = path.join(DEFAULT_PROFILE, "cordis.patch.yml");
  const before = await readFile(livePatch, "utf8");
  const beforeHash = sha(before);
  const result = await adapter.detect({ profileDir: DEFAULT_PROFILE });
  const after = await readFile(livePatch, "utf8");
  assert.equal(result.installed, true);
  assert.equal(result.profile, DEFAULT_PROFILE);
  assert.equal(sha(after), beforeHash);
  assert.match(result.mcpBin, /im2a-mcp\.exe$/);
});

test("DSH: public plan selects the host bundle and is read-only", async () => {
  const adapter = createDshAdapter();
  const dir = await sandboxProfile();
  try {
    const before = await readFile(path.join(dir, "cordis.patch.yml"), "utf8");
    const plan = await adapter.plan({ profileDir: dir });
    assert.equal(plan.mode, "host");
    assert.equal(plan.steps.length, 1);
    assert.match(plan.steps[0].preview, /dsh plugin .* add file:/);
    assert.doesNotMatch(plan.steps[0].preview, /IM2A_HARNESS_TOKEN/);
    assert.equal(await readFile(path.join(dir, "cordis.patch.yml"), "utf8"), before);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("DSH: legacy sandbox patch remains insert-only and redacted", async () => {
  const adapter = createDshAdapter();
  const dir = await sandboxProfile();
  try {
    const before = await readFile(path.join(dir, "cordis.patch.yml"), "utf8");
    const plan = await adapter.planPatch({ profileDir: dir });
    assert.match(plan.steps[0].preview, /^- insert:$/m);
    assert.doesNotMatch(plan.steps[0].preview, /^- id: im2a-mcp$/m);
    assert.match(plan.steps[0].preview, /IM2A_HARNESS_TOKEN: <redacted>/);
    assert.equal(await readFile(path.join(dir, "cordis.patch.yml"), "utf8"), before);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("DSH: apply only writes sandbox, rollback restores bytes", async () => {
  const adapter = createDshAdapter();
  const dir = await sandboxProfile();
  try {
    const before = await readFile(path.join(dir, "cordis.patch.yml"), "utf8");
    const plan = await adapter.planPatch({ profileDir: dir });
    const applied = await adapter.apply({ profileDir: dir, plan });
    const after = await readFile(path.join(dir, "cordis.patch.yml"), "utf8");
    assert.equal(applied.changed.length, 1);
    assert.match(after, new RegExp(MARKER_BEGIN.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
    assert.match(after, /^- insert:$/m);
    assert.doesNotMatch(after, /^- id: im2a-mcp$/m);
    assert.match(after, /serverName: im2a/);
    assert.match(after, /!!js process\.env\.IM2A_HARNESS_TOKEN/);

    await adapter.rollbackPatch({ profileDir: dir, applied });
    assert.equal(await readFile(path.join(dir, "cordis.patch.yml"), "utf8"), before);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("DSH: apply refuses the live profile by default", async () => {
  const adapter = createDshAdapter();
  const plan = await adapter.plan({ profileDir: DEFAULT_PROFILE });
  await assert.rejects(
    () => adapter.apply({ profileDir: DEFAULT_PROFILE, plan }),
    /refusing live DSH profile/,
  );
});

test("DSH: verify without token is BLOCKED, never PASS", async () => {
  const adapter = createDshAdapter();
  const dir = await sandboxProfile();
  try {
    const plan = await adapter.planPatch({ profileDir: dir });
    await adapter.apply({ profileDir: dir, plan });
    const result = await adapter.verify({ profileDir: dir, env: {} });
    assert.equal(result.ok, false);
    assert.equal(result.status, "blocked");
    assert.equal(result.checks.some((check) => check.name === "mcp-token" && check.ok === false), true);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
