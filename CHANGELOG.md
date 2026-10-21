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

## v0.0.7-client — 2026-10-21

**Web/PWA 客户端**：把"客户端只存在于未编译的 Flutter 代码里"变成**可打开、可验证**的交互。

- **§47 Web/PWA 客户端**（`apps/im2agent/public/app/`）：三态导航（审批 / 巡视 / 账本）、`logic.mjs` 纯函数层（可被 Node 直接测试）、manifest + service worker（离线只给壳，**API 数据一律不缓存**）、角色前端门控（viewer 不渲染写按钮，服务端仍二次校验）
- **静态服务**：`/app/*` 带 MIME 与缓存策略；**目录穿越防护**（`/app/../server.mjs` → 404，有测试）
- **验收**：`scripts/accept.mjs` 新增 `G-APP` Gate（壳 + manifest + SW 三者必须可达）；实测 `PASS 8 · FAIL 0 · SKIP 1`
- 测试：`tests/web-client.test.mjs` 3 项（纯函数 / 静态资源与 MIME / 穿越防护）
- 文档：`docs/27-v0.0.7-客户端交付说明.md`（能力矩阵、三态字段来源、角色与安全、PWA 离线、F1–F5、Flutter 差距）
- 契约：R18

**能力边界（不粉饰）**：Web/PWA 可用；**Flutter 三端仍是未编译等价物**——原生推送、手表、BLE、相机、离线写都不在 v0.0.7 内。

验证：全量 **695/695**；verify **101/101**；web-client 3；accept CLI 8 Gate PASS（含 G-APP）。

## v0.0.6-acceptance — 2026-10-21

**验收工具化**：把"本机全绿"变成"对真实目标可验收、可归档"。

- **§45 上线前预检**（`services/acceptance/src/preflight.mjs`，8 项测试）：目标可达、**鉴权三态真验**（无 token 401 / 错 token 401 / 对 token 200）、Node 版本、存储非 memory、外部依赖非 dev、时钟偏移、TLS/WARN；**只读**（测试断言前后目标状态条数不变）
- **§46 验收执行器**（`runner.mjs`，7 项测试）：8 个默认 Gate（HEALTH/AUTH/ROLE/AUDIT/STORE/METRICS/BACKUP/PROVIDERS）；单 Gate 异常/超时 → `FAIL` 且不中断；`SKIP/MANUAL` 不影响 ok；报告含 target/起止时间/每条耗时，可归档
- **CLI**：`node scripts/accept.mjs --target <url> --token <ADMIN_TOKEN> [--viewer-token] [--require-real-providers] [--only] [--report-dir]`，输出 JSON+Markdown 报告
- 文档：`docs/26-v0.0.6-真实环境验收Runbook.md`（33 条业务 Gate 的自动/人工账：**当前 2 自动 / 31 人工**；验收当日 T-1/T0/T+1 流程）
- 契约：R17

**实测**：对 `PROFILE=lan` 真实 HTTP 目标跑 CLI → 预检通过、`PASS 7 · FAIL 0 · SKIP 1`（G-PROVIDERS 因短信仍是 dev 被显式 SKIP），报告落盘。
**过程中修掉**：CLI 在 fetch 仍持有 socket 时 `process.exit()` 触发 Windows libuv 断言崩溃 → 改为设置 `process.exitCode` 让事件循环自然收敛。

验证：全量 **692/692**；verify **94/94**；preflight 8 · runner 7 · im2agent 16。

## v0.0.5-resilience — 2026-10-21

**内测保障**：不加业务功能，只保证"挂了能降级、坏了能恢复、出事有话说"。

- **§43 熔断与降级**（`services/resilience/src/provider-guard.mjs`，18 项测试）：`CLOSED/OPEN/HALF_OPEN` 三态；**OPEN 时不再调用主方**（快速失败）；半开只允许一次探测；降级必须标 `usedFallback + causeCode`；主备都失败 → `FALLBACK_FAILED`（不静默返回空值）
- **§44 备份与恢复**（`services/persistence/src/backup.mjs`，11 项测试）：备份带 `sha256`，`restore` 前必须验签（篡改 → `HASH_MISMATCH`）；**restore 只返回数据、不自动应用**；轮转不删最新；坏文件在 list 中显式标注 `skipped`
- **接线**：短信主通道故障（`IM2AGENT_SMS_FAULT=1`）→ 熔断 + 备用通道；机审故障（`IM2AGENT_MACHINE_FAULT=1`）→ 熔断 → `HUMAN_PENDING`（**fail-closed**，绝不放行）
- 产品端点：`/api/ops/resilience`、`/api/ops/backup`、`/api/ops/backup/restore`
- 文档：`docs/25-v0.0.5-内测保障.md`（降级矩阵、熔断值班、恢复 5 步、协议要点、E1–E6）
- 契约：R16

**过程中修掉的两个接线缺陷**：①重构短信守卫后残留 `smsProvider` 引用导致服务起不来（全量测试瞬间全红暴露）；②memory 存储下 `persist()` 跳过写入，备份拿到空快照（149 字节）——改为备份前显式写 store。

验证：全量 **677/677**；verify **89/89**；im2agent **16/16**；provider-guard 18 · backup 11。

## v0.0.4-observability — 2026-10-21

**可运营内测**：从"能跑的内测"变成"能看数、能告警、能举证的内测"。

- **§41 指标与告警**（`services/observability/src/metrics.mjs`，13 项测试）：付费漏斗六段（`PAID_SENT→审核→CHARGE→VIEWED→REPLIED→SETTLED`）+ 转化率（0 分母返回 0，不产生 NaN）；直方图 p50/p95；五条默认告警规则（机审失败率/人审积压/退款率/短信失败率/对账差异）为**纯函数**
- **§42 全链追溯**（`trace.mjs`，8 项测试）：按 `messageId` 合并审核/账本/争议/审计四源，按时间排序；**`complete=false` 时显式列出 `missing`**（如"有扣费无审核"），不假装完整；只读，不触发状态迁移
- 产品端点：`/api/ops/metrics`、`/api/ops/alerts`、`/api/ops/trace?messageId=`
- 文档：`docs/24-v0.0.4-可观测与运营.md`（漏斗解释、告警值班、D1–D6 Gate、复盘模板）
- 契约：R15

验证：全量 **647/647**；verify **84/84**；im2agent **15/15**；metrics 13 · trace 8。

## v0.0.3-review — 2026-10-21

**审核链前置**：付费消息从"能收钱"变成"敢收钱"。

- **§40 付费消息审核**（`services/moderation`，17 项测试）：机审 `PASS/FLAG/BLOCK` 三态分流；`FLAG` 转人审队列、`BLOCK` 自动拒但可申诉；**机审不可用/抛错一律 fail-closed → HUMAN_PENDING**；原审核人不得复审自己的案子（`SELF_REVIEW_FORBIDDEN`）；`canDeliver` 是投递与扣费的**唯一判定**
- **扣费顺序修正**：付费消息先审核后扣费——待审 202、被拒 403，**都不扣费、不产生消息**；申诉改判（`APPEAL_GRANTED`）后重试才放行
- 产品端点：`/api/moderation/{submit,review,appeal,appeal/resolve}`、`GET /api/moderation`
- 文档：`docs/23-v0.0.3-审核链.md`（C1–C6 Gate、人审手册、灰度策略、失败/误杀风险）
- 契约：R14

**边界纪律**：审核状态机（§40）与动作审批状态机（§4）、争议状态机（§39）三套分离；两个同名 `VERDICT`（§7 `PASS/REVIEW/BLOCK` vs §40 `PASS/FLAG/BLOCK`）不可跨模块传值。

验证：全量 **625/625**；verify **79/79**；im2agent **14/14**；moderation 17。

## v0.0.2-intake — 2026-10-21

**内测就绪**：把 v0.0.1 的付费闭环从"能跑"变成"能对内测用户开放"。

- **§38 短信通道与 OTP 投递**（`services/account`，23 项测试）：`dev/log/vendor` 三档 provider；验证码只经 `otp.deliver` 注入、**永不离开 AccountService/日志/错误/审计**；60s 冷却、日上限（按尝试计）、投递失败 `OTP_DELIVERY_FAILED` 不静默；vendor 超时/拒绝/失败分别映射
- **§39 退款争议与时间线**（`services/payment/src/dispute.mjs`，14 项测试）：已回复/已结算才可开争议（未回复 → `NOT_ELIGIBLE` 引导自助退）；人工 `APPROVE` 后**只能经账本 `refund-dispute:<id>` 幂等退款**（一条消息最多一笔 REFUND）；`timeline` 还原 `CHARGE→VIEWED→REPLIED→REFUND`
- 产品端点：`/api/sms`、`/api/dispute/{open,resolve,timeline}`；OTP 冷却 429、通道故障 502
- 文档：`docs/22-v0.0.2-内测就绪-短信与争议.md`（B1–B7 Gate、客服手册、黑产风险）
- 契约：R13

**过程中修掉一个真实缺陷**：OTP 投递失败时挑战未回滚，会留下"用户永远收不到的最新挑战"，导致用户拿上一条短信的码去注册反而 `OTP_INVALID`。已在 `requestOtp` 中回滚挑战并补测试（⑭）。

验证：全量 **607/607**；verify **72/72**；im2agent **13/13**；account 38 / payment 64 零回归。

## v0.0.1-product — 2026-10-21

**产品版第一个可内测闭环**（与 `v0.0.1-demo` 技术 tag 同名不同义：那个证明"能跑"，这个要求"能对真实的人收钱且能退"）。

- **§36 账户/实名/付费准入**（`services/account`，15 项测试）：手机 OTP 登录、实名核验只存 `sha256(salt+idNumber)` + 尾号掩码、未实名 `REALNAME_REQUIRED`、SUSPENDED 阻断写操作、会话过期不静默续期
- **§37 钱包**（`services/payment/src/wallet.mjs`，14 项测试）：充值/扣费/退款全幂等、**禁止负余额**、日发送上限、**退款仅限未回复态**（`REFUND_WINDOW_CLOSED`）、金额真相仍只在 §8 账本（新增 `DEPOSIT` 类型，纯加法）
- **产品端点**：`/api/account/{otp,register,verify}`、`/api/wallet{,/topup}`、`/api/paid/{send,reply,refund}`；未实名先拦、扣费失败不产生消息
- 文档：`docs/21-v0.0.1-产品版范围与验收.md`（In/Out 7/4、A1–A8 验收 Gate、合规口径、最可能翻车点）
- 契约：R12；`ENTRY_TYPE` 加法 DEPOSIT

验证：全量 **569/569**；verify **67/67**；e2e 12/12；im2agent **12/12**（含产品闭环）。

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
