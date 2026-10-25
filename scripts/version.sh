#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# 版本单一源读取/生成/校验器（薄包装；逻辑在 scripts/version.py）。
#   权威源：internal/version/version.go（core）+ editions.json（各 edition）
#   用法：bash scripts/version.sh {plain|semver|table|sync|check}
#   通常通过 make version-sync / make version-check 调用。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if ! command -v python3 >/dev/null 2>&1; then
  echo "[version] FATAL: python3 is required for version management" >&2
  exit 1
fi

exec python3 "$ROOT/scripts/version.py" --root "$ROOT" "$@"
