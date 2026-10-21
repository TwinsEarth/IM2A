# Debian/Ubuntu 一键安装 im2agent（局域网/生产形态）
# 用法：sudo ./install.sh [仓库根目录]
# 说明：本仓库原型是 Node 单进程（本机无 Go 工具链，故不产出静态 ELF）；
#      目标机需要 Node.js ≥ 22。安装后由 systemd 守护，配置在 /etc/im2agent/im2agent.env。
set -euo pipefail

REPO_SRC="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
PREFIX="/opt/im2agent"
DATA_DIR="/var/lib/im2agent"
CONF_DIR="/etc/im2agent"
SERVICE_USER="im2agent"

if [ "$(id -u)" -ne 0 ]; then
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

echo "==> 创建用户与目录"
id -u "$SERVICE_USER" >/dev/null 2>&1 || useradd --system --home "$PREFIX" --shell /usr/sbin/nologin "$SERVICE_USER"
mkdir -p "$PREFIX" "$DATA_DIR" "$CONF_DIR"
chown -R "$SERVICE_USER":"$SERVICE_USER" "$DATA_DIR"

echo "==> 复制代码到 $PREFIX"
if command -v rsync >/dev/null 2>&1; then
  rsync -a --delete --exclude '.git' --exclude '.data' --exclude 'node_modules' "$REPO_SRC"/ "$PREFIX"/
else
  cp -a "$REPO_SRC"/. "$PREFIX"/
  rm -rf "$PREFIX/.git" "$PREFIX/.data"
fi
chown -R "$SERVICE_USER":"$SERVICE_USER" "$PREFIX"

echo "==> 写入配置（首次安装时生成随机 ADMIN_TOKEN）"
if [ ! -f "$CONF_DIR/im2agent.env" ]; then
  TOKEN="$(head -c 24 /dev/urandom | base64 | tr -d '=+/\n')"
  cat > "$CONF_DIR/im2agent.env" <<EOF
PROFILE=lan
PORT=8080
HOST=0.0.0.0
ADMIN_TOKEN=$TOKEN
STORE=jsonfile
STORE_FILE=$DATA_DIR/state.json
# 大模型（可选）：填了 key 自动走真实 LLM，未填走确定性模板
# IM2AGENT_LLM_KEY=
# IM2AGENT_LLM_BASE_URL=https://api.deepseek.com/v1
# IM2AGENT_LLM_MODEL=deepseek-chat
EOF
  chmod 600 "$CONF_DIR/im2agent.env"
  echo "    已生成 ADMIN_TOKEN：$TOKEN"
  echo "    （保存在 $CONF_DIR/im2agent.env，请及时转存）"
else
  echo "    已存在 $CONF_DIR/im2agent.env，保持不变"
fi

echo "==> 安装 systemd 单元"
install -m 644 "$(dirname "$0")/im2agent.service" /etc/systemd/system/im2agent.service
systemctl daemon-reload
systemctl enable im2agent.service
systemctl restart im2agent.service
sleep 1

echo "==> 健康检查"
if command -v curl >/dev/null 2>&1; then
  curl -fsS "http://127.0.0.1:8080/api/health" && echo
else
  node -e "fetch('http://127.0.0.1:8080/api/health').then(r=>r.text()).then(console.log)"
fi

cat <<EOF

安装完成。
  运维界面   http://<本机IP>:8080/admin/?token=<ADMIN_TOKEN>
  状态检查   systemctl status im2agent
  日志       journalctl -u im2agent -f
  配置       $CONF_DIR/im2agent.env
  数据       $DATA_DIR/state.json

切换到生产形态：把 PROFILE 改为 prod 并确保 ADMIN_TOKEN 已设置，systemctl restart im2agent。
EOF
