# IM2A v2.5.0 — MCP 双向桥与 Agent Plugins 2.0 打包

| 项 | 值 |
|---|---|
| 设计对象 | MCP 双向桥（两条腿 + 工具面）与 Agent Plugins 2.0 打包契约 |
| 代码基线 | `E:\GPT\im2a-v2.3.45` · 分支 `release/2.4` · HEAD `5975ebe` · 平台 `2.4.6` / Go `2.3.26` |
| 协议基线（**不变**） | MCP `2026-07-28` · A2A `1.0`/`v1.0.1` · dsh-session-ref `1.0` · ident-code `1.0` · Agent Plugins `1.0`（本文新增 `2.0` 草案，与 1.0 并存） |
| 上游设计 | `AGENT-PLUGINS-2.0-AND-EVOLUTION-ARCH-v2.5.0.md`（分层、R1–R14）· `IM2A-v2.5.0-REALTIME-COLLAB-SESSION.md`（G1–G7、三通道）· `SKILL-RUNTIME-MVP-v2.5.0.md`（四技能工具、L0/L1/L2、G0–G5） |
| 本次交付 | 工具目录 schema（13 工具）· 插件/连接器包格式 · 2026-07-28 兼容缺口表 · allowlist/scope/27 条可执行契约负控 |
| 状态 | **契约可执行 / 运行时不存在**：新增两份 schema + 27 条负控全部通过；桥与打包的运行时代码未改，发布门禁未接线 |

---

## 0. 结论

**事实（可复核）**

1. **现在的桥只有一条腿。** `packages/im2a-http-mcp/server.mjs` 是 MCP **服务端**（外部客户端 → IM2A 工具），没有 MCP **客户端**能力；A2A 是另一条轴，不是这条腿的替代（`server.mjs:26-76,132-242`）。
2. **六个工具已经是既成契约。** 名称/参数被 `tests/workbuddy-connector-mcp.json`、`P0-LIVE-DSH-TOOL-CALL.json`（33 harnesses、plugin 0.1.1）和 `adapters.json:15` 的 `mcp__im2a__im2a_harness_list` 引用；v2.5.0 **不改这六个**。
3. **2026-07-28 的 pin 只有 URL，没有可复核产物。** `packages/protocol/versions.json:20-25` 是 `{protocolVersion, source(url), status, profileSchema}`；A2A 有 `releaseCommit` + `protoBlobSha` + `vendor/a2a/SOURCE.json`，MCP 没有。**因此本仓的"兼容"只能是"对已实测行为的兼容"，不是"对规范全文的兼容"。**
4. **现代结果形状是 DSH 逼出来的。** `P0-REPORT.md:341-398`：`server/discover` 曾返回 `-32601`、`tools/list` 缺 `resultType/ttlMs/cacheScope` 被拒，补齐才通。
5. **skills 与 memory 目前都不是 IM2A 的工具。** 技能是文件（4 个 `SKILL.md`，双份重复）；memory 由外部 `w2m-dsh-plugin` 2.0.8 的 `w2m_memory` 承担，只在技能 frontmatter 里被引用（`skills/memory-sync/SKILL.md:15-18`）。**没装 w2m 的端，这些技能会指名到不存在的工具。**
6. **技能工具的名字已被另一份设计占用。** `SKILL-RUNTIME-MVP-v2.5.0.md:18,199-232` 定义 `skills_list` / `skill_view` / `skill_manage` / `curator`。本文**沿用**这四名与返回形状；先前草案的 `skill_index` / `skill_load` 已废弃——同仓两张技能工具表就是下一次漂移的源头。

**判断**

- **"双向"是两条腿，不是两个服务端。** A 腿（南向）= MCP 服务端；B 腿（北向）= MCP 客户端，今天唯一需要的上游是 memory。两条腿共用**同一个边界点**：身份、scope、trace 只在边界解析一次。
- **工具面按"目录"增长，不按"能力"增长。** 新增 7 个（4 skill + 3 memory），全部可被 scope 过滤；`curator` 只挂给 curator 身份。不新增 `memory_write`、`skill_promote`、`formation_run`（§3.5）。
- **memory 走 B 腿做门面，不建第二个记忆库。** 否则"技能声明 `w2m_memory_read`"永远不可跨端移植（R13）。
- **打包兼容是"投影 + 双 schema"，不是"宽松解析"。** 2.0 文档里出现 1.0 的 `skills: ["skills/x/SKILL.md"]` 必须被拒（负控 P13）。

---

## 1. 证据基线

| 判断 | 证据 | 能证明 | 不能证明 |
|---|---|---|---|
| 桥是 MCP 服务端 | `server.mjs:26-76,132-242` | A 腿存在且可用 | 有 B 腿 |
| 六工具被外端引用 | `workbuddy-connector-mcp.json:1-12`、`P0-LIVE-DSH-TOOL-CALL.json`、`adapters.json:15` | 名字/参数不可随手改 | 其它 5 方客户端要求一致 |
| initialize 回显客户端版本 | `server.mjs:154-166` | 版本协商缺失 | 客户端是否会因此误判能力 |
| tools/list 硬编码公共缓存 | `server.mjs:169-180` | 一旦按 scope 过滤就泄漏工具名 | 客户端是否遵守 cacheScope |
| 接受 JSON-RPC 批量 | `server.mjs:273-287` | 批量被当数组处理 | 2026-07-28 是否仍允许 |
| 会话与游标进程级 | `server.mjs:18,23,222-226` | 多客户端共游标（G1） | 生产网关是否同样 |
| SSE 只有 keep-alive | `server.mjs:250-261` | 无事件投递、无 `Last-Event-ID`（G2） | 浏览器端是否必需 |
| README 与代码矛盾 | `im2a-http-mcp/README.md:16-17` 称 send/recv/history 仍 stdio-only，而 `server.mjs:204-235` 已实现 | 文档漂移本身是发布风险 | 哪边是"对"的 |
| MCP pin 只有 URL | `versions.json:20-25` vs `vendor/a2a/SOURCE.json` | 离线不可复核 | 上游是否已变 |
| 明文回环与 profile 冲突 | `mcp-profile.schema.json:39-45` vs `im2a-http-mcp/README.md:27-30` | 二选一未决（G6） | 生产是否 HTTPS |
| 插件清单无 `profileVersion` | `agent-plugin.schema.json:8`、`source/plugin.json:1-32` | 文档不自描述 | 第三方读者如何猜版本 |
| 生成物只有骨架 | `generate.mjs:73-79` | 12 端不是 12 个可安装产物 | 何时生成真实文件 |
| 编码腐坏可过校验 | `adapters.json:161`（doubao `displayName` 乱码） | 现门禁抓不到 NFC 腐坏 | 影响面 |
| 共享静态令牌 = 全体用户 | `docs/privacy/IM2A-IDENTITY-PRIVACY-REVIEW-v2.3.45.md:23,123`（F-02） | scope 必须由服务端解析 | 生产是否已注入身份 |
| 技能工具命名已被占用 | `SKILL-RUNTIME-MVP-v2.5.0.md:18,199-232` | 只能沿用，不能另起 | 后端实现进度 |

---

## 2. 双向桥：两条腿与边界

### 2.1 定义

| 腿 | 方向 | 发起方 | 协议 | 今天 | v2.5.0 交付 | 明确不做 |
|---|---|---|---|---|---|---|
| **A 南向** | 外部客户端 → IM2A | harness/浏览器 | MCP server（stdio / streamable-http） | 6 工具、单会话、无 scope | 13 工具、按 scope 过滤、per-caller 游标、typed error | 不新增 MCP 方法、不改六工具形状 |
| **B 北向** | IM2A → 上游 MCP | IM2A 运行时 | MCP client | **不存在** | memory 门面（首个上游 `w2m_memory`）、scope 收窄、trace 透传 | 不做通用 MCP 代理市场、不转发凭据 |

**为什么今天只需一条 B 腿上上游**：12 端里只有 memory 是"跨端共享且已被技能文本引用"的能力；其余（发消息、会话引用、调度）都在 IM2A 手里。**先接一个上游把契约跑通，再谈代理目录**——否则就是 R13 的"能力一多就加核心工具"。

### 2.2 边界规则（可机器检查）

| ID | 规则 | 违反后果 |
|---|---|---|
| **B1** | 身份只在边界解析一次：`token → principalUserId + scope[]`；工具参数、`_meta`、自报 `user_id` **一律不参与授权** | 伪造身份；F-02 复发 |
| **B2** | trace 只在边界铸一次：`{traceId, agentAddress, delegationId}`；两腿都透传，缺失即拒绝有副作用的调用（INV-2） | 无法一次查询定位产出者 |
| **B3** | scope 只能收窄：`child.scope ⊆ parent.scope`、`child.expiresAt ≤ parent.expiresAt`；上游 grant 取交集 | 委托放大权限（INV-1） |
| **B4** | 每个工具必须声明 scope + 副作用类；`tools/list` 是同一目录的**过滤视图**，不是第二份清单 | 目录漂移 |
| **B5** | 失败必须是值：`isError:true` + `errorCode` + `retryable`；不用 JSON-RPC 错误表达工具失败，也不假成功 | agent 无法恢复 |
| **B6** | 上游不可用 → `UPSTREAM_UNAVAILABLE`；超时 → `UNVERIFIED`；部分可用 → `degraded:{reason}` 且**不进缓存** | 静默降级 |

### 2.3 工具面：13 个，且封闭

`docs/contracts/fixtures/mcp-bridge-tool-catalog.valid.json` 是唯一清单；`scripts/check-bridge-contracts.mjs` 做**集合相等 + 名称→scope 语义表**断言（多一个、少一个、scope 错挂都失败）。

| 组 | 工具 | 何时用（描述首句） | scope | 副作用 | 幂等 |
|---|---|---|---|---|---|
| 既有 6 | `im2a_harness_list` | 列已注册 harness 与当前选择 | `bus:read` | read | pure |
| | `im2a_send` | 向总线发一条消息 | `bus:send` | message-send | keyed |
| | `im2a_recv` | 按**调用方自己的**游标取新消息 | `bus:read` | read | pure |
| | `im2a_presence` | 看在线的 harness 会话 | `bus:read` | read | pure |
| | `im2a_whoami` | 看本桥解析出的身份与 scope | `bus:read` | read | pure |
| | `im2a_history` | 从 seq 重放历史 | `bus:read` | read | pure |
| 技能 4 | `skills_list` | 先列 L0 索引（无正文） | `skill:read` | read | pure |
| | `skill_view` | L1 正文+文件清单 / L2 单个 reference | `skill:read` | read | pure |
| | `skill_manage` | **提案** create/patch/archive/write_file/remove_file | `skill:propose` | proposal-write | keyed |
| | `curator` | status/scan/propose/stage/report（只挂 curator 身份） | `skill:curate` | proposal-write | keyed |
| 记忆 3 | `memory_search` | 查已记录事实，避免重复推导 | `memory:read` | read | pure |
| | `memory_get` | 按 ref 取一条事实及出处 | `memory:read` | read | pure |
| | `memory_propose` | **提案**一条持久事实（不写 active） | `memory:propose` | proposal-write | keyed |

---

## 3. 交付①：skill / memory 工具 schema

### 3.1 契约文件

| 文件 | 作用 | 状态 |
|---|---|---|
| `docs/contracts/mcp-bridge-tool-catalog-2.0.schema.json` | 工具目录契约（13 工具的参数/返回/错误码/scope） | draft，已校验 |
| `docs/contracts/fixtures/mcp-bridge-tool-catalog.valid.json` | 唯一工具清单实例 | 正例通过 |
| `docs/contracts/fixtures/mcp-bridge-tool-catalog.negatives.json` | 14 条变异负控 + 1 正控 | 14/14 被拒 |
| `scripts/check-bridge-contracts.mjs` | 校验 + 集合相等 + scope 语义表 | `npm run bridge:contracts` |

`packages/protocol/schemas/mcp-profile.schema.json` 是 **1.0 的配置 schema**，本次不动；新 schema 放 `docs/contracts/` 并标 draft，避免在 ADR 之前改 L0。

### 3.2 参数与返回（字段级；完整见 fixture 与 SKILL-RUNTIME-MVP §4）

| 工具 | 关键入参 | 返回主干 | 错误码（本工具子集） |
|---|---|---|---|
| `skills_list` | `query`、`limit`(1–100, 默认 50)、`cursor` | `items[]{id,description,version,packageSha,tags}`、`total`、`nextCursor`、`indexEtag`、`delta` | `NOT_AUTHORIZED` `INTERNAL` |
| `skill_view` | `id`、`path?`、`offset`、`limit`(≤2000) | L1：`body`+`files[]`+`provenance`+`updateAvailable`；L2：`text`+`lines{offset,limit,total}`；两者都带 `packageSha`、`etag`、`disclosure` | `SKILL_NOT_FOUND` `SKILL_VERSION_GONE` `PATH_INVALID` `PATH_NOT_FOUND` `BINARY_FILE` |
| `skill_manage` | `action`、`id`、`proposalId?`、`baseSha?`、`description?`、`version?`、`body?`、`path?`、`content?`、`evidence{reason}` | `proposalId`、`status:"proposed"`、`candidateSha`、`diff`、`gates[]`、`next`、`traceId` | `SKILL_EXISTS` `SKILL_READ_ONLY` `BASE_MISMATCH` `PROPOSAL_*` `INVALID_ID` `DESCRIPTION_TOO_LONG` `BODY_TOO_LARGE` `PATH_INVALID` `FILE_TOO_LARGE` `NFC_REQUIRED` `PROPOSAL_LIMIT` |
| `curator` | `action`(status\|scan\|propose\|stage\|report)、`proposalId?`、`skillId?`、`evidence?`、`evalRun?`、`dryRun?` | `counts`/`candidates`/`runId`/`packageSha`/`gates[]`/`eval`/`markdown` | `FORBIDDEN` `PROPOSAL_NOT_FOUND` `BASE_MISMATCH` `CANDIDATE_DRIFT` `STAGE_SCHEMA` `STAGE_CONTENT` |
| `memory_search` | `query`、`scope`(local\|project\|fleet)、`topics[]`、`limit`、`includeStale` | `facts[]{ref,statement,topics,provenance,confidence,stale}`、`total`、`degraded?` | `NOT_AUTHORIZED` `UPSTREAM_UNAVAILABLE` |
| `memory_get` | `ref`、`includeHistory` | `fact`、`history[]` | `STALE_REFERENCE` |
| `memory_propose` | `statement`、`topics[]`、`scope`、`source`、`supersedes?`、`idempotencyKey`、`rationale?` | `proposalId`、`state:"pending-approval"`、`dedupe:created\|replayed`、`conflicts[]` | `MEMORY_CONFLICT` `INVALID_ARGUMENT` `RATE_LIMITED` |

**一套错误码，UPPER_SNAKE。** 桥级 6 个（`NOT_AUTHORIZED` `INVALID_ARGUMENT` `FORBIDDEN` `RATE_LIMITED` `UPSTREAM_UNAVAILABLE` `UNVERIFIED` `INTERNAL`）+ 技能域沿用 MVP §4.3/§4.2 的码 + 记忆域 2 个（`MEMORY_CONFLICT` `STALE_REFERENCE`）。桥**不改写**子系统码，否则同一个失败有两个名字。
`degraded` **不是**错误码：它是结果状态（`degraded:{reason}`），混用会把"部分可用"记成失败。

### 3.3 渐进披露（三级，沿用 MVP，不新增级别）

```text
L0 skills_list   → id + ≤60 字符描述；100 技能 ≈ 2k tokens（上下文是预算）
L1 skill_view(id)          → SKILL.md 正文（≤32 KiB）+ 文件清单（含 title）
L2 skill_view(id, path)    → 单文件一段（≤64 KiB，offset/limit，返回 lines.total）
```

三条约束（MVP §5，本文只做交叉引用，不复制）：不静默截断；每次 L1/L2 都回 `packageSha`（L2 加 `fileSha`）并记入 usage/ledger；**session pin**——一次任务中途不换指令，新版本通过 `updateAvailable`/`delta` 告知，旧包被清理返回 `SKILL_VERSION_GONE`。

### 3.4 不引入 resources（这是一个决定）

技能 L2 已经由 `skill_view(id, path)` 承担，**再加 `resources` 就是同一能力的第二套入口**（R13）。因此：

- v2.5.0 **不声明** `resources` / `prompts` 能力；`server/discover` 的 `capabilities` 只有 `tools`。
- memory 的 `memory://fact/{ref}` 也**不做**：`memory_get(ref)` 已是稳定引用，`resources` 只会多一层缓存失效问题。
- 若将来要加，判据是：**至少 2 个已验证 target 明确要求 URI 式读取**；否则不加。

### 3.5 明确不加的工具

| 不加 | 理由 | 替代 |
|---|---|---|
| `memory_write` / `memory_update` | 直接写 active = R1 自改 | `memory_propose` + L6 审批 |
| `memory_promote` / `skill_promote` / `skill_rollback` | 晋升是治理动作；MVP §4.5 明确只走 operator API（`/v1/skills/.../promote`） | operator API + `approvalId` |
| `skill_install` / `skill_publish` | 发布面属于打包管线与 L6 | 生成器 + allowlist |
| `formation_run` | 编排需独立契约（`formations[]` 已在 2.0 声明） | 后续 ADR |
| resources/prompts | 与 `skill_view(path)`/`memory_get` 重复 | 见 §3.4 |

### 3.6 契约级负控（已可执行）

`T1–T14`（工具目录）+ `P1–P13`（插件清单）= **27 条**，均为"变异正例的一条字段"，负控不会与真实面漂移。代表项：

| ID | 变异 | 期望 | 为什么 |
|---|---|---|---|
| T1 | `scope:["memory:write"]` | `not in enum` | 写权限不存在，只有提案 |
| T2 | `name:"skill-load"` | `pattern mismatch` | harness 工具 id 是 `mcp__im2a__<name>`，连字符会破 |
| T5 | `inputSchema.type:"string"` | `expected const` | 标量 schema 等于无法传命名参数 |
| T8 | 删 `profileVersion` | `missing required profileVersion` | 文档不自描述 = 靠路径猜版本 |
| T11 | `cacheScopePolicy:"public-always"` | `not in enum` | 按调用方过滤的工具表不可公共缓存 |
| T13 | 错误码写 `not-authorized` | `not in enum` | 只允许 UPPER_SNAKE 一套 |
| T14 | `name:"curator2"` | `pattern mismatch` | 无前缀形态只允许 `curator` |
| P13 | `skills:["skills/x/SKILL.md"]` | `expected object` | 2.0 只认内容寻址；1.0 形状由投影产生 |

---

## 4. 交付②：连接器 / 插件包格式

### 4.1 单一源 + 派生

```text
packages/agent-plugins/
├── source/                          # 唯一真相（手写）
│   ├── plugin.json                  # 2.0，自描述
│   ├── mcp.json                     # server 描述，env 只许 ${占位}
│   ├── tool-catalog.json            # 新增：= 交付① 的实例
│   └── skills/<id>/SKILL.md         # 内容寻址的源（sha256 进 plugin.json）
├── generator/{targets.json,src/*}   # 12 target → 确定性派生
└── dist/<target>/                   # 生成物（禁止手改）
```

判据：`dist` 是派生物，`generate:check` 用 `sourceHash` 判陈旧（`generate.mjs:58-66`）；v2.5.0 追加**两次 generate 必须字节相同**（产物内无时间戳）。

### 4.2 `plugin.json` 2.0 字段

| 字段 | 必填 | 语义 / 约束 |
|---|---|---|
| `profileVersion` | 是 | `const "2.0"`——新增的自描述位 |
| `name` / `version` / `description` / `license` | 前三个是 | 与 1.0 语义冻结 |
| `skills[]` | 是 | `{id, ref, sha256, profileVersion?, tags?}`；**只接受对象**（P13）；`sha256` 即 MVP 的 `packageSha` |
| `mcp` | 是 | `serverName` `transport` `protocolVersion:"2026-07-28"`；stdio 需 `command`；streamable-http 需 `url`+`tlsRequired` |
| `mcp.allowInsecureLoopback` | 否 | 只有显式 `true` 才允许 `http://127.0.0.1`/`[::1]`（解 G6），否则 `url` 必须 `https://` |
| `toolCatalogRef` | 否 | 本插件携带的工具目录路径（相对路径） |
| `targets[]` | 是 | 12 个 harness id，集合相等、唯一 |
| `agents[]` | 否 | `{id, role, scope[], tools[], maxDepth≤2, budget, outputContract}`；**prompt 不进协议** |
| `formations[]` | 否 | `{id,version,roles,edges[],entry,termination{maxDepth≤2,maxFanout≤5,maxRounds≤3},failurePolicy}` |
| `learning` | 否 | `{evalRefs[],curatorPolicy:"propose-only",promotionGates[],retentionDays}` |
| `extensions[]` | 否 | A2A 扩展 URI，不是 harness 名 |

**1.0 兼容靠投影**：`project(2.0)` 删 `profileVersion`、把 `skills[]` 降回路径字符串、删 `agents/formations/learning/toolCatalogRef` → 必须通过 1.0 schema。**不做"2.0 schema 也接受 1.0 形状"**：那会让"哪版"重新变成猜测（P13 就是这条的负控）。

### 4.3 `dist/<target>/` 与 `bundle.meta.json` 2.0

| target format | 产物（除 `bundle.meta.json` 与 `SHA256SUMS`） |
|---|---|
| `bundle`（dsh） | `cordis.patch.yml`（必须 `insert:`，见 `validate.mjs:77-80`）+ `skills/` |
| `bundle`（openclaw） | `openclaw.plugin.json` + `skills/` |
| `agent-plugins`（qwen/mimo） | `agent-plugin.json`（1.0 投影 + `profileVersion`）+ `skills/` + `mcp.json` |
| `mcp-config`（codex/claude/gemini/muse/vscode/copilot/workbuddy） | `mcp.json`（`mcpServers.im2a`，env 只许 `${...}`） |
| `remote-mcp`（doubao） | `mcp.remote.json`（`https` + header 占位） |

`bundle.meta.json` 2.0（**无时间戳**，可复算）：

```json
{ "schemaVersion": "2.0", "profileVersion": "2.0",
  "plugin": { "name": "im2a-fleet-control", "version": "0.2.0" },
  "source": { "sourceHash": "<64hex>", "toolCatalogHash": "<64hex>", "generatorVersion": "0.2.0" },
  "target": { "id": "dsh", "format": "bundle", "install": { "method": "bundle" } },
  "artifacts": [{ "path": "cordis.patch.yml", "sha256": "<64hex>" }],
  "projection": { "floor": "1.0", "ok": true } }
```

**两轴分离**：`target.format`（怎么装）与 `adapter.status`（`planned|observed|verified|blocked`，`contracts.ts:25`）不可互相顶替；`dist` 生成成功 ≠ 该端已验证。

### 4.4 连接器包（zip）——只加自描述与哈希

```text
twinsearth-im2a-workbuddy-connector-2.0.0.zip
├── connector-meta.json     # + profileVersion, capabilities[], toolCatalogHash, packageHash
├── mcp.json                # 唯一 server；stdio；command=${IM2A_MCP_BIN}
├── token-schema.json       # IM2A_HARNESS_TOKEN 必须 type:"password"
├── icon.svg
├── skills/im2a-connector/SKILL.md
├── README.md
└── SHA256SUMS              # 不含自身
```

沿用 `validate-workbuddy-connector.mjs` 既有断言（单 server、`${IM2A_MCP_BIN}`、占位符、password 字段、无凭据字样），**新增三条**：`profileVersion` 必填；`capabilities[] ⊆ 工具目录 scope 集`；`packageHash == SHA256SUMS 重算值`。

### 4.5 命名与版本

| 对象 | 规则 | 例子 |
|---|---|---|
| 插件包版本 | semver，与平台版本**解耦** | 插件 `0.2.0`，平台 `2.5.0` |
| `profileVersion` | `1.0`/`2.0`，弃用窗 = 2 个 minor | 2.0 引入，3.x 前保留 1.0 解析器 |
| 产物名 | `twinsearth-im2a-<target>-<pluginVer>.<tgz\|zip>` | `twinsearth-im2a-dsh-0.2.0.tgz` |
| 工具目录版本 | 随工具集合变化 | 新增工具 = minor；改参数 = major |

---

## 5. 交付③：现有 MCP 2026-07-28 兼容

### 5.1 已成立、**不得回退**

| 项 | 证据 | 回退即事故 |
|---|---|---|
| `server/discover` 返回 `supportedVersions/capabilities/instructions/ttlMs/cacheScope` | `server.mjs:134-153` | DSH 直接拒绝 |
| `tools/list` 带 `resultType/ttlMs/cacheScope` | `server.mjs:169-180` | 同上（`P0-REPORT.md:375-381`） |
| 工具失败走 `isError` 结果而非 JSON-RPC 错误 | `server.mjs:236-239` | agent 无法区分"工具失败"与"连接坏了" |
| 未知工具返回 `isError` 文本 | `server.mjs:236` | 崩溃式重试 |
| 通知类请求 202 无体 | `server.mjs:282-285` | 批量调用器挂起 |
| stdio 路径与 `IM2A_HARNESS_TOKEN` 显式映射 | `cordis.patch.yml:10-22` | DSH 侧凭据被沙箱清空 |

### 5.2 兼容缺口表

| ID | 项 | 现状 | 证据 | v2.5.0 动作 | 门禁 |
|---|---|---|---|---|---|
| C1 | `initialize` 协商 | ⚠️ **回显客户端值** | `server.mjs:159` | 恒返回 `2026-07-28`；请求不同版本时加 `_meta.io.im2a/negotiation:"forced"`，不假装兼容 | N15 |
| C2 | 能力声明 | ⚠️ 只有 `tools` | `server.mjs:141,161` | 保持只有 `tools`（§3.4 不引入 resources/prompts） | A8 |
| C3 | `tools/list` | ⚠️ 全量 + `public` | `server.mjs:169-180` | 按 scope 过滤 → `cacheScope` 必须 `private`；补 `annotations`（由副作用推导） | N1 |
| C4 | `tools/call` 校验与错误码 | ⛔ 无 `errorCode`、不校验入参 | `server.mjs:181-242` | 按工具 `inputSchema` 校验 → `INVALID_ARGUMENT`+`field` | T4 |
| C5 | 会话管理 | ⛔ 进程级 id、无 DELETE、不校验未知会话 | `server.mjs:18,82` | per-connection 会话；未知会话 404；DELETE 终止 | N18 |
| C6 | SSE 可恢复性 | ⛔ 只有 keep-alive、无事件、无 `id:` | `server.mjs:250-261` | `mcp.resumability:"none"`；**不声明** `Last-Event-ID`（G2） | N19 |
| C7 | 批量请求 | ⚠️ 接受数组 | `server.mjs:273-287` | pin 产物确认前 feature-flag 保留；不许时 `-32600` 整批拒 | N17 |
| C8 | `MCP-Protocol-Version` 头 | ⛔ 不读 | — | 读取校验；不支持 → `400`，不静默降级 | N15 |
| C9 | `ping` | ✅ 形状未核 | `server.mjs:168` | 保持；记入"未核验"（§5.5） | — |
| C10 | JSON-RPC 错误码 | ⚠️ 只有 `-32700/-32601` | `server.mjs:88-90,241` | 补 `-32602`（参数结构非法）；工具语义失败仍走结果 | N16 |
| C11 | 入站鉴权 | ⛔ 无鉴权；令牌共享（F-02） | `server.mjs` 全文无 auth | 非回环必须 Bearer；回环需 `allowInsecureLoopback`；身份由 token 解析 | N2 |
| C12 | resources/prompts | ⛔ 未实现 | — | **不引入**（§3.4）；L2 由 `skill_view(path)` 承担 | A8 |

### 5.3 结果信封（13 个工具统一）

```json
{ "resultType": "complete",
  "content": [{ "type": "text", "text": "{...JSON...}" }],
  "isError": true,
  "_meta": { "io.im2a/trace": { "traceId": "tr-…", "agentAddress": "…", "delegationId": "del-…" } } }
```

- 数据一律是 `content[0].text` 里的 **JSON 文本**（既有做法；只解析文本的客户端不会坏）。
- 失败时 text 内是 `{errorCode, message, retryable, field?}`，同时 `isError:true`。
- `_meta` 用 IM2A 命名空间；不读 `_meta` 的客户端降级为"只有 JSON"，**不丢信息**。
- **不用 `structuredContent`/`outputSchema` 作契约**：见 §5.5。

### 5.4 pin 加固

| 动作 | 内容 | 为什么 |
|---|---|---|
| 新建 `packages/protocol/vendor/mcp/SOURCE.json` | `{specUrl, retrievedAt, artifact:"schema.json", sha256}` | 让 MCP 兼容可离线复核，与 A2A 同级 |
| `versions.json` mcp 段加 `specSha256` | 未取到就写 `null` + `status:"pinned-unverified"` | 诚实：URL ≠ 可复核 |
| 声明纪律 | 任何 "supports X" 必须能指到 §5.2 一行 + 一次本地实测 | 防 README 式漂移 |

### 5.5 未核验、因此**不声明**

`structuredContent`/`outputSchema`（很可能存在，但本仓无该版 schema 产物）；JSON-RPC 批量是否被移除；`ping` 结果形状；各客户端对 `ttlMs/cacheScope` 的实际遵守程度。

---

## 6. 交付④：allowlist / scope / 负控

### 6.1 scope 词表（闭集，7 个）

| scope | 覆盖工具 | 说明 |
|---|---|---|
| `bus:read` | `im2a_harness_list` `im2a_presence` `im2a_recv` `im2a_whoami` `im2a_history` | 读总线；`recv` 只推进调用方游标 |
| `bus:send` | `im2a_send` | 唯一有对外副作用的总线权限 |
| `skill:read` | `skills_list` `skill_view` | 读技能池 |
| `skill:propose` | `skill_manage` | 只写提案 |
| `skill:curate` | `curator` | **只挂 curator 身份**；普通 agent 的 `tools/list` 里没有它 |
| `memory:read` | `memory_search` `memory_get` | 读记忆 |
| `memory:propose` | `memory_propose` | 只写提案 |

保留但**不是工具**：`memory:promote`、`skill:promote`、`skill:rollback`、`formation:run`。

执行四点（缺一不可）：
1. `tools/list`：无 scope 的工具**不出现**（缺席优于拒绝——不暴露能力名）；
2. `tools/call`：直接点名也拒，返回 `NOT_AUTHORIZED`；
3. 上游：`grant.scope = caller.scope ∩ upstream.scope`；
4. 审计：每次调用记 `{traceId, principal, tool, scope, errorCode?}`。

三条硬规则：scope 只来自 token 解析（B1）；`memory_search.scope` 只能收窄——请求 `fleet` 而 grant 只有 `local` → `NOT_AUTHORIZED`，**不得**自动降级后假装满足；子 grant 满足 `⊆` 且 `expiry ≤`（B3）。

### 6.2 allowlist v2

三处硬伤：版本字符串在 6 处硬编码（`package.json:3`、`version-matrix.json:6,7`、`release-allowlist.json:3`、`release-manifest.json:3,5`、`validate.mjs:40`、`p0.test.mjs:13`）；产物只查"该有有没有"、**不查"多没多"**（`check-release-allowlist.mjs:40-44`）；`scripts/`、`tests/` 整目录豁免凭据扫描（`:30`）。

| 改动 | 内容 | 负控 |
|---|---|---|
| 单一版本源 | 只从 `version-matrix.json.platform.version` 读；其余 5 处做**相等断言** | N22 |
| 产物表对象化 | `allowedArtifacts:[{name,sha256,kind}]`；目录里出现未列出文件即 FAIL | N20 |
| 摘要校验 | 每个产物重算 `sha256` 比对 | N21 |
| 扫描豁免收窄 | 改为 `allowedSyntheticCredentialFiles[]`，只许固定假值 | N23 |
| v2.5.0 新产物 | 12 个 target 产物 + connector zip + SBOM + `SHA256SUMS` + `RELEASE-NOTES.md` | — |
| 既有禁止项 | `.tmp/`、`node_modules/`、`.env`、`.pem`、`.key` | — |

### 6.3 负控矩阵

**契约级 27 条今天可跑**（T1–T14、P1–P13）。**运行时/门禁级 24 条**（N1–N24）见下表，v2.5.0 运行时不存在，先落 `tests/bridge-negative.test.mjs` 骨架，不得先用文档"声明通过"。

| ID | 面 | 动作 | 期望 | 为什么 |
|---|---|---|---|---|
| N1 | scope | `bus:read` 主体列工具 | `skills_*`/`memory_*`/`im2a_send`/`curator` **不出现**；`cacheScope:"private"` | 过滤后的表不可公共缓存 |
| N2 | 身份 | 参数自报 `user_id` 与 token 主体不一致 | `NOT_AUTHORIZED`，不采用自报值 | F-02 |
| N3 | curator | 普通 agent 调 `curator(action:"stage")` | 工具不存在；即使误授 scope 也 `FORBIDDEN` | MVP §4.4 挂载范围 |
| N4 | 会话 | A、B 并发 `im2a_recv` | 各自游标独立（G1） | 全局游标永久丢消息 |
| N5 | 委托 | 子 grant scope ⊄ 父 | 签发即拒，不建会话 | INV-1 |
| N6 | 工具面 | 申请 `skill:promote`/`memory:promote` | 工具与 scope 都不存在 | 晋升只走 operator API |
| N7 | skill | `skill_view(id, "../../etc/passwd")`、绝对路径、`%2e%2e`、反斜杠、symlink | `PATH_INVALID`/`PATH_NOT_FOUND`，包外零读取 | MVP T02 |
| N8 | skill | `skill_manage(patch)` 的 `baseSha` ≠ 当前 active | `BASE_MISMATCH`，`active.json` 字节不变 | MVP T03 |
| N9 | skill | 对 bundled 技能 `skill_manage` | `SKILL_READ_ONLY` | MVP §4.3 |
| N10 | skill | SKILL.md 声明 `env`/`permissions`/`tools` | `STAGE_CONTENT`（导入 1.0 时剥离并记 `droppedFields`） | MVP G1；R5 |
| N11 | memory | `statement` 含 `Bearer <token>` 形状 | `INVALID_ARGUMENT`（credential-like），不落库 | ADR 0003 |
| N12 | memory | 同 `idempotencyKey` 两次 | 1 条提案；第二次 `dedupe:"replayed"` 同 `proposalId` | 重放要收回首次结果 |
| N13 | memory | 与 active 事实冲突 | 进 `conflicts[]`，不静默覆盖，状态仍 `pending-approval` | memory-sync 边界 |
| N14 | memory | 读已废止 ref 且 `includeHistory:false` | `STALE_REFERENCE` | 过期引用不得当有效 |
| N15 | MCP | `initialize` 请求 `2024-11-05`；`MCP-Protocol-Version` 不支持 | 回 `2026-07-28` + `negotiation:"forced"`；头不支持 → 400 | C1/C8 |
| N16 | MCP | `tools/call` 未知工具 | 结果 `isError:true`，**非** `-32601` | C10 |
| N17 | MCP | 批量数组（若不许） | `-32600` 整批拒，不半处理 | 半处理产生"部分成功"错觉 |
| N18 | MCP | 未知 `Mcp-Session-Id` | 404；不得静默新开会话 | 会话不可伪造 |
| N19 | MCP | 带 `Last-Event-ID` 重连 | 不声称可恢复；结果 `resyncRequired:true` | 静默空洞 = 丢消息 |
| N20 | allowlist | 产物目录出现未列出文件 | FAIL | 现在只查缺不查多 |
| N21 | allowlist | 产物摘要不符 | FAIL | 产物可替换 |
| N22 | allowlist | 6 处版本字符串任一不一致 | FAIL | 单一版本源 |
| N23 | allowlist | `tests/` 出现真实令牌形状 | FAIL（现在整目录豁免） | 豁免面过宽 |
| N24 | 打包 | 文本非 NFC（doubao 型腐坏）；2.0 缺 `profileVersion`；投影后过不了 1.0 | FAIL，往返必须相等 | `adapters.json:161`、P1、A2 |

### 6.4 门禁映射

| 命令 | 覆盖 | 现状 |
|---|---|---|
| `node scripts/validate.mjs` | 版本/12 端集合/schema/dist 非陈旧 | 绿（本次未改这些断言） |
| `node --test tests/p0.test.mjs` | P0 + 篡改不可自愈 | 绿 |
| `node scripts/check-release-allowlist.mjs` | allowlist/版本/凭据 | 绿 |
| `node scripts/check-bridge-contracts.mjs`（`npm run bridge:contracts`） | 2 契约 + 27 负控 + 名称→scope 表 | **本次新增，绿** |
| `tests/bridge-negative.test.mjs` | N1–N19 | 未实现（无运行时） |

CI 接线（一处）：`.github/workflows/ci.yml` 在 `Release allow-list and version check` 后加 `- name: Bridge contracts` / `run: node scripts/check-bridge-contracts.mjs`。

---

## 7. 未决决策（需签字）

| # | 决策 | 建议 | 影响面 |
|---|---|---|---|
| D1 | memory 门面 vs 自建库 | **门面**（B 腿首个上游 = `w2m_memory`） | 决定 B 腿是否存在 |
| D2 | 明文回环 | 允许，但必须 `allowInsecureLoopback:true` + 仅 `127.0.0.1`/`[::1]` | 解 G6；已写成 schema + 正控 P-V1 |
| D3 | 批量请求 | feature-flag 保留 + 记录；pin 产物到手后二选一 | C7 |
| D4 | resources | **不引入**（§3.4）；技能 L2 已由 `skill_view(path)` 覆盖 | C2/C12 |
| D5 | 技能池真相目录 | 合并为 `skills/` 单一真相，`source/skills/` 降为生成物或删除 | MVP §2 的前置条件 |
| D6 | `toolCatalogHash` 定义 | 规范化 JSON（键排序、NFC、无空白）取 sha256 | 打包可复算 |
| D7 | `curator` 工具名 | 动代码前改为 `skill_curator` 更符合 verb_noun；不改则记录"它是挂载点不是能力" | 与 MVP 的命名一致性 |

---

## 8. 验收

| ID | 指标 | 阈值 |
|---|---|---|
| A1 | 工具目录：13/13 有 scope + 副作用 + 错误码 + 出入参，名称→scope 表一致 | 100% |
| A2 | 2.0 → 1.0 投影 | 正例 100%；≥6 条投影负例被拒 |
| A3 | 契约负控 | 27/27 被拒（今天 27/27） |
| A4 | scope 泄漏 | 7 种 scope 组合下 `tools/list` 泄漏 0 |
| A5 | 内容寻址 | `packageSha`/`baseSha` 不符时正文 0 字节或 `BASE_MISMATCH`，100/100 |
| A6 | 提案幂等 | 同 key 两次 → 1 条提案，100/100 |
| A7 | 打包确定性 | 连续两次 generate 字节相同；产物内时间戳 0 |
| A8 | 兼容诚实性 | C1–C12 每行有状态+证据；0 处"支持"无本地实测 |
| A9 | 会话隔离 | 双客户端游标互不影响 100/100（G1） |
| A10 | allowlist | 多产物/摘要不符/版本漂移三类负控各 1 条，全部 FAIL |
| A11 | 编码 | 非 NFC 0 处（当前 doubao 1 处必须修） |
| A12 | 12 target 覆盖 | 12/12 产物带 `sourceHash`+`toolCatalogHash`；0 处由骨架推导"已支持" |

---

## 9. 本次产物与运行证据

```text
docs/design/IM2A-v2.5.0-MCP-BRIDGE-AND-PLUGINS2-PACKAGING.md   本文
docs/contracts/mcp-bridge-tool-catalog-2.0.schema.json          交付①（draft）
docs/contracts/agent-plugin-2.0.schema.json                     交付②（draft）
docs/contracts/fixtures/mcp-bridge-tool-catalog.valid.json      13 工具唯一清单
docs/contracts/fixtures/mcp-bridge-tool-catalog.negatives.json  T1–T14 + T-V1
docs/contracts/fixtures/agent-plugin-2.0.valid.json             2.0 正例
docs/contracts/fixtures/agent-plugin-2.0.negatives.json         P1–P13 + P-V1
scripts/check-bridge-contracts.mjs                              npm run bridge:contracts
```

运行证据（2026-10-30）：`{"ok":true}`；工具目录 `14/14`、插件清单 `13/13`、正控 `2/2`；既有 `validate.mjs` / `p0.test.mjs` / `check-release-allowlist.mjs` 保持绿（未触碰版本、12 端集合、分布式断言）。

## 附录 A · 相关文件

```text
packages/im2a-http-mcp/server.mjs                    A 腿实现（C1–C12 落点）
packages/im2a-dsh-plugin/cordis.patch.yml            stdio 客户端接线（insert 语义）
packages/agent-plugins/{source,generator,dist}       打包源/生成器/产物
packages/im2a-workbuddy-connector/*                  connector 包既有形状
packages/protocol/{versions.json,schemas,fixtures}   L0 契约（1.0 不动）
scripts/{validate,schema-validator,check-release-allowlist,check-bridge-contracts}.mjs
docs/design/SKILL-RUNTIME-MVP-v2.5.0.md              四技能工具、L0/L1/L2、G0–G5（命名来源）
docs/design/AGENT-PLUGINS-2.0-AND-EVOLUTION-ARCH-v2.5.0.md   R1–R14 的上游
docs/design/IM2A-v2.5.0-REALTIME-COLLAB-SESSION.md           G1–G7 与三通道
```
