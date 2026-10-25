#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# LICENSE 一致性检查：根 LICENSE（MIT / 2026 IM2A contributors）+ npm 包 + 插件包 license 字段。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail=0
note_fail() { echo "[license] FAIL: $*" >&2; fail=1; }

# 1) 根 LICENSE
if [ ! -f "$ROOT/LICENSE" ]; then
  note_fail "missing LICENSE"
else
  grep -q '^MIT License' "$ROOT/LICENSE" || note_fail "LICENSE is not 'MIT License'"
  grep -q 'Copyright (c) 2026 IM2A contributors' "$ROOT/LICENSE" || note_fail "LICENSE missing 'Copyright (c) 2026 IM2A contributors'"
fi

# 2) npm 包 license 字段 + files 包含 LICENSE
if [ ! -f "$ROOT/npm/package.json" ]; then
  note_fail "missing npm/package.json"
else
  if ! python3 - "$ROOT/npm/package.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
ok = d.get("license") == "MIT" and "LICENSE" in d.get("files", [])
sys.exit(0 if ok else 1)
PY
  then
    note_fail "npm/package.json must have license=MIT and files[] containing LICENSE"
  fi
fi

# 3) OpenClaw channel-relation 插件包（若存在）
PLUGIN_PKG="$ROOT/packages/openclaw-channel-relation/package.json"
if [ -f "$PLUGIN_PKG" ]; then
  if ! python3 - "$PLUGIN_PKG" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
sys.exit(0 if d.get("license") == "MIT" else 1)
PY
  then
    note_fail "packages/openclaw-channel-relation/package.json license != MIT"
  fi
fi

# 4) README license 徽章
grep -q 'img.shields.io/badge/license-MIT-green' "$ROOT/README.md" || note_fail "README license badge != MIT"

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "[license] OK: root LICENSE is MIT (2026 IM2A contributors); npm/plugin license fields = MIT"
