# IM2A 技术方案 v0.1

> 定位：**独立开源项目**。本方案不依赖 w2m / DSH 的代码与服务；参考的是其工程经验（任务账本、租约、内容寻址、门控、审计），
> 并给出 IM2A 自己的实现选择与理由。每个关键选择都写「备选方案 + 否决理由」，便于评审与替换。

## 1. 架构总览

```
┌────────────────────────────────────────────────────────────────────┐
│  L4 产品面   Web UI（IM / 审批态 / Agent 面板 / 审计回放）           │
├────────────────────────────────────────────────────────────────────┤
│  L3 Agent 运行时                                                   │
│    Planner（LLM JSON 计划 | 离线规则）                              │
│    Permission Gate（Agent 声明白名单 + 信任档位）                    │
│    Skill Executor（本地 Skill / 可替换为 MCP 适配器）                 │
│    Egress Guard（外发内容过审）→ Draft                             │
├────────────────────────────────────────────────────────────────────┤
│  L2 服务层（冻结语义，services/）                                    │
│    trust-zone  capability-gate  approval-engine  negotiation-engine │
│    memory-store  egress-guard  payment(ledger, paid-message)        │
│    messaging(envelope, seq, dedupe)  audit(hash chain)              │
├────────────────────────────────────────────────────────────────────┤
│  L1 存储与基础设施                                                  │
│    SQLite（v0.1：w2m relay 单机账本的等价替代）                       │
│    → v0.2 任务账本 + 事件流；v0.3 多节点 relay / PostgreSQL / blob   │
└────────────────────────────────────────────────────────────────────┘
```

**三条流量分离**（沿用 w2m 的核心结论，独立实现）：
1. 人类消息：需要到达、离线补推、多端一致 → HTTP + 轮询/v0.2 SSE；
2. Agent 任务：不需要给人看，需要幂等、可重放 → 任务账本/事件流，不进 IM 会话；
3. 审批与审计：只追加、可校验 → 审计哈希链 + 审批事件溯源。

## 2. 关键设计选择（含被否方案）

| # | 选择 | 备选与被否理由 |
|---|---|---|
| 1 | 单进程 Node（≥22）+ 内置 `node:sqlite`，零第三方依赖 | 备选：Go 微服务 / Kafka / PostgreSQL。**否决（v0.1）**：验证期先证明产品主线与治理语义，不引入运维面；v0.3 再拆任务账本与 relay |
| 2 | 治理语义与产品面分层：`services/` 冻结、`apps/` 迭代 | 备选：全部塞进 server.mjs。**否决**：门控/扣费/审计一旦与 UI 耦合，测试与替换成本指数上升 |
| 3 | 运行 Agent 任务（v0.2）用 append-only 任务账本 + worker 领取 | 备选：直接函数调用。**否决（v0.2）**：无法重放、无法多机、无法限流与审计 |
| 4 | 大产物用内容寻址 blob（sha256），DB 存引用 | 备选：全部塞 DB。**否决**：大文本撑爆行、审计/去重困难 |
| 5 | 消息序：会话内单调 `bizSeq` + `bizId` 去重 | 备选：全局时间戳排序。**否决**：跨端并发下顺序不稳、无法幂等；w2m 的分片锁思路在单进程先用单写者约束等价实现 |
| 6 | 模型接入：OpenAI 兼容（DeepSeek-V4.1-Flash 默认） | 备选：绑定某厂商 SDK。**否决**：模型会变、网关会换；OpenAI 兼容层是事实标准 |
| 7 | 离线模板 + 规则规划器为默认降级路径 | 备选：模型不可用即报错。**否决**：Demo/CI 必须可运行，且降级要「诚实标注来源」 |
| 8 | 权限判定在代码层，提示词只描述职责 | 备选：用 system prompt 约束。**否决**：提示注入即越权，资金/信任操作不可接受 |

## 3. 模块契约（v0.1 现状 → v0.2 重写）

| 模块 | 职责 | 关键不变量 | 状态 |
|---|---|---|---|
| `services/orchestrator/trust-zone` | 五档信任矩阵、过期回落 | 未知关系默认 STRANGER；读档不改变状态 | ✅ |
| `capability-gate` | Agent 声明 + 信任 + 预算 → 允许/转审/拒绝 | 拒绝必须具名（SKILL_NOT_ALLOWED / TRUST_TOO_LOW） | ✅ |
| `approval-engine` | 9 态审批 + 事件溯源 + 超时升级 | 终态不可二次决策；升级幂等 | ✅ |
| `negotiation-engine` | Agent 协商 8 态 + 轮次/token 熔断 | 超限必须上抛人审批，不得静默继续 | ✅ |
| `paid-message` + `ledger` | 9 态付费消息 + 不可变账本 | 幂等键唯一；重复回调不二次扣费 | ✅ |
| `egress-guard` | 外发内容规则（身份证/手机号/URL 等） | BLOCK 内容不得进入会话 | ✅ |
| `audit` | 哈希链只追加 | `hash = sha256(prevHash + canonical(record))`；篡改可定位 | ✅ |
| `store`（apps） | SQLite 落库：会话/消息/审计/序号 | 重启后链连续；reset 保留表结构 | ✅ |
| `auth`（apps） | scrypt 口令 + HMAC Cookie | 身份只来自会话；越权审批 403 | ✅ |
| `llm`（apps） | OpenAI 兼容客户端 + 预算 + 离线降级 | 无 key/失败/超预算必须降级并标注 | ✅ |
| `agent-runtime`（apps） | 规划→门控→Skill→起草→护栏 | 模型只建议 tool，不能执行 | ✅ |
| **TaskLedger**（v0.2） | 任务状态机 + 幂等 + 租约 | 至少一次执行 + 结果幂等去重 | ⏳ |

## 4. 安全与本轮已实现边界

- **身份**：scrypt 口令 + HMAC-SHA256 签名 Cookie（HttpOnly/SameSite=Strict，HTTPS 可 Secure）；身份只来自会话。
- **授权**：审批单只能由名下用户裁决（403）；query/body 的 userId 一律忽略。
- **门控**：Skill 的执行需同时满足「Agent 声明」与「信任档位」；模型输出只是计划建议。
- **输出**：EgressGuard 过审；BLOCK 内容不回显。
- **审计**：哈希链；`verify()` 返回 `brokenAt`；持久化后跨重启可校验。
- **已知边界（不回避）**：无状态会话登出仅清 Cookie（无吊销表）；速率限制只有 LLM 预算；无多租户；SQLite 单写者。
  这些在 v0.2/v0.3 的路线表中均有明确落点。

## 5. 部署形态

```
开发/演示：node apps/im2a/server.mjs                     # http://127.0.0.1:8797
生产（v0.1 可用）：systemd + 反向代理（Caddy/Nginx）终止 TLS → 127.0.0.1:8797
              IM_AUTH=1 · IM_SESSION_SECRET · IM_TLS_CERT/KEY（或前置代理）· IM_DB_PATH
v0.3：relay 与 worker 分离部署；blob 目录挂载；metrics/healthz 接入监控
```

## 6. v0.2 重写执行方案（本文件是契约，代码按此逐模块重写）

1. **先写契约测试**（红）：任务账本状态机、幂等、重放；SSE 断线重连；消息 bizSeq 保序去重。
2. **实现顺序**：`core/`（ids/hash/canonical + 事件）→ `TaskLedger` → worker → IM 消息序 → SSE → 现有 UI 适配新 API。
3. **迁移策略**：旧 API 兼容层保留一个版本；数据迁移脚本（SQLite 加表，不删旧表）。
4. **验收**：同一套端到端测试（聊天/审批/付费）在旧路径与新路径都通过后，删除兼容层。

## 6.5 开源生态选型（详见 LANDSCAPE.md）

结论：**「熟人社交 + 陌生人付费」没有成熟开源业务实现**。IM2A 自研两块核心（五档信任矩阵、付费消息状态机），
其余层按需借鉴而非依赖：AICQ/Sigil（身份与协议）、WuKongIM（投递底座）、llm-rag-mcp-orchestrator（路由三阶段）、
MindRoom/HiClaw（子 Agent 拓扑与凭证隔离）、AstrBot/LangBot（接入与插件）、MagicChat（产品范式）、imcodes（审计模式）。
详见 [LANDSCAPE.md](./LANDSCAPE.md)。

## 7. 与 w2m 的关系（明确边界）

- **借鉴**：任务账本/租约/幂等、消息序号与去重、内容寻址 blob、代码层门控、不可变审计、三流量分离。
- **不复用**：不引入 w2m 包、不连 w2m relay、不共享 token、不复制其代码；本仓库所有模块独立实现与测试。
- **差异**：w2m 面向「多机 agent 执行网络」，IM2A 面向「熟人社交 IM + 个人 Agent 团队」；未来若互通，只通过公开 HTTP 协议对接。
