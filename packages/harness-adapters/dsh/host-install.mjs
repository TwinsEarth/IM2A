/**
 * P0-9: install the IM2A DSH host bundle into a sandbox profile.
 *
 * Safety:
 * - never writes the live desktop profile unless explicitly allowed;
 * - single-writer lock around the read/install/verify window;
 * - before/after file hashes and a backup directory;
 * - planDigest must match the plan that authorized the install.
 */
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { closeSync, existsSync, openSync, readFileSync, statSync, unlinkSync, writeFileSync } from "node:fs";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

export const PLUGIN_NAME = "@twinsearth/im2a-dsh-plugin";
export const DEFAULT_DSH_BIN = "E:\\DS\\resources\\runtime\\cli\\bin\\dsh.cmd";
export const DEFAULT_PLUGIN_PATH = path.resolve(
  path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1")),
  "..",
  "..",
  "im2a-dsh-plugin",
);
const FILES = ["package.json", "pnpm-lock.yaml", "cordis.patch.yml"];
const LOCK_TTL_MS = 120000;

const sha = (value) => createHash("sha256").update(value).digest("hex");
const profileName = (profileDir) => path.basename(profileDir);
const dshHomeFromProfile = (profileDir) => path.dirname(path.dirname(profileDir));

function isLive(profile) {
  const live = path.resolve(process.env.DSH_PROFILE_DIR ?? path.join(os.homedir(), ".dsh", "profiles", "desktop"));
  return path.resolve(profile) === live;
}

async function snapshot(profileDir) {
  const result = {};
  for (const name of FILES) {
    const file = path.join(profileDir, name);
    const text = existsSync(file) ? await readFile(file, "utf8") : null;
    result[name] = { text, sha256: text === null ? null : sha(text) };
  }
  return result;
}

async function backupSnapshot(profileDir, before) {
  const dir = path.join(profileDir, ".bak", `im2a-host-${Date.now()}-${sha(JSON.stringify(before)).slice(0, 12)}`);
  await mkdir(dir, { recursive: true });
  for (const [name, entry] of Object.entries(before)) {
    if (entry.text !== null) await writeFile(path.join(dir, name), entry.text, { encoding: "utf8", mode: 0o600 });
  }
  return dir;
}

async function restoreSnapshot(profileDir, before) {
  for (const [name, entry] of Object.entries(before)) {
    const file = path.join(profileDir, name);
    if (entry.text === null) {
      try { unlinkSync(file); } catch {}
    } else {
      await writeFile(file, entry.text, { encoding: "utf8", mode: 0o600 });
    }
  }
}

async function withLock(profileDir, fn) {
  const lock = path.join(profileDir, ".im2a-install.lock");
  let fd;
  try {
    fd = openSync(lock, "wx");
  } catch (error) {
    if (error.code !== "EEXIST") throw error;
    const age = Date.now() - (existsSync(lock) ? statSync(lock).mtimeMs : 0);
    if (age > LOCK_TTL_MS) {
      unlinkSync(lock);
      fd = openSync(lock, "wx");
    } else {
      throw new Error(`DSH install lock busy: ${lock}`);
    }
  }
  writeFileSync(fd, `${process.pid} ${new Date().toISOString()}\n`);
  try {
    return await fn();
  } finally {
    try { closeSync(fd); } catch {}
    try { unlinkSync(lock); } catch {}
  }
}

function runDsh(dshBin, args, env) {
  const outFile = path.join(os.tmpdir(), `im2a-dsh-${process.pid}-${Date.now()}.log`);
  const fd = openSync(outFile, "w");
  try {
    const result = spawnSync(dshBin, args, {
      env,
      cwd: os.tmpdir(),
      stdio: ["ignore", fd, fd],
      windowsHide: true,
      timeout: 180000,
      shell: process.platform === "win32" && /\.cmd$/i.test(dshBin),
    });
    closeSync(fd);
    let output = "";
    try { output = readFileSync(outFile, "utf8"); } catch {}
    return { status: result.status, error: result.error ? String(result.error.message) : null, output };
  } finally {
    try { closeSync(fd); } catch {}
    try { unlinkSync(outFile); } catch {}
  }
}

async function assertComposed(dshBin, profile, env) {
  const dump = runDsh(dshBin, ["--profile", profileName(profile), "--dump-config"], env);
  if (dump.status !== 0) throw new Error(`dsh --dump-config failed: ${dump.error ?? ""} ${dump.output.slice(-800)}`);
  if (!/id: im2a-mcp/.test(dump.output) || /entry .*not found/i.test(dump.output)) {
    throw new Error("composed config does not contain a live im2a-mcp row");
  }
  return dump.output;
}

export async function installHost({
  profileDir,
  dshBin = process.env.DSH_BIN ?? DEFAULT_DSH_BIN,
  pluginPath = process.env.IM2A_DSH_PLUGIN_PATH ?? DEFAULT_PLUGIN_PATH,
  planDigest,
  allowLive = process.env.IM2A_ALLOW_LIVE_DSH === "1",
} = {}) {
  if (!profileDir) throw new Error("profileDir is required");
  const profile = path.resolve(profileDir);
  if (isLive(profile) && !allowLive) throw new Error("refusing live DSH profile");
  if (!existsSync(path.join(profile, "package.json"))) throw new Error("sandbox profile not initialized");
  if (!existsSync(dshBin)) throw new Error(`dsh CLI not found: ${dshBin}`);
  if (!existsSync(pluginPath)) throw new Error(`plugin path not found: ${pluginPath}`);
  const expectedDigest = sha(JSON.stringify({ profile, pluginPath, kind: "dsh-host-bundle" }));
  if (planDigest && planDigest !== expectedDigest) throw new Error("planDigest mismatch");
  const env = { ...process.env, DSH_HOME: dshHomeFromProfile(profile) };

  return withLock(profile, async () => {
    const before = await snapshot(profile);
    const backup = await backupSnapshot(profile, before);
    const add = runDsh(dshBin, ["plugin", "--profile", profileName(profile), "add", `file:${pluginPath}`], env);
    if (add.status !== 0) {
      await restoreSnapshot(profile, before);
      throw new Error(`dsh plugin add failed: ${add.error ?? ""} ${add.output.slice(-800)}`);
    }
    let dump;
    try {
      const pkg = JSON.parse(await readFile(path.join(profile, "package.json"), "utf8"));
      const bundles = pkg?.dsh?.profile?.bundles ?? [];
      if (!bundles.includes(PLUGIN_NAME)) throw new Error(`bundle not selected: ${PLUGIN_NAME}`);
      dump = await assertComposed(dshBin, profile, env);
    } catch (error) {
      await restoreSnapshot(profile, before);
      throw error;
    }
    const after = await snapshot(profile);
    return {
      mode: "host",
      profile,
      plugin: PLUGIN_NAME,
      planDigest: expectedDigest,
      backup,
      beforeSha256: Object.fromEntries(Object.entries(before).map(([k, v]) => [k, v.sha256])),
      afterSha256: Object.fromEntries(Object.entries(after).map(([k, v]) => [k, v.sha256])),
      dumpSnippet: dump.split(/\r?\n/).filter((line) => /im2a|serverName/.test(line)).slice(0, 12),
    };
  });
}

export async function uninstallHost({
  profileDir,
  dshBin = process.env.DSH_BIN ?? DEFAULT_DSH_BIN,
  planDigest,
  allowLive = process.env.IM2A_ALLOW_LIVE_DSH === "1",
} = {}) {
  const profile = path.resolve(profileDir);
  if (isLive(profile) && !allowLive) throw new Error("refusing live DSH profile");
  const expectedDigest = sha(JSON.stringify({ profile, kind: "dsh-host-bundle-uninstall" }));
  if (planDigest && planDigest !== expectedDigest) throw new Error("planDigest mismatch");
  const env = { ...process.env, DSH_HOME: dshHomeFromProfile(profile) };
  return withLock(profile, async () => {
    const before = await snapshot(profile);
    const backup = await backupSnapshot(profile, before);
    const remove = runDsh(dshBin, ["plugin", "--profile", profileName(profile), "remove", PLUGIN_NAME], env);
    if (remove.status !== 0) {
      await restoreSnapshot(profile, before);
      throw new Error(`dsh plugin remove failed: ${remove.error ?? ""} ${remove.output.slice(-800)}`);
    }
    const dump = runDsh(dshBin, ["--profile", profileName(profile), "--dump-config"], env);
    if (dump.status !== 0) throw new Error(`dump after remove failed: ${dump.output.slice(-500)}`);
    if (/id: im2a-mcp/.test(dump.output)) throw new Error("im2a-mcp row still present after remove");
    const after = await snapshot(profile);
    return {
      mode: "host-uninstall",
      profile,
      plugin: PLUGIN_NAME,
      planDigest: expectedDigest,
      backup,
      beforeSha256: Object.fromEntries(Object.entries(before).map(([k, v]) => [k, v.sha256])),
      afterSha256: Object.fromEntries(Object.entries(after).map(([k, v]) => [k, v.sha256])),
      removedRow: true,
    };
  });
}
