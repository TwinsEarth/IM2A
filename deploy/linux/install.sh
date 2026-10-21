#!/usr/bin/env bash
# Debian/Ubuntu 一键安装 im2agent（局域网/生产形态）
#
# 用法：
#   sudo ./deploy/linux/install.sh [仓库根目录] [--dry-run]
#
# 说明：
#   · 本仓库原型是 Node 单进程（本机无 Go 工具链，故不产出静态 ELF）；目标机需要 Node.js ≥ 22
#   · 首次安装生成两个凭证：ADMIN_TOKEN（可写）、VIEWER_TOKEN（只读）
#   · --dry-run 只打印将执行的动作，不写任何文件（用于预演/审计）
set -euo pipefail

REPO_SRC=""
DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) REPO_SRC="$arg" ;;
  esac
done
REPO_SRC="${REPO_SRC:-$(cd "$(dirname "$0")/../.." && pwd)}"

PREFIX="/opt/im2agent"
DATA_DIR="/var/lib/im2agent"
CONF_DIR="/etc/im2agent"
SERVICE_USER="im2agent"
PORT="${PORT:-8080}"

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "  [dry-run] $*"
  else
    "$@"
  fi
}

echo "==> im2agent 安装（dry-run=$DRY_RUN，源目录：$REPO_SRC）"

if [ "$DRY_RUN" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then
  echo "请用 root 运行：sudo ./install.sh" >&2
  exit 1
fi

if ! command -v node >/dev/null 2>&1; then
  echo "未找到 node。请先安装 Node.js ≥ 22（例如 apt install nodejs，或使用 nvm）。" >&2
  exit 1
fi
NODE_MAJOR="$(node -p 'process.versions.node.split(".")[0]')"
if [ "$NODE_MAJOR" -lt 22 ]; then
  echo "Node.js 版本过低：$(node -v)，需要 ≥ 22" >&2
  exit 1
fi
echo "    Node $(node -v) ✓"

if [ -e "$REPO_SRC/apps/im2agent/server.mjs" ]; then
  echo "    源目录校验 ✓"
else
  echo "源目录里没有 apps/im2agent/server.mjs：$REPO_SRC" >&2
  exit 1
fi

echo "==> 创建用户与目录"
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  run useradd --system --home "$PREFIX" --shell /usr/sbin/nologin "$SERVICE_USER"
fi
run mkdir -p "$PREFIX" "$DATA_DIR" "$CONF_DIR"
run chown -R "$SERVICE_USER":"$SERVICE_USER" "$DATA_DIR"

echo "==> 复制代码到 $PREFIX"
if [ "$DRY_RUN" -eq 0 ]; then
  if command -v rsync >/dev/null 2>&1; then
    rsync -a --delete --exclude '.git' --exclude '.data' --exclude 'node_modules' "$REPO_SRC"/ "$PREFIX"/
  else
    cp -a "$REPO_SRC"/. "$PREFIX"/
    rm -rf "$PREFIX/.git" "$PREFIX/.data"
  fi
  chown -R "$SERVICE_USER":"$SERVICE_USER" "$PREFIX"
else
  echo "  [dry-run] rsync -a --delete --exclude .git --exclude .data $REPO_SRC/ $PREFIX/"
fi

echo "==> 写入配置（首次安装生成 ADMIN_TOKEN / VIEWER_TOKEN）"
if [ -f "$CONF_DIR/im2agent.env" ]; then
  echo "    已存在 $CONF_DIR/im2agent.env，保持不变（升级不会覆盖凭证）"
else
  ADMIN_TOKEN="$(head -c 24 /dev/urandom | base64 | tr -d '=+/\n')"
  VIEWER_TOKEN="$(head -c 24 /dev/urandom | base64 | tr -d '=+/\n')"
  if [ "$DRY_RUN" -eq 0 ]; then
    cat > "$CONF_DIR/im2agent.env" <<EOF
PROFILE=lan
PORT=$PORT
HOST=0.0.0.0
# 操作者（可审批/可触发起草）
ADMIN_TOKEN=$ADMIN_TOKEN
# 只读观察者（看板/账本/审计，不能审批）
VIEWER_TOKEN=$VIEWER_TOKEN
STORE=jsonfile
STORE_FILE=$DATA_DIR/state.json
# 反代后必须置 1，否则限流只能看到 127.0.0.1
TRUST_PROXY=0
RATE_LIMIT_PER_MIN=120
# 大模型（可选）：填了 key 自动走真实 LLM，未填走确定性模板
# IM2AGENT_LLM_KEY=
# IM2AGENT_LLM_BASE_URL=https://api.deepseek.com/v1
# IM2AGENT_LLM_MODEL=deepseek-chat
EOF
    chmod 600 "$CONF_DIR/im2agent.env"
  else
    echo "  [dry-run] 生成 $CONF_DIR/im2agent.env（含 ADMIN_TOKEN / VIEWER_TOKEN，chmod 600）"
  fi
  echo "    ADMIN_TOKEN  = $ADMIN_TOKEN"
  echo "    VIEWER_TOKEN = $VIEWER_TOKEN"
  echo "    （已写入 $CONF_DIR/im2agent.env，请及时转存；丢失只能重新生成）"
fi

echo "==> 安装 systemd 单元"
run install -m 644 "$(dirname "$0")/im2agent.service" /etc/systemd/system/im2agent.service
run systemctl daemon-reload
run systemctl enable im2agent.service
run systemctl restart im2agent.service

if [ "$DRY_RUN" -eq 0 ]; then
  sleep 1
  echo "==> 健康检查"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS "http://127.0.0.1:$PORT/healthz" && echo
  else
    node -e "fetch('http://127.0.0.1:$PORT/healthz').then(r=>r.text()).then(console.log)"
  fi
fi

cat <<EOF

安装完成（或已预演）。
  运维界面   http://<本机IP>:$PORT/admin/?token=<ADMIN_TOKEN>
  状态检查   systemctl status im2agent
  日志       journalctl -u im2agent -f
  配置       $CONF_DIR/im2agent.env
  数据       $DATA_DIR/state.json

验收命令（逐条应通过）：
  curl -s localhost:$PORT/healthz                          # OK
  curl -s -o /dev/null -w '%{http_code}\n' localhost:$PORT/api/admin/state          # 401
  curl -s -o /dev/null -w '%{http_code}\n' -H "x-admin-token: <VIEWER_TOKEN>" localhost:$PORT/api/admin/state   # 200（只读）
  curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "x-admin-token: <VIEWER_TOKEN>" -H 'content-type: application/json' -d '{"ticket_id":"tkt-procurement-1","action":"approve"}' localhost:$PORT/api/approval/decide  # 403

切换生产：编辑 $CONF_DIR/im2agent.env 设 PROFILE=prod 且反代时 TRUST_PROXY=1，然后 systemctl restart im2agent。
详细 Runbook：deploy/linux/README.md
EOF
