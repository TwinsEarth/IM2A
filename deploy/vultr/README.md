# IM2A × OpenClaw — Vultr 共享生产机部署手册

> 目录：`deploy/vultr/`（仓库内可复现的所有部署产物）
> 目标机：`root@202.182.123.154`（Vultr，Debian 11，1 vCPU，959 MB RAM + 3 GB swap，30 GB 盘）
> 本机同时承载 **w2m 舰队**（xray / nginx / gsn-daemon / w2m-* / agent-universe-relay），
> 本部署**只新增**文件与服务，不改动任何既有服务、既有 nginx server 块和防火墙规则。

| 项目 | 值 |
|---|---|
| IM2A 服务单元 | `im2a-gateway.service`（systemd，`Restart=always`，`EnvironmentFile=/etc/im2a/env`） |
| 后端监听 | `0.0.0.0:18080`（代码 `main.go` 固定 `":"+port`；ufw 默认拒绝外部访问，实测公网不可达） |
| 公网入口 | **http://im2a.202.182.123.154.nip.io/**（nginx 新增 vhost，:80 → 127.0.0.1:18080） |
| 裸 IP | **仍走 w2m 的 `default_server`（刻意设计）**：`http://202.182.123.154/` 是 w2m relay，不是 IM2A；IM2A 只从 nip.io 域名访问 |
| 本机健康检查 | `curl -H "Authorization: Bearer $IM2A_ADMIN_TOKEN" http://127.0.0.1:18080/healthz` |
| 公开只读端点 | `/v1/plugins`、`/v1/about/attribution`（匿名，带 `?format=text` 可读版） |
| OpenClaw 运行方式 | **on-demand**（CLI 子进程）；常驻 gateway 在本机 320M/384M 实测 OOM，见 §6 |
| 归属 | OpenClaw：MIT，`Copyright (c) 2026 OpenClaw Foundation`，见 `/v1/about/attribution` |

---

## 1. 资源评估（实测）

### 1.1 主机与既有负载

| 指标 | 实测值 | 说明 |
|---|---|---|
| CPU | 1 vCPU | `nproc` = 1；`uname` 6.5.6-bbrplus (Debian 11) |
| 内存 | 959 MB total（`MemTotal: 982180 kB`） | 与 w2m 舰队共享 |
| swap | **3 GB `/swapfile`（`swapon --show` 实测 3G，已用 ~209 MB；`/etc/fstab` 有持久条目，`swapfile.swap` active）** | 2026-10-25 复核：**已有 3 GB，无需再加 2 GB**。注意：本部署**不允许** IM2A 依赖 swap 常驻；swap 也**不能**防「并行跑全量测试把内存打到 fork 失败」——见 §11 |
| 盘 | 30 GB，部署前剩 16 GB | `/opt` 同一分区 |
| 部署前可用内存 | **321–620 MB 波动**（实测最小值出现在 OpenClaw npm 安装期间：available=321 MB） | 既有服务 used ≈ 180–490 MB |
| 既有常驻进程（勿动） | xray、nginx、gsn-daemon、w2m-{rabbit,localside,member-tencent,pub,stun,bootstrap}、agent-universe-relay（node :8787、:8911） | 只用 `ss -lntp` 观察，不重启 |

### 1.2 内存预算

| 组件 | 限制 | 依据 |
|---|---|---|
| `im2a-gateway.service` | `MemoryHigh=384M` / `MemoryMax=512M` / `MemorySwapMax=64M` | Go 网关稳态 RSS 仅 ~10 MB；但插件启动探测会 fork 一个 Node24 的 `openclaw` 子进程，**它要把 619 MB 的包树页进内存，而页缓存计入本 cgroup**。实测 `MemoryHigh=192M` 会触发 `memory.high` 强节流，使探测永不完成（手动无 cgroup 时 ~19 s 成功）。因此按"包体积"而非"RSS"留余量 |
| `openclaw-gateway.service` | **不安装、不启用**（文件保留在仓库） | 320M/384M 均 OOM（§6）；需 ≥640 MB 专用余量并重新实测才可开 |
| 部署后红线 | `free -m` available **≥ 80 MB** | 低于红线必须降 `MemoryMax` 或保持 on-demand，并在本文件记录取舍 |

### 1.3 部署后实测（本轮，2026-10-25 05:02 UTC）

| 指标 | 实测值 |
|---|---|
| `im2a-gateway` 稳态 RSS | **9.7 MB**（`ps -o rss -C im2a-gateway`；不同时刻 9.7–10.6 MB） |
| `im2a-gateway` systemd cgroup `MemoryCurrent` | **11.7 MB**（12,345,344 B；`MemoryMax=512M`） |
| 插件启动探针峰值 | `openclaw --version` 单进程 cgroup 峰值 **66.7 MB**（69,914,624 B）；最终启动 `probe took 10.36s` |
| 部署后 `free -m` available | **601 MB**（used 175 MB，buff/cache 481 MB） |
| 部署后 `free -m` used | 175 MB |
| 启动到 active | **12 s**（selfcheck 通过；插件探测 10.36 s） |
| 插件状态 | dsbot `ok` + openclaw `ok`（`openclaw 2026.10.7 (157d8c2); channel openclaw-weixin ready (WeChat); account not configured (QR login required)`） |
| 结论 | 内存红线（80 MB）富余 7.5 倍；OpenClaw on-demand 方案可行，无需常驻 |

---

## 2. 端口表

| 端口 | 监听地址 | 服务 | 归属 | 改动 |
|---|---|---|---|---|
| 22 | 0.0.0.0 | sshd | 既有 | 未动 |
| 80 | 0.0.0.0 / [::] | nginx `default_server` → w2m relay（`/`、`/healthz`、`/v1/stream`、`/pub/`） | **w2m 既有** | **未动** |
| 80（Host=`im2a.202.182.123.154.nip.io`） | — | nginx 新增 vhost → 127.0.0.1:18080 | IM2A | **新增** `/etc/nginx/conf.d/im2a.conf` |
| 8787 | 0.0.0.0 | w2m rabbit（HTTP API） | 既有 | 未动 |
| 8911 | 0.0.0.0 | w2m localside（node） | 既有 | 未动 |
| 8899 | 0.0.0.0 | 既有 python 服务 | 既有 | 未动 |
| 4001/4002/556 | * | xray / w2m stun 等 | 既有 | 未动 |
| 31300/31302/31296/31297/35521 | 127.0.0.1 / 0.0.0.0 | nginx ssl / xray（geefish.cn 订阅） | 既有 | 未动 |
| **18080** | 0.0.0.0（Go `:port`） | **IM2A gateway** | **IM2A 新增** | 新增 |
| **5432** | **127.0.0.1（仅 loopback，外网不可达）** | **PostgreSQL 13.23**（B2-b E2E 真库 `im2a_e2e`，**非生产数据**） | IM2A E2E 辅助 | 新增（2026-10-25，详见 `deploy/bench/README.md` §10） |
| 18789 | 127.0.0.1（仅当可选常驻单元启用） | OpenClaw gateway WebSocket | 可选，未启用 | 未监听 |

> **18080 外部不可达是预期行为**：ufw `default deny incoming`，只放行
> 22/80/556/666/5566/8787/3478-udp/41234-udp/51820-udp/4001（实测从公网
> `:18080` 超时、`:80` 与 `:8787` 返回 200）。公网入口走 nginx :80 的 nip.io vhost。
> 若确实需要 `IP:18080` 直连，需新增一条 `ufw allow 18080/tcp`——本部署**没有**这么做。

---

## 3. 部署布局

```
/opt/im2a/
├── bin/im2a-gateway             # Go 二进制（install.sh 构建，-selftest PASS 后才安装）
├── data/build-info.txt          # 构建时间/源码目录/commit/sha256（可追溯）
├── logs/                        # 预留（日志默认走 journald）
├── scripts/selfcheck.sh         # systemd ExecStartPost 启动自检（需鉴权 /healthz=200）
├── scripts/verify.sh            # 一键验收证据（只读）
├── scripts/attribution-check.sh # MIT 归属自检（仓库原件；对部署树自动进入部署树模式）
├── ATTRIBUTION.md  NOTICE  LICENSE  README.md   # README 副本供归属自检的"公示章节"检查
├── THIRD_PARTY_LICENSES/*.txt   # OpenClaw-MIT.txt（+ OpenClaw-Weixin-MIT.txt，若源码树提供）
└── DEPLOYMENT.md                # 本文件

/etc/im2a/env                    # 0600 root:root，install.sh 生成 3 个 32-byte hex 密钥
/etc/systemd/system/im2a-gateway.service
/etc/nginx/conf.d/im2a.conf      # 新增文件；既有 alone/w2m/subscribe.conf 未改动
/var/backups/im2a/install-<ts>/  # 每次安装前对将覆盖文件的备份（含 manifest.tsv）
```

> `/opt/im2a` 只放**部署物**，不放源码树。归属自检脚本在检测到部署树（无 `$ROOT/internal`）时
> 自动跳过源码级检查并打印 `INFO 部署树模式`，许可证/ATTRIBUTION/NOTICE/README/运行时端点
> 仍严格检查。

镜像仓库对应文件：

```
deploy/vultr/
├── install.sh
├── README.md
├── env/im2a.env.example
├── nginx/im2a.conf
├── scripts/selfcheck.sh
├── scripts/verify.sh
└── systemd/im2a-gateway.service
    systemd/openclaw-gateway.service   # 可选常驻单元，本机不启用
```

---

## 4. 安装 / 升级

前置：Go 1.27（`/usr/local/go/bin`）、Node 24 + `openclaw`（`/usr/local/node24/bin`）、nginx、curl、jq。
源码/构建树在 `/root/im2a-work/build`（由仓库 `sync-build.ps1` 刷新；`install.sh` 会与它共用
`/tmp/im2a-build.lock` 避免和同步循环打架）。

```bash
# 0) 先看会做什么，不改任何东西
bash /root/im2a-work/deploy/vultr/install.sh --dry-run --build-dir /root/im2a-work/build

# 1) 正式安装/升级（幂等；已存在的密钥不会被覆盖）
bash /root/im2a-work/deploy/vultr/install.sh --build-dir /root/im2a-work/build

# 2) 只用预编译二进制（服务器上已构建好时）
bash /root/im2a-work/deploy/vultr/install.sh --binary /root/im2a-work/im2a-gateway

# 3) 轮换 /etc/im2a/env 中的三个密钥（同时重启服务）
bash /root/im2a-work/deploy/vultr/install.sh --rotate-secrets
```

`install.sh` 行为要点：

* `set -euo pipefail`；参数错误/前置检查失败时**不做任何修改**即退出。
* 幂等：重复执行 = 重建二进制 + 重装单元 + `daemon-reload` + `restart`；`/etc/im2a/env` 中
  已有的 `IM2A_ADMIN_TOKEN` / `GATEWAY_TOKEN` / `IM2A_JWT_SECRET` 原样保留（仅 `--rotate-secrets` 才换）。
* 密钥：`openssl rand -hex 32` 生成，只写入 `/etc/im2a/env`（0600 root:root），**不写入仓库、不打印**。
* **升级注意（egress fail-closed，2026-10-25）**：新版本 `internal/edge.EgressGuard` 的默认语义从
  "空白名单 = 全放行"改为 **fail-closed（空白名单拒绝一切出站）**。Vultr 升级到本版本后，若使用
  `POST /v1/edge/pipeline` 的端侧出站能力，**必须先**在 `/etc/im2a/env` 显式配置
  `IM2A_EGRESS_ALLOW=host1,host2`（逗号分隔、不含 scheme）；未配置时 `egress_allowed` 恒为 `false`。
  当前现网未配置该变量、运行的是升级前旧二进制（fail-open），下次升级会切换语义（见 §9 风险表）。
  > **预期行为（不是故障）**：升级后 `POST /v1/edge/pipeline` 在未配置 `IM2A_EGRESS_ALLOW` 时返回
  > `egress_allowed:false`，这是**有意的安全默认**（产品承诺"音频不出端"）；不要当成服务故障。
  > Lead 已裁决升级后保持 `IM2A_EGRESS_ALLOW` **空**（fail-closed）；将来要做出站联调时再显式配置。
* 覆盖 systemd/nginx 文件前先 `cp -a` 到 `/var/backups/im2a/install-<ts>/`；`nginx -t` 失败会自动
  还原上一份 nginx 配置并退出（不会留下坏配置）。
* 启动自检：`ExecStartPre=<bin> -selftest`（路由自检）+ `selfcheck.sh`（等监听并验证
  **带 token 的 /healthz=200**，120 s 超时）。自检失败 → 单元 failed → `Restart=always` 重试，
  不会出现"服务在跑但不可用"的静默状态。
* 不碰：xray、w2m-*、gsn-daemon、agent-universe-relay、既有 nginx server 块、防火墙、sshd。
* **OpenClaw 探测是并行 + 分层的**（2026-10-25 修订）：网关启动时插件**并行**跑
  `openclaw --version`（超时 12 s）与 `openclaw channels list --all`（超时 20 s，config-only，
  **不拉起 gateway**），实测 **probe took 8.96 s**、启动到 listen 约 10–12 s。
  历史教训：早期版本串行跑 `--version` + `gateway status` + `channels capabilities`，
  实测 24.68 s 仍超时，且 `channels capabilities` 会拉起 200 MB+ 的 gateway 孙进程；
  更严重的是 CLI 孙进程继承 stdout 管道，只杀父进程会让 `CombinedOutput` **永久阻塞**。
  现已改为进程组整组击杀（`Setpgid` + `kill(-pid)`）并移除启动期的 capabilities 调用。
  - `systemctl status` 会先显示 `activating` 约 10–15 s 才变 `active`；
  - **重启后 10–20 秒内 nginx 返回 502 属预期现象**，不是故障；
  - `TimeoutStartSec=300`、`ExecStartPost` 自检窗口 `IM2A_SELFCHECK_TIMEOUT=240` 已放宽。

---

## 5. 验证（验收证据）

一键（只读，全部硬断言；失败退出码非 0）：

```bash
bash /opt/im2a/scripts/verify.sh
# 或仓库内同一份：bash deploy/vultr/scripts/verify.sh
```

逐条命令（task-7 要求）：

```bash
# 1) 服务状态
systemctl status im2a-gateway --no-pager | head -15

# 2) 监听
ss -lntp | grep 18080

# 3) 健康（注意：/healthz 需要 admin token；token 为空时 auth 会整体跳过 = 开发模式）
set -a; . /etc/im2a/env; set +a
curl -sS -m 5 -i -H "Authorization: Bearer $IM2A_ADMIN_TOKEN" http://127.0.0.1:18080/healthz | head -20
# 无 token 应为 401（证明 token 生效）：
curl -sS -m 5 -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18080/healthz

# 4) 公开只读端点（匿名，不要带 header —— 证明公众可查）
curl -sS -m 5 http://127.0.0.1:18080/v1/plugins
curl -sS -m 5 "http://127.0.0.1:18080/v1/about/attribution?format=text"

# 5) 公网入口（nginx vhost，带 X-Forwarded-*）
curl -sS -m 8 http://im2a.202.182.123.154.nip.io/
curl -sS -m 8 http://im2a.202.182.123.154.nip.io/v1/plugins

# 6) 资源
free -m
ps -eo pid,rss,comm,args --sort=-rss | head -12
systemctl show im2a-gateway -p MemoryCurrent -p MemoryMax

# 7) MIT 归属公示（仓库文件 + 运行时端点）
bash /opt/im2a/scripts/attribution-check.sh --runtime

# 8) OpenClaw 运行时（on-demand）
/usr/local/node24/bin/openclaw --version
/usr/local/node24/bin/openclaw gateway status --json | head -12
/usr/local/node24/bin/openclaw channels list --all | grep -i -A1 'weixin\|wechat'
# 微信插件验收探针（不触发扫码登录；真实发消息需用户本人扫码）
/usr/local/node24/bin/openclaw channels capabilities --channel openclaw-weixin
```

---

## 6. OpenClaw 内存可行性结论：**on-demand（常驻不可行）**

### 6.1 实测方法

在**同一台机器**上，用 systemd 瞬时单元给 `openclaw gateway` 加硬 cgroup 上限，并隔离状态目录
（`OPENCLAW_STATE_DIR=/root/im2a-work/oc-test/state`，不碰生产 `/root/.openclaw`）：

```bash
systemd-run --unit=oc-memtest --collect -p MemoryAccounting=yes -p MemoryMax=${LIM}M \
  -p MemorySwapMax=0 -p Restart=no -p StandardOutput=journal \
  --setenv=HOME=/root --setenv=OPENCLAW_STATE_DIR=/root/im2a-work/oc-test/state \
  --setenv=PATH=/usr/local/node24/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  /usr/local/node24/bin/openclaw gateway --allow-unconfigured --bind loopback --port 19100
# 轮询 systemctl show oc-memtest -p MemoryCurrent / journalctl -u oc-memtest
```

### 6.2 结果

| 限制 | 结果 | 证据 |
|---|---|---|
| `--version`（320M scope） | ✅ 正常 | `OpenClaw 2026.10.7 (157d8c2)` |
| `gateway status --json` | ✅ 正常（不常驻，纯 CLI 探测） | exit 0，`loaded:false` |
| **MemoryMax=320M** | ❌ **启动中 OOM** | 启动 ~12 s、卡在 `state.schema-preflight`；`status=9/KILL`、`result='oom-kill'`；从未 listen 19100 |
| **MemoryMax=384M** | ❌ **启动中 OOM** | 启动 ~16 s、卡在 `config.auth`；同样 `oom-kill`；从未 listen |

结论：**`openclaw gateway` 启动瞬态 > 384 MB**（且 1 vCPU 上启动 15 s+）。本机 available 只有
321–620 MB 且波动，红线 80 MB，再放一个 >384 MB 的常驻进程必然击穿红线并威胁 w2m 舰队。
→ **本机 OpenClaw 一律 on-demand**：IM2A 子插件只解析 bin、跑 `--version`、`gateway status`、
`message send` 等短命 CLI 子进程；`systemd/openclaw-gateway.service` 仅作为"更高内存主机"的
备选文件保留在仓库，`install.sh --with-openclaw-unit` 只复制、不 enable、不 start。

### 6.3 对 IM2A 启动的影响

* 插件 `Start()` 按契约**同步**探测：`openclaw --version`（实测 ~0.3 s）+
  `channels list --all`（快路径，config-only）+ 必要时 `gateway status`/`capabilities`。
  2026-10-25 05:00 最终实测：**启动到 listen 12 s，`probe took 10.36s`，插件状态 `ok`**。
  `TimeoutStartSec=300`、`ExecStartPost` 自检窗口 240 s 已覆盖；**重启后 15–20 s 内 502 属预期**。
* 实测插件最终状态为 **`ok`**：`openclaw 2026.10.7 (157d8c2); channel openclaw-weixin ready
  (WeChat); account not configured (QR login required)` —— 账号未登录是正常态（等用户扫码），
  `/v1/plugins` 里 `state` 如实反映。
* 历史坑（已由 `internal/plugin/openclaw` 修复，勿回退）：早期探测走 `channels capabilities`，
  该命令会经 gateway RPC 拉起 200 MB+ 的 gateway 孙进程，在 256 MB cgroup 下被节流 +
  管道不关导致**启动永久阻塞**。现改为 `channels list --all` 快路径 + 进程组整体 SIGKILL 兜底。
* OpenClaw 缺失/损坏时插件进入 `degraded`，**IM2A 其余功能照常启动**（契约原则 2 不阻塞启动）。
* 端到端延迟：每次 on-demand 调用要付 Node 冷启动成本（实测 `--version` 6–10 s；`gateway
  status` 类似量级），因此**不适合高频调用**；扫码登录/发消息为低频操作，可接受。
* 若未来升级到 ≥2 GB 内存主机：按 `systemd/openclaw-gateway.service` 顶部注释重新实测
  （建议 `MemoryMax=640M` 起）后再 enable，并保持 `MemorySwapMax=0`。

### 6.4 微信通道（外部社区插件，非 OpenClaw 内置）

OpenClaw 内置通道**不含微信**。微信通道是外部社区插件 **`openclaw-weixin@3.3.1`**
（MIT，`Copyright (c) 2026 Tencent` + `Copyright (c) 2026 Contributors to NewFuture/openclaw-weixin`，
基于腾讯 iLink bot API），由运维在服务器上单独安装，**不随本仓库分发**。

已完成的安装方式（可复现，踩坑记录）：

* 包解包到 **`/opt/openclaw-plugins/openclaw-weixin`**，`npm install --omit=dev` 装
  `qrcode-terminal`，再用 `openclaw plugins install --link` 链接进 OpenClaw。
* **坑**：直接 `openclaw plugins install openclaw-weixin` 会**静默失败**——本机 `/tmp` 属主是
  uid 501（镜像从 macOS 带入的错误属主），安装器拒绝使用共享 temp 目录，日志只留一行
  `temp workspace directory "/tmp" has an untrusted owner uid 501; expected uid 0`。
  解法是 `npm pack` 到 root 私有目录 + `--link`。**绝不要**去 `chown`/`chmod` `/tmp`
  （会影响整机所有服务）。
* 现状（验收证据）：`plugins list` → `WeChat / openclaw-weixin / enabled / 3.3.1`；
  `channels capabilities --channel openclaw-weixin` → `Status: not configured, enabled`。
  真正收发消息需要**用户本人微信扫码**，部署验收不做、也不应做扫码登录。

```bash
openclaw plugins list | grep -i weixin                       # WeChat | openclaw-weixin | enabled | 3.3.1
openclaw channels list --all | grep -i weixin                # WeChat: installed, not configured, disabled
openclaw channels capabilities --channel openclaw-weixin     # Status: not configured, enabled  <- 验收探针
# 不要跑 openclaw channels login（会挂起等待用户扫码）
```

---

## 7. 归属与许可（MIT 合规）

* 上游：**OpenClaw** <https://github.com/openclaw/openclaw>，MIT，
  `Copyright (c) 2026 OpenClaw Foundation`；本仓库以**独立子插件**形式集成，未复制/未内联上游源码。
* 运行时公示：`GET /v1/about/attribution`（支持 `?format=text`）与 `GET /v1/plugins`（匿名只读）。
  当前构建（v2.3.25）实测返回 **3 条 thirdParty**：`dsbot`(Apache-2.0) / `openclaw`(MIT) /
  `openclaw-weixin`(MIT)；每条都带上游 URL、版权行、`noticePath` 与 `usage` 清单。
  本文只负责 OpenClaw 系；dsbot 的归属由同一份 `ATTRIBUTION.md`/`NOTICE` 覆盖。
* 分发物内随附：`/opt/im2a/ATTRIBUTION.md`、`NOTICE`、`LICENSE`、
  `THIRD_PARTY_LICENSES/{OpenClaw-MIT,OpenClaw-Weixin-MIT,DeepSeek-Bot-LICENSE,DeepSeek-Bot-NOTICE}.txt`
  （逐字复制，未改动）。
* 机器可执行自检：`bash /opt/im2a/scripts/attribution-check.sh --runtime`
  （仓库文件 + 运行时端点，全部 PASS 才是验收通过）。
* `openclaw-weixin`（微信通道）是**外部社区插件**，由运维单独安装，不随本仓库分发；
  其 MIT 许可文本随源码树落在 `THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt`，归属见
  `ATTRIBUTION.md` §2。
* 本部署文档本身遵循仓库 `LICENSE`。

---

## 8. 回滚

```bash
# 标准回滚：停用并移除 im2a-gateway，还原最近一次安装前备份的 unit 与 nginx vhost，
# 重新 nginx -t + reload；保留 /opt/im2a（二进制/数据）与 /etc/im2a/env（密钥）。
bash /root/im2a-work/deploy/vultr/install.sh --rollback

# 彻底清理（连数据与密钥一起删；谨慎）
bash /root/im2a-work/deploy/vultr/install.sh --rollback --purge
```

手动兜底（回滚脚本不可用时）：

```bash
systemctl disable --now im2a-gateway
rm -f /etc/systemd/system/im2a-gateway.service && systemctl daemon-reload
rm -f /etc/nginx/conf.d/im2a.conf
nginx -t && systemctl reload nginx
# 既有 w2m/xray 配置从未被修改，无需恢复
```

备份位置：`/var/backups/im2a/install-<ts>/`（含将被覆盖的 `im2a-gateway.service`、
`im2a.conf` 与 `manifest.tsv`；原本不存在的文件记为 `absent`，回滚时删除）。

---

## 9. 风险与遗留

| 风险 | 说明 / 缓解 |
|---|---|
| 公网入口依赖 nip.io DNS | `im2a.202.182.123.154.nip.io` 由公共通配 DNS 解析；DNS 故障时可用 `curl -H 'Host: im2a.202.182.123.154.nip.io' http://202.182.123.154/` 直连；正式方案应换真实域名 + TLS |
| **裸 IP 不是 IM2A（刻意设计）** | `http://202.182.123.154/` 命中 w2m 的 `default_server`，这是为了**不改既有 nginx 配置**。IM2A 只从 nip.io 域名访问；不要把排障流量引导到裸 IP |
| 无 TLS | 当前 :80 明文（与既有 w2m 一致）。上真实域名时再加证书与 301 |
| 18080 对公网暴露面 | 代码监听 `0.0.0.0:18080`，但 ufw 拒绝外部访问（实测超时）；若日后放行该端口，需同时评审核查 |
| 管理端点 | `/healthz`、`/metrics`、`/v1/ops/*` 需 `IM2A_ADMIN_TOKEN`；`/v1/plugins`、`/v1/about/attribution` 匿名只读为设计如此。token 为空 = auth 整体跳过（开发模式），**生产必须设置**（install.sh 已生成） |
| 1 vCPU 资源竞争 | OpenClaw CLI 冷启动 6–10 s 是 CPU-bound；与 w2m 舰队同机，避免高频触发 |
| 无数据库 | 未配置 `PG_DSN` 时网关用内存实现（重启丢状态），部分端点返回 503；本部署只验证网关/插件/归属链路，未接 PG |
| 内存波动 | available 会在 321–620 MB 间波动；`MemoryMax=512M`（High=384M/SwapMax=64M）+ 红线 80 MB，部署后实测 available 601 MB（见 §1.3） |
| OpenClaw 常驻 | 明确不可行（§6）；不要在本机 enable `openclaw-gateway.service` |
| 构建树共享 | `/root/im2a-work/build` 同时被 `sync-build.ps1` 刷新；`install.sh` 用 `/tmp/im2a-build.lock` 互斥 |
| **测试/构建内存峰值（已发生过事故）** | 2026-10-25 有人在 Vultr 并行跑「带 `PG_DSN` 的全量 `go test -p 1 ./...` + E2E」，959 MB 被打到 **fork 失败：TCP 22 能 accept 但 sshd 完成不了握手**。**3 GB swap 也防不住**这种瞬时驻留峰值。**纪律见 §11** |
| 本地 PG（E2E，非生产） | 2026-10-25 为 B2-b 真库 E2E 新装 PostgreSQL **13.23**（Debian 11 已无 PG16 源），**只监听 127.0.0.1:5432**，仅 `im2a_e2e` 测试库、非生产数据，内存上限 256M；生产网关仍未配置 `PG_DSN`（详见本文 §12） |
| 出站白名单 fail-closed（升级语义变更） | 新版本 `IM2A_EGRESS_ALLOW` 未配置时**拒绝一切出站**（旧版本为空白名单=放行）。Vultr 现网未配置该变量、当前运行旧二进制（实测 `POST /v1/edge/pipeline` 返回 `egress_allowed:true`）；**下次升级后**如需端侧出站必须先显式配置白名单，否则相关调用会得到 `egress_allowed:false` |

---

## 10. 运维速查

> **重启须知**：`systemctl restart im2a-gateway` 后会有 **15–20 秒**的启动探测窗口
> （插件同步跑 3 个 openclaw CLI），期间 `systemctl status` 是 `activating`、nginx 返回 502，
> 属预期。`systemctl restart` 本身会等 `ExecStartPost` 自检通过才返回。

```bash
journalctl -u im2a-gateway -n 100 --no-pager          # 日志（含 OpenClaw 归属横幅）
systemctl restart im2a-gateway                        # 重启（会跑启动自检）
systemctl show im2a-gateway -p MemoryCurrent -p MemoryMax
cat /opt/im2a/data/build-info.txt                     # 当前二进制来源
nginx -t && systemctl reload nginx                    # nginx 变更后（reload，不是 restart）
tail -20 /var/log/nginx/im2a.access.log
```

调整 `MemoryMax`：改 `/etc/systemd/system/im2a-gateway.service` 的 `MemoryMax=`（仓库同文件同步改），
然后 `systemctl daemon-reload && systemctl restart im2a-gateway`。改后必须重跑 `verify.sh` 确认
available ≥ 80 MB。

---

## 11. 测试 / 构建内存纪律（2026-10-25 事故后新增，必读）

**事故经过**：2026-10-25 在 Vultr（959 MB RAM，同时承载 nginx/xray/w2m 舰队/im2a-gateway/PG）
上并行执行「带 `PG_DSN` 的全量 `go test -p 1 ./...`」与 E2E，内存被打满，出现
**fork 失败、TCP 22 可 accept 但 sshd 无法完成握手**（需人工介入等待自愈）。
机器已有 **3 GB swap**（`/swapfile`，`/etc/fstab` 持久化，`swapon --show` 可验），
但 swap **挡不住**「大量进程同时驻留」造成的瞬时内存/VM 峰值。

**长期约束（必须遵守）**：

1. **不要并行**跑「全量测试 + PG 真库用例 + E2E」。三者串行，一次只跑一类。
2. 带 `PG_DSN` 的全量验证必须 **`-p 1` 且逐包跑**（避免 1 vCPU 上多个 test 二进制同时驻留）：
   ```bash
   cd /root/im2a-work/build
   source /root/im2a-work/e2e-env.sh      # 只在当前 shell，不要全局 export
   for pkg in $(go list ./...); do
     GOMAXPROCS=1 nice -n 10 go test -p 1 -count=1 "$pkg" || break
   done
   ```
3. **`PG_DSN` 只按需 source**：`source /root/im2a-work/e2e-env.sh`（0600，密码不落仓库）。
   **不要**全局 `export`、不要写 `/etc/environment`、不要写进 systemd unit —— 否则普通
   `go test ./...` 也会命中真实 PG 用例，内存直接翻倍。
4. 跑测试/E2E 时给进程组加 cgroup 上限（防止把 sshd 顶死）：
   ```bash
   systemd-run --scope -p MemoryMax=400M -p MemorySwapMax=1G -- \
     bash -lc 'cd /root/im2a-work/build && source /root/im2a-work/e2e-env.sh && GOMAXPROCS=1 go test -p 1 -count=1 ./internal/store'
   ```
   cgroup 只会限制/杀死测试进程组，不会波及 sshd/nginx/w2m/im2a-gateway。
5. 若再次发生内存打满：先停掉测试用 transient 单元（`systemctl list-units 'run-*'`）或
   `pkill -f "go test"`，**不要**动既有生产服务；sshd 握手失败时等负载回落再重连。

---

## 12. Vultr 本地 PG（B2-b E2E 用，**非生产数据**）

> 2026-10-25 为 B2-b「kill -9 零丢」真库 E2E 在 **Vultr `202.182.123.154`** 上装的本地 PG。
> **只监听 127.0.0.1，不外网可达；不是 IM2A 生产数据**（生产网关也未配置 `PG_DSN`）。

| 项目 | 值 |
|---|---|
| 版本 | **PostgreSQL 13.23**（Debian 11 bullseye archive，`postgresql-13` + `postgresql-contrib-13`）。说明：Debian 11 已 EOL，PGDG 已删除 `bullseye-pgdg` 仓库（实测 404），bookworm 的 PG16 包要求 libc≥2.34 无法装；迁移契约测试已在 PG13.23 上全 PASS（13/16 在 sequence/JSONB/ON CONFLICT/pgcrypto 语义一致）。需要 PG16 精确对齐时请用干净机 `134.175.243.146`（PG 16.15）。 |
| 监听 | `127.0.0.1:5432`（`listen_addresses='127.0.0.1'`） |
| 集群 | Debian `13/main`，`pg_lsclusters` online；`postgresql.service` enabled |
| 测试库 | `im2a_e2e`（owner `im2a_e2e`，pgcrypto）——可随时 drop 重建 |
| 连接串 | `source /root/im2a-work/e2e-env.sh`（0600 root；密码只在本机，不打印/不进 git） |
| 内存 | `shared_buffers=32MB, max_connections=30`；systemd drop-in `MemoryHigh=192M / MemoryMax=256M` |
| 实测占用 | PG 主进程 RSS ≈ 10 MB，全部 PG 进程 ≈ 51 MB；cgroup ≈ 6.8–25 MB |

```bash
# 只在需要 PG 的 shell 里 source，不要全局 export
source /root/im2a-work/e2e-env.sh
psql "$PG_DSN" -c 'select version();'
cd /root/im2a-work/build && PG_DRIVER=pgx PG_DSN="$PG_DSN" \
  go test ./internal/store -run TestMigrationContract -count=1 -v
```

> 安装踩坑：本机 `/tmp` 属主为 uid 501 导致 `apt-key` 报 `Couldn't create temporary file`；
> 用 `TMPDIR=/var/tmp` 绕过（**不要** chmod/chown `/tmp`）。
