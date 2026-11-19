#!/usr/bin/env node
/**
 * Published-layout check.
 *
 * The repo layout and the published tarball are not the same tree: a module can
 * import a sibling package that exists in the repo and does not exist after
 * install. This script packs the package, extracts it to a temp directory and
 * imports every kernel module from *that* tree — the closest thing to a
 * consumer's view without installing.
 *
 * Usage:
 *   node scripts/check-dist.mjs                 # packs, then checks
 *   node scripts/check-dist.mjs path/to.tgz     # checks an existing tarball
 */
import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const failures = [];

function pack() {
  const out = mkdtempSync(path.join(os.tmpdir(), "im2a-pack-"));
  // Run npm's CLI with the current node binary: no shell, no .cmd shim, no
  // DEP0190 warning, and identical behaviour on Windows and POSIX.
  const npmCli = path.join(path.dirname(process.execPath), "node_modules", "npm", "bin", "npm-cli.js");
  const args = ["pack", "--pack-destination", out, "--json"];
  const raw = existsSync(npmCli)
    ? execFileSync(process.execPath, [npmCli, ...args], {
        cwd: root,
        encoding: "utf8",
        stdio: ["ignore", "pipe", "pipe"],
      })
    : execFileSync("npm", args, { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], shell: true });
  const info = JSON.parse(raw)[0];
  return { tarball: path.join(out, info.filename), files: info.files.map((file) => file.path) };
}

function extract(tarball) {
  const dir = mkdtempSync(path.join(os.tmpdir(), "im2a-dist-"));
  execFileSync("tar", ["-xzf", tarball, "-C", dir], { stdio: ["ignore", "pipe", "pipe"] });
  return path.join(dir, "package");
}

async function main() {
  const given = process.argv[2];
  const packed = given ? { tarball: path.resolve(given), files: null } : pack();
  const dir = extract(packed.tarball);
  console.log(`checking ${path.basename(packed.tarball)} at ${dir}`);

  for (const rel of ["plugin.json", "package.json", "LICENSE", "README.md"]) {
    if (!existsSync(path.join(dir, rel))) failures.push(`missing from the tarball: ${rel}`);
  }

  const manifest = JSON.parse(readFileSync(path.join(dir, "plugin.json"), "utf8"));
  let loaded = 0;
  for (const entry of manifest.kernel.runtime_modules ?? []) {
    const file = path.join(dir, entry.path);
    if (!existsSync(file)) {
      failures.push(`declared runtime module is not in the tarball: ${entry.path}`);
      continue;
    }
    try {
      await import(pathToFileURL(file).href);
      loaded += 1;
    } catch (error) {
      failures.push(`import failed in the published layout: ${entry.path} (${error.code ?? error.name}: ${error.message})`);
    }
  }

  if (packed.files) {
    for (const rel of packed.files) {
      if (rel.startsWith("scripts/") || rel.startsWith(".github/")) {
        failures.push(`repo-only file leaked into the tarball: ${rel}`);
      }
    }
  }

  if (failures.length > 0) {
    console.error(`published-layout check failed (${failures.length}):`);
    for (const failure of failures) console.error(`  - ${failure}`);
    process.exitCode = 1;
    return;
  }
  console.log(
    `published-layout check passed: ${loaded}/${(manifest.kernel.runtime_modules ?? []).length} kernel modules load from the tarball`,
  );
}

try {
  await main();
} finally {
  // temp dirs are left to the OS; nothing in the repo is touched.
  rmSync(path.join(os.tmpdir(), "im2a-dist-"), { recursive: true, force: true, maxRetries: 0 });
}
