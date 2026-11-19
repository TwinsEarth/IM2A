import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const here = path.dirname(fileURLToPath(import.meta.url));
const pluginRoot = path.resolve(here, "..", "packages", "dsh-im2a-plugin");
const kernelDir = path.join(pluginRoot, "kernel");

// The published tarball contains this package and nothing else, so any import
// that leaves the package root resolves fine in the repo and fails after install.
// That exact failure shipped once (topology-metrics.mjs -> ../../im2a-http-mcp).
const OUTSIDE = /from\s+"(\.\.\/|\.\.\\)/;

test("no kernel module imports across the package boundary", async () => {
  const files = (await readdir(kernelDir)).filter((name) => name.endsWith(".mjs"));
  assert.ok(files.length >= 7, `expected the kernel modules, found ${files.length}`);
  const offenders = [];
  for (const name of files) {
    const source = await readFile(path.join(kernelDir, name), "utf8");
    for (const [index, line] of source.split(/\r?\n/).entries()) {
      if (OUTSIDE.test(line)) offenders.push(`${name}:${index + 1} ${line.trim()}`);
    }
  }
  assert.deepEqual(offenders, [], `kernel imports must stay inside the package:\n${offenders.join("\n")}`);
});

test("every kernel module loads on its own", async () => {
  const files = (await readdir(kernelDir)).filter((name) => name.endsWith(".mjs"));
  for (const name of files) {
    await assert.doesNotReject(
      import(new URL(`../packages/dsh-im2a-plugin/kernel/${name}`, import.meta.url)),
      `${name} must import without side effects or missing dependencies`,
    );
  }
});
