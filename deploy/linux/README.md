# ③ Linux 服务器部署 Runbook（systemd + 可选反代）

> 目标：一台纯净 Ubuntu 22.04/Debian 12，**不需要 Go 环境**（本原型是 Node 单进程），
> 一条 `install.sh` 装到 `/opt/im2agent`，systemd 守护，健康检查通过。
>
> 交付形态说明：这不是 7.1M 静态 ELF（本机无 Go 工具链）；若目标就是要 ELF + `<10MB` 镜像，
> 请按 `apps/im2agent/PORTING-TO-GO.md` 用 `CGO_ENABLED=0` 构建，其余部署步骤不变。

## 一、前置条件

| 项 | 要求 | 检查命令 |
|---|---|---|
| Node.js | ≥ 22 | `node -v` |
| 系统 | systemd + root | `systemctl --version` |
| 端口 | 8080 未被占用（或改 `PORT`） | `ss -lntp \| grep 8080` |
| 反代（可选） | Caddy 或 nginx，用于 80/443 + HTTPS | `caddy version` / `nginx -v` |

## 二、安装

```bash
sudo ./deploy/linux/install.sh /path/to/agent-im
# 或先演练（只打印将执行的动作，不写盘）
sudo ./deploy/linux/install.sh /path/to/agent-im --dry-run
```

脚本会做：

1. 校验 Node ≥ 22（不满足直接退出，不装半成品）
2. 建系统用户 `im2agent`、目录 `/opt/im2agent`、数据目录 `/var/lib/im2agent`
3. 复制代码（排除 `.git` / `.data` / `node_modules`）
4. **首次安装生成两个凭证**写进 `/etc/im2agent/im2agent.env`（`chmod 600`）：
   - `ADMIN_TOKEN`：操作者（可审批、可触发起草）
   - `VIEWER_TOKEN`：只读观察者（看板/账本/审计，**不能审批**）
5. 装 systemd 单元（`ProtectSystem=strict`，只允许写 `/var/lib/im2agent`）
6. `systemctl enable --now`，并调 `/healthz` 健康检查

## 三、验收（必须逐条过）

```bash
systemctl status im2agent --no-pager          # active (running)
journalctl -u im2agent -n 50 --no-pager       # 无反复重启
curl -s localhost:8080/healthz                # OK
curl -s localhost:8080/api/health | jq .      # ok:true, auditOk:true, pending:2

# 鉴权与两级权限
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/api/admin/state                       # 401
curl -s -H "x-admin-token: $VIEWER_TOKEN" localhost:8080/api/admin/state | jq .role           # "viewer"
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "x-admin-token: $VIEWER_TOKEN" \
  -H 'content-type: application/json' -d '{"ticket_id":"tkt-procurement-1","action":"approve"}' \
  localhost:8080/api/approval/decide                                                        # 403
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "x-admin-token: $ADMIN_TOKEN" \
  -H 'content-type: application/json' -d '{"ticket_id":"tkt-procurement-1","action":"approve"}' \
  localhost:8080/api/approval/decide                                                        # 200

# 持久化（若 STORE=jsonfile）
ls -l /var/lib/im2agent/state.json
sudo systemctl restart im2agent && curl -s localhost:8080/api/health | jq .pending   # 状态不回到 2
```

## 四、反代（二选一）

### Caddy（推荐，自动 HTTPS）

```caddyfile
im.example.com {
    encode zstd gzip
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
        X-Content-Type-Options "nosniff"
        X-Frame-Options "DENY"
        Referrer-Policy "no-referrer"
        -Server
    }
    reverse_proxy 127.0.0.1:8080
}
```

反代后**必须**在 `im2agent.env` 里加：

```ini
TRUST_PROXY=1          # 否则限流只能看到 127.0.0.1，等于没限流
PROFILE=prod           # 强制 ADMIN_TOKEN + HSTS
```

### nginx

```nginx
server {
    listen 80;
    server_name im.example.com;
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## 五、日常运维

| 动作 | 命令 |
|---|---|
| 看日志 | `journalctl -u im2agent -f` |
| 重启 | `sudo systemctl restart im2agent` |
| 改配置 | 编辑 `/etc/im2agent/im2agent.env` → `systemctl restart im2agent` |
| 备份 | `cp /var/lib/im2agent/state.json /backup/state-$(date +%F).json` |
| 升级 | 重跑 `install.sh`（代码覆盖，配置与数据保留） |
| 卸载 | `systemctl disable --now im2agent && rm -rf /opt/im2agent /etc/im2agent /var/lib/im2agent` |

## 六、已知边界

- 本原型**单节点**：没有 WS 横向扩展，在线状态在内存；多节点方案见 `apps/im2agent/ROADMAP-v0.6.1+.md`。
- `STORE=postgres` 未实现驱动，会显式报 `STORE_NOT_AVAILABLE`；先用 `jsonfile` 挂数据目录。
- 限流是**按 IP 固定窗口**，够挡扫描与滥用；分布式限流需 Redis（同上路线图）。
