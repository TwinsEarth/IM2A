#!/usr/bin/env node
// SPDX-License-Identifier: MIT
//
// relation-gating 一致性 CLI（独立进程，供 CI / 第三方复核调用）。
//
// 读取 spec/vectors/relation-gating.v1.json，用本包的纯函数 decide() 逐条比对
// {allowed, requiresApproval, reasonCode}；任何不一致都打印向量 id 与差异并退出 1。
//
// 用法：
//   node scripts/conformance.mjs
//   node scripts/conformance.mjs --vectors <path>
//   node scripts/conformance.mjs --json
//   RELATION_VECTORS=<path> node scripts/conformance.mjs
//
// 退出码：0 = 全部一致；1 = 向量文件不可用 / 结构非法 / 任一向量不一致。

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import { decide } from "../src/relation.ts";

const PACKAGE_ROOT = fileURLToPath(new URL("../", import.meta.url));
const DEFAULT_VECTORS = path.resolve(
  PACKAGE_ROOT,
  "../../spec/vectors/relation-gating.v1.json",
);
const EXPECTED_SPEC = "relation-gating/v1";

const argv = process.argv.slice(2);
const opts = parseArgs(argv);

if (opts.help) {
  printUsage();
  process.exit(0);
}
if (opts.error) {
  console.error(`ERROR: ${opts.error}`);
  printUsage();
  process.exit(1);
}

const vectorsPath = path.resolve(opts.vectors);

let file;
try {
  file = JSON.parse(readFileSync(vectorsPath, "utf8"));
} catch (err) {
  fail(
    `无法读取向量文件 ${vectorsPath}: ${err.message}\n` +
      "提示：本包位于 im2agent 仓库内时默认路径为 spec/vectors/relation-gating.v1.json；" +
      "独立检出请用 --vectors <path> 指定。",
  );
}

const structureError = validateVectorsFile(file);
if (structureError) {
  fail(`向量文件结构非法 (${vectorsPath}): ${structureError}`);
}

const failures = [];
let passed = 0;

for (const vector of file.vectors) {
  let actual;
  try {
    actual = decide(vector.input.capability, vector.input.call);
  } catch (err) {
    failures.push({
      id: vector.id,
      description: vector.description,
      category: vector.category,
      error: `decide() threw: ${err?.message ?? String(err)}`,
    });
    continue;
  }

  const diffs = [];
  for (const field of ["allowed", "requiresApproval", "reasonCode"]) {
    if (actual[field] !== vector.expect[field]) {
      diffs.push({ field, expected: vector.expect[field], actual: actual[field] });
    }
  }

  if (diffs.length === 0) {
    passed += 1;
    continue;
  }

  failures.push({
    id: vector.id,
    description: vector.description,
    category: vector.category,
    diffs,
    expected: {
      allowed: vector.expect.allowed,
      requiresApproval: vector.expect.requiresApproval,
      reasonCode: vector.expect.reasonCode,
    },
    actual: {
      allowed: actual.allowed,
      requiresApproval: actual.requiresApproval,
      reasonCode: actual.reasonCode,
    },
    reason: actual.reason,
  });
}

const total = file.vectors.length;
const failed = failures.length;
const ok = failed === 0;

if (opts.json) {
  const report = {
    ok,
    spec: file.spec,
    impl: "@im2a/openclaw-channel-relation",
    vectors: vectorsPath,
    total,
    passed,
    failed,
    failures,
  };
  console.log(JSON.stringify(report, null, 2));
  process.exit(ok ? 0 : 1);
}

console.log(
  `relation-gating conformance (spec=${file.spec}, vectors=${vectorsPath}, total=${total})`,
);

for (const failure of failures) {
  console.log(`FAIL ${failure.id} [${failure.category}]`);
  if (failure.error) {
    console.log(`  error     ${failure.error}`);
  } else {
    for (const diff of failure.diffs) {
      console.log(`  ${diff.field}: expected ${JSON.stringify(diff.expected)}, actual ${JSON.stringify(diff.actual)}`);
    }
    console.log(`  reason    ${failure.reason}`);
  }
  console.log(`  vector    ${failure.description}`);
}

console.log(`RESULT: ${passed}/${total} PASS, ${failed} FAIL`);
process.exit(ok ? 0 : 1);

// ---- helpers ----

function parseArgs(args) {
  const parsed = {
    vectors: process.env.RELATION_VECTORS || DEFAULT_VECTORS,
    json: false,
    help: false,
  };

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === "--json") {
      parsed.json = true;
    } else if (arg === "--help" || arg === "-h") {
      parsed.help = true;
    } else if (arg === "--vectors" || arg === "-v") {
      const value = args[i + 1];
      if (!value) return { ...parsed, error: `${arg} 需要参数` };
      parsed.vectors = value;
      i += 1;
    } else if (arg.startsWith("--vectors=")) {
      const value = arg.slice("--vectors=".length);
      if (!value) return { ...parsed, error: "--vectors= 需要参数" };
      parsed.vectors = value;
    } else if (arg.startsWith("-")) {
      return { ...parsed, error: `未知参数：${arg}` };
    } else {
      return { ...parsed, error: `未知位置参数：${arg}（请用 --vectors <path>）` };
    }
  }
  return parsed;
}

function validateVectorsFile(value) {
  if (!value || typeof value !== "object") return "顶层必须是 JSON 对象";
  if (value.version !== 1) return `version 必须是 1，实际 ${JSON.stringify(value.version)}`;
  if (value.spec !== EXPECTED_SPEC) return `spec 必须是 "${EXPECTED_SPEC}"，实际 ${JSON.stringify(value.spec)}`;
  if (!Array.isArray(value.vectors)) return "vectors 必须是数组";
  if (value.vectors.length === 0) return "vectors 不能为空";

  for (const [index, vector] of value.vectors.entries()) {
    const where = `vectors[${index}]`;
    if (!vector || typeof vector !== "object") return `${where} 必须是对象`;
    if (typeof vector.id !== "string" || vector.id.length === 0) return `${where}.id 缺失`;
    if (typeof vector.description !== "string") return `${where}.description 缺失`;
    if (typeof vector.category !== "string") return `${where}.category 缺失`;
    if (!vector.input || typeof vector.input !== "object") return `${where}.input 缺失`;
    if (!vector.input.capability || typeof vector.input.capability !== "object") {
      return `${where}.input.capability 缺失`;
    }
    if (!vector.input.call || typeof vector.input.call !== "object") {
      return `${where}.input.call 缺失`;
    }
    if (!vector.expect || typeof vector.expect !== "object") return `${where}.expect 缺失`;
    for (const field of ["allowed", "requiresApproval", "reasonCode"]) {
      if (!(field in vector.expect)) return `${where}.expect.${field} 缺失`;
    }
    if (typeof vector.expect.allowed !== "boolean") return `${where}.expect.allowed 必须是 boolean`;
    if (typeof vector.expect.requiresApproval !== "boolean") return `${where}.expect.requiresApproval 必须是 boolean`;
    if (typeof vector.expect.reasonCode !== "string") return `${where}.expect.reasonCode 必须是 string`;
  }
  return null;
}

function printUsage() {
  console.error(
    [
      "usage: node scripts/conformance.mjs [--vectors <path>] [--json]",
      "",
      `default vectors: ${DEFAULT_VECTORS}`,
      "env: RELATION_VECTORS=<path>",
      "exit: 0 = all vectors match; 1 = mismatch or invalid input",
    ].join("\n"),
  );
}

function fail(message) {
  console.error(`ERROR: ${message}`);
  process.exit(1);
}
