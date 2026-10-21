# im2agent · QA 验收矩阵（对齐 v0.0.1 → v0.6.1 十个 tag）

> 口径：**✅ 本机实测通过**（命令可复现） · **⚠ 需目标机执行**（本机缺 Go/Flutter/Docker/Caddy） · **⛔ 未实现**

| # | QA 项 | 判定标准 | 本机结果 | 命令 / 说明 |
|---|---|---|---|---|
| 1 | Demo 连通性 | `GET /healthz` 返回 `OK`；`/api/health` 返回 `ok:true` | ✅ | `node apps/im2agent/server.mjs`；`curl -s localhost:8787/healthz` → `OK` |
| 2 | 审批闭环 | 通过陌生人来意 → `COMPLETED`，付费消息 `SETTLED`，账本恰好一条 CHARGE | ✅ | `tests/im2agent.test.mjs` ④ |
| 3 | 账本幂等 | 重复/重试不二次扣费（幂等键 `pay:<msgId>`） | ✅ | 同上，`charges.length === 1` |
| 4 | 终态保护 | 已完成审批再决策 → 409 `ILLEGAL_TRANSITION` | ✅ | 同上 |
| 5 | 运维鉴权 | 无 `x-admin-token` → 401；带令牌 → 200 | ✅ | 测试 ② |
| 6 | 安全响应头 | `X-Frame-Options: DENY`、CSP、`nosniff`；prod 额外 HSTS | ✅ | 测试 ②③ |
| 7 | 生产不裸奔 | `PROFILE=prod` 且无 `ADMIN_TOKEN` → **拒绝启动** | ✅ | 测试 ③ |
| 8 | LLM 降级 | 无 key → `template`；上游失败 → `fallback` 且 `degraded:true`；成功 → `real` | ✅ | 测试 ⑤（注入 fetch，不依赖真网） |
| 9 | LLM 生成审批理由 | `POST /api/approval/preview` 返回非空 `context` 与档位 | ✅ | 测试 ⑨ |
| 10 | 多端同步 | SSE `/api/events`：一端决策，另一端收到 `decide` 事件 | ✅ | 测试 ⑩ |
| 11 | 持久化恢复 | jsonfile 重启后审批状态与审计链延续 | ✅ | 测试 ⑦ |
| 12 | 存储拒绝静默降级 | `STORE=postgres` → `STORE_NOT_AVAILABLE` | ✅ | 测试 ⑪ |
| 13 | **客户端三端同步** | Windows/Android/iOS 同时点同一审批单，状态一致 | ⚠ | 需 Flutter SDK + 三端设备；`client/README.md` |
| 14 | **docker 镜像体积** | `docker images` < 15MB | ⚠ 预计不达标 | 本机无 Docker；Node 基镜像约 130MB。**要 <15MB 必须 Go 静态编译**，见 `PORTING-TO-GO.md` |
| 15 | **端口暴露面** | 局域网 Nmap 仅 80/443（或 8080） | ⚠ | 需目标机；当前应用只监听单端口，反代后应只暴露 80/443 |
| 16 | **HTTPS 自动签发** | 测试域名解析到机器，Caddy 自动申请 Let's Encrypt | ⚠ | 需公网域名 + 80/443；`deploy/prod/` |
| 17 | 纯净 Ubuntu 免 Go 运行 | 无 Go 环境可直接跑 | ✅（Node 形态） | `deploy/linux/install.sh` 只需 Node ≥22 |
| 18 | PostgreSQL 读写 | migration + CRUD | ⛔ | 未实现驱动；`STORE=postgres` 显式报错 |
| 19 | **两级权限** | viewer 只读（看板/账本/审计），operator 可写（审批/起草）；两者配同一凭证拒绝启动 | ✅ | 测试 ⑫：viewer 写 → 403 `FORBIDDEN_ROLE`；operator 写 → 200 |
| 20 | **应用层限流** | 超窗 → 429 + `Retry-After`；健康检查豁免；反代后需 `TRUST_PROXY=1` | ✅ | 测试 ⑫：3 次放行、第 4 次 429，`/api/health` 不受影响 |
| 21 | **部署产物静态检查** | install.sh/systemd/Dockerfile/compose/Caddyfile 必备项齐全、无危险缺失 | ✅ | `node scripts/deploy-lint.mjs` → 14/14 |
| 22 | **Linux 目标机实测** | 纯净 Ubuntu 上 `install.sh` 装完 → `systemctl status` active → `/healthz` OK | ⚠ | 本机沙箱禁止命名管道，Git Bash 起不来；按 `deploy/linux/README.md` 在目标机执行 |
| 23 | **LLM 降级压测** | 200 次、30% 上游失败：无异常穿透、无空回复、失败一律 `degraded` | ✅ | `node scripts/llm-stress.mjs --calls 200 --fail-rate 0.3` → fallback 62 / real 138 / thrown 0 |
| 24 | **未实名拦截** | 未实名账号 `POST /api/paid/send` → 403 `REALNAME_REQUIRED` | ✅ | 测试 ⑬；契约 §36 |
| 25 | **身份证明文不落库** | 只存 `sha256(salt+idNumber)` + `****1234`；接口不回传明文 | ✅ | 测试 ⑬ + account 15 项 |
| 26 | **扣费幂等与负余额** | 同 messageId 只扣一次；余额不足 402 且不产生消息 | ✅ | 测试 ⑬ + wallet 14 项 |
| 27 | **退款仅未回复** | `PAID/VIEWED` 可退并回补；`REPLIED/SETTLED` → 409 `REFUND_WINDOW_CLOSED` | ✅ | 测试 ⑬（含已回复夹具反向验证） |
| 28 | **日发送上限** | 同日累计超限 → `DAILY_CAP_EXCEEDED`，次日恢复；退款不回滚额度 | ✅ | wallet 14 项 |
| 29 | **OTP 冷却** | 同号 60s 内重发 → 429 `OTP_COOLDOWN`，provider 不重发 | ✅ | 测试 ⑭ |
| 30 | **投递失败不静默** | vendor 失败/超时 → `OTP_DELIVERY_FAILED`/`SMS_*`，502；且挑战回滚（防"收不到码却报 OTP_INVALID"） | ✅ | otp-delivery 13 项 + 测试 ⑭ |
| 31 | **验证码不落日志** | dev/log/vendor 的日志、错误、审计均无 code 明文 | ✅ | sms-provider 10 项 |
| 32 | **已回复争议只能人工** | 未回复 → `NOT_ELIGIBLE`；已回复 APPROVE → 账本恰一条 `refund-dispute:<id>`；重放 409 不增加退款 | ✅ | 测试 ⑭ + dispute 14 项 |
| 33 | **机审通过才放行** | `PASS` → `APPROVED` 且记录 rule/model 版本 | ✅ | moderation 17 项 |
| 34 | **待审/被拒不扣费** | `FLAG` → 202 `HUMAN_PENDING`、`BLOCK` → 403 `REJECTED`，余额不变、无消息 | ✅ | 测试 ⑮ |
| 35 | **审核是唯一前置** | 所有付费入口先过 `canDeliver`；申诉改判后才可投递并扣费 | ✅ | 测试 ⑮ + docs/23 警告（老路径先扣费则整链失效） |
| 36 | **机审异常 fail-closed** | machine 抛错/null/非法 verdict → `HUMAN_PENDING`，绝不自动放行 | ✅ | moderation 17 项 |
| 37 | **不能自审** | 原审核人复核自己的案子 → `SELF_REVIEW_FORBIDDEN` | ✅ | moderation 17 项 |
| 38 | **漏斗转化率正确** | 六段计数与转化率；分母为 0 返回 0（不得 NaN） | ✅ | metrics 13 项 |
| 39 | **告警越线触发** | 五条默认规则：恰好阈值不告警、越线告警；同输入同输出 | ✅ | metrics 13 项 + 测试 ⑯（21 条积压触发） |
| 40 | **全链追溯可举证** | 一条消息串起审核/扣费/回复/退款；顺序正确、`complete=true` | ✅ | trace 8 项 + 测试 ⑯ |
| 41 | **缺环显式** | 有扣费无审核 → `complete=false` 且 `missing` 明确列出 | ✅ | trace 8 项 |
| 42 | **熔断快速失败** | 连续失败 → `OPEN`；此后**不再调用主方**，走备用通道并标 `usedFallback` | ✅ | provider-guard 18 项 + 测试 ⑰ |
| 43 | **半开只探测一次** | 冷却到期 → `HALF_OPEN`；成功→`CLOSED`，失败→重新 `OPEN` 并重置计时 | ✅ | provider-guard 18 项 |
| 44 | **机审故障 fail-closed** | 机审熔断 → `HUMAN_PENDING` 且 `machineUnavailable=true`，绝不自动放行 | ✅ | 测试 ⑰ |
| 45 | **备份可验签** | 备份带 sha256；篡改任一字节 → `HASH_MISMATCH`；缺失 → `NOT_FOUND` | ✅ | backup 11 项 |
| 46 | **恢复不自动应用** | `restore` 只返回数据，运行时状态不变（`applied:false`） | ✅ | 测试 ⑰ + backup 11 项 |
| 47 | **预检鉴权三态** | 无 token→401、错 token→401、对 token→200（任一不符 `AUTH_FAILED`） | ✅ | preflight 8 项 |
| 48 | **预检只读** | 预检前后目标 audit/ledger/tickets 条数不变 | ✅ | preflight 8 项 |
| 49 | **验收可执行** | `scripts/accept.mjs` 对真实 HTTP 目标跑 8 Gate 并输出可归档报告 | ✅ | 本机 lan 实例：PASS 7 / SKIP 1 |
| 50 | **不可自动化显式 MANUAL** | 33 条业务 Gate 中仅 2 条自动（D1/E4），其余标人工签，不口头通过 | ⚠ 需真实环境 | docs/26 §3 |
| 51 | **客户端可打开** | `/app/` 返回三态壳；manifest/SW 可访问（MIME 正确） | ✅ | web-client 3 项 + G-APP |
| 52 | **客户端逻辑可 Node 测试** | `logic.mjs` 纯函数（金额/归纳/角色/队列/漏斗），脏数据不抛 | ✅ | web-client 3 项 |
| 53 | **前端门控 + 服务端双保险** | viewer 不渲染写按钮；直调写接口仍 403 | ✅ | 测试 ⑫ + web-client |
| 54 | **离线不缓存敏感数据** | SW 只缓存壳，`/api/*` 不缓存；断网显示壳而非白屏 | ⚠ 需浏览器走查 | docs/27 §5 |

## 本地一键回归

```powershell
node tests/im2agent.test.mjs            # 11 项（含权限/限流/SSE/持久化）
node scripts/llm-stress.mjs --calls 200 --fail-rate 0.3   # 降级压测（无需网络）
node scripts/deploy-lint.mjs            # 部署产物静态检查 14 项
node scripts/test.mjs                   # 全量 500+ 项
node scripts/verify.mjs                 # 文档/契约/代码一致性 55 项
node scripts/quickstart.mjs --check     # 主仓库 13 项自检
```

## QA 里最容易"看起来通过"的三处

1. **LLM 降级**：把 `IM2AGENT_LLM_KEY` 填错后，页面必须显示"已降级"、`stats.failures` 递增；若仍显示正常输出，说明把降级当成了成功。
2. **多端同步**：SSE 连上不代表同步——要**两个客户端同时打开**，在 A 端审批，B 端在 1s 内刷新。只测单端等于没测。
3. **生产形态**：`PROFILE=prod` 起得来不等于安全——必须验证"无 token 的 `/api/admin/state` 返回 401"和"HSTS 头存在"。
