# 干净压测 / 部署验证机：`134.175.243.146`（Ubuntu 24.04，**非生产**）

> ## ⛔ 合规红线（2026-10-25，长期有效）
>
> **`106.53.197.47` 为用户生产机：禁止任何读写**——不 ssh / 不 scp / 不 curl / 不端口探测 /
> 不读日志 / 不查 systemctl（"只读诊断"也不行）。本仓库任何脚本不得把它作为目标或默认值。
> 本文件与 `run-bench.sh` 的目标**只有**下面这台干净机（以及 Vultr 部署机 `202.182.123.154`）。

> 用户提供的**干净临时机**，用途只有三件：① 连接网关容量压测（真实容量曲线）② PG16 真库验证
> ③ 部署形态验证（embedded 网关 + 独立 conn-gateway 共存）。
> **不是生产机**：不接域名、不对公网开放 22 以外的端口；与 Vultr 部署机、用户生产机都无关。
> 本目录产物：`run-bench.sh`（一键压测）、`README.md`（本文件）。

---

## 1. 访问方式

| 项 | 值 |
|---|---|
| 公网 / 内网 | `134.175.243.146` / `192.168.0.5`（NAT；`eth0` 上只有内网 IP） |
| 用户 | `ubuntu`（**passwordless sudo**：`sudo -n true` 成功） |
| 登录 | **fleet 公钥**：`~/.ssh/authorized_keys` 内含 `SHA256:UErraNTAJOoVzos1rZ+8Rr4LBYqVUTQzAEnSEbhuVWE w2m-fleet-deploy`（本机 `~/.ssh/w2m-fleet-deploy` 可直接登）；另有 fleet 的 `w2m-vps` 公钥 |
| 密码 | 仅一次性 `sshpass` 装公钥时使用过（**建议任务结束后轮换或销毁该凭据**）；当前 sshd **password auth 仍开启**（任务期保留，防止 key 问题锁死；结束流程：确认 key 稳定 ≥24h → 保留备用 key → `PasswordAuthentication no` + `systemctl reload ssh`） |

---

## 2. 实测硬件（以实测为准）

| 项 | 实测值 |
|---|---|
| OS | **Ubuntu 24.04.4 LTS (noble)**，kernel `6.8.0-138-generic`，x86_64 |
| CPU | **4 vCPU**（Intel Xeon Platinum 8576C，KVM，2 core/socket × 2 threads/core，1 socket） |
| 内存 | **7,623 MiB**（≈7.45 GiB，8 GB 档）；压测前 available ≈ **7.0 GB** |
| swap | **1,987 MiB**（≈1.94 GiB），压测前未使用 |
| 磁盘 | `/dev/vda2` **50 GB**，可用 ~42 GB |
| 网卡 | `eth0` 虚拟 KVM 网卡（`Speed: Unknown!`，不能从驱动读速率；**带宽以实测吞吐为准**） |
| 内核参数 | 默认 `somaxconn=4096`、`tcp_max_syn_backlog=512`、`ip_local_port_range=32768-60999`；**压测脚本临时**调为 8192/8192/10000-65000（不持久化，重启即回） |
| `ulimit -n` | **已持久化调为 65535**（`/etc/security/limits.conf` 的 ubuntu/root + `/etc/systemd/system.conf` 的 `DefaultLimitNOFILE=65535`）；登录 shell 与 transient systemd service 双路实测均 65535 |

---

## 3. 已装环境

| 组件 | 版本 / 位置 | 说明 |
|---|---|---|
| Go | **1.27.3**，`/usr/local/go1.27.3/bin/go`（并 `ln -s /usr/local/bin/go`） | **旁装**，未动系统包；`GOPROXY` 机器默认 `https://mirrors.tencent.com/go,direct`（实测可用；proxy.golang.org 不可达） |
| PostgreSQL | **16.15**（Ubuntu noble 官方源 `postgresql-16`） | **只监听 127.0.0.1:5432**；集群 `16/main` online；`shared_buffers=256MB, max_connections=100`；E2E 库 **`im2a_e2e`**（owner `im2a_e2e`，已装 `pgcrypto`） |
| PG 连接串 | `source /home/ubuntu/im2a-bench/e2e-env.sh` | 文件 **0600 ubuntu**；密码在机器上生成、未打印/未进 git；导出 `PG_DRIVER=pgx` `PG_DSN=postgres://im2a_e2e:***@127.0.0.1:5432/im2a_e2e?sslmode=disable` |
| 源码 | `/home/ubuntu/im2a-bench/im2agent`（本机 tar+scp 同步，最新含 `cmd/`、45 个迁移、Wave A/B2/C1–C4） | 该机不连 GitHub |
| 二进制 | `/home/ubuntu/im2a-bench/bin/{conn-gateway,loadgen}`（`go build ./cmd/...`） | 压测用 |
| 结果目录 | `/home/ubuntu/im2a-bench/bench-results/`（`loadgen-<N>.log`、`metrics-<N>.txt`、`fd-samples.log`） | |

---

## 4. 怎么重建 / 怎么跑测试

### 4.0 交接状态（2026-10-25，给 wire-dev / openclaw-arch）

这台机**已就绪，保持可用；`im2a_e2e` 库不要 drop**（C4 复验与 PG16 全量还要用）：

| 项 | 当前值 |
|---|---|
| 登录 | `ssh -i <fleet-key> ubuntu@134.175.243.146`（免密 sudo） |
| PG | `PostgreSQL 16.15`，`127.0.0.1:5432`，集群 `16/main` online |
| 库 | `im2a_e2e`（owner `im2a_e2e`，pgcrypto 已装，45 个迁移可跑通） |
| 连接串 | `source /home/ubuntu/im2a-bench/e2e-env.sh`（0600 ubuntu，密码不在仓库） |
| 源码 | `/home/ubuntu/im2a-bench/im2agent` |
| 已释放 | 压测 transient unit/进程已全部停止；无残留 `conn-gw-*` 单元 |

**一行验证（给 wire-dev 的入口）**：

```bash
ssh ubuntu@134.175.243.146
source /home/ubuntu/im2a-bench/e2e-env.sh
psql "$PG_DSN" -c 'select version();'
cd /home/ubuntu/im2a-bench/im2agent
GOMAXPROCS=1 go test ./internal/store -run TestMigrationContract -count=1 -v
# PG-gated 全量请用 -p 1 逐包（见 §4.4），且只 source 当前 shell，不要全局 export
```

> 当前库**已存在且已验证可用**：除非明确要做"全新空库"实验，**不要**执行 `dropdb`（§4.2 仅作重建参考）。

> **出站白名单（fail-closed，升级语义变更）**：新版本 `internal/edge.EgressGuard` 默认
> **空白名单 = 拒绝一切出站**（旧版本是"空白名单=放行"）。本机当前部署的二进制仍是旧语义
> （实测 `POST /v1/edge/pipeline` → `egress_allowed:true`，且 `/etc/im2a/env` 未配
> `IM2A_EGRESS_ALLOW`）；**下次在本机重新部署当前分支后**，如需测端侧出站，必须先在
> `/etc/im2a/env` 配置 `IM2A_EGRESS_ALLOW=host1,host2`（逗号分隔、不含 scheme），否则按
> "预期拒绝（`egress_allowed:false`）"验收。示例模板见 `deploy/vultr/env/im2a.env.example`。

### 4.1 同步源码（本机 → 该机）

```powershell
# 本机 Windows
$src='E:\GPT\im2a-work\im2agent'; $tgz='E:\GPT\im2a-work\clean-src.tgz'
$items=@('internal','services','cmd','scripts','proto','deploy','migrations','go.mod','go.sum','Makefile',
         'ATTRIBUTION.md','NOTICE','LICENSE','README.md','THIRD_PARTY_LICENSES') |
  Where-Object { Test-Path (Join-Path $src $_) }
& tar -czf $tgz -C $src @items
& scp -i "$env:USERPROFILE\.ssh\w2m-fleet-deploy" $tgz ubuntu@134.175.243.146:/home/ubuntu/im2a-bench-src.tgz
```

```bash
# 该机
cd /home/ubuntu/im2a-bench && rm -rf im2agent && mkdir im2agent && tar -C im2agent -xzf ../im2a-bench-src.tgz
export PATH=/usr/local/go1.27.3/bin:$PATH
cd im2agent && go mod download && go build -o ../bin/conn-gateway ./cmd/conn-gateway && go build -o ../bin/loadgen ./cmd/loadgen
```

### 4.2 PG16 重建（幂等）

```bash
# 库/角色（密码只在机器上生成）
sudo -u postgres psql -c "ALTER ROLE im2a_e2e WITH LOGIN PASSWORD '$(openssl rand -hex 20)';"   # 或首次 CREATE ROLE
sudo -u postgres dropdb --if-exists im2a_e2e && sudo -u postgres createdb -O im2a_e2e im2a_e2e
sudo -u postgres psql -d im2a_e2e -c 'CREATE EXTENSION IF NOT EXISTS pgcrypto;'
# 更新 ~/im2a-bench/e2e-env.sh 里的 PG_DSN（密码别打印/别提交）
```

### 4.3 迁移契约（**PG16 真库**，45 个迁移）

```bash
source /home/ubuntu/im2a-bench/e2e-env.sh
cd /home/ubuntu/im2a-bench/im2agent
GOMAXPROCS=1 go test ./internal/store -run TestMigrationContract -count=1 -v
```

### 4.4 PG-gated 全量测试（给 wire-dev 用）

```bash
source /home/ubuntu/im2a-bench/e2e-env.sh          # 只 source 当前 shell，不要全局 export
cd /home/ubuntu/im2a-bench/im2agent
for pkg in $(go list ./...); do
  GOMAXPROCS=1 go test -p 1 -count=1 "$pkg" || break
done
```
> 8 GB 内存足够，但仍建议**串行**跑（`-p 1`、逐包），避免结果相互污染；带 PG 的用例会真写 `im2a_e2e`。

---

## 5. 怎么跑压测

```bash
cd /home/ubuntu/im2a-bench
setsid nohup bash /home/ubuntu/im2a-bench/im2agent/deploy/bench/clean-host/run-bench.sh \
  > bench-run.log 2>&1 < /dev/null &
tail -f bench-run.log
```
（`deploy/bench/clean-host/run-bench.sh` 即本目录脚本的仓库副本。）

脚本行为：
1. 临时 sysctl（somaxconn/syn_backlog/端口范围，**不持久化**）；
2. 以 **transient systemd unit `conn-gw-bench`** 启动 `conn-gateway`：
   `-addr 127.0.0.1:18081 -uds-disable -echo -dev-insecure -debug`，`LimitNOFILE=65535`，
   `OPENCLAW_ENABLED=off DSBOT_ENABLED=off`（**插件探测的 Node 进程不计入压测数字**）；
3. 三档：`500 / 2000 / 5000` 连接，每连接 1 frame/s，60/60/300 s；每档前后记录
   `available / swap`，全程每 30 s 采样网关 RSS、fds、threads、cgroup 内存、load；
4. 阶段间 idle 30 s 让系统回落；结束停 transient unit。
5. 结果：`bench-results/loadgen-<N>.log`（p50/p95/p99、dial/read/reconnect、RSS）、
   `metrics-<N>.txt`（网关 `/metrics`）、`fd-samples.log`（时序 RSS/fds 曲线）。

**指标口径**：`dial_ok/dial_fail`（握手）、`reconnect`（被动断开重连）、`read_err`、
`frames_out/in`（发出/收回，echo 模式下应接近 1:1）、`lat p50/p95/p99`、`rss_kb`（loadgen 自身）、
网关 RSS/fds/cgroup；`/healthz` 的 `hub.conns/frames_drop/slow_kicks/degrades` 也应全 0。
**PASS 判据**：`dial_fail=0`、`read_err=0`、`reconnect=0`、`frames_drop=0`、`slow_kicks=0`、
RSS 随连接数近似线性（≈77KB/conn）、无 OOM、5k 长稳 RSS 斜率接近 0。

---

## 6. 实测结果（2026-10-25，原始日志在 `bench-results/`）

压测条件：`conn-gateway` standalone + `-echo -dev-insecure -uds-disable`（WS-only、回显模式），
`conn-gateway` 由 transient unit 启动（`LimitNOFILE=65535`），loadgen 每连接 **1 frame/s**，
`OPENCLAW_ENABLED=off DSBOT_ENABLED=off`；阶段间 idle 30s；临时 sysctl（somaxconn/syn_backlog=8192）。

| 阶段 | conns | dur | dial_ok/fail | reconnect | frames_out/in | fps | p50 | p95 | p99 | GW RSS | fds | cgroup |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| L1 | 500 | 60s | **500 / 0** | **0** | 30,239 / 30,239 | 504 | 129.2µs | 487.4µs | 772.0µs | 35.9 MB | 506 | 29.5 MB |
| L2 | 2000 | 60s | **2000 / 0** | **0** | 121,344 / 121,344 | 2,022 | 127.7µs | 418.0µs | 635.1µs | 98.9 MB | 2,006 | 96.3 MB |
| L3 | 5000 | **300s** | **5000 / 0** | **0** | 1,504,222 / 1,504,222 | 5,013 | 127.0µs | 514.4µs | 953.7µs | **246.3 MB** | **5,006** | 252.3 MB |

* `read_err` 分别为 500/2000/5000 —— **等于连接数**，是收尾时 loadgen 主动 `conn.Close()` 使读循环各收到一次
  EOF/error 的计数，不是运行期丢帧（`frames_out == frames_in` 精确相等、`reconnect=0`、网关 `/metrics` 的
  `frames_dropped_total{reason=ring|shard|ackq|lease|uds}=0`、`slow_kicks=0`、`degrade_total=0`）。
* **每连接内存**：网关 RSS 相对基线（~10.4 MB）增量 ≈ 51 / 44 / **47 KB/conn**（设计模型 77 KB/conn，实测更好）。
* **5000 连接长稳（300s）**：RSS 从 t+120s 的 246,136 KB 到 t+301s 的 246,340 KB，**+204 KB / 181s ≈ 1.1 KB/s**，
  漂移 **0.08%**（远小于 10% 阈值）→ 无泄漏迹象；cgroup 内存 252.06→252.29 MB 同样持平。
* **CPU**：5k 阶段网关 `utime+stime` 4,306−433 = 3,873 ticks / 331s ≈ **11.7% of 1 core**（约 2.9% of 4 vCPU）。
* **loadgen 侧**：RSS 28 MB / 80 MB / 208 MB（500/2000/5000），代码自身 GC 正常。
* **主机**：available 6992 MB → 5k 峰值最低 **6560 MB**（额外占用 ~336 MB）→ 结束回落 6739–6745 MB；
  **swap 全程 0 使用**。
* **结论（PASS）**：三档 `dial_fail=0`、`reconnect=0`、零丢帧、零 drop/backpressure、无 OOM、
  延迟 p99 < 1 ms（loopback echo）、5k 300s RSS 持平。**4 vCPU / 8 GB 上 5k 连接远未到瓶颈**：
  内存只用了 ~250 MB，fds 5,006（上限 65,535），CPU 11.7%，带宽 ~0.8 MB/s。

> 口径提醒：这是 **echo 回显模式**下的容量曲线（每帧经 hub 回写，不接内核 UDS），绝对延迟只代表环回
> 路径；真实内核/DB 链路（C4 E2E）会另测。rate 为 **每连接** 帧率（1/s/conn → 总 5,013 fps @5k）。

## 7. 部署形态验收（embedded 18080 + 独立 conn-gateway 18081）

已用仓库 `deploy/vultr/install.sh --skip-nginx --binary …` 部署完成（这台机没有 nginx，
install.sh 已修成 nginx 可选；`--skip-nginx` 时不会要求 nginx 存在）：

| 验收项 | 结果 |
|---|---|
| `im2a-gateway.service` | **active**，`*:18080` 监听，`NRestarts=0`，RSS ≈ 16 MB，cgroup 3.7 MB |
| `/healthz`（Bearer admin token） | **200** `{"service":"gateway","status":"ok","version":"v2.3.25"}` |
| 匿名 `/v1/plugins` | **200**，`dsbot`/`openclaw` 均 **`state:"disabled"`**（`DSBOT_ENABLED=0` / `OPENCLAW_ENABLED=false`，**未虚报 ok**） |
| 匿名 `/v1/about/attribution` | **200**，`thirdParty` **3 条**（dsbot Apache-2.0 / openclaw MIT / openclaw-weixin MIT），text 版可读 |
| `POST /v1/msg/seq` | noauth 401 / `GATEWAY_TOKEN` **400**（`{"error":"userId required"}`），**非 404** |
| `GET /v1/ws`（无 topic） | noauth 401 / `GATEWAY_TOKEN` **400** `{"error":"missing topic"}`（embedded 行为） |
| `attribution-check.sh --runtime` | **全部通过 ✅**（部署树模式，`ATTR_RC=0`） |
| unit/进程 `LimitNOFILE` | unit 写 **65536**，进程实际 `Max open files 65536/65536`（≥ B1 要求的 65535） |
| ufw | active：**仅 22/tcp 对 Anywhere**；18080/18081 仅放行 `192.168.0.0/16`（内网）；新 SSH 连接验证 OK |
| **共存** | embedded(:18080) 与 standalone `conn-gateway`(:18081) 同时跑：两者 `/healthz` 均 **200**、embedded `/v1/plugins` 仍 200；停 standalone 后 embedded 仍 200 |
| 二进制 | `go build ./services/gateway`（当前分支 Wave A/B2/C1–C4），**21,069,447 B / sha256 `c212196e…`**，unit 的 `-selftest` **15 项 PASS** |

部署产物：`/opt/im2a/{bin,data,scripts,ATTRIBUTION.md,NOTICE,LICENSE,README.md,THIRD_PARTY_LICENSES,DEPLOYMENT.md}`、
`/etc/im2a/env`（0600，密钥机器生成）、`/etc/systemd/system/im2a-gateway.service`、备份 `/var/backups/im2a/install-*`。

> 本机**没有**安装 Node/OpenClaw/DSBot 运行时（按 Lead 分工：先只做压测+部署验证；完整插件形态另开一轮）。
> 插件在 `/v1/plugins` 里如实显示 `disabled`，归属公示仍聚合 3 上游。

---

## 8. 清理 / 关机前

```bash
# 停止所有 transient 压测单元
sudo systemctl stop conn-gw-bench 2>/dev/null; sudo systemctl reset-failed conn-gw-bench 2>/dev/null
# 停部署服务（如不再需要）
sudo systemctl disable --now im2a-gateway 2>/dev/null

# 删压测产物（可选）
rm -rf /home/ubuntu/im2a-bench/bench-results /home/ubuntu/im2a-bench/bench-run.log

# 删测试库（可选；会丢掉 E2E 数据）
sudo -u postgres dropdb --if-exists im2a_e2e

# 临时 sysctl 不持久化，reboot 自动恢复；如要立即恢复：
sudo sysctl -w net.core.somaxconn=4096 net.ipv4.tcp_max_syn_backlog=512 net.ipv4.ip_local_port_range="32768 60999"

# 卸载（可选）
sudo apt-get remove -y postgresql-16
sudo rm -rf /usr/local/go1.27.3 /usr/local/bin/go
```

---

## 9. 安全 / 纪律

* 公网只放行 **22**（ufw 规则见部署记录）；18080/18081 只监听本机/loopback 或由 ufw 挡住，**不接域名、不做对外服务**。
* `PG_DSN` 只在需要的 shell `source`，**不要全局 export**、不写 `/etc/environment`、不写 systemd unit。
* 压测/全量测试**串行**跑；带 PG 的测试用 `-p 1` 逐包。
* 密码凭据一次性使用，**建议轮换或销毁**；任务结束后按 §1 的流程收紧 sshd。
* 该机所有临时 systemd unit 均为 `conn-gw-bench`（transient），无持久部署单元残留（除显式部署的 `im2a-gateway.service`）。
