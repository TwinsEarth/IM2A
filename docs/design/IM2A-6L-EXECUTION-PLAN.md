# IM2A 六层框架：执行路线图

配套文档：`docs/design/IM2A-SYSTEM-FRAMEWORK-6L.md`（架构定稿）。
本文回答"先做什么、怎么验收、本轮做到哪"。

## 1. 现状审计（2026-11-15）

| 层 | 已有 | 缺口 |
| --- | --- | --- |
| L1 智能提供 | Harness 侧模型配置、三种 API 模式可接 | **无能力声明契约**：模型能力靠硬编码，经济/自治无法按能力比价 |
| L2 Agent | Kernel + 9 模块骨架；skill/memory/trace/reflect/curator/guard/claim/audit 已落地；identity/economy/governance 为 `planned` | 拓扑指标、Shapley 归因、信用消费仍缺 |
| L3 人机边界 | 宪法 C1–C7 文档化；`reserved_ratio`、`trajectory:contribute`、`budget_tokens` 有实现点 | 物理动作 scope、注意力预算的**统一裁决入口**未定义 |
| L4 硬件终端 | Surface 层可移植性设计（DID 绑定） | 无导出/导入格式，换设备等于重建 |
| L5 软件载体 | DSH 插件（单包）、浏览器扩展、IM Bot、OpenClaw | **即时生成世界无契约**：`ui_artifact` 未定义 |
| L6 基础设施 | gateway（18080）、MCP bridge（18082）、SQLite、Memory Hub | **无资源计量记录**：算力/存储/带宽消耗不可归集，边际定价无输入 |

一句话：**第 2 层的骨架在，第 1/5/6 层与该层的三条接缝没有契约**。
先补接缝，再填实现——否则每加一个模块都要重新发明一遍跨层约定。

## 2. 工作包

### WP-0 跨层契约冻结（本轮执行）

3 条契约 + 否定控制 + 语义控制 + 接入主闸。

| 契约 | 方向 | 冻结内容 |
| --- | --- | --- |
| `model-capability-manifest-1.0` | L1 → L2 | 端点、协议、上下文/输出上限、能力位、单价、声明时间 |
| `resource-metering-record-1.0` | L6 → L2 | 资源种类/数量/单位、用途三分类、成本、是否可计费 |
| `ui-artifact-1.0` | L5.2 → L2 | 轨迹产物、内容寻址、`ephemeral/promoted`、升格引用 |

**验收**

```text
node scripts/check-layer-contracts.mjs        exit 0，valid 全过
每条契约 ≥ 5 条 schema 负控被拒
每条契约 ≥ 2 条语义负控被拒（schema 表达不了的规则）
≥ 1 条正控（合法变体必须被接受，证明不是一律拒绝）
node scripts/validate.mjs                     含新校验且 exit 0
```

**为什么先做这个**：三条接缝分别决定"能不能比价"、"能不能定价"、"能不能沉淀"。
契约未冻结时写实现，等于把跨层假设埋进代码。

### WP-1 L1 能力注册表（依赖 WP-0）

- 交付：`kernel/model-registry.mjs`，加载 `capability_manifest[]`，
  提供 `list()/get(id)/estimateCost(tokensIn, tokensOut)`。
- 验收：未知模型返回 `model_not_declared`；价格只取自 manifest；
  同一 provider 多端点按 `region` 选择；不实现路由策略（留给 3.4）。

### WP-2 L6 计量接入（依赖 WP-0）

- 交付：`kernel/meter.mjs` 写 `resource_metering_record`；2.6 经济桩消费三池分配。
- 验收：一次模型调用产生 1 条 compute 记录；内部用途记录 `billable=false`；
  三池之和 == 总预算，比例下限 10% 不被突破。

### WP-3 L5.2 `ui_artifact` 落地（依赖 WP-0）

- 交付：`trace_ingest` 接受可选 `ui_artifact`；`reflect_trigger` 命中后走 `skill_claim` 升格。
- 验收：`ephemeral` 不进贡献链；`promoted` 必须有 `promoted_skill_id` 且进入贡献链；
  重复升格幂等。

### WP-4 L4 DID 可移植（依赖 WP-0/WP-1）

- 交付：DID 导出/导入包（`skills/` + `goal_buffer` + `credit_profile` + 贡献链只读副本）。
- 验收：换设备后同一 DID 的资产哈希一致；导入不产生新的贡献条目。

### WP-5 拓扑指标（依赖 WP-1/2/3，与 2.5/2.6 并行）

- 交付：`node_count / quality_index / attribution_precision` 三项指标与快照。
- 验收：指标可由已有记录重算，不引入新采集面。

### WP-6 治理与物理授权（依赖 WP-4）

- 交付：scope 变更 proposal（C1）；物理动作 scope 的显式授予入口。
- 验收：Agent 无自主申请路径；高影响变更必须人类裁决。

## 3. 排序与依赖

```text
WP-0 ──┬── WP-1 ──┐
       ├── WP-2 ──┼── WP-5
       ├── WP-3 ──┘
       └── WP-4 ───── WP-6
```

WP-0 是唯一的前置阻塞项；WP-1/2/3 可并行；WP-4 独立；WP-5/6 最后。

## 4. 执行状态（滚动更新）

| WP | 状态 | 交付物 | 验收 |
| --- | --- | --- | --- |
| WP-0 契约冻结 | ✅ 完成 | 3 schema + 6 fixtures + `scripts/check-layer-contracts.mjs`，接入主闸 | 20 schema 负控 / 8 语义负控 / 4 正控全过；外部负控 2 项（改语义、删必填）均 exit 1 |
| WP-1 L1 能力注册表 | ✅ 完成 | `packages/dsh-im2a-plugin/kernel/model-registry.mjs` | 8 tests：未声明模型报 `model_not_declared`、价格只取声明值、端点按区/协议选择、重复声明拒绝 |
| WP-2 L6 计量 | ✅ 完成 | `packages/dsh-im2a-plugin/kernel/meter.mjs` | 6 tests：一次调用一条记录、内部用途不计费、三池分配、10% 保留下限拒突破 |
| WP-3 L5.2 ui_artifact | ✅ 完成 | `packages/im2a-http-mcp/ui-artifact.mjs` | 7 tests：`ephemeral/promoted` 状态对、升格幂等、异目标冲突、坏输入不产物 |
| WP-3a `trace_ingest` 接入 | ✅ 完成 | `trace-store.mjs` 侧车 `ui-artifacts.jsonl` + 服务端 ingest schema | 6 tests：侧车落盘、无产物不建文件、坏产物整批拒绝且不写 trace、按 trace 隔离、仅追加 |
| WP-4 L4 DID 可移植 | ✅ 完成 | `packages/dsh-im2a-plugin/kernel/did-portability.mjs` | 9 tests：同资产跨机同摘要、篡改必失败、异 DID 导入拒绝（C4）、导入追加 0 条贡献 |
| WP-5 拓扑指标 | ✅ 完成 | `packages/dsh-im2a-plugin/kernel/topology-metrics.mjs` | 8 tests：节点跨三源去重、质量=执行成功率、归因精度=独立验证补丁占比、空缺因子为 null 而非 0、摘要忽略生成时间 |
| WP-6 治理与物理授权 | ✅ 完成 | `packages/dsh-im2a-plugin/kernel/scope-governance.mjs` | 10 tests：Agent 只能提议不能授予、Agent 不能批准（含自己的提议）、物理 scope 无提议路径、直接授予需人类+理由、天花板拒绝、拒绝后不可重开、同人重复批准不重复计数 |
| WP-6a 治理工具面 | ✅ 完成 | `governance_manage` + `packages/im2a-http-mcp/governance-store.mjs` | 5 tests：提议不授予、重启后回放、物理 scope 不落盘、仅追加、全局 status；实机 4 次调用验证 |

累计：WP-0..WP-6a 全部完成；新增 59 tests（21 + 6 + 9 + 8 + 10 + 5），全量 **127 tests 通过**。

**契约校准记录**（实现倒逼契约修正，都是真实约束）：

- `trace_id` 实际是裸 32 位小写 hex（`trace-store.mjs:189`），**不是** `trace_` 前缀。
  两份契约与 fixtures 已按实际校准——这就是先写契约再接实现的价值：错误在夹具期暴露，
  而不是在集成期。
- `goal_id` 是 `goal_` + 24 hex（`goal-buffer.mjs:163`），既有契约正确。
- 「`billable` ⇔ 成本 > 0」是错的：内部用途可以有真实成本但不计费。语义规则改为
  「计费 ⇒ 有成本」，并补正控 V2（`self_improvement` + 0.01 + `billable:false` 必须被接受）。

## 4.1 WP-3a 的裁定与实现

**定：走侧车文件。** `execution-trace-1.0` 是 `additionalProperties: false` 且已冻结，
不把 `ui_artifacts` 塞进 trace 记录，而是写 `ui-artifacts.jsonl`：一行一个产物，
按 `trace_id` 索引，读取用只读的 `getUiArtifacts(traceId)`。

接入分工：客户端只声明产物**事实**（`kind` / `format` / `sha256` / `bytes` + 可选
`goal_id`、`parent_artifact_id`）；`trace_id` 与 `artifact_id` 由服务端铸造——客户端在
ingest 时根本还不知道 trace id。坏产物会让**整批在写 trace 之前**被拒绝，不留半条记录。

## 4.2 WP-4 的两条硬不变量

- **一个 bundle 只能导回它自己的 DID**：`planDidImport` 对 `targetDid !== bundle.did`
  直接拒绝，因为把资产导入另一个 DID 等于改写血缘（C4）。
- **导入不追加贡献条目**：bundle 只携带贡献链的只读证明（`count` + `head_hash`），
  计划里 `contributions_appended` 恒为 0。恢复的是历史，不是改写历史。

## 4.3 WP-5 的指标口径（重要）

三项指标全部由**已有记录重算**，没有新增采集面：

| 指标 | 口径 | 数据来源 |
| --- | --- | --- |
| `node_count` | 三源去重后的 `agent_did` 数 | 计量记录 + 轨迹 + 贡献条目 |
| `quality_index` | **观测到的执行成功率**（成功事件 / 总事件） | 轨迹 `events[].success` |
| `attribution_precision` | 有效补丁 / 全部补丁（有效 = 有验证证据且验证者非作者） | 贡献链条目，复用 `isEffectivePatch` |

两条诚实性约束写进了结构而不是文案：

- **`quality_index` 不是信用档位**。身份模块（2.5）仍是 `planned`，没有信用数据可读；
  快照里 `credit_tier_coverage: 0` 且 `credit_source` 明确写「以执行可靠性代理」。
  等身份模块落地后只换数据源，不改快照形状。
- **`composite` 只是快照间可比的相对指标**，不是智能度量，也不主张「指数级涌现」。
  任一因子缺失时 `composite.value = null`，绝不补 0 假装算出来。

## 4.4 WP-6 的三条硬边界

治理模块把「谁能改权限」拆成两条互不相通的路径，而不是一条带开关的路径：

1. **提议 ≠ 授予**。`proposeScopeChange` 只产出一条 `pending` 提议，永远不返回 grant；
   生效必须由人类 `recordHumanDecision` 批准。Agent 不能批准任何东西——包括它自己的提议。
2. **物理权限没有 Agent 提议入口**。`physical:*` / `actuator:*` / `bio:*` 在
   `proposeScopeChange` 里直接抛 `physical_scope_requires_human_grant`，**连提议对象都不会产生**。
   理由很直白：只要能创建提议，提议就能被游说成事实。物理授权只走
   `grantScopeDirectly`，必须人类主体 + 记录理由。
3. **天花板归治理层**。提议超出 `proposableScopes` 抛 `scope_above_ceiling`；
   `economy:settle` 这类不在可提议集合里的权限，Agent 连问都问不了。

补充实现细节：Agent 主体可以直接用 DID 表示（`did:...` 自动识别为 `agent`），
避免「Agent 试图批准」被误报成「无效输入」而绕过真正的拒绝路径；
同一个人类重复批准只算一次；被拒绝的提议不可重开（`proposal_closed`）。

## 4.5 治理工具面（2026-11-15 裁定）

采用方案 B：只新增一个 `governance_manage`，`action` 枚举 `propose | status`，工具面 30 → 31。

**为什么裁决不进 MCP**：`recordHumanDecision` 在内核里信任调用方传入的 `principal` 字符串；
一旦包成 MCP 工具，Agent 只要传 `principal: "human:alice"` 就能自批。所以 decide / grant
一律不暴露，人类裁决走 DSH 审批面（或未来的 ed25519 人类签名路径）。

**scope 归属**：工具要求 `governance:propose`（已加入 catalog 的封闭 scope 集合）。
它**不在** Agent 可提议集合内，因此「谁有权提议」本身由人类直接授予决定。

**契约变更**：`governance` 前缀加入工具名模式；`governance:propose` 加入 scope 枚举；
catalog 30 → 31。别名表仍为 30 条——`governance_manage` 是原生规范名，没有「旧名」可迁移，
登记在生成器的 `CANONICAL_ONLY` 里，避免为了新工具而放宽漂移检查。

**实现**：`governance-store.mjs` 用 append-only JSONL 持久化提议；坏行跳过不崩溃；重启后回放。
实机验证：`status` 空态、`propose skill:write` 得到 pending、`propose physical:motion` 被
`physical_scope_requires_human_grant` 拒绝、`status` 仍显示 `effective_scopes: []`（提议不授予）。

## 5. 风险

| 风险 | 缓解 |
| --- | --- |
| 契约与实现漂移 | 契约进主闸；fixtures 即最小实现样例 |
| 三条契约过度设计 | 字段取最小可用集；`additionalProperties: false` 强制显式扩展 |
| 语义规则藏在 prose 里 | 语义负控进校验脚本，失败即红灯 |
| 把第 2 层做成第 1 层 | 第 1 层只声明能力，路由与定价留在 3.4/2.6 |
