#!/usr/bin/env bash
# 兼容性门禁（Compat）：发布前确认契约稳定、版本一致、跨平台可编译。
# 任一步失败即非零退出，供 CI 与本地 make compat 共用。
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$(go env GOPATH 2>/dev/null)/bin:$PATH"

echo "[compat] 1) proto 契约数量（期望 9）"
n=$(ls proto/*.proto | wc -l | tr -d ' ')
[ "$n" = "9" ] || { echo "FAIL: proto count=$n want 9"; exit 1; }
echo "  OK: $n proto contracts"

echo "[compat] 2) 统一版本常量存在且被 healthz 暴露"
grep -q 'const Version' internal/version/version.go
grep -q '"version": version.Version' internal/api/api.go
echo "  OK: version wired into healthz"

echo "[compat] 3) 关键路由契约（webhook/付费/审批/协商）"
grep -rq '/v1/im/webhook' internal/api/
grep -rq 'paid_message_ledger' internal/store/
grep -rq 'approvals' internal/api/
grep -rq 'negotiations' internal/api/
echo "  OK: core routes present"

echo "[compat] 4) 跨平台矩阵编译（linux/darwin/windows × amd64/arm64）"
for os in linux darwin windows; do
  for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -o /dev/null ./services/gateway \
      && echo "  OK: $os/$arch" \
      || { echo "FAIL: $os/$arch"; exit 1; }
  done
done

echo "[compat] ALL CHECKS PASS"
