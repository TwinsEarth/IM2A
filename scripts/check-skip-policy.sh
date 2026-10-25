#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# 静默 skip 守卫：禁止无原因的 `t.Skip()`。
# 允许的形式（任一）：
#   1) t.Skip("requires PG: ...") / t.Skip("reason: ...")  —— 带原因参数
#   2) `t.Skip()` 上方 3 行内出现 PG_DSN / PG_ / postgres / requires 等说明（PG-gated 语境）
# CI 已提供真实 PG16 service，PG-gated 用例会真实执行；此守卫只防"静默跳过"。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bad=0

while IFS= read -r line; do
  file="${line%%:*}"
  rest="${line#*:}"
  lineno="${rest%%:*}"
  start=$((lineno > 3 ? lineno - 3 : 1))
  ctx="$(sed -n "${start},${lineno}p" "$file" 2>/dev/null || true)"
  if printf '%s\n' "$ctx" | grep -qE 'PG_DSN|PG_|postgres|Postgres|requires|requires PG'; then
    continue
  fi
  echo "[skip-policy] FAIL: undocumented silent skip at ${file#"$ROOT"/}:${lineno}" >&2
  bad=1
done < <(grep -rn --include='*_test.go' -E '\bt\.Skip\(\)' "$ROOT" 2>/dev/null || true)

if [ "$bad" -ne 0 ]; then
  echo "[skip-policy] FAILED: replace silent t.Skip() with t.Skip(\"requires PG: ...\") or add a PG_DSN-gated comment" >&2
  exit 1
fi
echo "[skip-policy] OK: no undocumented silent t.Skip()"
