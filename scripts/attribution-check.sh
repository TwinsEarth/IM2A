#!/usr/bin/env bash
# attribution-check.sh — IM2A 第三方归属合规自检（OpenClaw, MIT）
#
# 用途：机器可执行地验证「引用 OpenClaw 的公示」是否完整。
# 退出码：0 = 全部通过；1 = 有检查项失败。
#
# 用法：
#   bash scripts/attribution-check.sh            # 只查仓库文件
#   bash scripts/attribution-check.sh --runtime  # 同时探测运行中的 /v1/about/attribution
#   BASE_URL=http://127.0.0.1:18080 bash scripts/attribution-check.sh --runtime
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_URL="${BASE_URL:-http://127.0.0.1:18080}"
CHECK_RUNTIME=0
[ "${1:-}" = "--runtime" ] && CHECK_RUNTIME=1

FAIL=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
info() { printf '  \033[36mINFO\033[0m %s\n' "$1"; }

need_file()      { [ -f "$ROOT/$1" ] && ok "文件存在: $1" || bad "缺少文件: $1"; }
need_contains()  { # file, pattern, label
  if [ -f "$ROOT/$1" ] && grep -qF -- "$2" "$ROOT/$1"; then ok "$3"; else bad "$3 (期望包含: $2)"; fi
}

echo "[1] 许可证原文（MIT 义务：保留版权与许可声明）"
need_file "THIRD_PARTY_LICENSES/OpenClaw-MIT.txt"
need_contains "THIRD_PARTY_LICENSES/OpenClaw-MIT.txt" "MIT License" "许可证标题为 MIT License"
need_contains "THIRD_PARTY_LICENSES/OpenClaw-MIT.txt" "Copyright (c) 2026 OpenClaw Foundation" "保留上游版权行"
need_contains "THIRD_PARTY_LICENSES/OpenClaw-MIT.txt" "The above copyright notice and this permission notice shall be included" "保留 MIT 许可声明段"

echo "[1b] 传递依赖：openclaw-weixin（微信通道插件）"
need_file "THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt"
need_contains "THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt" "Copyright (c) 2026 Tencent" "保留 Tencent 版权行"
need_contains "THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt" "Contributors to NewFuture/openclaw-weixin" "保留社区贡献者版权行"
need_contains "ATTRIBUTION.md" "openclaw-weixin" "ATTRIBUTION 声明 weixin 传递依赖"
need_contains "README.md" "openclaw-weixin" "README 声明微信通道前置依赖"

echo "[1c] 源码级移植：DeepSeek-Bot（Apache-2.0，义务重于 MIT）"
need_file "THIRD_PARTY_LICENSES/DeepSeek-Bot-LICENSE.txt"
need_contains "THIRD_PARTY_LICENSES/DeepSeek-Bot-LICENSE.txt" "Apache License" "许可证为 Apache License"
need_contains "THIRD_PARTY_LICENSES/DeepSeek-Bot-LICENSE.txt" "Version 2.0" "许可证版本 2.0"
need_file "THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt"
need_contains "THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt" "Fei2411" "NOTICE 保留上游版权人"
need_contains "THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt" "DeepSeek Harness" "NOTICE 的传递归属（DSH MIT）被保留"
need_contains "ATTRIBUTION.md" "DeepSeek-Bot" "ATTRIBUTION 声明 DeepSeek-Bot"
need_contains "ATTRIBUTION.md" "Apache-2.0" "ATTRIBUTION 声明 Apache-2.0"
need_contains "ATTRIBUTION.md" "移植" "ATTRIBUTION 含修改/移植声明"
need_contains "README.md" "DeepSeek-Bot" "README 公示 DeepSeek-Bot 引用"

echo "[2] 归属声明文件"
need_file "ATTRIBUTION.md"
need_contains "ATTRIBUTION.md" "https://github.com/openclaw/openclaw" "声明上游仓库 URL"
need_contains "ATTRIBUTION.md" "Copyright (c) 2026 OpenClaw Foundation" "声明上游版权"
need_contains "ATTRIBUTION.md" "MIT" "声明许可证为 MIT"
need_contains "ATTRIBUTION.md" "THIRD_PARTY_LICENSES/OpenClaw-MIT.txt" "指向许可证原文"
need_contains "ATTRIBUTION.md" "无隶属" "包含无隶属/无背书声明"

echo "[3] NOTICE 与 README 可见性"
need_file "NOTICE"
need_contains "NOTICE" "OpenClaw" "NOTICE 提及 OpenClaw"
need_contains "NOTICE" "OpenClaw Foundation" "NOTICE 含版权行"
need_contains "README.md" "OpenClaw" "README 公示 OpenClaw 引用"

echo "[4] 代码层归属标注与硬编码清理"
if [ -d "$ROOT/internal" ]; then
  if grep -rq "契约来源：OpenClaw" "$ROOT/internal" 2>/dev/null; then
    ok "存在「契约来源：OpenClaw」标注"
  else
    info "未找到「契约来源：OpenClaw」标注（若插件尚未落地可忽略）"
  fi
  LEGACY_HITS=$(grep -rn "/opt/node-v22.23.4-linux-x64/bin/openclaw" "$ROOT/internal" --include='*.go' 2>/dev/null | grep -v '_test.go' | wc -l | tr -d ' ')
  if [ "$LEGACY_HITS" = "0" ]; then
    ok "非测试代码中已无旧版绝对路径硬编码"
  else
    bad "仍有 $LEGACY_HITS 处旧版绝对路径硬编码（应改为配置解析器）"
  fi
  if [ -d "$ROOT/internal/plugin/openclaw" ]; then ok "子插件目录存在: internal/plugin/openclaw"; else bad "缺少 internal/plugin/openclaw"; fi
  if [ -d "$ROOT/internal/plugin/dsbot" ]; then
    ok "子插件目录存在: internal/plugin/dsbot"
    if grep -rq "移植自 Fei2411/DeepSeek-Bot" "$ROOT/internal/plugin/dsbot" 2>/dev/null; then
      ok "dsbot 移植文件含来源与修改声明"
    else
      bad "dsbot 移植文件缺少「移植自 Fei2411/DeepSeek-Bot」声明（Apache-2.0 §4(b)）"
    fi
  else
    info "未找到 internal/plugin/dsbot（若八字插件尚未落地可忽略）"
  fi
else
  info "部署树模式（无 internal/ 源码目录），跳过源码级检查 [4]"
fi

if [ "$CHECK_RUNTIME" = "1" ]; then
  echo "[6] 运行时端点 ($BASE_URL)"
  if command -v curl >/dev/null 2>&1; then
    A=$(curl -fsS -m 5 "$BASE_URL/v1/about/attribution" 2>/dev/null || true)
    if printf '%s' "$A" | grep -q "openclaw"; then ok "/v1/about/attribution 返回 openclaw"; else bad "/v1/about/attribution 未返回 openclaw"; fi
    if printf '%s' "$A" | grep -q "MIT"; then ok "/v1/about/attribution 声明 MIT"; else bad "/v1/about/attribution 未声明 MIT"; fi
    if printf '%s' "$A" | grep -q "OpenClaw Foundation"; then ok "/v1/about/attribution 含版权行"; else bad "/v1/about/attribution 缺版权行"; fi
    P=$(curl -fsS -m 5 "$BASE_URL/v1/plugins" 2>/dev/null || true)
    if printf '%s' "$P" | grep -q '"openclaw"'; then ok "/v1/plugins 注册了 openclaw"; else bad "/v1/plugins 未注册 openclaw"; fi
    if printf '%s' "$P" | grep -q '"dsbot"'; then ok "/v1/plugins 注册了 dsbot"; else bad "/v1/plugins 未注册 dsbot"; fi
    if printf '%s' "$A" | grep -qi "DeepSeek-Bot"; then ok "/v1/about/attribution 含 DeepSeek-Bot 归属"; else bad "/v1/about/attribution 缺 DeepSeek-Bot 归属"; fi
  else
    info "无 curl，跳过运行时检查"
  fi
fi

echo
if [ "$FAIL" -eq 0 ]; then
  echo "归属合规自检：全部通过 ✅"
  exit 0
else
  echo "归属合规自检：$FAIL 项失败 ❌"
  exit 1
fi
