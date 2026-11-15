import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { validateSchema } from "../scripts/schema-validator.mjs";
import {
  UiArtifactError,
  createUiArtifact,
  isPromotable,
  promoteUiArtifact,
  validateUiArtifact,
} from "../packages/im2a-http-mcp/ui-artifact.mjs";

const schema = JSON.parse(await readFile(
  new URL("../docs/contracts/ui-artifact-1.0.schema.json", import.meta.url),
  "utf8",
));

const TRACE_ID = "0123456789abcdef0123456789abcdef";
const HASH = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
const at = (iso) => () => new Date(iso);

function artifact(overrides = {}) {
  return createUiArtifact({
    traceId: TRACE_ID,
    agentDid: "did:im2a:lead",
    kind: "interactive",
    format: "html",
    sha256: HASH,
    bytes: 4096,
    goalId: "goal_0123456789abcdef01234567",
    now: at("2026-11-15T00:00:00Z"),
    ...overrides,
  });
}

test("a fresh artifact is ephemeral and satisfies the frozen contract", () => {
  const value = artifact();
  assert.equal(value.status, "ephemeral");
  assert.equal(value.promoted_skill_id, null);
  assert.match(value.artifact_id, /^uiart_[0-9a-f]{24}$/);
  assert.equal(value.bytes, 4096);
  assert.equal(validateSchema(schema, value).ok, true);
  assert.deepEqual(validateUiArtifact(value), []);
});

test("promotion attaches a skill id and a timestamp", () => {
  const promoted = promoteUiArtifact(artifact(), {
    skillId: "skill-demo",
    now: at("2026-11-16T00:00:00Z"),
  });
  assert.equal(promoted.status, "promoted");
  assert.equal(promoted.promoted_skill_id, "skill-demo");
  assert.equal(promoted.promoted_at, "2026-11-16T00:00:00.000Z");
  assert.equal(validateSchema(schema, promoted).ok, true);
  assert.equal(isPromotable(promoted), false);
});

test("promotion is idempotent for the same skill", () => {
  const once = promoteUiArtifact(artifact(), { skillId: "skill-demo", now: at("2026-11-16T00:00:00Z") });
  const twice = promoteUiArtifact(once, { skillId: "skill-demo", now: at("2026-11-20T00:00:00Z") });
  assert.deepEqual(twice, once, "a retried promotion must not append a second event");
});

test("promotion to a different skill is a conflict, not an overwrite", () => {
  const once = promoteUiArtifact(artifact(), { skillId: "skill-demo", now: at("2026-11-16T00:00:00Z") });
  assert.throws(
    () => promoteUiArtifact(once, { skillId: "skill-other" }),
    (error) => error instanceof UiArtifactError && error.code === "artifact_already_promoted",
  );
});

test("the status pair is enforced in both directions", () => {
  const ephemeralWithSkill = artifact();
  ephemeralWithSkill.promoted_skill_id = "skill-demo";
  assert.ok(validateUiArtifact(ephemeralWithSkill).includes("ephemeral must not carry promoted_skill_id"));

  const promotedWithoutTime = promoteUiArtifact(artifact(), { skillId: "skill-demo", now: at("2026-11-16T00:00:00Z") });
  delete promotedWithoutTime.promoted_at;
  assert.ok(validateUiArtifact(promotedWithoutTime).includes("promoted requires promoted_at"));

  const promotedWithoutSkill = promoteUiArtifact(artifact(), { skillId: "skill-demo", now: at("2026-11-16T00:00:00Z") });
  promotedWithoutSkill.promoted_skill_id = null;
  assert.ok(validateUiArtifact(promotedWithoutSkill).includes("promoted requires promoted_skill_id"));
});

test("bad inputs never produce an artifact", () => {
  assert.throws(
    () => artifact({ kind: "hologram" }),
    (error) => error.code === "invalid_artifact",
  );
  assert.throws(
    () => artifact({ sha256: "xyz" }),
    (error) => error.code === "invalid_artifact",
  );
  assert.throws(
    () => artifact({ traceId: "trace_123" }),
    (error) => error.code === "invalid_artifact" && error.message.includes("trace_id"),
  );
  assert.throws(
    () => promoteUiArtifact(artifact(), { skillId: "" }),
    (error) => error.code === "invalid_skill_id",
  );
});

test("isPromotable is true only while the artifact is still a trace product", () => {
  const fresh = artifact();
  assert.equal(isPromotable(fresh), true);
  const promoted = promoteUiArtifact(fresh, { skillId: "skill-demo", now: at("2026-11-16T00:00:00Z") });
  assert.equal(isPromotable(promoted), false);
  assert.equal(isPromotable({ ...fresh, status: "archived" }), false);
});
