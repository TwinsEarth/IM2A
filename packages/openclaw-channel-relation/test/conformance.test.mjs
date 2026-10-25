// SPDX-License-Identifier: MIT
//
// 公开向量一致性测试（与 scripts/conformance.mjs 同一口径，但作为 node:test 用例执行）。
//
// 数据源：spec/vectors/relation-gating.v1.json（版本 1，115 条，含 matrix/order/unknown/
// boundary/monotonic/invariant/purity 七类）。逐条用 src/relation.ts 的 decide() 比对
// {allowed, requiresApproval, reasonCode}；不一致时打印向量 id、期望/实际与描述。
//
// 运行：node --test test/conformance.test.mjs

import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import { decide } from "../src/relation.ts";

const PACKAGE_ROOT = fileURLToPath(new URL("../", import.meta.url));
const VECTORS_PATH = path.resolve(
  PACKAGE_ROOT,
  "../../spec/vectors/relation-gating.v1.json",
);
const EXPECTED_SPEC = "relation-gating/v1";
const CATEGORIES = ["boundary", "invariant", "matrix", "monotonic", "order", "purity", "unknown"];

test("relation-gating v1: decide() matches every published vector", () => {
  assert.ok(
    existsSync(VECTORS_PATH),
    `向量文件不存在：${VECTORS_PATH}（本测试需在 im2agent 仓库内运行）`,
  );

  let file;
  try {
    file = JSON.parse(readFileSync(VECTORS_PATH, "utf8"));
  } catch (err) {
    assert.fail(`无法解析向量文件 ${VECTORS_PATH}: ${err.message}`);
  }

  assert.equal(file.version, 1, "向量文件 version 必须是 1");
  assert.equal(file.spec, EXPECTED_SPEC);
  assert.ok(Array.isArray(file.vectors), "vectors 必须是数组");
  assert.ok(file.vectors.length >= 60, "向量文件至少应有 60 条（schema minItems）");

  // 文件自身完整性：id 唯一、七类齐全、matrix 恰好 35 条、三态期望自洽。
  const ids = new Set();
  const categories = new Set();
  for (const vector of file.vectors) {
    assert.ok(!ids.has(vector.id), `向量 id 重复：${vector.id}`);
    ids.add(vector.id);
    categories.add(vector.category);
    assert.ok(CATEGORIES.includes(vector.category), `未知 category：${vector.id} -> ${vector.category}`);

    const e = vector.expect;
    if (e.allowed) {
      assert.equal(e.requiresApproval, false, `${vector.id}: allowed=true 却 requiresApproval`);
      assert.equal(e.reasonCode, "OK", `${vector.id}: allowed=true 的 reasonCode 必须是 OK`);
    } else if (e.requiresApproval) {
      assert.ok(
        ["SCOPE_REQUIRES_APPROVAL", "AMOUNT_ABOVE_THRESHOLD"].includes(e.reasonCode),
        `${vector.id}: 审批态 reasonCode 非法`,
      );
    } else {
      assert.notEqual(e.reasonCode, "OK", `${vector.id}: 拒绝态不得期望 OK`);
    }
  }
  assert.deepEqual([...categories].sort(), CATEGORIES, "七类向量必须齐全");
  assert.equal(
    file.vectors.filter((v) => v.category === "matrix").length,
    35,
    "matrix 类必须恰好 35 条（5×7）",
  );

  // 逐条比对。
  const failures = [];
  let passed = 0;
  for (const vector of file.vectors) {
    let actual;
    try {
      actual = decide(vector.input.capability, vector.input.call);
    } catch (err) {
      failures.push({ id: vector.id, error: String(err), description: vector.description });
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
    } else {
      failures.push({
        id: vector.id,
        category: vector.category,
        description: vector.description,
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
        diffs,
        reason: actual.reason,
      });
    }
  }

  if (failures.length > 0) {
    for (const failure of failures) {
      console.error(`FAIL ${failure.id} [${failure.category ?? "?"}]`);
      if (failure.error) {
        console.error(`  error     ${failure.error}`);
      } else {
        for (const diff of failure.diffs) {
          console.error(
            `  ${diff.field}: expected ${JSON.stringify(diff.expected)}, actual ${JSON.stringify(diff.actual)}`,
          );
        }
        console.error(`  reason    ${failure.reason}`);
      }
      console.error(`  vector    ${failure.description}`);
    }
  }

  assert.equal(
    failures.length,
    0,
    `${failures.length}/${file.vectors.length} 条向量不一致：${failures.map((f) => f.id).join(", ")}`,
  );
  assert.equal(passed, file.vectors.length, "通过数必须等于向量总数");
  console.log(`conformance: ${passed}/${file.vectors.length} vectors PASS (${VECTORS_PATH})`);
});
