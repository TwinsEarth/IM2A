#!/usr/bin/env node
/**
 * DSH pin check.
 *
 * Answers one question in one command: "does the plugin pin still match the DSH
 * build installed on this machine, and do the repo's own records agree?"
 *
 * Three sources are tried for the runtime version, cheapest first:
 *   1. --dsh-version / DSH_VERSION (explicit override)
 *   2. `dsh --version`
 *   3. <resources>/runtime/primary-runtime/runtime.json  (desktopVersion)
 *
 * Exit codes are deliberately distinct so CI or a human can branch on them:
 *   0  OK                 runtime == both pins == matrix target
 *   1  NEEDS_RELEASE      runtime moved; a plugin release is required
 *   2  REPO_INCONSISTENT  pins and/or matrix disagree with each other
 *   3  UNKNOWN            the DSH version could not be determined
 */
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

export const EXIT = Object.freeze({ OK: 0, NEEDS_RELEASE: 1, REPO_INCONSISTENT: 2, UNKNOWN: 3 });

const here = path.dirname(fileURLToPath(import.meta.url));
export const defaultRoot = path.resolve(here, "..");

function readJson(root, rel) {
  return JSON.parse(readFileSync(path.join(root, rel), "utf8"));
}

function bumpPatch(version) {
  const match = /^(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$/.exec(String(version ?? ""));
  if (!match) return null;
  return `${match[1]}.${match[2]}.${Number(match[3]) + 1}`;
}

/**
 * Pure verdict function: everything the CLI decides lives here so it can be
 * tested without touching the filesystem or spawning a process.
 */
export function evaluatePinState({ runtime, matrixTarget, packagePin, pluginPin, pluginVersion }) {
  const differences = [];
  if (packagePin !== pluginPin) {
    differences.push({ field: "pin", expected: packagePin, actual: pluginPin, where: "plugin.json" });
  }
  if (matrixTarget !== packagePin) {
    differences.push({ field: "matrix", expected: packagePin, actual: matrixTarget, where: "version-matrix.json dsh-runtime.target" });
  }

  if (!runtime) {
    return {
      status: "UNKNOWN",
      exitCode: EXIT.UNKNOWN,
      runtime: null,
      differences,
      reason: "the DSH runtime version could not be determined",
      hint: "pass --dsh-version <x.y.z-rc.n> or set DSH_VERSION",
    };
  }

  const runtimeMatches = runtime === packagePin;
  const repoConsistent = differences.length === 0;

  if (!runtimeMatches) {
    return {
      status: "NEEDS_RELEASE",
      exitCode: EXIT.NEEDS_RELEASE,
      runtime,
      differences,
      reason: `runtime ${runtime} does not match plugin pin ${packagePin}`,
      suggestedVersion: bumpPatch(pluginVersion),
      edit: [
        "packages/dsh-im2a-plugin/package.json  -> engines.dsh",
        "packages/dsh-im2a-plugin/plugin.json   -> engines.dsh",
        "version-matrix.json                   -> dsh-runtime.current/target",
        "tests/v321-plugin-shell.test.mjs      -> RUNTIME_PIN",
      ],
    };
  }

  if (!repoConsistent) {
    return {
      status: "REPO_INCONSISTENT",
      exitCode: EXIT.REPO_INCONSISTENT,
      runtime,
      differences,
      reason: "the runtime matches, but the repo's own records disagree",
    };
  }

  return { status: "OK", exitCode: EXIT.OK, runtime, differences, reason: "runtime and repo records agree" };
}

function runtimeJsonVersion(env) {
  const installDir = env.DSH_INSTALL_DIR;
  if (!installDir) return null;
  const parts = installDir.split(/[\\/]/);
  const index = parts.lastIndexOf("resources");
  if (index < 0) return null;
  const candidate = path.join(parts.slice(0, index + 1).join(path.sep), "runtime", "primary-runtime", "runtime.json");
  try {
    const parsed = JSON.parse(readFileSync(candidate, "utf8"));
    return typeof parsed.desktopVersion === "string" ? { version: parsed.desktopVersion, source: candidate } : null;
  } catch {
    return null;
  }
}

/** Resolve the installed DSH build. Returns { version, source } or { version: null }. */
export function resolveDshVersion({ override, env = process.env, run = execFileSync } = {}) {
  if (typeof override === "string" && override.trim()) {
    return { version: override.trim(), source: "--dsh-version" };
  }
  if (typeof env.DSH_VERSION === "string" && env.DSH_VERSION.trim()) {
    return { version: env.DSH_VERSION.trim(), source: "DSH_VERSION" };
  }
  try {
    const isWindows = process.platform === "win32";
    const raw = isWindows
      ? run("cmd", ["/c", "dsh --version"], { encoding: "utf8", timeout: 20000 })
      : run("dsh", ["--version"], { encoding: "utf8", timeout: 20000 });
    const version = String(raw).trim().split(/\r?\n/).pop()?.trim();
    if (version && /^\d+\.\d+\.\d+/.test(version)) return { version, source: "dsh --version" };
  } catch {
    // Fall through to the file source.
  }
  const fromFile = runtimeJsonVersion(env);
  if (fromFile) return { version: fromFile.version, source: fromFile.source };
  return { version: null, source: null };
}

function pctOk(flag) {
  return flag ? "OK  " : "DRIFT";
}

export function collectState(root = defaultRoot) {
  const matrix = readJson(root, "version-matrix.json");
  const pkg = readJson(root, "packages/dsh-im2a-plugin/package.json");
  const manifest = readJson(root, "packages/dsh-im2a-plugin/plugin.json");
  const runtime = matrix.components.find((component) => component.id === "dsh-runtime");
  return {
    matrixTarget: runtime?.target ?? null,
    matrixCurrent: runtime?.current ?? null,
    packagePin: pkg?.engines?.dsh ?? null,
    pluginPin: manifest?.engines?.dsh ?? null,
    pluginVersion: manifest?.version ?? null,
    packageVersion: pkg?.version ?? null,
  };
}

function main(argv) {
  const json = argv.includes("--json");
  const versionArgIndex = argv.indexOf("--dsh-version");
  const override = versionArgIndex >= 0 ? argv[versionArgIndex + 1] : undefined;

  const state = collectState(defaultRoot);
  const resolved = resolveDshVersion({ override });
  const verdict = evaluatePinState({
    runtime: resolved.version,
    matrixTarget: state.matrixTarget,
    packagePin: state.packagePin,
    pluginPin: state.pluginPin,
    pluginVersion: state.pluginVersion,
  });

  if (json) {
    console.log(JSON.stringify({
      verdict: verdict.status,
      exitCode: verdict.exitCode,
      runtime: resolved.version,
      runtimeSource: resolved.source,
      repo: state,
      reason: verdict.reason,
      differences: verdict.differences,
      suggestion: verdict.suggestedVersion ?? null,
      edit: verdict.edit ?? [],
      hint: verdict.hint ?? null,
    }, null, 2));
  } else {
    console.log("DSH pin check");
    console.log(`  runtime (${resolved.source ?? "unknown"}) : ${resolved.version ?? "unknown"}`);
    console.log(`  package.json engines.dsh : ${state.packagePin ?? "missing"}   ${pctOk(state.packagePin === resolved.version)}`);
    console.log(`  plugin.json  engines.dsh : ${state.pluginPin ?? "missing"}   ${pctOk(state.pluginPin === resolved.version)}`);
    console.log(`  version-matrix target    : ${state.matrixTarget ?? "missing"}   ${pctOk(state.matrixTarget === resolved.version)}`);
    console.log(`  plugin version           : ${state.pluginVersion ?? "missing"}`);
    console.log("");
    console.log(`${verdict.status}: ${verdict.reason}`);
    if (verdict.status === "NEEDS_RELEASE") {
      console.log(`  suggested version: ${verdict.suggestedVersion}`);
      console.log("  edit:");
      for (const line of verdict.edit) console.log(`    - ${line}`);
      console.log("  then: node scripts/validate.mjs && npm run pin:check");
    }
    if (verdict.status === "REPO_INCONSISTENT") {
      for (const diff of verdict.differences) {
        console.log(`  ${diff.where}: expected ${diff.expected}, got ${diff.actual}`);
      }
    }
    if (verdict.hint) console.log(`  hint: ${verdict.hint}`);
  }
  process.exitCode = verdict.exitCode;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv.slice(2));
}
