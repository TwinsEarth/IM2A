# CHANGELOG · IM2A

> 版本策略：`v0.x` 为验证原型形态（可演示、未商用）；`v1.0.0-baseline` 为工程基线（四条主张可复现）；
> `v2.x` 起才进入产品化（登录/实名/支付/合规）。

## v1.0.0-baseline — 2026-10-21

工程基线建立：把"四条不可妥协主张"与代码证据钉在一起（见 `docs/18-IM2A-工程基线.md`）。

- 契约：`contracts/INTERFACES.md` v1.0(R10)，§1–§34；变更记录 R1–R10 可追溯
- 实现：零依赖 Node ESM，`services/` 19 个模块；**500 passed / 0 failed**
- 一致性验收：`scripts/verify.mjs` **55/55**
- 可运行：`apps/im-live`（浏览器 IM + `/v2` 八类机制演示）、`apps/demo-server`、`apps/im2agent`（7 形态）
- 文档：`docs/00–18`
- 明确不含：生产 Go/PG/Flutter 实现、硬件、真实支付与实名、跨组织 A2A

## v1.2.0-context — 2026-10-21

把「Context 主权」落成代码（§33–§35，契约 R11）：

- **§33 Person Runtime**：PERSONAL/TASK/WORKING 三层状态；`snapshot` **只含 PERSONAL+TASK**（WORKING 永不迁移）、带 hash、`restore` 校验 version+hash；`secret:*` 写入 → `SECRET_IN_LAYER`；任务检查点与等待条件换实例可续跑
- **§34 确认锁定 Context**：Agent 只能 `propose`；只有属主能 `confirm`；LOCKED 后改口 → `CONFLICT` 且原值不变；`unlock` 必须 reason 并留审计
- **§35 scoped 授权 + 签名回执**：scope 白名单、每 scope 单次消耗、过期/撤销、回执 hash 绑定 `messageId+scope+action+at`；**priceCents 不参与授权判定（付费不扩权）**
- 文档：`docs/20-Context飞轮与Personal-Runtime.md`（四层技术栈取/不取、三个结构性差异、两个风险处置、⛔ 路线图）
- 演示：`/v2` 页「Context 主权」卡片 + `GET /api/v2/context-sovereignty`

测试：person-runtime 12 · context-lock 14 · scoped-grant 11 → 全量 **539/539**；verify **62/62**；quickstart 13/13；e2e 12/12。

## 验证原型线（im2agent，7 形态）

| Tag | 内容 | 验证 |
|---|---|---|
| `v0.0.1-demo` | 单进程、内存/文件双存储、内嵌网页 | ✅ 测试 |
| `v0.2.0-linux` | systemd unit + `install.sh`（目标机需 Node ≥22） | ⚠ 未在 Linux 执行 |
| `v0.2.1-admin` | 网页运维界面（注册表/审批/账本/审计/LLM） | ✅ 鉴权与安全头 |
| `v0.3.0-client` | Flutter 三端、三态导航 | ⚠ 未编译 |
| `v0.4.0-lan` | docker compose + HEALTHCHECK | ⚠ 无 Docker |
| `v0.5.0-llm` | OpenAI 兼容三档降级（template→fallback→real） | ✅ 注入 fetch 验证 |
| `v0.6.1-prod` | Caddy 自动 HTTPS + ADMIN_TOKEN + 安全头 | ⚠ 未签发证书 |
| `v0.7.0-contract` | 对齐 Go 规范：`/healthz`、snake_case、Go 视图、SSE、`Ticket.Context` | ✅ 10 项测试 |
| `v0.8.0-ops` | ③ Linux 交付硬化（dry-run/双凭证/Runbook）· ④ 两级权限（viewer/operator）· ⑦ 应用层限流 · ⑥ LLM 压测工具 | ✅ 11 项测试 + deploy-lint 14/14 + 压测 |

> 过程记录（不隐藏）：`v0.8.0-ops` 首次提交时带了一个红测试（跨用例共用 `.data` 状态导致假失败）。
> 已修：测试统一强制 `STORE=memory`，并清理被污染的状态文件；该 tag 已重新指向修复后的提交。

## 主仓库演进（契约版本）

| 版本 | 主题 | 关键增补 |
|---|---|---|
| v1 | 治理层 | 信任矩阵 / 权限门控 / 审批 / 协商 / 付费消息 / 账本 / 审计 |
| v2 | Agent 一等公民 | 身份 / 网关 / 可见域 / 频道 / 会话 / 签名审计 |
| v3 | 人格与协作 | 人格记忆生命周期 / 多 Agent 审计讨论 / 细粒度工具审批 |
| v4 | 宿主能力 | 可信上下文 / 旁观沉默 / 敏感模型路由 / 轮次内投递 |
| v5 | 可信传达 | 签名信封 + 重放保护 / 第三人称转达 / 确认层 |
| v6 | 持久与委托 | 持久邮箱 / 带衰减委托 |
| v7 | 可替换底座 | IM 适配层（PaaS⇄自托管）/ 陌生来信审批挂起 |

## 已知欠账

1. v3–v7 共 12 个模块无 `.proto`；v2 的 4 个 proto 未编译（本机无 protoc）。
2. Flutter 客户端未编译；Docker/Caddy 配置未在目标机执行。
3. `STORE=postgres` 未实现驱动，显式报 `STORE_NOT_AVAILABLE`。
4. 登录/实名/支付不在基线内（见 `apps/im2agent/ROADMAP-v0.6.1+.md`）。
