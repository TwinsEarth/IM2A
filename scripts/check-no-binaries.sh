#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# 二进制守卫：拒绝把 >1MB 的非文本文件提交进仓库（上游曾误提交 10.27MB 的 `gateway` blob）。
# 白名单：.git / node_modules / dist / client/app/build / THIRD_PARTY_LICENSES。
#
# 退出码：0 = 干净；1 = 发现违规大二进制（CI 门禁）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAX=$((1024 * 1024)) # 1 MiB

fail=0
count=0

while IFS= read -r -d '' f; do
  size="$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f" 2>/dev/null || echo 0)"
  [ "$size" -le "$MAX" ] && continue
  # grep -I 把二进制文件视为"不匹配"：能匹配到任意行说明是文本文件，放行。
  if LC_ALL=C grep -Iq . "$f" 2>/dev/null; then
    continue
  fi
  echo "[binary-guard] FAIL: ${f#"$ROOT"/} (${size} bytes > 1MB, non-text)" >&2
  fail=1
  count=$((count + 1))
done < <(find "$ROOT" \
  \( -path "$ROOT/.git" -o -path "$ROOT/.git/*" \
     -o -name node_modules -o -path "*/node_modules/*" \
     -o -path "$ROOT/dist" -o -path "$ROOT/dist/*" \
     -o -path "$ROOT/client/app/build" -o -path "$ROOT/client/app/build/*" \
     -o -path "$ROOT/THIRD_PARTY_LICENSES" -o -path "$ROOT/THIRD_PARTY_LICENSES/*" \) -prune -o \
  -type f -size +1M -print0 2>/dev/null)

if [ "$fail" -ne 0 ]; then
  echo "[binary-guard] FAILED: $count oversized binary file(s); add to .gitignore or remove before committing" >&2
  exit 1
fi
echo "[binary-guard] OK: no >1MB non-text files in repo (whitelist: .git/node_modules/dist/client build/THIRD_PARTY_LICENSES)"
