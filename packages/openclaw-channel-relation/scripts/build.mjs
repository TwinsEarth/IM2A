#!/usr/bin/env node
// SPDX-License-Identifier: MIT
//
// IM2A Relation · 验证构建（validation build）
//
// 本脚本**不引入任何依赖、不做 bundle、不生成 dist/**。它的职责只有一件事：
// 证明源码入口在目标 Node 运行时下可加载，且 edition 元数据与代码里的常量不漂移。
//
// 为什么需要原生 TS：本包以 .ts 源码直接发布（宿主负责编译），验证构建用 Node 的
// type stripping 直接 import `src/index.ts`。Node >= 22.18 默认开启；低于此版本
// 请升级 Node（package.json 的 engines.node 已写明）。
//
// 退出码：0 = 通过；1 = 加载失败 / 导出缺失 / 元数据漂移。

import { readFileSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import { isDeepStrictEqual } from "node:util";
import path from "node:path";

const PACKAGE_ROOT = fileURLToPath(new URL("../", import.meta.url));
const ENTRY = path.join(PACKAGE_ROOT, "src", "index.ts");

// ---- 0) 运行时能力（先于 import 检查，给出可操作的错误而不是堆栈） ----

const [nodeMajor, nodeMinor] = process.versions.node.split(".").map((n) => Number.parseInt(n, 10));
const nodeSupportsTypeStripping =
  nodeMajor > 22 || (nodeMajor === 22 && nodeMinor >= 18);

if (!nodeSupportsTypeStripping) {
  fail(
    `Node ${process.versions.node} 不支持原生 TypeScript type stripping；` +
      "需要 Node >= 22.18.0（或用 --experimental-strip-types）。",
  );
}
if (process.features.typescript !== "strip" && process.features.typescript !== "transform") {
  fail(
    `当前 Node 的 TypeScript 支持为 ${JSON.stringify(process.features.typescript)}；` +
      "需要 process.features.typescript === 'strip'（Node >= 22.18 默认开启）。",
  );
}

// ---- 1) 读取元数据 ----

const pkg = readJson("package.json");
const edition = readJson("edition.json");

// ---- 2) 加载源码入口并断言关键导出 ----

let mod;
try {
  mod = await import(pathToFileURL(ENTRY).href);
} catch (err) {
  fail(`无法 import ${path.relative(PACKAGE_ROOT, ENTRY)}: ${err?.stack ?? err}`);
}

const REQUIRED_EXPORTS = [
  "EDITION",
  "PLUGIN_VERSION",
  "COMPATIBLE_CORE",
  "CHANNEL_ID",
  "EDITION_CAPABILITIES",
  "im2aRelationChannel",
];

for (const name of REQUIRED_EXPORTS) {
  if (typeof mod[name] === "undefined") {
    fail(`缺少必需导出：${name}（src/index.ts）`);
  }
}

if (mod.EDITION !== "openclaw-channel-relation") {
  fail(`EDITION = ${JSON.stringify(mod.EDITION)}，期望 "openclaw-channel-relation"`);
}
if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(String(mod.PLUGIN_VERSION))) {
  fail(`PLUGIN_VERSION 不是合法 semver：${JSON.stringify(mod.PLUGIN_VERSION)}`);
}
if (mod.im2aRelationChannel?.id !== mod.CHANNEL_ID) {
  fail(
    `im2aRelationChannel.id (${JSON.stringify(mod.im2aRelationChannel?.id)}) ` +
      `!= CHANNEL_ID (${JSON.stringify(mod.CHANNEL_ID)})`,
  );
}
if (typeof mod.im2aRelationChannel?.config?.resolveAccount !== "function") {
  fail("im2aRelationChannel.config.resolveAccount 缺失（宿主 channel 契约）");
}
if (typeof mod.default?.register !== "function") {
  fail("默认导出缺少 register()（OpenClaw 插件入口契约）");
}

// ---- 3) edition.json / package.json / 代码常量三方一致 ----

expectEqual("package.json edition", pkg.edition, edition.edition);
expectEqual("代码常量 EDITION", mod.EDITION, pkg.edition);
expectEqual("package.json version", pkg.version, edition.pluginVersion);
expectEqual("代码常量 PLUGIN_VERSION", mod.PLUGIN_VERSION, pkg.version);
expectEqual("package.json im2a.edition", pkg.im2a?.edition, pkg.edition);
expectEqual("package.json im2a.pluginVersion", pkg.im2a?.pluginVersion, pkg.version);
expectDeepEqual("package.json im2a.capabilities", pkg.im2a?.capabilities, [
  ...mod.EDITION_CAPABILITIES,
]);
expectDeepEqual("edition.json capabilities", edition.capabilities, [
  ...mod.EDITION_CAPABILITIES,
]);
expectEqual("package.json compatibleCore", pkg.compatibleCore, mod.COMPATIBLE_CORE);
expectEqual("package.json openclaw.channel.id", pkg.openclaw?.channel?.id, mod.CHANNEL_ID);
expectEqual("edition.json coreVersionSource", edition.coreVersionSource, "internal/version/version.go");
expectEqual("package.json im2a.coreVersionSource", pkg.im2a?.coreVersionSource, "internal/version/version.go");

// 已知元数据漂移：只警告、不阻断（语义以 package.json / editions.json / 代码常量为准）。
// 纯函数门控的 reasonCode 才是协议口径；compatibleCore 只影响安装时的版本说明。
if (edition.compatibleCore !== mod.COMPATIBLE_CORE) {
  warn(
    `edition.json compatibleCore (${JSON.stringify(edition.compatibleCore)}) ` +
      `!= package.json/代码 (${JSON.stringify(mod.COMPATIBLE_CORE)})；` +
      "以 package.json 为准，待 edition 负责人同步 edition.json。",
  );
}

// ---- 4) 冒烟：纯函数在加载后立即可用 ----

const smoke = mod.decide({}, { scope: "schedule", trustLevel: 0 });
if (
  smoke?.allowed !== true ||
  smoke?.requiresApproval !== false ||
  smoke?.reasonCode !== "OK"
) {
  fail(`decide 冒烟失败：${JSON.stringify(smoke)}`);
}

// ---- 5) 摘要 ----

const levelCount = Object.keys(mod.PERMISSION_MATRIX ?? {}).length;
const scopeCount = mod.SCOPES.length;
const cellCount = Object.values(mod.PERMISSION_MATRIX ?? {}).reduce(
  (n, row) => n + Object.keys(row).length,
  0,
);

console.log("IM2A Relation · build (validation only, no bundling, no dependencies)");
console.log(`  node             ${process.versions.node} (typescript=${process.features.typescript})`);
console.log(`  entry            ${path.relative(PACKAGE_ROOT, ENTRY)}`);
console.log(`  edition          ${mod.EDITION}`);
console.log(`  plugin version   ${mod.PLUGIN_VERSION}`);
console.log(`  channel id       ${mod.CHANNEL_ID}`);
console.log(`  compatible core  ${mod.COMPATIBLE_CORE}`);
console.log(`  capabilities     ${mod.EDITION_CAPABILITIES.join(", ")}`);
console.log(`  matrix           ${levelCount} levels x ${scopeCount} scopes = ${cellCount} cells`);
console.log(`  exports checked  ${REQUIRED_EXPORTS.join(", ")}`);
console.log(`  smoke            decide(L0, schedule) -> ${smoke.reasonCode}`);
console.log(`  metadata         edition.json matches package.json (edition/version)`);
console.log(`  dist/            not emitted (verification build; release toolchain owns dist)`);
console.log("BUILD OK");

// ---- helpers ----

function readJson(relPath) {
  const absPath = path.join(PACKAGE_ROOT, relPath);
  try {
    return JSON.parse(readFileSync(absPath, "utf8"));
  } catch (err) {
    fail(`无法读取/解析 ${relPath}: ${err.message}`);
  }
}

function expectEqual(label, actual, expected) {
  if (actual !== expected) {
    fail(`${label} 漂移：实际 ${JSON.stringify(actual)}，期望 ${JSON.stringify(expected)}`);
  }
}

function expectDeepEqual(label, actual, expected) {
  if (!isDeepStrictEqual(actual, expected)) {
    fail(
      `${label} 漂移：实际 ${JSON.stringify(actual)}，` +
        `期望 ${JSON.stringify(expected)}`,
    );
  }
}

function warn(message) {
  console.warn(`WARN: ${message}`);
}

function fail(message) {
  console.error(`BUILD FAILED: ${message}`);
  process.exit(1);
}
