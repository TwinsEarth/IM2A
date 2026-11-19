#!/usr/bin/env node
/**
 * Package self-check for the published artifact.
 *
 * Runs in CI on every push to the plugin repository. Everything asserted here is
 * something a consumer would notice only after install, which is too late.
 */
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { loadPlugin, validateManifest, verifyRuntimeModules } from "../kernel/registry.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const TOOL_NAME_RE = /^im2a_[a-z][a-z0-9_]{2,40}$/;
const SEMVERISH = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;

const failures = [];
function check(condition, message) {
  if (!condition) failures.push(message);
}

function readJson(rel) {
  return JSON.parse(readFileSync(path.join(root, rel), "utf8"));
}

const pkg = readJson("package.json");
const aliases = readJson("migrations/3.2.1-tool-aliases.json");
const { manifest } = await loadPlugin(root);

// --- manifest and package must agree -------------------------------------
check(pkg.name === "@twinsearth/dsh-im2a-plugin", `unexpected package name: ${pkg.name}`);
check(pkg.name.endsWith(`/${manifest.name}`), `package name does not end with manifest name: ${manifest.name}`);
check(pkg.version === manifest.version, `version drift: package ${pkg.version} vs manifest ${manifest.version}`);
check(SEMVERISH.test(pkg.version ?? ""), `version is not semver: ${pkg.version}`);

// --- runtime pin ----------------------------------------------------------
check(SEMVERISH.test(pkg.engines?.dsh ?? ""), `engines.dsh missing or invalid in package.json`);
check(pkg.engines?.dsh === manifest.engines?.dsh, `engines.dsh drift: ${pkg.engines?.dsh} vs ${manifest.engines?.dsh}`);

// --- licensing ------------------------------------------------------------
check(pkg.license === "MIT", `license must be MIT, got ${pkg.license}`);
try {
  const licenseText = readFileSync(path.join(root, "LICENSE"), "utf8");
  check(/^MIT License/m.test(licenseText), "LICENSE file is not an MIT license");
} catch {
  failures.push("LICENSE file is missing");
}

// --- the manifest guard itself -------------------------------------------
const guard = validateManifest(manifest);
for (const error of guard.errors) failures.push(`manifest: ${error}`);

// --- runtime modules actually exist on disk ------------------------------
const runtimeFiles = await verifyRuntimeModules(manifest, root);
for (const entry of runtimeFiles) {
  check(entry.exists, `declared runtime module is missing: ${entry.path}`);
}

// --- alias table ----------------------------------------------------------
check(Array.isArray(aliases.entries) && aliases.entries.length > 0, "alias table is empty");
const seenOld = new Set();
const seenNew = new Set();
for (const entry of aliases.entries ?? []) {
  check(!seenOld.has(entry.old_name), `duplicate alias source: ${entry.old_name}`);
  seenOld.add(entry.old_name);
  check(TOOL_NAME_RE.test(entry.new_name), `alias target is not a valid tool name: ${entry.new_name}`);
  seenNew.add(entry.new_name);
  check(/^sha256:[0-9a-f]{64}$/.test(entry.schema_hash ?? ""), `bad schema_hash for ${entry.old_name}`);
}
check(aliases.generatedFrom?.toolCount >= aliases.entries.length, "generatedFrom.toolCount is smaller than the alias table");

// --- shipped files exist --------------------------------------------------
for (const rel of ["plugin.json", "package.json", "README.md", "icon.svg", "cordis.patch.yml"]) {
  try {
    readFileSync(path.join(root, rel));
  } catch {
    failures.push(`missing published file: ${rel}`);
  }
}

// --- every kernel module must load on its own -----------------------------
// A module that only resolves because a sibling package happens to exist in the
// repo will pass every static check and still break after install.
for (const entry of manifest.kernel.runtime_modules ?? []) {
  try {
    await import(new URL(`../${entry.path}`, import.meta.url));
  } catch (error) {
    failures.push(`kernel module failed to import: ${entry.path} (${error.code ?? error.name}: ${error.message})`);
  }
}

if (failures.length > 0) {
  console.error(`package check failed (${failures.length}):`);
  for (const failure of failures) console.error(`  - ${failure}`);
  process.exitCode = 1;
} else {
  console.log(
    `package check passed: ${manifest.name}@${manifest.version} ` +
      `(engines.dsh ${manifest.engines.dsh}, ${runtimeFiles.length} runtime modules, ` +
      `${aliases.entries.length} aliases)`,
  );
}
