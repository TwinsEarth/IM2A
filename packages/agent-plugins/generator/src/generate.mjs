import { createHash } from "node:crypto";
import { mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";

const REQUIRED_PLUGIN_FIELDS = ["name", "version", "description", "skills", "mcp", "targets"];

export async function readJson(file) {
  return JSON.parse(await readFile(file, "utf8"));
}

async function hashSource(sourceDir) {
  const files = [];
  async function walk(dir) {
    for (const entry of (await readdir(dir, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) await walk(full);
      else files.push(full);
    }
  }
  await walk(sourceDir);
  const hash = createHash("sha256");
  for (const file of files) {
    hash.update(path.relative(sourceDir, file).replaceAll("\\", "/"));
    hash.update("\0");
    hash.update(await readFile(file));
    hash.update("\0");
  }
  return { hash: hash.digest("hex"), files };
}

export async function validateSource(sourceDir) {
  const plugin = await readJson(path.join(sourceDir, "plugin.json"));
  const missing = REQUIRED_PLUGIN_FIELDS.filter((field) => plugin[field] === undefined);
  if (missing.length) throw new Error(`plugin.json missing: ${missing.join(", ")}`);

  for (const skill of plugin.skills) {
    if (!/^skills\/[a-z0-9-]+\/SKILL\.md$/.test(skill)) {
      throw new Error(`invalid skill path: ${skill}`);
    }
    await readFile(path.join(sourceDir, skill));
  }

  if (!Array.isArray(plugin.targets) || plugin.targets.length !== 12) {
    throw new Error("targets must contain exactly 12 harness ids");
  }

  return plugin;
}

export async function generate({ root, check = false } = {}) {
  const sourceDir = path.join(root, "packages", "agent-plugins", "source");
  const targetsFile = path.join(root, "packages", "agent-plugins", "generator", "targets.json");
  const plugin = await validateSource(sourceDir);
  const targets = await readJson(targetsFile);
  const { hash, files } = await hashSource(sourceDir);
  const outRoot = path.join(root, "packages", "agent-plugins", "dist");

  if (check) {
    for (const target of targets.targets) {
      const file = path.join(outRoot, target.id, "bundle.meta.json");
      const existing = JSON.parse(await readFile(file, "utf8"));
      if (existing.source?.sourceHash !== hash) {
        throw new Error(`stale generated bundle: ${target.id}`);
      }
    }
    return { ok: true, sourceHash: hash, targets: targets.targets.length, files: files.length };
  }

  await rm(outRoot, { recursive: true, force: true });
  for (const target of targets.targets) {
    const dir = path.join(outRoot, target.id);
    await mkdir(dir, { recursive: true });
    await writeFile(path.join(dir, "bundle.meta.json"), JSON.stringify({
      schemaVersion: "1.0",
      source: { name: plugin.name, version: plugin.version, sourceHash: hash },
      target: { id: target.id, format: target.format, status: "skeleton" },
      generatedAt: "deterministic-p0",
      note: "P0 skeleton; no platform-specific files are emitted yet."
    }, null, 2) + "\n");
  }

  return { ok: true, sourceHash: hash, targets: targets.targets.length, files: files.length };
}
