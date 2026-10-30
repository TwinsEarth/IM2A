/**
 * IM2A DSH adapter (sandbox-first, P0)
 *
 * Safety contract:
 * - detect() reads the real DSH profile but never writes it.
 * - plan() is read-only and redacts credentials in previews.
 * - apply() refuses the live desktop profile unless IM2A_ALLOW_LIVE_DSH=1.
 * - rollback() refuses to overwrite a file that changed after apply.
 * - verify() runs the real MCP bridge; a missing token is BLOCKED, not PASS.
 */
import { createHash, randomUUID } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { copyFile, mkdir, readFile, rename, stat, unlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { PLUGIN_NAME, installHost as installHostBundle, uninstallHost as uninstallHostBundle } from "./host-install.mjs";
import { verifyMcp } from "./verify.mjs";
import { verifyMcpFile } from "./verify-file.mjs";

export const MARKER_BEGIN = "# >>> im2a-dsh-adapter managed";
export const MARKER_END = "# <<< im2a-dsh-adapter managed";
export const DEFAULT_DSH_HOME = path.join(os.homedir(), ".dsh");
export const DEFAULT_PROFILE = path.join(DEFAULT_DSH_HOME, "profiles", "desktop");
export const DEFAULT_MCP_BIN = "E:\\GPT\\release\\w2m-im2a-2.0.3-rc3\\im2a-mcp.exe";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const now = () => new Date().toISOString();

function resolveProfile(options = {}) {
  return path.resolve(options.profileDir ?? process.env.DSH_PROFILE_DIR ?? DEFAULT_PROFILE);
}

function isLiveProfile(profileDir) {
  return path.resolve(profileDir) === path.resolve(process.env.DSH_PROFILE_DIR ?? DEFAULT_PROFILE);
}

function entryBlock({ mcpBin, harnessUrl = "http://127.0.0.1:18080" }) {
  const command = JSON.stringify(mcpBin);
  return [
    MARKER_BEGIN,
    "- insert:",
    "    - id: im2a-mcp",
    '      name: "@deepseek-ai/dsh-mcp-client"',
    "      config:",
    "        serverName: im2a",
    "        transport: stdio",
    `        command: ${command}`,
    "        args: []",
    "        env:",
    `          IM2A_HARNESS_URL: ${JSON.stringify(harnessUrl)}`,
    "          IM2A_HARNESS_TOKEN: !!js process.env.IM2A_HARNESS_TOKEN",
    '          IM2A_HARNESS_ID: "deepseek-harness"',
    '          IM2A_AGENT_NAME: "dsh"',
    "        toolCallTimeoutMs: 60000",
    "        failOnStartupError: false",
    "        reconnect:",
    "          enabled: true",
    "          initialDelayMs: 500",
    "          maxDelayMs: 30000",
    "          maxAttempts: 10",
    "        # credential is read from the DSH process environment, never embedded",
    MARKER_END,
  ].join("\n");
}

function redactedPreview(block) {
  return block.replace(/IM2A_HARNESS_TOKEN:\s+.*$/m, "IM2A_HARNESS_TOKEN: <redacted>");
}

function mergePatch(existing, block) {
  const text = existing.replace(/^\uFEFF/, "");
  if (text.includes(MARKER_BEGIN)) {
    if (text.includes(block)) return { text, changed: false };
    throw new Error("im2a marker exists with different content; refusing to merge");
  }
  const trimmed = text.trim();
  if (trimmed === "[]" || trimmed === "") {
    return { text: `${block}\n`, changed: true };
  }
  return { text: `${text.replace(/\s*$/, "")}\n${block}\n`, changed: true };
}

function withoutBlock(text) {
  const begin = text.indexOf(MARKER_BEGIN);
  const end = text.indexOf(MARKER_END);
  if (begin < 0 || end < 0 || end < begin) return text;
  const before = text.slice(0, begin).replace(/\s+$/, "");
  const after = text.slice(end + MARKER_END.length).replace(/^\s+/, "");
  return `${before}${after ? `\n${after}` : ""}${before || after ? "\n" : ""}`;
}

async function atomicWrite(file, text) {
  const tmp = `${file}.tmp-${process.pid}-${randomUUID().slice(0, 8)}`;
  await writeFile(tmp, text, { encoding: "utf8", mode: 0o600 });
  await rename(tmp, file);
}

export function createDshAdapter(options = {}) {
  const mcpBin = options.mcpBin ?? process.env.IM2A_MCP_BIN ?? DEFAULT_MCP_BIN;
  const dshHome = path.resolve(options.dshHome ?? process.env.DSH_HOME ?? DEFAULT_DSH_HOME);

  return {
    id: "dsh",
    version: "0.1.0",
    contractVersion: "sandbox-v1",

    async detect({ profileDir } = {}) {
      const profile = resolveProfile({ profileDir });
      const patch = path.join(profile, "cordis.patch.yml");
      const packageFile = path.join(profile, "package.json");
      const mcpClient = path.join(profile, "node_modules", "@deepseek-ai", "dsh-mcp-client");
      return {
        installed: existsSync(patch) && existsSync(packageFile),
        version: existsSync(packageFile) ? "profile-present" : null,
        configPaths: [patch],
        platform: process.platform,
        dshHome,
        profile,
        mcpClientInstalled: existsSync(mcpClient),
        mcpBin,
        mcpBinPresent: existsSync(mcpBin),
      };
    },

    async installHost({ profileDir, allowLive = false } = {}) {
      return installHostBundle({ profileDir: resolveProfile({ profileDir }), allowLive });
    },

    async uninstallHost({ profileDir, allowLive = false } = {}) {
      return uninstallHostBundle({ profileDir: resolveProfile({ profileDir }), allowLive });
    },

    async planHost({ profileDir } = {}) {
      const profile = resolveProfile({ profileDir });
      const pluginPath = options.pluginPath ?? process.env.IM2A_DSH_PLUGIN_PATH ?? path.resolve(
        path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1")),
        "..", "..", "im2a-dsh-plugin",
      );
      return {
        mode: "host",
        planDigest: sha256(JSON.stringify({ profile, pluginPath, kind: "dsh-host-bundle" })),
        changed: true,
        steps: [{
          action: "install",
          path: profile,
          preview: `dsh plugin --profile ${path.basename(profile)} add file:${pluginPath}`,
          redacted: true,
        }],
      };
    },

    async plan({ profileDir } = {}) {
      return this.planHost({ profileDir });
    },

    async planPatch({ profileDir } = {}) {
      const profile = resolveProfile({ profileDir });
      const patch = path.join(profile, "cordis.patch.yml");
      const before = existsSync(patch) ? await readFile(patch, "utf8") : "";
      const block = entryBlock({ mcpBin, harnessUrl: options.harnessUrl ?? process.env.IM2A_HARNESS_URL });
      const merged = mergePatch(before, block);
      const planDigest = sha256(JSON.stringify({ profile, before: sha256(before), block }));
      return {
        planDigest,
        beforeSha256: sha256(before),
        steps: [{
          action: "write",
          path: patch,
          expectedSha256: sha256(before),
          afterSha256: sha256(merged.text),
          preview: redactedPreview(block),
          redacted: true,
        }],
        changed: merged.changed,
      };
    },

    async apply({ profileDir, plan, mode } = {}) {
      const profile = resolveProfile({ profileDir });
      if (isLiveProfile(profile) && process.env.IM2A_ALLOW_LIVE_DSH !== "1") {
        throw new Error("refusing live DSH profile; set IM2A_ALLOW_LIVE_DSH=1 only for an approved maintenance window");
      }
      if (mode === "host" || plan?.mode === "host") {
        return installHostBundle({ profileDir: profile, planDigest: plan?.planDigest, allowLive: false });
      }
      if (!plan?.steps?.length) throw new Error("plan is required");
      const step = plan.steps[0];
      const patch = path.resolve(step.path);
      if (path.dirname(patch) !== path.resolve(profile)) throw new Error("plan path escaped profile");
      const before = existsSync(patch) ? await readFile(patch, "utf8") : "";
      if (sha256(before) !== step.expectedSha256) {
        throw new Error("profile changed since plan; refusing to overwrite");
      }
      if (!plan.changed) {
        return { planDigest: plan.planDigest, changed: [], backups: [], noop: true };
      }
      const block = entryBlock({ mcpBin, harnessUrl: options.harnessUrl ?? process.env.IM2A_HARNESS_URL });
      const merged = mergePatch(before, block);
      const backupDir = path.join(profile, ".bak");
      await mkdir(backupDir, { recursive: true });
      const backup = path.join(backupDir, `im2a-${Date.now()}-${sha256(before).slice(0, 12)}.yml`);
      await writeFile(backup, before, { encoding: "utf8", mode: 0o600 });
      await atomicWrite(patch, merged.text);
      return {
        planDigest: plan.planDigest,
        changed: [patch],
        backups: [backup],
        beforeSha256: sha256(before),
        afterSha256: sha256(merged.text),
        noop: false,
      };
    },

    async verify({ profileDir, env = process.env } = {}) {
      const profile = resolveProfile({ profileDir });
      const patch = path.join(profile, "cordis.patch.yml");
      const text = existsSync(patch) ? await readFile(patch, "utf8") : "";
      const pkgFile = path.join(profile, "package.json");
      const pkg = existsSync(pkgFile) ? JSON.parse(await readFile(pkgFile, "utf8")) : null;
      const hostInstalled = pkg?.dsh?.profile?.bundles?.includes(PLUGIN_NAME) === true;
      const patchInstalled = text.includes(MARKER_BEGIN) && text.includes(MARKER_END);
      const checks = [{
        name: "profile-mcp-installed",
        ok: hostInstalled || patchInstalled,
        detail: hostInstalled ? `bundle=${PLUGIN_NAME}` : (patchInstalled ? "sandbox patch marker" : "no host bundle or patch marker"),
      }];
      const mcpBinSha256 = existsSync(mcpBin)
        ? createHash("sha256").update(readFileSync(mcpBin)).digest("hex")
        : null;
      const withEvidence = (result) => ({
        ...result,
        evidence: {
          schemaVersion: "1.0",
          checkedAt: now(),
          adapter: "dsh",
          profile,
          mcpBin,
          mcpBinSha256,
          tool: "im2a_harness_list",
          transport: result.transport ?? "pipe",
          result: {
            ok: result.ok,
            status: result.status,
            code: result.code ?? null,
            harnessCount: result.harnessCount ?? null,
          },
          checks: result.checks ?? checks,
          credential: "process-env:IM2A_HARNESS_TOKEN (not recorded)",
        },
      });
      if (!checks[0].ok) return withEvidence({ ok: false, status: "blocked", code: "CONFIG_MISSING", checks });
      if (!existsSync(mcpBin)) {
        checks.push({ name: "mcp-binary", ok: false, detail: `not found: ${mcpBin}` });
        return withEvidence({ ok: false, status: "blocked", code: "BINARY_MISSING", checks });
      }
      const token = env.IM2A_HARNESS_TOKEN;
      if (!token) {
        checks.push({ name: "mcp-token", ok: false, detail: "IM2A_HARNESS_TOKEN missing; real tool call would be 401" });
        return withEvidence({ ok: false, status: "blocked", code: "TOKEN_MISSING", checks });
      }
      const url = env.IM2A_HARNESS_URL ?? options.harnessUrl ?? "http://127.0.0.1:18080";
      const verifyEnv = {
        ...env,
        IM2A_HARNESS_ID: env.IM2A_HARNESS_ID ?? "deepseek-harness",
        IM2A_AGENT_NAME: env.IM2A_AGENT_NAME ?? "dsh",
      };
      let result = await verifyMcp({ mcpBin, env: verifyEnv, url });
      if (result.code === "SPAWN_BLOCKED") {
        result = verifyMcpFile({ mcpBin, env: verifyEnv, url });
      }
      return withEvidence({ ...result, checks: [...checks, ...(result.checks ?? [])] });
    },

    async rollback({ profileDir, applied } = {}) {
      const profile = resolveProfile({ profileDir });
      if (applied?.mode === "host" || applied?.mode === "host-uninstall") {
        return uninstallHostBundle({ profileDir: profile, allowLive: false });
      }
      return this.rollbackPatch({ profileDir: profile, applied });
    },

    async rollbackPatch({ profileDir, applied } = {}) {
      const profile = resolveProfile({ profileDir });
      if (!applied?.backups?.length) throw new Error("rollback requires apply result with backups");
      const patch = path.join(profile, "cordis.patch.yml");
      const backup = applied.backups[0];
      const current = await readFile(patch, "utf8");
      if (sha256(current) !== applied.afterSha256) {
        throw new Error("refusing rollback: profile changed after apply");
      }
      const restored = await readFile(backup, "utf8");
      await atomicWrite(patch, restored);
      if (sha256(restored) !== applied.beforeSha256 && applied.beforeSha256 !== sha256("")) {
        throw new Error("rollback byte comparison failed");
      }
      return { restored: [patch], backup };
    },
  };
}
