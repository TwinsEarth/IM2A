# AgentIM · AI 人机混合熟人社交 IM 平台

> 让每个人都像大老板一样：有自己的总管 Agent、领域子 Agent 团队和 24 小时在线的随身硬件入口，
> 把日常杂碎交给 Agent 团队，人只处理情感层与最终决策。
>
> 本仓库 = **技术文档（架构 / 框架 / SDK / 协议 / 开发计划 / 产品 / UI / 运营）** + **可运行的参考实现**。

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
| 技术文档集（00 总览 + 10 篇专题） | ✅ 约 3000 行 |
| 契约文件（4 proto + OpenAPI 3.1 + 索引） | ✅ 人工自检；CI 待补 protoc/buf |
| Flutter 客户端骨架 | ⚠ 未编译（本机无 Flutter SDK，README 已声明） |
| 生产化：Go 微服务、IM PaaS 接入、实机、硬件 ODM | ⬜ 见 [09-开发计划](docs/09-开发计划与里程碑.md) |
