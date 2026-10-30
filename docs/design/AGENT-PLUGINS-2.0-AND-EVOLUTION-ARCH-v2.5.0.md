# IM2A v2.5.0 — Agent Plugins 2.0 与「智能/进化」总体架构

Status: **design proposal (pre-ADR)**. No code, no schema mutation, no tag.
Baseline: platform `2.4.6` / go `2.3.26`，`scripts/validate.mjs` green，sourceHash `7b26bf11…`.
Reference: Hermes Agent (<https://github.com/NousResearch/hermes-agent>)，2026-10-30 main.
Scope of this doc: 分层架构、协议/版本边界、数据流、失败/回滚、跨端一致性、必须拒绝的设计、优先级与验收指标。

---

## 0. 一句话结论

> Hermes 的**形状**可以搬，Hermes 的**信任模型**不能搬。

Hermes 是单租户个人代理，「agent 可以修改或删除任何 skill」是特性；IM2A 是六方发布面 + 公开码/凭据双平面的多方平台，同一个特性等于把发布面、凭据面和审计一起交出去。
因此 v2.5.0 的「进化」必须是 **proposal → staging → promotion（提案—暂存—晋升）**，而不是 **self-modify（自改）**。
这是本设计中唯一不可妥协的分界线；其余一切（渐进披露、技能即程序性记忆、隔离子代理、窄核心）都可以借鉴。

---

## 1. 已核实的现状（设计的地基，不是假设）

| 项 | 实测状态 | 对 2.0 的含义 |
|---|---|---|
| Agent Plugins profile | `1.0`，**文档内无 `profileVersion` 字段**，版本只存在于 `packages/protocol/versions.json` 的旁表 | 出厂 bundle 不自描述版本；第 3 方 harness 无法判断该按哪版解析 |
| `agent-plugin.schema.json` | 根级 `additionalProperties: false` | 增加 `agents`/`formations`/`learning` 会**直接校验失败**，不是警告 |
| 生成器 | 12 个 target，每个只产出 `bundle.meta.json`，`status: "skeleton"`，注释 `no platform-specific files are emitted yet` | 「12 端」目前是**骨架声明**，不是可安装产物 |
| 技能池 | 4 个 `SKILL.md`，在 `skills/` 与 `packages/agent-plugins/source/skills/` **逐字节重复**（已验 SHA256 全等） | 双源真相；生成器只哈希后者，前者经 allowlist 照发，**零门禁覆盖** |
| 适配器 | 12 个 id：`verified` 1（dsh）、`observed` 3（workbuddy/doubao/muse）、`planned` 8 | 与「六方 PASS」不是同一根轴，不可互相替代 |
| `doubao.displayName` | 双重编码乱码（"豆包…/ Ark" 被破坏），schema 只查 `minLength:1`，**校验通过** | 现有门禁抓不到编码腐坏 |
| 校验器 | `scripts/schema-validator.mjs` 手写子集求值器，自述 "CI must replace it with a standards-complete validator (ajv)" | 2.0 的 `oneOf`/条件约束会超出其能力 |
| 身份 | ident-code 1.0：公开寻址面 / 私有凭据面分离（ADR 0003/0004）；`agentAddress` = 12 位用户号 `.` 10 位子码；`grant{userId,agentSubcode,scope,audience,expiresAt,credentialId}` | **编队可直接复用**，无需新身份体系 |
| 编队/进化基建 | `curator` 0 处、`subagent` 0 处、`evolution` 0 处、无 trace_id、无 eval 套件 | v2.5.0 两个新平面从零起步，必须先立契约再写运行时 |

已有资产里最值钱的一条：`skills/fleet-control/SKILL.md` 已经写对了委托不变量（一个子任务一个 owner、handoff ≠ approval、离线报 `blocked` 不静默改道、重试一次后报 `unverified`）。**v2.5.0 的任务是把这条经验从技能文本提升为协议约束。**

---

## 2. 分层架构

六层 + 两条跨切面不变量。依赖只能向下，不能向上。

```
┌─ L6 治理层 Governance ────────────────────────────────────────────────┐
│  身份(公开面/私有面) · HITL 闸门 · 审计 · 成本/预算 · kill switch      │
│  唯一有权把 staged 变成 active 的层                                    │
└───────────▲──────────────────────────────────────────────────────────┘
┌─ L5 进化层 Evolution [新] ───────────────────────────────────────────┐
│  轨迹 → 评测 → Curator 提案 → 暂存 → 影子 → 金丝雀                    │
│  只读 L2/L3/L4；只写 staging；永不直接改 active                        │
└───────────▲──────────────────────────────────────────────────────────┘
┌─ L4 编队层 Formation [新] ───────────────────────────────────────────┐
│  formations[] 声明 · 委托契约 · 编排/终止/预算 · 结果归并               │
└───────────▲──────────────────────────────────────────────────────────┘
┌─ L3 单代理运行时 Agent Runtime ──────────────────────────────────────┐
│  一个 agent 实例 × 一个 harness × 一个 adapter；上下文预算；工具作用域   │
└───────────▲──────────────────────────────────────────────────────────┘
┌─ L2 技能/知识层 Skill ───────────────────────────────────────────────┐
│  共享技能池 · 渐进披露 · 内容寻址 · 来源/出处 · 权限声明                │
│  纯指令，无副作用，无密钥                                              │
└───────────▲──────────────────────────────────────────────────────────┘
┌─ L1 能力层 Capability ───────────────────────────────────────────────┐
│  MCP server · tools · transports(stdio/streamable-http) · adapters    │
│  有副作用；最小权限；每次调用可审计                                     │
└───────────▲──────────────────────────────────────────────────────────┘
┌─ L0 契约层 Contract/Pin ─────────────────────────────────────────────┐
│  version-matrix.json · versions.json · schemas/ · ADRs                │
│  纯数据，无运行时，唯一真相                                            │
└───────────────────────────────────────────────────────────────────────┘
```

| 层 | 该层**唯一**能做什么 | 该层**明确不能**做什么 |
|---|---|---|
| L0 | 钉住版本、发布 schema、记录 ADR | 承载逻辑、承载证据、隐式 `latest` |
| L1 | 执行工具调用、上报 trace | 决定该不该调用、持有长期凭据 |
| L2 | 提供指令与知识 | 产生副作用、含密钥、自授权限 |
| L3 | 完成单代理一轮循环 | 派生其它 agent、改自己的技能、越界用工具 |
| L4 | 分解、委托、归并、终止 | 直接执行子任务、跨过 L6 提升技能 |
| L5 | 生成提案与评测 | 改 active、碰凭据面、调业务工具 |
| L6 | 判定、批准、回滚、断流 | 生成内容、绕过 eval |

### 两条跨切面不变量（可机器检查，非口号）

- **INV-1 授权单调收窄**：任何一层都不能扩大另一层的权限。委托时 `child.scope ⊆ parent.scope` 且 `child.expiresAt ≤ parent.expiresAt`；`credentialId` 永不跨代理传递。
- **INV-2 全链路可归因**：每一个有副作用的调用都携带 `traceId` + `agentAddress` + `delegationId`；给定一个错误输出，必须能用**一次** trace 查询定位到产出它的 agent。

违反 INV-1/INV-2 的实现，无论功能多完整，**不允许进入 v2.5.0**。

---

## 3. Agent Plugins 2.0：协议与版本边界

### 3.1 为什么不能「就地升版」

现状是「扁平 + 封闭」：`plugin.json` 只有 `{name, version, description, license, skills[], mcp, targets[], extensions[]}`，根级 `additionalProperties:false`。若直接把 `$id` 改成 2.0 并追加新块，会在**同一时刻**打断：

1. 12 个已生成 target（`generate --check` 的 sourceHash 契约）；
2. 手写校验器（`oneOf`/条件约束超纲）；
3. 8 个 `planned` harness 的解析器（它们只认 1.0 形态）。

这是典型的「一次改动炸掉所有消费者」。必须**增量 + 自描述 + 可协商**。

### 3.2 三条硬边界

**边界 A — 文档自描述。** 2.0 起每个 `plugin.json` / `SKILL.md` / `formation.json` 都必须自带 `profileVersion`（现在没有）。版本判定不得依赖「文件路径」或仓库旁表。

**边界 B — 双 schema 并存 + 投影降级。** `agent-plugin-1.0.schema.json` 与 `agent-plugin-2.0.schema.json` 同时发布；2.0 文档必须能**投影**（剥离 2.0 专有块）后通过 1.0 校验。这给出可测试的向后兼容，而不是「应该兼容」。

**边界 C — 读者协商，不猜。** 消费方声明 `maxProfileVersion`；生产方声明 `minReader`。协商结果只有两种：按高版解析，或按 floor 版解析并**显式发 `degraded-profile` 事件**。没有第三种（静默降级 = 静默失败）。

### 3.3 2.0 新增块（只列契约，不贴实现）

```
plugin.json 2.0
├── profileVersion: "2.0"          # 新增，必填
├── name / version / description / license / mcp / extensions   # 1.0 语义冻结
├── skills[]        → 升级为 {id, ref, sha256, profileVersion}  # 从"路径"到"内容寻址"
├── targets[]       → 保留 12 个 harness id（集合不变）
├── agents[]        # 新增：角色声明
│     {id, role, scope[], tools[], modelRouteHint, maxDepth, budget, outputContract}
├── formations[]    # 新增：拓扑声明
│     {id, version, roles[], edges[], entry, termination, budget, failurePolicy}
└── learning        # 新增：进化策略
      {evalRefs[], curatorPolicy, promotionGates[], retention}
```

三个设计取舍，明确写下来：

- `skills[]` 从「路径字符串」改为「内容寻址」，是为了让晋升/回滚成为**指针移动**而不是文件覆盖。这是 L5 可回滚的前提。
- `agents[]` 只声明**能做什么**（scope/tools/budget），不声明**怎么做**（prompt 不进协议）。prompt 属于 harness 侧，跨端不可移植。
- `formations[]` 里 `edges` 是**声明式**的，运行时不得自创边。编排器越权自组拓扑是编队失控的头号原因。

### 3.4 版本矩阵（v2.5.0 目标态）

| 协议 | 当前 | v2.5.0 目标 | 变更方式 |
|---|---|---|---|
| Agent Plugins | 1.0 draft | **2.0** + 1.0 并存 | 新 schema + 投影测试 + 一个版本的弃用窗 |
| A2A | 1.0 / v1.0.1（commit 钉死） | 不变 | 升级须新 ADR，禁 `latest` |
| MCP | 2026-07-28 | 不变 | 同上 |
| dsh-session-ref | 1.0 | 不变 | 语义冻结 |
| ident-code | 1.0 | 1.0 + **委托派生规则**（不升协议版） | 在 1.0 `grant` 上叠加 `delegation` 约束，不新增码型 |

**关键判断：ident-code 不升版。** 编队需要的身份能力，`agentAddress` + `grant` 已经够用；再引入一套「代理身份 2.0」会制造第三套命名空间，违反 ADR 0003 的双平面约束。

---

## 4. 数据流

四条主流，各自的失败面在 §5 展开。

### F1 技能解析（L0 → L2 → L3）

```
请求 → 技能索引(仅 name+description, ~L0 级开销)
     → 命中 → 加载 SKILL.md 全文(绑定 sha256)
     → 需要细节 → 按需加载 references/*
     → 未命中 → 显式 skill-unavailable（禁止即兴发挥）
```

渐进披露沿用 Hermes 的三级模型，但**加一条 Hermes 没有的约束**：加载必须绑定内容哈希并记入 trace。否则「昨天能跑、今天不行」无法归因到某次技能晋升。

### F2 委托（L4 → L3）

```
编排器
  ├─ 生成 delegation 契约（含 scope ⊆ 父、expiry ≤ 父、idempotencyKey、depth+1）
  ├─ 并行分发（fan-out ≤ 5）
  ├─ 每个子代理返回 {status, output, confidence, evidenceRefs, degraded?}
  └─ 归并：全到 / 部分到 / 全失败 —— 三种都必须有确定分支
```

委托契约字段（这是跨端契约，必须进 schema）：

```
{delegationId, traceId, from, to, intent, inputsRef, outputContract,
 scope[], expiresAt, idempotencyKey, depth, budget}
```

**为什么 `inputsRef` 而不是内联内容**：内联会让编排器上下文随扇出线性膨胀，且把敏感内容复制到每个子代理的作用域里。引用 + 按需取用同时解决上下文预算与最小权限。

### F3 进化（L3/L4 → L5 → L6）

```
轨迹(trace) ─┐
失败队列 ────┼→ 评测(L5, 只读) → Curator 提案 → staging(哈希钉住)
技能池(只读) ┘                                  ↓
                                        影子流量校验
                                                ↓
                                      L6 晋升闸门（证据 + 审批）
                                                ↓
                                    active 指针切换（可一键回滚）
```

Curator 只产出五类提案，且只写 staging：
`skill.patch` / `skill.create` / `eval.case.add` / `memory.fact` / `formation.tune`。

Curator 的权限边界（静态可查）：**读** trace/eval/skill-pool/failure-queue；**写** staging；**禁止**访问凭据面、禁止调用业务工具、禁止授予 scope。

### F4 六方接入（L6 → L3）

```
外部面(浏览器扩展/微信/IM Bot/OpenClaw/专家/运维)
   → 公开面解析(只返回固定投影，绝不返回凭据状态)
   → 私有面校验(短时、一次性、audience 绑定)
   → session-ref 解析（不透明，非 bearer，需 scope + 未过期 + 未撤销）
   → L3 运行时
```

**六方 ≠ 12 适配器。** 前者是发布面（发布门禁轴），后者是 harness 兼容轴。两者只能在 `compatibility-matrix.json` 里被**并列呈现**，不能在文案里互相替代。

---

## 5. 失败与回滚

### 5.1 失败分类与恢复（编队场景）

| 失败 | 检出 | 恢复 |
|---|---|---|
| 子代理硬失败/超时 | 错误码、deadline | 退避重试 1 次 → 收窄作用域的降级子代理 → 结构化 degraded 结果 |
| 子代理静默错误 | outputContract 校验 + 置信度 | 带具体纠正的重试 → 标记 `unverified` 上报 |
| 部分失败 | schema 完整性检查 | 编排器按 `failurePolicy` 决定：补跑 / 局部结果 / 整体降级 |
| 矛盾 | 显式矛盾检测（同字段不同值） | 仲裁 → 人工；禁止多数表决掩盖 |
| 级联 | 检查点校验 | 回滚到最近检查点，从失败点重跑 |
| 循环不收敛 | 轮次上限 + 分数平台检测 | 强制退出，带最优结果升级 |
| 上下文超载 | 输出 schema + 指令遵循检查 | 压缩重跑；若会丢必填字段则**停机升级**，不静默截断 |

### 5.2 默认上限（写进 `formations[].termination`，不是口头约定）

`maxDepth: 2`、`maxFanout: 5`、`maxRounds: 3`、墙钟与 token 双预算、超限**强制终止并返回已得最优结果**。
理由：链长 > 5 质量衰减、扇出 > 7 超出归并能力、迭代 > 3 边际收益归零。这些是默认值，偏离需在 `formation.json` 里写明理由。

### 5.3 回滚面

| 对象 | 机制 | 目标 |
|---|---|---|
| 技能 | 内容寻址 + active 指针 | 一条命令切回上一哈希，≤60s，来源留存 |
| 编队 | formation 版本化（`{id, version}`） | 回到上一版本声明，运行中实例不受影响 |
| 代理配置 | agents[] 随 plugin 版本走 | 与插件同版本回滚 |
| 发布 | 现有九门禁 + 双 schema | 任一投影/一致性问题 → `NO-GO` |

**幂等性要求**：可重试的步骤必须幂等。委托用 `idempotencyKey`；重复 key 只产生一次效果。不可幂等的副作用（发消息、写外部系统）必须定义补偿动作，并在动作前打检查点。

---

## 6. 跨端一致性

现有 `validate.mjs` 已经做对了一件重要的事：`adapters.json` / `targets.json` / `plugin.json.targets` **三方集合相等**（12/12，含重复 id 检测），且 `install.method` 与 `target.format` 必须一致。这是要继续守住的。

需要补的五条：

1. **单一技能源**：合并两条 `SKILL.md` 路径为一条真相，其余为校验过的镜像或直接删除；生成器哈希必须覆盖最终发布的那一份。
2. **生成物一致性**：12 个 target 的技能哈希必须等于源池哈希（现在是 0 覆盖）。
3. **编码完整性**：所有规范 JSON 文本必须 NFC 且能往返（当前 `doubao.displayName` 已腐坏且能通过校验）。
4. **证据诚实性**：`verified` 必须有存在且 `passed` 的证据文件；`planned` 不得在任何面向用户的文案里被表述为已支持。
5. **兼容矩阵即产物**：生成 `compatibility-matrix.json`（适配器状态 × profile 版本 × transport），文档与 CI 消费同一份数据，禁止散落 prose 声明。

> 现状风险提示：`plugin.json` 声明 12 个 target、`targets.json` 全部 `planned`、dist 全部 `skeleton`。这三者不矛盾（骨架是合法中间态），但任何「已支持 12 端」的表述都不成立。

---

## 7. 必须拒绝的设计

每条给出拒绝理由与替代方案。这一节是评审闸门，不是建议。

| # | 拒绝的设计 | 理由 | 替代 |
|---|---|---|---|
| R1 | Agent 可修改/删除 active 技能（Hermes 式自改） | 单租户特性；在六方 + 凭据面上等于交出发布面与审计 | proposal → staging → 影子 → 金丝雀 → 审批晋升 |
| R2 | 默认 mesh 拓扑 | 调试成本最高、上下文指数增长、共识可能死锁 | 默认层次化；mesh 需主持人 + 终止条件 + 书面理由 |
| R3 | 代理之间传递 `credentialId` / `authCode` | 违反 ADR 0003 双平面；泄露面随跳数放大 | 每跳派生**收窄**的 grant，子代理只认自身 audience |
| R4 | 用公开码（IM2A 号/句柄/子码）作为委托鉴权主体 | 公开码必须是可印在海报上的，不可鉴权 | 凭据面校验；公开面只返回固定投影 |
| R5 | 技能声明 env 变量并自动注入沙箱 | 把密钥生命周期交给内容文件 | 显式能力授予；密钥留 KMS/secret store |
| R6 | 就地改 `$id` 升 2.0 并一次性重生成 12 端 | 同时打断生成器哈希、校验器、8 个 planned 解析器 | 双 schema + 投影降级 + 弃用窗 |
| R7 | 以「生成器产出 12 个目录」宣称 12 端支持 | 骨架 ≠ 可安装产物；证据缺失 | 以 status + 证据文件为准，未验即 `blocked` |
| R8 | 无深度/扇出上限的子代理递归 | 成本与延迟无上界，故障面指数扩散 | `maxDepth/maxFanout/maxRounds` 硬上限 |
| R9 | 扇出分支共享可变全局状态 | 竞态 + 结果不可复现 | 分支独立状态 + 显式归并 |
| R10 | 静默截断上下文/技能 | 生产静默失败的首要成因 | 压缩失败即停机升级 |
| R11 | Curator 无评测、无审批直接晋升 | 闸门剧场；一次坏晋升污染所有下游 | 评测地板（≥20 例）+ 审批 + 可回滚 |
| R12 | 把原始会话/PII 写入共享记忆或长期 trace | 与 ADR 0003、隐私评审冲突 | 结构化事实 + 保留期 + 脱敏 |
| R13 | 每个能力都新增核心 MCP 工具 | 违反窄核心；工具面膨胀拖垮缓存与选择质量 | 足迹阶梯：扩展现有 → 技能 → 门控工具 → 插件 → MCP 目录 → 核心工具 |
| R14 | 把「六方」重新定义为包含 12 适配器 | 范围膨胀，且会让门禁失去意义 | 两轴并列呈现，互不替代 |

---

## 8. 优先级

P0 是不可跳过的地基：没有它，P1 的编排与 P2 的进化都建立在不可归因、不可回滚的沙上。

**P0 — 契约与地基（v2.5.0 阻断项）**
1. 文档级 `profileVersion` 补齐（plugin / skill / formation）。
2. 双 schema + 投影降级测试 + 协商规则。
3. 技能池单一真相 + 覆盖 12 target 的哈希一致性。
4. 校验器升级到 ajv 或等价物（不再自称「子集足够」）。
5. 编码完整性校验（NFC / 往返）。
6. trace_id + agentAddress + delegationId 全链路落点。
7. 证据诚实性门禁（status × evidence 交叉校验）。

**P1 — 编队（首个可见能力）**
8. `formations[]` / `agents[]` schema + 示例（层次化一种拓扑足够）。
9. 委托契约 + scope 收窄 + 幂等键。
10. 终止/预算/降级三分支 + 结构化 degraded 结果。
11. 矛盾检测与仲裁路径。

**P2 — 进化（提案，不自动生效）**
12. eval 套件骨架（每代理 ≥20 例，含基线）。
13. Curator 只读 + staging 写，权限边界静态可查。
14. staging → 影子 → 晋升闸门 + 一键回滚演练。
15. 从失败自动生成 eval 用例的闭环。

**P3 — 规模与治理**
16. `compatibility-matrix.json` 产物化。
17. 成本/延迟预算执行与超限中止。
18. 晋升精度与提案接受率的带宽告警（防闸门剧场）。

---

## 9. 验收指标

每条都可在现有九门禁框架内执行，不发明新流程。

### P0

| ID | 指标 | 阈值 |
|---|---|---|
| A1 | 带 `profileVersion` 的文档占比 | 100%，0 例外 |
| A2 | 2.0 fixture 投影后通过 1.0 校验 | 100%；≥2 个负例必须被拒 |
| A3 | 规范技能池份数 / 重复份数 | 1 份 / 0 份重复 |
| A4 | 12 个 target 技能哈希与源池一致 | 12/12 |
| A5 | 规范 JSON 文本编码完整性 | 0 处非 NFC / 不可往返 |
| A6 | `verified` 适配器对应证据文件存在且 `passed` | 100% |
| A7 | 全链路调用带 trace 三元组 | 100% |

### P1

| ID | 指标 | 阈值 |
|---|---|---|
| A8 | 深度/扇出/轮次上限在诱导递归下生效 | 100/100 次在阈值处被阻断，0 次无界 |
| A9 | 同 `idempotencyKey` 重复委托只产生一次效果 | 100/100 |
| A10 | 子代理四类故障（超时/报错/垃圾/残缺）均返回结构化 degraded | 100%，0 次静默改道 |
| A11 | 子 grant 满足 `scope ⊆ 父` 且 `expiry ≤ 父` | 属性测试 ≥1000 例，0 违例 |
| A12 | 注入 20 个故障后，用 1 次 trace 查询定位到产出代理 | 20/20 |
| A13 | `credentialId` 跨代理传递 | 0 次 |

### P2

| ID | 指标 | 阈值 |
|---|---|---|
| A14 | 晋升前具备 ≥20 例 eval + 已录基线 | 100%，0 例外 |
| A15 | 一键回滚到上一 active 哈希 | ≤60s，演练 100% 通过，来源留存 |
| A16 | Curator 权限静态检查（只读 + staging 写、无凭据面、无业务工具） | 0 违规 |
| A17 | 晋升精度（滚动 20 次晋升无回归比例） | ≥90%，低于阈值冻结晋升 |
| A18 | 提案接受率带宽（防闸门剧场） | 落在 10%–60%；越界触发校准告警 |

### P3

| ID | 指标 | 阈值 |
|---|---|---|
| A19 | 编队运行超硬成本上限而未中止 | 0 次 |
| A20 | 六方每个发布面有对应证据或显式 `blocked` | 6/6 或显式标注，0 处含糊表述 |

**A17/A18 成对存在是刻意的**：只看精度会退化成「只晋升必然成功的」，只看接受率会退化成「什么都批」。两个指标同时可视，才能判断闸门是真的在工作。

---

## 10. 与 Hermes 的取舍对照

| Hermes 的做法 | IM2A v2.5.0 | 原因 |
|---|---|---|
| 窄核心、能力在边缘 | **采纳**（足迹阶梯进评审闸门 R13） | 与六方多端结构天然契合 |
| 技能渐进披露 L0/L1/L2 | **采纳 + 加哈希绑定与 trace** | 省 token，但必须可归因 |
| 技能即程序性记忆、自我改进 | **改造为提案制** | 信任模型不可移植（R1/R11） |
| Curator 策展 | **采纳形态，收紧权限** | 只读 + staging，晋升归 L6 |
| 子代理隔离（独立模型/预算） | **采纳** | 正好落在 L4 契约上 |
| Prompt 缓存不可破 | **采纳** | 会话中不改系统提示，技能变更以增量注记传递 |
| 插件目录准入规则 | **采纳** | 对应现有 12 适配器准入 |
| 「agent 可改任意技能」 | **拒绝** | R1 |
| 技能声明 env 自动注入沙箱 | **拒绝** | R5，与 ADR 0003 冲突 |
| agentskills.io 兼容 | **参考，不承诺** | 需先完成 2.0 自描述与投影，否则兼容声明无依据 |

---

## 11. 证据（本设计的事实来源）

- `node scripts/validate.mjs` → `ok: true`，12 adapters / 12 targets / 4 skills / 9 gates，sourceHash `7b26bf11…`。
- 技能重复：`skills/<id>/SKILL.md` 与 `packages/agent-plugins/source/skills/<id>/SKILL.md` SHA256 **全等**（4/4）。
- `packages/agent-plugins/dist/*/bundle.meta.json` → `status: "skeleton"`，`generatedAt: "deterministic-p0"`。
- `packages/harness-adapters/adapters.json` → 1 `verified` / 3 `observed` / 8 `planned`；`doubao.displayName` 为乱码。
- `scripts/schema-validator.mjs` 自述需替换为 ajv；`additionalProperties:false` 在 L116 强制执行（1.0 schema 根级 L7）。
- 全仓检索：`curator` / `subagent` / `evolution` 命中 0。

---

## 12. 未决问题（需要决策，不阻塞本文）

1. 技能池真相落在 `skills/` 还是 `packages/agent-plugins/source/skills/`？（建议前者，指向「平台资产」而非「某插件内部」）
2. 2.0 弃用窗长度：是否允许 1.0 与 2.0 共存两个 minor 版本？
3. mesh 拓扑是否在 v2.5.0 完全禁止，还是保留「主持人 + 终止条件」的受限通路？
4. Curator 是否需要独立的身份（`agentSubcode`）以便审计区分提案者与执行者？
