# 契约目录（contracts/）

> 本目录是模块级与协议级契约的**唯一来源**。实现、文档、SDK、proto、openapi 都必须与这里逐字对齐。
> 当前契约版本：**v1.0.0**（冻结于 2026-10-21）。
> **验证状态：⚠ 未编译验证。** 本机仅有 Node.js 24，无 protoc / buf / Go / Flutter；proto 与 OpenAPI 为人工严格自检的契约源码。CI 必须补齐 `buf lint`、`buf breaking`、`protoc` 编译与 OpenAPI 3.1 校验（见 §5）。

## 1. 契约索引与权威性

| 文件 | 类型 | 权威范围 | 状态 | 校验方式 |
|---|---|---|---|---|
| [`INTERFACES.md`](./INTERFACES.md) | Markdown（冻结） | **模块签名、状态机、错误码、字段名、不变量的最高权威** | v1.0 冻结 | 人工评审 + 单测逐字对照 |
| [`proto/common.proto`](./proto/common.proto) | Protobuf（proto3） | 跨协议公共类型：TrustLevel / AgentKind / GateDecision / ErrorCode / TraceContext / Money / AuditEvent | v1.0 | CI：buf lint + protoc |
| [`proto/negotiation.proto`](./proto/negotiation.proto) | Protobuf（proto3） | 内部 Agent 协商：TaskState / Intent / NegotiationTurn / NegotiationSession / NegotiationService | v1.0 | CI：buf lint + protoc |
| [`proto/approval.proto`](./proto/approval.proto) | Protobuf（proto3） | 审批工作流：TicketState / TicketType / DecisionAction / ApprovalEvent / ApprovalService | v1.0 | CI：buf lint + protoc |
| [`proto/paid_message.proto`](./proto/paid_message.proto) | Protobuf（proto3） | 付费消息：MessageState / BillingMode / LedgerEntry / PaidMessageService | v1.0 | CI：buf lint + protoc |
| [`openapi/orchestrator.openapi.yaml`](./openapi/orchestrator.openapi.yaml) | OpenAPI 3.1 | 编排层 HTTP 面（陌生人意图、审批决策、状态、健康检查） | v1.0.0 | CI：OpenAPI validator + 路由对照 |
| [`../docs/03-协议设计.md`](../docs/03-协议设计.md) | Markdown | 协议语义、状态机、超时/重试、安全（**解释性**，冲突时以 INTERFACES.md 为准） | v1.0 | 人工评审 |
| [`../docs/04-SDK与端侧设计.md`](../docs/04-SDK与端侧设计.md) | Markdown | SDK 分层、BLE 帧格式、隐私时序、Skill 清单（**解释性**） | v1.0 | 样机实测后回填 |

**冲突时的权威顺序**：

1. `INTERFACES.md`（模块级语义与字符串，唯一最高权威）；
2. 与 INTERFACES.md 无交集的部分：proto 的**线格式与字段号**、OpenAPI 的**路径与方法**各自在其领域内权威；
3. 若 proto/OpenAPI 与 INTERFACES.md 不一致，一律视为**契约缺陷**，修 proto/OpenAPI，**不得**反向修改 INTERFACES.md；
4. docs/ 只做解释，不得作为实现依据；`apps/`、`services/` 是消费者，不得反向定义契约。

## 2. 枚举对照（INTERFACES.md ↔ proto ↔ OpenAPI）

| 语义 | INTERFACES.md | proto | OpenAPI |
|---|---|---|---|
| 信任档 | `TRUST_LEVELS` SELF=0…STRANGER=4 | `common.proto: TrustLevel` 数值一致；允许条件 `levelOf <= trustZoneMin` | `TrustLevel` 字符串枚举 |
| Agent 类型 | `AGENT_KIND` butler/subagent/skill | `common.proto: AgentKind`（BUTLER/SUBAGENT/SKILL） | `AgentView.kind`（小写字符串） |
| 门控裁决 | `DECISION` ALLOW/REQUIRE_APPROVAL/DENY | `common.proto: GateDecision` | 门控结果体现在错误码 |
| 错误码 | 各模块 Error 的 `code` | `common.proto: ErrorCode`（去重合并） | `ErrorResponse.error` |
| 协商状态 | `TASK_STATE` 8 态 | `negotiation.proto: TaskState` | `NegotiationView.state` |
| 协商意图 | `INTENT` 6 值 | `negotiation.proto: Intent` | `NegotiationTurnView.intent` |
| 审批状态 | `TICKET_STATE` 9 态 | `approval.proto: TicketState` | `TicketState` |
| 审批类型 | `TICKET_TYPE` 4 值 | `approval.proto: TicketType` | `TicketView.ticketType` |
| 决策动作 | decide 的 'APPROVE'/'MODIFY'/'REJECT' | `approval.proto: DecisionAction` | `DecisionAction` |
| 付费消息状态 | `MSG_STATE` 9 态 | `paid_message.proto: MessageState` | `MessageState` |
| 计费模式 | `BILLING` SEND_PAY=1 / VIEW_REPLY_PAY=2 | `paid_message.proto: BillingMode` 数值一致 | `BillingMode` 字符串 |
| 账本类型 | `ENTRY_TYPE` CHARGE/REFUND/SETTLE | `paid_message.proto: EntryType` | `LedgerEntryView.type` |
| 业务消息类型 | `BIZ_TYPE` 5 值 | （不在 proto 面） | `EnvelopeView.bizType` |

一致性结论（v1.0）：枚举**名称逐字一致**；proto 仅在 proto3 强制要求处引入 0 值占位（`ErrorCode.ERROR_CODE_UNSPECIFIED`、`BillingMode.BILLING_MODE_UNSPECIFIED`、`EntryType.ENTRY_TYPE_UNSPECIFIED`、`ScreeningVerdict.SCREENING_VERDICT_UNSPECIFIED`），其余首值取该领域的自然默认态（如 `TaskState.SUBMITTED=0`）。`TicketPriority` 在 INTERFACES.md 中是**小写字符串**，proto 枚举名为大写（NORMAL/URGENT/HIGH/LOW），JSON/JS 侧映射为小写——这是枚举命名规范的唯一大小写差异。

## 3. 冻结与变更流程

```text
提案（提 issue / 变更单，写明动机、影响面、迁移方案）
   ↓
Lead 审批（涉及语义变更必须先改 INTERFACES.md，再改投影）
   ↓
版本号递增（见 §4）+ CHANGELOG 条目
   ↓
同步更新：proto / openapi / docs / SDK / 测试
   ↓
CI 全绿（buf lint + buf breaking + openapi validator + 契约漂移检查）
   ↓
冻结（打 tag `contracts-vX.Y.Z`，Lead 在 INTERFACES.md 顶部更新版本与日期）
```

硬规则：

1. **冻结文件中不得擅自改动状态名、错误码、字段名**；改一个字符串即视为破坏性变更。
2. 任何实现方发现契约缺口（如 §6），只能**提提案**，不能"先实现再补契约"。
3. 同一语义只能有一个权威文件；proto 与 OpenAPI 不得各自发明第二套状态名。
4. 变更必须同时更新本 README 的索引与 CHANGELOG。

## 4. 版本演进策略

| 变更类型 | 版本位 | 示例 | 兼容要求 |
|---|---|---|---|
| 加可选字段 / 加 proto 字段号 / 加 OpenAPI 可选属性、新路径 | **minor**（1.1.0） | 给 Ticket 加 `revision` | 旧客户端忽略新字段；服务端不依赖客户端新字段 |
| 加枚举值（末尾追加，不重编号） | **minor** | 新 `Intent` 值 | 客户端必须有 unknown 分支；不得假设枚举封闭 |
| 删除/改字段类型/复用字段号/改枚举数值/改状态名 | **major**（2.0.0） | 状态名更名 | 需双写/双读迁移期，SDK 主版本同步 |
| 文档澄清、注释、示例修正 | **patch**（1.0.1） | 补充错误说明 | 无语义变化 |

具体规则：

- **Protobuf**：字段号只增不复用；删除字段必须 `reserved <号>; reserved "<名>";`。`package agentim.contracts.*.v1` 的主版本仅在 semver major 时升级。
- **OpenAPI**：次版本只做加法；`info.version` 与契约版本对齐；破坏性变更需新增路径版本（如 `/api/v2/...`）或 major。
- **SDK**：SDK 主版本跟随契约主版本；SDK 启动时校验 `x-contract-version` 主版本，不匹配拒绝启动并提示升级（见 docs/04 §6）。
- **弃用**：字段/路径标 `deprecated: true` 后至少保留一个发布周期，移除只允许发生在 major。
- **A2A / MCP 外部版本**：不随本目录版本号演进；由 `docs/03 §8` 的版本协商规则单独管理。

## 5. 校验方式（CI 必须补齐；本机不可执行）

本机无 protoc/buf/OpenAPI validator，**以下命令未在本机运行过**，CI 接入后作为门禁：

```bash
# 1) Proto 编译与 lint（buf 模块根 = contracts/proto）
buf lint contracts/proto
buf breaking contracts/proto --against '.git#branch=main,subdir=contracts/proto'
protoc -I contracts/proto -I "$(buf ls-files --well-known-types)" \
  --descriptor_set_out=/tmp/agentim.pb contracts/proto/*.proto

# 2) OpenAPI 3.1 校验
npx @redocly/cli lint contracts/openapi/orchestrator.openapi.yaml
#   或
npx @stoplight/spectral-cli lint contracts/openapi/orchestrator.openapi.yaml

# 3) 契约漂移检查（建议新增 scripts/check-contracts.mjs，零依赖）
node scripts/check-contracts.mjs   # 对照 INTERFACES.md 的枚举/错误码与 proto/openapi，不一致即 exit 1

# 4) 路由对照（建议新增 tests/contracts.test.mjs）
node tests/contracts.test.mjs      # 启动 demo-server，逐条断言 openapi paths == 实际路由
```

补充说明：

- `buf.yaml` 建议置于 `contracts/proto/`，`lint.use: [DEFAULT]`，并显式 `except: [ENUM_VALUE_PREFIX, ENUM_ZERO_VALUE_SUFFIX]`——因为本契约要求枚举名与 INTERFACES.md 逐字一致（例外项需在评审中说明）。
- 导入路径：proto 之间用同目录相对导入（`import "common.proto";`）；well-known types（`google/protobuf/struct.proto`）由 buf 或 protoc include 路径提供。
- 本机人工自检已覆盖：缩进 2 空格、无 Tab、枚举显式赋值与包内唯一、枚举首值 0、`$ref` 可解析、无重复 YAML 键、所需路径与方法齐全。**人工自检不能替代编译器。**

## 6. 已知缺口与待复核（v1.0）

| # | 项 | 影响 | 处置 |
|---|---|---|---|
| 1 | `CANCELED` 无协议入口 | 8 态之一不可达 | ✅ 已闭环：契约 R3 补记 `fail()/cancel()`，实现与 proto 均已提供 |
| 2 | `maxSeconds` 只定义参数未定义超时行为 | 墙钟超时语义不明 | ✅ 已闭环：明确"超时由外层调度器判定，本层不实现"（契约 §5 注） |
| 3 | `deadline` 默认 30 min 与首次升级 `afterMs[0]` 同刻 | 过期/升级次序歧义 | ✅ 已闭环：契约 §4 固定"先升级、后过期"，实现幂等可测 |
| 4 | demo-server 缺 `POST /api/intent` 及四个流转路由 | OpenAPI 契约领先于实现 | ✅ 已闭环：5 条路由已实现，e2e 覆盖 submit→screen→pay→view→reply 与 NEEDS_SCREENING |
| 5 | demo-server 错误码 `BAD_ACTION` / `internal_error` 不在契约表 | 错误码漂移 | ✅ 已闭环：统一为 `INVALID_ARGUMENT` / `INTERNAL` |
| 6 | MCP `2026-07-28`、A2A `v1.0.0` 版本与字段 | 外部规范未联网核对 | ⚠ 待复核：核对官方原文后回填，标注复核日期 |
| 7 | proto 中 `ErrorCode` 的 0 值占位 | 与"逐字一致"存在设计性偏差 | 已在 §2 声明；读取方必须拒绝 0 值 |
| 8 | OpenAPI 中 `/api/reset` 为演示路由 | 生产不应暴露 | 网关屏蔽；标注 `x-demo-only: true` |
| 9 | `protoc` / `buf` 未在本机执行 | proto 仅人工+脚本自检 | ⚠ CI 必装：`buf lint` + breaking + OpenAPI validator |
| 6 | MCP `2026-07-28`、A2A `v1.0.0` 版本与字段 | 外部规范未联网核对 | 核对官方原文后回填，标注复核日期 |
| 7 | proto 中 `ErrorCode` 的 0 值占位 | 与"逐字一致"存在设计性偏差 | 已在 §2 声明；读取方必须拒绝 0 值 |
| 8 | OpenAPI 中 `/api/reset` 为演示路由 | 生产不应暴露 | 网关屏蔽；标注 `x-demo-only: true` |

## 7. 变更记录（CHANGELOG）

| 版本 | 日期 | 变更 | 审批 |
|---|---|---|---|
| 1.0.0 | 2026-10-21 | 初始冻结：INTERFACES.md v1.0 + 4 个 proto + OpenAPI 1.0 + 协议/SDK 文档；人工自检通过，编译验证待 CI | Lead |
