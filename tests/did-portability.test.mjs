import assert from "node:assert/strict";
import test from "node:test";
import {
  DidPortabilityError,
  canonicalJson,
  exportDidBundle,
  planDidImport,
  verifyDidBundle,
} from "../packages/dsh-im2a-plugin/kernel/did-portability.mjs";

const DID = "did:im2a:lead";
const OTHER_DID = "did:im2a:other";
const HEAD = `sha256:${"a".repeat(64)}`;

const skills = [
  { id: "skill-b", version: "1.2.0", sha256: `sha256:${"b".repeat(64)}` },
  { id: "skill-a", version: "0.3.1", sha256: `sha256:${"a".repeat(64)}` },
];
const goals = [
  { goal_id: "goal_bbbbbbbbbbbbbbbbbbbbbbbb", status: "pending" },
  { goal_id: "goal_aaaaaaaaaaaaaaaaaaaaaaaa", status: "scheduled" },
];
const credit = { verification_tier: "T2", score: 0.83 };
const contribution = { count: 4, head_hash: HEAD };

function bundle(overrides = {}) {
  return exportDidBundle({
    did: DID,
    skills,
    goals,
    credit,
    contribution,
    clock: () => new Date("2026-11-15T00:00:00Z"),
    ...overrides,
  });
}

test("the same assets on another machine produce the same digest", () => {
  const first = bundle();
  const second = exportDidBundle({
    did: DID,
    // Reversed input order and a different export time must not change identity.
    skills: [...skills].reverse(),
    goals: [...goals].reverse(),
    credit,
    contribution,
    clock: () => new Date("2027-01-01T12:00:00Z"),
  });
  assert.equal(first.digest, second.digest);
  assert.notEqual(first.exported_at, second.exported_at);
  assert.deepEqual(first.assets.skills.map((skill) => skill.id), ["skill-a", "skill-b"]);
  assert.equal(verifyDidBundle(second).ok, true);
});

test("different assets produce a different digest", () => {
  const base = bundle();
  const changed = exportDidBundle({
    did: DID,
    skills: [{ ...skills[0] }],
    goals,
    credit,
    contribution,
  });
  assert.notEqual(base.digest, changed.digest);
});

test("the digest covers the contribution proof, so history cannot be swapped", () => {
  const base = bundle();
  const tamperedHead = structuredClone(base);
  tamperedHead.contribution.head_hash = `sha256:${"c".repeat(64)}`;
  const check = verifyDidBundle(tamperedHead);
  assert.equal(check.ok, false);
  assert.ok(check.errors.some((error) => error.includes("digest")));
});

test("tampering with any asset is detected", () => {
  const tampered = structuredClone(bundle());
  tampered.assets.skills[0].version = "9.9.9";
  assert.equal(verifyDidBundle(tampered).ok, false);

  const forgedDid = structuredClone(bundle());
  forgedDid.did = OTHER_DID;
  assert.equal(verifyDidBundle(forgedDid).ok, false);
});

test("duplicate or malformed assets are rejected at export time", () => {
  assert.throws(
    () => exportDidBundle({ did: DID, skills: [skills[0], { ...skills[0] }] }),
    (error) => error instanceof DidPortabilityError && error.code === "duplicate_skill",
  );
  assert.throws(
    () => exportDidBundle({ did: "not-a-did" }),
    (error) => error.code === "invalid_did",
  );
  assert.throws(
    () => exportDidBundle({ did: DID, goals: [{ goal_id: "goal_short" }] }),
    (error) => error.code === "invalid_bundle",
  );
  assert.throws(
    () => exportDidBundle({ did: DID, credit: { verification_tier: "T9", score: 0.5 } }),
    (error) => error.code === "invalid_bundle",
  );
});

test("import refuses to move a bundle into a different DID (C4)", () => {
  assert.throws(
    () => planDidImport(bundle(), { targetDid: OTHER_DID }),
    (error) => error instanceof DidPortabilityError && error.code === "did_mismatch",
  );
});

test("import appends no contribution entries and treats history as a read-only proof", () => {
  const source = bundle();
  const plan = planDidImport(source, { targetDid: DID });
  assert.equal(plan.contributions_appended, 0);
  assert.equal(plan.digest, source.digest);
  assert.deepEqual(plan.contribution, { count: 4, head_hash: HEAD, mode: "read_only_proof" });
  assert.deepEqual(plan.writes, ["skills/skill-a", "skills/skill-b", "goal_buffer", "credit_profile"]);
  // Planning twice is idempotent: a retried import cannot double-write.
  assert.deepEqual(planDidImport(source, { targetDid: DID }), plan);
});

test("an invalid bundle cannot be planned at all", () => {
  const broken = structuredClone(bundle());
  broken.digest = `sha256:${"d".repeat(64)}`;
  assert.throws(
    () => planDidImport(broken, { targetDid: DID }),
    (error) => error.code === "invalid_bundle",
  );
});

test("canonical JSON is key-order independent", () => {
  assert.equal(canonicalJson({ b: 1, a: { d: 2, c: 3 } }), canonicalJson({ a: { c: 3, d: 2 }, b: 1 }));
  assert.notEqual(canonicalJson({ a: [1, 2] }), canonicalJson({ a: [2, 1] }));
});
