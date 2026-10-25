#!/usr/bin/env node
// SPDX-License-Identifier: MIT
//
// 打包面检查（不联网、不安装依赖）：
//   1) 运行 `npm pack --dry-run --json`，拿到真实会发布的文件清单；
//   2) 断言清单只含 package.json `files` 白名单（外加 npm 必带的 package.json），
//      且白名单中已存在的文件都被打进去（避免"写了白名单却漏包"）；
//   3) 断言没有单个文件 > 1 MiB（与 scripts/check-no-binaries.sh 口径一致）。
//
// npm 不可用时（例如 Node 由独立运行时提供、PATH 无 npm），打印 SKIP + 具体原因并以
// 0 退出。SKIP 不是 PASS：第三方复核请以 CI 里真实 npm 的 pack 结果为准。
//
// 用法：node scripts/pack-check.mjs
// 退出码：0 = 通过或 SKIP；1 = 打包内容越界/超限，或 npm 已存在但执行失败。

import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const PACKAGE_ROOT = fileURLToPath(new URL("../", import.meta.url));
const MAX_BYTES = 1024 * 1024; // 1 MiB
const pkg = JSON.parse(readFileSync(path.join(PACKAGE_ROOT, "package.json"), "utf8"));
const declared = Array.isArray(pkg.files) ? pkg.files : [];

console.log(`pack-check: ${pkg.name}@${pkg.version} (edition=${pkg.edition})`);
console.log(`  files whitelist ${declared.join(", ") || "(empty)"}`);

const located = locateNpm();
if (!located.npm) {
  console.log("SKIP: pack-check 未执行 `npm pack --dry-run --json` —— 本机没有可用的 npm。");
  for (const reason of located.reasons) console.log(`  尝试：${reason}`);
  console.log("  原因：PATH/npm_execpath 均找不到 npm；这是环境限制，不是本包缺陷。");
  console.log("  影响：本次未验证真实 tarball 内容与体积（edition.test.mjs 仍会检查本地 files 存在性）。");
  console.log("  复现：安装 npm 后重跑 `node scripts/pack-check.mjs`；CI 的 npm 任务自带 npm。");
  console.log("  退出码 0（SKIP != PASS）。");
  process.exit(0);
}

console.log(`  npm             ${located.npm.version ?? "?"} (${located.npm.label})`);

const pack = runNpmPack(located.npm);
const failures = [];
const packedPaths = new Set();

if (!Array.isArray(pack.files) || pack.files.length === 0) {
  failures.push("npm pack --dry-run --json 没有返回 files 清单");
} else {
  for (const file of pack.files) {
    const filePath = file.path ?? file.name;
    if (typeof filePath !== "string" || filePath.length === 0) {
      failures.push(`npm pack 返回了无路径的条目：${JSON.stringify(file)}`);
      continue;
    }
    packedPaths.add(filePath);

    if (!isDeclared(filePath)) {
      failures.push(`越界文件：${filePath} 不在 package.json files 白名单内`);
    }

    const size = Number.isFinite(file.size) ? file.size : sourceSize(filePath);
    if (size > MAX_BYTES) {
      failures.push(`超大文件：${filePath} ${formatBytes(size)} > 1 MiB`);
    }
  }
}

// 白名单中"应发布且本地已存在"的条目必须真的在包里。
for (const entry of declared) {
  const isGeneratedDir = entry.endsWith("/");
  if (isGeneratedDir) {
    const abs = path.join(PACKAGE_ROOT, entry);
    if (!existsSync(abs) || !statSync(abs).isDirectory()) continue;
    if (readdirSync(abs).length === 0) continue;
    const included = [...packedPaths].some(
      (p) => p === entry.slice(0, -1) || p.startsWith(entry),
    );
    if (!included) failures.push(`白名单目录 ${entry} 存在但未被打包`);
    continue;
  }

  const abs = path.join(PACKAGE_ROOT, entry);
  if (!existsSync(abs) || !statSync(abs).isFile()) {
    failures.push(`白名单文件 ${entry} 在本地不存在（打包会静默漏掉）`);
    continue;
  }
  if (!packedPaths.has(entry)) failures.push(`白名单文件 ${entry} 未被打包`);
}
if (!packedPaths.has("package.json")) failures.push("package.json 未被打包");

const packedBytes = (pack.files ?? []).reduce(
  (n, f) => n + (Number.isFinite(f.size) ? f.size : 0),
  0,
);
const largest = [...(pack.files ?? [])]
  .filter((f) => Number.isFinite(f.size))
  .sort((a, b) => b.size - a.size)[0];

if (failures.length > 0) {
  for (const failure of failures) console.error(`FAIL: ${failure}`);
  console.error(`RESULT: FAIL (${failures.length} problem(s), ${packedPaths.size} packed files)`);
  process.exit(1);
}

console.log(`  packed files    ${packedPaths.size}`);
console.log(
  `  packed size     ${formatBytes(packedBytes)}` +
    (Number.isFinite(pack.unpackedSize) ? ` (unpacked ${formatBytes(pack.unpackedSize)})` : ""),
);
if (largest) console.log(`  largest file    ${largest.path ?? largest.name} (${formatBytes(largest.size)})`);
console.log("  whitelist       OK (no out-of-whitelist paths)");
console.log("  size limit      OK (all files <= 1 MiB)");
console.log("PACK CHECK OK");

// ---- helpers ----

function isDeclared(filePath) {
  if (filePath === "package.json") return true; // npm 总是打包 package.json
  return declared.some((entry) =>
    entry.endsWith("/")
      ? filePath === entry.slice(0, -1) || filePath.startsWith(entry)
      : filePath === entry,
  );
}

function sourceSize(relPath) {
  try {
    return statSync(path.join(PACKAGE_ROOT, relPath)).size;
  } catch {
    return 0;
  }
}

function formatBytes(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MiB`;
}

/** 找到一个可执行的 npm；返回 { npm, reasons }。 */
function locateNpm() {
  const reasons = [];
  const candidates = [];

  if (process.env.npm_execpath && existsSync(process.env.npm_execpath)) {
    candidates.push({
      kind: "node",
      label: `node ${process.env.npm_execpath}`,
      command: process.execPath,
      prefixArgs: [process.env.npm_execpath],
    });
  }
  if (process.platform === "win32") {
    const comspec = process.env.ComSpec || "cmd.exe";
    for (const label of ["npm.cmd", "npm"]) {
      candidates.push({
        kind: "cmd",
        label,
        command: comspec,
        prefixArgs: ["/d", "/s", "/c", label],
      });
    }
  } else {
    candidates.push({ kind: "direct", label: "npm", command: "npm", prefixArgs: [] });
  }

  for (const candidate of candidates) {
    const result = spawnNpm(candidate, ["--version"], 20_000);
    if (result.error) {
      reasons.push(`${candidate.label} --version → ${result.error.code ?? result.error.message}`);
      continue;
    }
    if (result.status !== 0) {
      const stderr = (result.stderr ?? "").trim().split(/\r?\n/).filter(Boolean).pop();
      reasons.push(`${candidate.label} --version → exit ${result.status}${stderr ? `: ${stderr}` : ""}`);
      continue;
    }
    const version = (result.stdout ?? "").trim().split(/\r?\n/).filter(Boolean).pop();
    return { npm: { ...candidate, version }, reasons };
  }

  if (reasons.length === 0) reasons.push("PATH 中没有 npm / npm.cmd，且未设置 npm_execpath");
  return { npm: null, reasons };
}

function spawnNpm(candidate, args, timeout) {
  if (candidate.kind === "cmd") {
    // 用 cmd.exe /c 而不是 shell:true，避免 Node 对 shell 参数的弃用/注入告警。
    const commandLine = [...candidate.prefixArgs.slice(0, -1), `${candidate.prefixArgs.at(-1)} ${args.join(" ")}`];
    return spawnSync(candidate.command, commandLine, {
      cwd: PACKAGE_ROOT,
      encoding: "utf8",
      timeout,
      maxBuffer: 16 * 1024 * 1024,
      windowsHide: true,
    });
  }
  return spawnSync(candidate.command, [...candidate.prefixArgs, ...args], {
    cwd: PACKAGE_ROOT,
    encoding: "utf8",
    timeout,
    maxBuffer: 16 * 1024 * 1024,
    windowsHide: true,
  });
}

function runNpmPack(npm) {
  const result = spawnNpm(npm, ["pack", "--dry-run", "--json"], 180_000);

  if (result.error) {
    console.error(`ERROR: npm pack 执行失败：${result.error.code ?? result.error.message}`);
    process.exit(1);
  }
  if (result.status !== 0) {
    console.error(`ERROR: npm pack --dry-run --json 退出码 ${result.status}`);
    if (result.stderr) console.error(result.stderr.trim());
    process.exit(1);
  }

  const stdout = result.stdout ?? "";
  const start = stdout.indexOf("[");
  const end = stdout.lastIndexOf("]");
  if (start < 0 || end <= start) {
    console.error("ERROR: npm pack --dry-run --json 输出不是 JSON 数组");
    console.error(stdout.slice(0, 2000));
    if (result.stderr) console.error(result.stderr.trim());
    process.exit(1);
  }

  let parsed;
  try {
    parsed = JSON.parse(stdout.slice(start, end + 1));
  } catch (err) {
    console.error(`ERROR: 无法解析 npm pack JSON：${err.message}`);
    process.exit(1);
  }

  const entry = Array.isArray(parsed) ? parsed[0] : parsed;
  if (!entry || typeof entry !== "object") {
    console.error("ERROR: npm pack JSON 为空");
    process.exit(1);
  }
  return entry;
}
