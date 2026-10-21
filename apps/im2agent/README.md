# im2agent · 极轻量验证原型（7 形态）

> 目的：把「人机混合 IM」的**治理闭环**压缩到一个能在 10 秒内跑起来的单进程里，用于验证产品直觉、
> 演示给评审、以及作为后续 Go/Flutter 生产线的语义参照。
>
> 本机只有 Node.js（无 Go / Flutter / Docker / Caddy），因此交付的是**语义等价的 Node 实现 + 可部署配置**；
> 未在本机验证的部分在下面「诚实清单」里逐条标注，不含糊。

## 一、7 个形态与落点

| # | 形态 | 本仓库落点 | 本机验证 | 等价物说明 |
|---|---|---|---|---|
| ① | Simple & Demo | `apps/im2agent/{server,domain,store,llm}.mjs` + `public/index.html` | ✅ 8 项测试（demo/审批/账本/审计/LLM/持久化） | `node apps/im2agent/server.mjs` 零依赖起闭环；**不是 Go 单二进制**（本机无 Go） |
| ② | 客户端 iOS/Android/Windows | `client/`（Flutter，三态导航：审批/账本/审计） | ⚠ 未编译（无 Flutter SDK） | 真连 `/api/admin/state` + `/api/approval/decide`；打包命令见 `client/README.md` |
| ③ | Linux 服务器 | `deploy/linux/{im2agent.service,install.sh}` | ⚠ 未在 Linux 执行 | systemd + 一键安装到 `/opt/im2agent`；**不产出 7.1M 静态 ELF**（Node 运行时，需 Node ≥22） |
| ④ | 网页运维界面 | `apps/im2agent/public/admin.html` | ✅ 鉴权/安全头测试 | 黑底白字：Agent 注册表 / 审批流 / 付费账本 / 审计流 / LLM 状态 |
| ⑤ | 局域网部署版 | `deploy/lan/{Dockerfile,docker-compose.yml}` | ⚠ 未构建（无 Docker） | 绑定 0.0.0.0 + 打印局域网地址；镜像基于 `node:24-alpine`（**不是 scratch <10MB**） |
| ⑥ | 大模型驱动版 | `apps/im2agent/llm.mjs` + `/api/llm/draft` | ✅ 三档降级有测试 | OpenAI 兼容（DeepSeek / 通义 / Ollama / vLLM）；template → fallback → real |
| ⑦ | 公网生产部署版 | `deploy/prod/{Caddyfile,docker-compose.yml,.env.example}` | ⚠ 未签发证书 | Caddy 自动 HTTPS + `ADMIN_TOKEN` 强制 + 安全头；PostgreSQL 为**可选适配点但未实现驱动** |

## 二、一条命令起闭环

```bash
node apps/im2agent/server.mjs
# 打开 http://127.0.0.1:8787/         状态页
# 打开 http://127.0.0.1:8787/admin/   运维界面（demo 形态免令牌）
```

三种形态同一份代码，只改环境变量：

```bash
# 局域网（打印本机所有 IPv4，手机/平板可直接连）
PROFILE=lan ADMIN_TOKEN=dev-token-123 STORE=jsonfile node apps/im2agent/server.mjs

# 生产（没有 ADMIN_TOKEN 直接拒绝启动；带安全头 + HSTS）
PROFILE=prod ADMIN_TOKEN='长随机串' STORE=jsonfile node apps/im2agent/server.mjs

# 大模型（填 key 即走真实 LLM；不填走确定性模板）
IM2AGENT_LLM_KEY=sk-xxx IM2AGENT_LLM_BASE_URL=https://api.deepseek.com/v1 node apps/im2agent/server.mjs
```

## 三、API

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/healthz` | — | **Go 规范**：纯文本 `OK`（带 `Accept: application/json` 时回 JSON） |
| GET | `/api/health` | — | 健康检查 + 待审批数 + 审计链状态 |
| GET | `/api/events` | — | **SSE 多端同步**：`hello` / `preview` / `decide` 事件（客户端亦可 1s 轮询兜底） |
| GET | `/api/state` | — | 对外状态（不含敏感细节） |
| GET | `/api/llm` | — | LLM 引擎状态（档位/模型/调用/失败/lastError） |
| GET | `/api/admin/state` | `x-admin-token` | 运维全量 + **Go 视图**（`ticketsView` snake_case / `audits` / `ledgers`） |
| POST | `/api/approval/preview` | `x-admin-token` | `{ticket_id}` 用 LLM 生成 `Ticket.Context`（离线走模板，不报错） |
| POST | `/api/approval/decide` | `x-admin-token` | 兼容两种写法：`{ticketId, action}` 与规范 `{ticket_id, action\|decision}` |
| GET | `/api/ledger` | `x-admin-token` | 账本条目 + 统计（幂等键可见） |
| GET | `/api/audit` | `x-admin-token` | 审计链 + 验链结果 |
| POST | `/api/llm/draft` | `x-admin-token` | `{text}` 试三档降级 |
| GET | `/api/config` | `x-admin-token` | 形态/存储/LLM/LAN 地址（token 打码） |

> 验收矩阵见 [QA.md](./QA.md)；Go 目录/结构体/函数映射见 [PORTING-TO-GO.md](./PORTING-TO-GO.md)；
> v0.6.1 之后的商业化缺口（登录实名 / WS 横向扩展 / 支付网关与防白嫖）见 [ROADMAP-v0.6.1+.md](./ROADMAP-v0.6.1+.md)。

## 四、治理语义（与主仓库契约对齐）

- **信任档**：`STRANGER(4) … FAMILY(1)`，付费且回复后 `陌生 → 已知`。
- **审批**：`PENDING → APPROVED → EXECUTING → COMPLETED`（驳回走 `REJECTED`）；终态不可二次决策（409）。
- **付费消息**：`AWAITING_PAYMENT → PAID → VIEWED → REPLIED → SETTLED`；**先预审后付费**。
- **账本**：`idempotencyKey = pay:<messageId>`，重复调用不二次扣费；`SETTLE` 不改变余额口径。
- **审计**：sha256 哈希链，只追加；`/api/audit` 返回链完整性。
- **LLM 铁律**：模型只**起草**回复；不调用 Skill、不扣费、不审批；失败降级要标记 `degraded`。

> 主仓库的 `services/` 是冻结契约的全量实现；本原型是"同一语义的极简版"，用于快速验证，不替代生产模块。

## 五、诚实清单（未验证 / 未实现）

1. **没有 Go 静态 ELF**：本机无 Go 工具链；③ 交付的是 Node 单进程 + systemd 配置，目标机需 Node ≥22。
2. **Flutter 未编译**：② 的 Dart 代码未跑 `flutter analyze`/真机；接口路径已按后端实现对齐。
3. **Docker/Caddy 未执行**：⑤⑦ 的镜像与反代配置未在本机构建；体积/证书/健康检查需目标机复核。
4. **PostgreSQL 未接驱动**：`STORE=postgres` 会**显式报 `STORE_NOT_AVAILABLE`**，不静默降级；生产先用 `jsonfile` 挂卷，或按主仓库 `docs/05` 接 PG。
5. **IM 底座**：本原型自带内存事件模型（不依赖任何 IM PaaS）；接入真实渠道请走主仓库 `§31 im-adapter`（统一 MessageEvent + 能力声明）。
6. **限流/多租户/审计签名**：本原型未做；主仓库有 `§12 网关限流`、`§16 签名审计`。
