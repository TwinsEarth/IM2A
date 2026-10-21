# AgentIM · AI 人机混合熟人社交 IM 平台

> 让每个人都像大老板一样：有自己的总管 Agent、领域子 Agent 团队和 24 小时在线的随身硬件入口，
> 把日常杂碎交给 Agent 团队，人只处理情感层与最终决策。
>
> 本仓库 = **技术文档（架构 / 框架 / SDK / 协议 / 开发计划 / 产品 / UI / 运营）** + **可运行的参考实现**。

> ## 🆕 v2：面向 Agent 的 IM（参考 9 个开源项目重新校准）
>
> v1 把 Agent 当成"服务端里的函数调用"，v2 把 **Agent 变成消息内核的一等公民**：
>
> | 变更 | 借鉴 | 修正了 v1 的什么 |
> |---|---|---|
> | V2-1 Agent 作为一等客户端（独立连接/在线状态/订阅，可第三方托管） | MagicChat、WKongAI、Buzz | Agent 无法独立部署、无法复用在线状态、第三方接不进来 |
> | V2-2 消息内核自托管 + 频道类型化（DIRECT/GROUP/TASK/AGENT_NEGOTIATION/STRANGER_QUEUE） | WuKongIM、WKongAI | 依赖 IM PaaS 使"三流分离"名义化，厂商链路还占 P99 预算 |
> | V2-3 Ed25519 自持身份 + 自签 AgentCard | Sigil、AICQ | Agent 只是个内部字符串 id，"它代表谁"无法向第三方证明 |
> | V2-4 读侧可见域（按信任档裁剪字段，MEMORY 只回摘要） | MagicChat | 门控只管"能不能做"，不管"能看见什么" |
> | V2-5 Agent Gateway 单一数据平面（认证→限流→门控→护栏→签名审计） | Agentgateway | 门控散落在编排层内部，第三方 Agent 无统一入口 |
> | V2-6 签名审计（哈希链 + Ed25519） | Buzz | 只有哈希链：有权改库者可整链重算，无法证明"是谁写的" |
>
> 详见 [docs/11-AI原生IM架构-v2.md](docs/11-AI原生IM架构-v2.md)、[docs/12-开源IM项目对标与取舍.md](docs/12-开源IM项目对标与取舍.md)。
> 契约扩到 §11–§16（`contracts/INTERFACES.md` R5），新增 `services/{identity,gateway}` 与 `services/messaging/{channel,session}.mjs`、`services/audit/signed-audit.mjs`。
> 演示页：`node apps/im-live/server.mjs` → **http://127.0.0.1:8797/v2**（Agent 网络 / 未读隔离 / 可见域矩阵 / 网关事件 / 审计篡改演示）。
>
> ### v3：按能力分层再补三块（人格记忆 / 讨论审计 / 细粒度审批）
>
> | 能力层 | 首选参考 | 我们补了什么 | 落点 |
> |---|---|---|---|
> | 人格与长期记忆生命周期 | nekoclaw | SOUL 与会话记忆分离；WORKING→EPISODIC→SEMANTIC 只按「时间+访问次数」晋升；遗忘为软删；跨平台可携带包（签名） | `services/orchestrator/src/persona-memory.mjs`（§17） |
> | 陌生人协商的模式化审计 | imcodes | `DISCUSS / AUDIT / REVIEW` 多 Agent 结构化讨论；**异议原样保留**；纪要只把 hash 与证据面写入签名链 | `services/orchestrator/src/discussion-engine.mjs`（§18） |
> | 细粒度工具审批 | MindRoom | Agent×Skill 策略：金额超限是**可批**（APPROVE），信任不足才是**不可批**（DENY） | `services/orchestrator/src/tool-policy.mjs`（§19） |
> | 快速产品验证 | Xiajiao | 一条命令 `node scripts/quickstart.mjs` → 起服务 + 9 项自检 + 打印入口 | `scripts/quickstart.mjs` |
>
> 分层选型与逐项裁决（✅3 / ◐3 / 子项否决）见 [docs/13-分层选型与落地映射.md](docs/13-分层选型与落地映射.md)。
>
> ### v4：宿主型 IM（Agent 入驻 IM，而不是在 IM 上加机器人）
>
> 判据：Agent 有独立身份、与人类共享消息会话模型、有独立授权、是频道成员、被服务端注入可信上下文、离线/轮次内仍被驱动。按此补了四块宿主能力：
>
> | 机制 | 借鉴 | 没有它会怎样 | 落点 |
> |---|---|---|---|
> | **服务端可信上下文** | MagicChat | 每个 Agent 各自查关系链 → 权限口径不一、越权查询无法集中审计 | `services/gateway/src/trusted-context.mjs`（§22，签名注入，Agent 只读不可改） |
> | **旁观与主动沉默** | nekoclaw | 家庭群里 Agent 过度活跃，熟人社交最需要的"不打扰"被破坏 | `services/orchestrator/src/presence-policy.mjs`（§23，OBSERVE=记录但不说；1:1 除外） |
> | **敏感场景模型路由** | MindRoom | 家庭/法律对话被送到云端大模型，隐私承诺失效 | `services/orchestrator/src/model-router.mjs`（§24，敏感域与家庭档钉本地） |
> | **轮次内投递** | Flopwire | 协商提案要等下一次用户提示才可见，实时协作退化成批处理 | `services/messaging/src/turn-inbox.mjs`（§25，MID_TURN / QUEUED，未消费不静默丢） |
>
> 判据论证与逐项目重排见 [docs/14-宿主型IM判据与机制落点.md](docs/14-宿主型IM判据与机制落点.md)。
>
> ### v5：可信传达（开源解决连接，我们解决信任）
>
> | 机制 | 借鉴 | 没有它会怎样 | 落点 |
> |---|---|---|---|
> | **签名信封 + 重放保护 + TOFU** | Beacon | 陌生人 Agent 可伪造/重放来意，付费门控建在沙上 | `services/identity/src/signed-envelope.mjs`（§26）；**验签通过 ≠ 有权发**，授权仍在网关与付费状态机 |
> | **第三人称转达** | Pisces/Convia | Agent 只能伪装成人说话，或干脆进不了人类会话 | `services/orchestrator/src/relay-summary.mjs`（§27）；`notFromHuman` 恒 true，冒充人类 → `IMPERSONATION_BLOCKED` |
> | **确认层不可预批准** | ZLAgent | 规划器可以"预批准"扣费，审批形同虚设 | `services/orchestrator/src/confirmation-tier.mjs`（§28）；未登记技能 fail-closed，`PLANNER_PREAPPROVAL_REJECTED` 进审计 |
>
> 四层技术栈映射与"必须自研"的边界见 [docs/15-四层技术栈映射与自研边界.md](docs/15-四层技术栈映射与自研边界.md)。

---

## 1. 三十秒理解这套架构

一句话：**把「Agent 能做什么」和「系统允许它做什么」分成两层——模型只负责提出意图，权限门控与状态机负责执行边界。**

三条不可妥协的设计：

| # | 设计 | 为什么 |
|---|---|---|
| 1 | **三条流量分离**：人类 IM 消息走 IM PaaS 长连接；Agent↔Agent 协商走独立 gRPC 通道；审批与审计事件走内部消息队列 | 协商是高频多轮结构化的，塞进 IM 通道会污染未读/推送计数，且无法做细粒度审计；协商中间态不该出现在用户会话列表 |
| 2 | **权限门控是代码，不是提示词** | 五档信任矩阵（本人/家庭/信任/已知/陌生）在每次工具调用前由 `CapabilityGate` 强制检查；模型"自觉遵守"等于没有边界 |
| 3 | **先 Agent 预审，后付费** | 付费买的是"已被对方 Agent 预审过的有效触点"，绝不是"绕过过滤的通路"。付费发生在预审之后，不可颠倒 |

产品叙事（三个角色在一屏之内完成）：

```
陌生人周先生 ──► 周的总管Agent ══协商 4 轮══► 林总的总管Agent
                                                │
                                     陌生人过滤子Agent 预审（规则：接受工作/融资类询盘）
                                                │
                                   ┌────────────▼────────────┐
                                   │  审批卡片（人类 IM 通道） │  ← 人只在这里出现
                                   │  来意 + 协商摘要 + ¥50   │
                                   └────────────┬────────────┘
                                     通过 ──────┴────── 驳回
                                       │                 └─► 对方**不被扣费**，礼貌拒绝
                            扣费→查看→回复→结算
                            关系档位 陌生 → 已知（信任是挣来的，不是买来的）
```

---

## 2. 快速开始

本机只依赖 Node.js ≥ 22（**零第三方依赖**，不需要 `npm install`）。

```powershell
cd E:\GPT\agent-im

# 一条命令：起服务 → 跑 11 项自检 → 打印入口 → 保持运行
node scripts/quickstart.mjs
# 或只自检（CI 用，exit 0/1）
node scripts/quickstart.mjs --check

# 跑全量测试（单进程，不 spawn 子进程）
node scripts/test.mjs

# 交付物一致性验收（文档/契约/代码三方不漂移）
node scripts/verify.mjs

# 只跑某一层
node scripts/test.mjs --filter=orchestrator
node scripts/test.mjs --filter=payment
node scripts/test.mjs --filter=e2e

# 启动演示服务 + 审批态/巡视态控制台
node apps/demo-server/server.mjs
# 浏览器打开 http://127.0.0.1:8787

# 启动「人机混合 IM」实时 Demo（浏览器 IM + 审批卡 + 真实 fleet 面板）
node apps/im-live/server.mjs
# 浏览器打开 http://127.0.0.1:8797（顶部切换身份：林总 / 周先生 / 林母）
# 生产模式：启用登录鉴权 + SQLite + TLS（证书就绪后）
$env:IM_AUTH = '1'; $env:IM_SESSION_SECRET = '<长随机串>'
$env:IM_TLS_CERT = 'C:\certs\fullchain.pem'; $env:IM_TLS_KEY = 'C:\certs\privkey.pem'

# 接真实大模型（可选；缺省为离线模板）
$env:DEEPSEEK_API_KEY = 'sk-...'; $env:DEEPSEEK_MODEL = 'deepseek-v4.1-flash'
node apps/im-live/server.mjs
```

控制台两个态：

- **审批态**（默认）：Agent 呈报的待决策事项，含来意、协商摘要、预审结论、计费模式、风险等级、建议动作，一键通过/驳回。目标：30 秒完成一次决策。
- **巡视态**：Agent 团队在干什么——协商轮次与时间线、付费消息状态流水线、不可变账本、出口拦截记录，以及 **12 项治理有效性自检**（可执行证据，不是 PPT）。

> ⚠ `node --test` 在本机 Windows 沙箱下会因禁止子进程管道而 `EPERM` 失败，故使用 `scripts/test.mjs`（单进程 import，`node:test` 在进程退出时汇总）。

---

## 3. 目录结构

```
agent-im/
├─ docs/                       技术文档集（先读 00-技术文档-总览.md）
├─ contracts/
│  ├─ INTERFACES.md            ★ 冻结模块契约 v1.0（签名/错误码/状态机，唯一权威）
│  ├─ proto/                   内部协商 / 审批 / 付费消息 的 Protobuf 定义
│  ├─ openapi/                 编排层 REST 面（与 demo-server 路由一致）
│  └─ README.md                契约索引与变更流程
├─ services/
│  ├─ orchestrator/            ★ Agent 编排层（自研核心，7 个治理模块）
│  │  ├─ src/trust-zone.mjs          五档信任矩阵
│  │  ├─ src/agent-registry.mjs      Agent/Skill 能力声明
│  │  ├─ src/capability-gate.mjs     代码层权限门控 + 日预算
│  │  ├─ src/approval-engine.mjs     审批 9 态状态机 + 事件溯源 + 超时升级
│  │  ├─ src/negotiation-engine.mjs  Agent 协商 8 态状态机 + 轮次/Token 硬上限
│  │  ├─ src/memory-store.mjs        三层记忆 + 按 Agent 隔离
│  │  └─ src/egress-guard.mjs        出口合规护栏
│  ├─ payment/                 付费消息 9 态状态机 + 不可变账本（exactly-once）
│  └─ messaging/               人类 IM 通道：业务封装 / 64 分片保序序号 / 去重
├─ apps/
│  ├─ demo-server/             演示服务（HTTP + 静态控制台）+ 业务主线 flow.mjs
│  ├─ im-live/                 ★ 人机混合 IM 实时 Demo（浏览器聊天 + 审批卡 + fleet 面板）
│  └─ clients-flutter/         Flutter 客户端骨架（本机无 Flutter SDK，未编译验证）
├─ tests/e2e.test.mjs          端到端集成测试（装配后的业务命题断言）
├─ scripts/test.mjs            单进程测试运行器
└─ package.json
```

---

## 4. 文档导航

| 文档 | 内容 | 读者 |
|---|---|---|
| [00-技术文档-总览](docs/00-技术文档-总览.md) | 全套文档入口、系统全景、五条核心决策、契约与代码映射 | 所有人（先读这个） |
| [01-总体架构](docs/01-总体架构.md) | 六层架构、三流量分离、关键时序、容量与延迟预算、失败模式 | 架构师 / 后端 |
| [02-技术选型与框架](docs/02-技术选型与框架.md) | 每个选型的候选/理由/否决理由/切换触发条件；参考实现与生产架构映射 | 技术负责人 |
| [03-协议设计](docs/03-协议设计.md) | 内部协商 / 审批 / MCP / A2A 边界 / 人类消息封装；错误码与兼容规则 | 后端 / 协议 |
| [04-SDK与端侧设计](docs/04-SDK与端侧设计.md) | 客户端 SDK 分层、BLE 桥接协议、隐私时序、端云分流、Skill SDK | 客户端 / 端侧 |
| [05-数据模型与存储](docs/05-数据模型与存储.md) | 完整 DDL、索引与分区、账本不可变、记忆隔离、留存周期 | 后端 / DBA |
| [06-产品形态与硬件](docs/06-产品形态与硬件.md) | 耳夹耳机首发 / 眼镜第二形态、续航工程定义、隐私硬件规格、BOM 与定价 | 产品 / 硬件 |
| [07-客户端与UI交互](docs/07-客户端与UI交互.md) | 审批态/巡视态、来意卡片、五档权限 UI、手表三屏、30 秒决策设计 | 设计 / 客户端 |
| [08-运营客服运维与合规](docs/08-运营客服运维与合规.md) | 资质清单与时序、审核闭环、Agent 行为审计、分层客服 SLA、SLO | 运营 / 法务 / SRE |
| [09-开发计划与里程碑](docs/09-开发计划与里程碑.md) | Phase0–3 按周 WBS、人力、依赖与关键路径、Go/No-Go、灰度 | 项目管理 |
| [10-风险与验收登记册](docs/10-风险与验收登记册.md) | 结构化风险表 + 四组验收矩阵 + Go-Live 清单 | 全员 |
| [交付物清单-统一 v1.0](docs/交付物清单-统一-v1.0.md) | 两条并行交付线（本仓库 Node/DSH+W2M · Mac mini Go/Flutter）的统一清单、对照矩阵、合并计划与验证状态 | 全员 / 验收 |
| [架构总览-图与实现对照 v0.1](docs/架构总览-图与实现对照-v0.1.md) | 总体架构图逐层对照：L1-L6 各层到底有没有代码、有没有验证（不拿文档冒充实装） | 全员 / 验收 |

---

## 5. 参考实现的边界（重要）

**这不是可上线的生产系统**，而是「治理逻辑的可执行规格」：

| 本仓库 | 生产架构 |
|---|---|
| 零依赖 Node ESM，单进程内存态 | Go + Go-Zero 微服务，gRPC/Protobuf，Kafka + PostgreSQL + Redis + pgvector |
| 内存信任矩阵 / 账本 | PostgreSQL 强一致事务，账本 `idempotency_key` 唯一索引 |
| 关键词式出口护栏 | 机审 API + 人工复核队列（规则与模型双层） |
| 演示 HTTP 服务 | API 网关 + 腾讯云 IM PaaS（消息投递）+ 自研 Agent 协商 gRPC 通道 |
| Flutter 骨架（未编译） | Flutter 手机/PC/手表三端 + BLE 硬件桥接 |

映射关系见 [02-技术选型与框架](docs/02-技术选型与框架.md) 的「参考实现与生产架构映射表」。这样做的好处是：**治理语义（门控顺序、状态机白名单、幂等键）可以在这里被测试钉死，移植到 Go 时不会走样。**

---

## 6. 治理有效性自检（控制台「巡视态」实时展示）

| # | 命题 | 期望 |
|---|---|---|
| 1 | 总管调用未授权 Skill | `DENY / SKILL_NOT_ALLOWED` |
| 2 | 采购子 Agent 对陌生人行事 | `DENY / TRUST_TOO_LOW` |
| 3 | 采购金额超过自动阈值 | `REQUIRE_APPROVAL` |
| 4 | 子 Agent 当日额度耗尽 | `DENY / BUDGET_EXCEEDED` |
| 5 | 未预审直接付费 | `NEEDS_SCREENING` |
| 6 | 连点 5 次支付 | 账本仅 1 条扣费 |
| 7 | 预审 BLOCK 后付费 | `NOT_PAYABLE` |
| 8 | 协商超过 5 轮反提案 | `INPUT_REQUIRED / MAX_ROUNDS` |
| 9 | 外发内容含身份证号 | `BLOCK` |
| 10 | 总管读子 Agent 原始记忆 | `MEMORY_ISOLATED` |
| 11 | 审批单已通过后再次通过 | `ILLEGAL_TRANSITION` |
| 12 | 信任授权过期 | 自动回落 `STRANGER` |

---

## 7. 术语

| 术语 | 含义 |
|---|---|
| 总管 Agent（butler） | L0，任务路由、优先级仲裁、跨子 Agent 协调、向用户呈报；用户的第一界面 |
| 子 Agent（subagent） | L1，单一领域闭环（工作邮件/采购/物业/日程/法律/教育/陌生人过滤） |
| Skill | L2，原子能力（MCP Server 形式注册），声明所需权限与财务影响 |
| TrustZone | 五档信任（SELF/FAMILY/TRUSTED/KNOWN/STRANGER），门控的判定依据 |
| 审批卡片 | 需要人决策的事项；Agent 只呈报与建议，钱/承诺/对外动作由人批准 |
| 来意卡片 | 陌生人消息的入口形态：结构化来意 + 协商摘要 + 计费模式，人到这一步才出现 |
| 蒸馏 | 用户的审批/驳回/修改作为反馈，收紧或放宽子 Agent 自动决策阈值（不改模型权重） |

---

## 8. 状态

| 项 | 状态 |
|---|---|
| 冻结契约（`contracts/INTERFACES.md` v1.0 · 修订 R3） | ✅ 含变更记录；语义只加不改 |
| 编排层 7 模块（信任/注册表/门控/审批/协商/记忆/护栏） | ✅ 107 项单测 |
| 资金与消息通道（账本/付费消息/封装/序号/去重） | ✅ 58 项单测 |
| 端到端集成（业务主线 + HTTP 协议面 + MODIFY patch） | ✅ 8 项 |
| 全量测试 | ✅ **180 passed / 0 failed**，exit 0 |
| 交付物一致性验收 | ✅ **11/11**，exit 0 |
| 演示服务 + 审批态/巡视态控制台 | ✅ 本地可跑 |
| 人机混合 IM 实时 Demo（`apps/im-live`） | ✅ 浏览器可用；8 项端到端测试；fleet 面板 simulated/live 双态 |
| Agent 大模型燃料（DeepSeek-V4.1-Flash） | ✅ OpenAI 兼容接入 + 离线降级 + 出口护栏前置；7 项单测（注入 fetch，不依赖真网） |
| Agent Runtime + W2M Skill 执行层 | ✅ DSH 风格 规划→权限→Skill→起草→护栏 闭环；10 项单测；fleet.devices/fleet.run 走真实 relay |
| SQLite 持久化（`apps/im-live/store.mjs`） | ✅ Node 24 内置 node:sqlite；会话/消息/审计链跨重启恢复并校验；4 项单测 |
| 登录鉴权（`apps/im-live/auth.mjs`） | ✅ scrypt 口令 + HMAC HttpOnly Cookie + 会话身份（忽略 body/query userId）+ 越权审批 403；5 项单测 |
| HTTPS（可选 TLS） | ✅ `IM_TLS_CERT/IM_TLS_KEY` 启用 https；缺证书抛 TLS_CONFIG_MISSING 不静默降级；3 项测试 |
| 技术文档集（00 总览 + 10 篇专题） | ✅ 约 3000 行 |
| 契约文件（4 proto + OpenAPI 3.1 + 索引） | ✅ 人工自检；CI 待补 protoc/buf |
| Flutter 客户端骨架 | ⚠ 未编译（本机无 Flutter SDK，README 已声明） |
| 生产化：Go 微服务、IM PaaS 接入、实机、硬件 ODM | ⬜ 见 [09-开发计划](docs/09-开发计划与里程碑.md) |
