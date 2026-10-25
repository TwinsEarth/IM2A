// SPDX-License-Identifier: MIT
//
// 包元数据与发布面的一致性测试。
//
// 守护三类漂移：
//   1) package.json / edition.json / src/index.ts 的 edition、version、capabilities；
//   2) openclaw.plugin.json 的 channel 声明与源码的 CHANNEL_ID / CHANNEL_CONFIG_SCHEMA；
//   3) package.json `files` / `scripts` 引用的路径是否真实存在（防止"引用不存在文件"）。
//
// 关于 dist/：它是 `files` 里的**发布期生成目录**（.gitignore 忽略），由发布工具链
// 编译 TS 后产出；本仓库的验证构建（scripts/build.mjs）明确不做 bundle。因此本测试
// 对 `dist/` 的要求是"要么是非空目录，要么是被 runtimeExtensions 引用且由 prepack
// 声明的生成目录"，其余 `files` 条目必须是真实存在的文件。
//
// 运行：node --test test/edition.test.mjs

import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import {
  CHANNEL_CONFIG_SCHEMA,
  CHANNEL_ID,
  COMPATIBLE_CORE,
  EDITION,
  EDITION_CAPABILITIES,
  PLUGIN_VERSION,
  im2aRelationChannel,
} from "../src/index.ts";

const PACKAGE_ROOT = fileURLToPath(new URL("../", import.meta.url));
const pkg = readJson("package.json");
const edition = readJson("edition.json");
const plugin = readJson("openclaw.plugin.json");

function readJson(relPath) {
  return JSON.parse(readFileSync(path.join(PACKAGE_ROOT, relPath), "utf8"));
}

function readText(relPath) {
  return readFileSync(path.join(PACKAGE_ROOT, relPath), "utf8");
}

// ---- package.json ↔ edition.json ----

test("package.json name/version/license/edition align with edition.json", () => {
  assert.equal(pkg.name, "@im2a/openclaw-channel-relation");
  assert.equal(edition.package, pkg.name, "edition.json.package 必须等于 package.json.name");

  assert.equal(pkg.version, "0.1.0");
  assert.equal(edition.pluginVersion, pkg.version, "edition.json.pluginVersion 必须等于 package.json.version");

  assert.equal(pkg.license, "MIT");
  assert.ok(existsSync(path.join(PACKAGE_ROOT, "LICENSE")), "LICENSE 文件必须存在");
  assert.match(readText("LICENSE"), /MIT License/, "LICENSE 必须是 MIT 文本");

  assert.equal(pkg.edition, "openclaw-channel-relation");
  assert.equal(edition.edition, pkg.edition, "edition.json.edition 必须等于 package.json.edition");
  assert.equal(pkg.im2a?.edition, pkg.edition, "package.json.im2a.edition 必须一致");
  assert.equal(pkg.im2a?.pluginVersion, pkg.version, "package.json.im2a.pluginVersion 必须一致");

  assert.deepEqual(pkg.im2a?.capabilities, [...EDITION_CAPABILITIES]);
  assert.deepEqual(edition.capabilities, [...EDITION_CAPABILITIES]);

  assert.equal(edition.platform, "openclaw");
  assert.equal(edition.coreVersionSource, "internal/version/version.go");
  assert.equal(pkg.im2a?.coreVersionSource, "internal/version/version.go");
  assert.equal(pkg.compatibleCore, pkg.im2a?.compatibleCore);
});

test("src constants stay in sync with package.json", () => {
  assert.equal(EDITION, pkg.edition);
  assert.equal(PLUGIN_VERSION, pkg.version);
  assert.equal(COMPATIBLE_CORE, pkg.compatibleCore);
  assert.equal(CHANNEL_ID, pkg.openclaw?.channel?.id);
  assert.deepEqual([...EDITION_CAPABILITIES], pkg.im2a?.capabilities);
  assert.equal(im2aRelationChannel.id, CHANNEL_ID);
  assert.equal(im2aRelationChannel.meta.id, CHANNEL_ID);
});

// ---- openclaw.plugin.json ----

test("openclaw.plugin.json exists and declares channel id im2a-relation", () => {
  const pluginPath = path.join(PACKAGE_ROOT, "openclaw.plugin.json");
  assert.ok(existsSync(pluginPath), "openclaw.plugin.json 必须存在");
  assert.ok(statSync(pluginPath).isFile());

  assert.equal(plugin.id, "im2a-relation");
  assert.equal(plugin.id, CHANNEL_ID);
  assert.deepEqual(plugin.channels, ["im2a-relation"]);
  assert.ok(plugin.channels.includes(CHANNEL_ID));
  assert.equal(plugin.version, pkg.version);
  assert.equal(typeof plugin.name, "string");
  assert.equal(typeof plugin.description, "string");

  assert.ok(
    Object.prototype.hasOwnProperty.call(plugin.channelConfigs ?? {}, CHANNEL_ID),
    `channelConfigs 必须包含 ${CHANNEL_ID}`,
  );
  assert.deepEqual(
    plugin.channelConfigs[CHANNEL_ID].schema,
    CHANNEL_CONFIG_SCHEMA,
    "openclaw.plugin.json 的 channelConfigs schema 与 src 的 CHANNEL_CONFIG_SCHEMA 漂移",
  );
  assert.equal(plugin.channelConfigs[CHANNEL_ID].label, im2aRelationChannel.meta.label);
});

test("openclaw entry metadata points at existing entry files and the same npm spec", () => {
  assert.equal(pkg.type, "module");
  assert.deepEqual(pkg.openclaw?.extensions, ["./index.ts"]);
  assert.ok(existsSync(path.join(PACKAGE_ROOT, "index.ts")), "index.ts 入口必须存在");
  assert.deepEqual(pkg.openclaw?.runtimeExtensions, ["./dist/src/index.js"]);
  assert.equal(pkg.openclaw?.install?.npmSpec, pkg.name);
  assert.equal(pkg.openclaw?.channel?.id, CHANNEL_ID);
  assert.deepEqual(pkg.openclaw?.channel?.aliases, [...im2aRelationChannel.meta.aliases]);
});

// ---- files 白名单 ----

test("every path in package.json files exists (generated dist/ handled explicitly)", () => {
  assert.ok(Array.isArray(pkg.files) && pkg.files.length > 0, "files 必须是非空数组");
  const allowed = new Set(pkg.files);

  // 必需的可发布文件必须在白名单里。
  for (const required of ["index.ts", "openclaw.plugin.json", "edition.json", "README.md", "LICENSE"]) {
    assert.ok(allowed.has(required), `files 必须包含 ${required}`);
  }

  for (const entry of pkg.files) {
    const abs = path.join(PACKAGE_ROOT, entry);
    if (entry.endsWith("/")) {
      // 生成目录：存在时不得为空；缺失时必须由 runtimeExtensions + prepack 声明为生成物。
      assert.ok(
        pkg.openclaw?.runtimeExtensions?.some((p) => p.replace(/^\.\//, "").startsWith(entry)),
        `files 中的目录 ${entry} 必须被 runtimeExtensions 引用（否则是死条目）`,
      );
      assert.equal(typeof pkg.scripts?.prepack, "string", `files 中的目录 ${entry} 必须有 prepack 生成链路`);
      if (existsSync(abs)) {
        assert.ok(statSync(abs).isDirectory(), `${entry} 必须是目录`);
        assert.ok(readdirSync(abs).length > 0, `${entry} 存在但为空；发布时会静默丢包`);
      }
      continue;
    }
    assert.ok(existsSync(abs), `files 列出的路径不存在：${entry}`);
    assert.ok(statSync(abs).isFile(), `files 列出的路径不是文件：${entry}`);
  }

  assert.equal(allowed.size, pkg.files.length, "files 不应有重复条目");
});

// ---- scripts 与零依赖 ----

test("package.json scripts reference existing local files and cover the full check chain", () => {
  for (const name of ["build", "test", "conformance", "check", "pack:check", "prepack"]) {
    assert.equal(typeof pkg.scripts?.[name], "string", `缺少 scripts.${name}`);
  }

  for (const [name, command] of Object.entries(pkg.scripts)) {
    for (const token of command.split(/\s+/)) {
      if (!/\.(?:mjs|ts)$/.test(token)) continue;
      const rel = token.replace(/^\.\//, "");
      assert.ok(existsSync(path.join(PACKAGE_ROOT, rel)), `scripts.${name} 引用了不存在的文件：${rel}`);
    }
  }

  // npm test 必须覆盖五个测试文件（与 CI/第三方复核入口一致）。
  for (const file of [
    "test/edition.test.mjs",
    "test/relation.test.mjs",
    "test/paid-attention.test.mjs",
    "test/ui.test.mjs",
    "test/conformance.test.mjs",
  ]) {
    assert.ok(pkg.scripts.test.includes(file), `scripts.test 未包含 ${file}`);
    assert.ok(existsSync(path.join(PACKAGE_ROOT, file)), `${file} 必须存在`);
  }
  assert.ok(pkg.scripts.test.startsWith("node scripts/build.mjs"), "测试前必须先跑 build");
  assert.ok(pkg.scripts.check.includes("pack-check.mjs"), "check 必须包含 pack-check");
});

test("zero npm dependencies are declared (no dependencies/devDependencies)", () => {
  for (const field of ["dependencies", "devDependencies", "optionalDependencies", "bundledDependencies"]) {
    assert.equal(pkg[field], undefined, `不得声明 ${field}（本包必须零依赖）`);
  }
  // peerDependencies 是宿主契约，允许保留，但不得变成安装依赖。
  assert.deepEqual(Object.keys(pkg.peerDependencies ?? {}), ["openclaw"]);
});

test("engines.node declares native TypeScript support (>=22.18.0)", () => {
  const range = pkg.engines?.node;
  assert.equal(typeof range, "string", "engines.node 必须写明最低 Node 版本");
  const match = /^>=\s*(\d+)\.(\d+)\.(\d+)$/.exec(range.trim());
  assert.ok(match, `engines.node 必须是 >=x.y.z 形式，实际 ${JSON.stringify(range)}`);
  const [major, minor] = [Number(match[1]), Number(match[2])];
  assert.ok(
    major > 22 || (major === 22 && minor >= 18),
    `engines.node (${range}) 必须 >= 22.18.0（原生 TS type stripping）`,
  );

  const [currentMajor, currentMinor] = process.versions.node.split(".").map((n) => Number(n));
  assert.ok(
    currentMajor > major || (currentMajor === major && currentMinor >= minor),
    `当前 Node ${process.versions.node} 不满足 engines.node ${range}`,
  );
});

// ---- README ----

test("README.md documents positioning, verification commands, and capability boundaries", () => {
  assert.ok(existsSync(path.join(PACKAGE_ROOT, "README.md")), "README.md 必须存在（files 引用了它）");
  const readme = readText("README.md");
  assert.ok(readme.length > 1_000, "README 过短，需包含定位/用法/边界");
  for (const needle of [
    "internal/version/version.go",
    "npm test",
    "npm run conformance",
    "npm run pack:check",
    "能力边界",
    "K0",
    "dist/",
    "22.18",
  ]) {
    assert.ok(readme.includes(needle), `README 必须说明 ${needle}`);
  }
});
