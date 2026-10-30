# ADR 0006：实时会话语义（D/P/C 三通道、恢复游标、接管、背压）

| 项 | 值 |
|---|---|
| 状态 | **Accepted** — 语义冻结（2026-10-30）；**运行时未就绪**（见 §5 差异登记 G1–G10） |
| 补充 | `docs/design/IM2A-v2.5.0-REALTIME-COLLAB-SESSION.md`（设计说明）；本 ADR 只记“选了哪个”和“现状差在哪”，不重复论证 |
| 代码基线 | `E:\GPT\im2a-v2.3.45` · platform `2.4.6` / Go `2.3.26` |
| 协议基线（冻结，不新增） | A2A `1.0`（`v1.0.1`）· MCP `2026-07-28` · agent-plugins `1.0` · `dsh-session-ref 1.0` · `ident-code 1.0` |
| 契约产物 | `packages/protocol/schemas/realtime-session-profile.schema.json`（常量）· `realtime-message.schema.json`（消息形状）· `scripts/validate-realtime.mjs`（两者耦合校验） |

---

## 1. 上下文：实测事实（不是推断）

| # | 事实 | 位置 | 后果 |
|---|---|---|---|
| F1 | 单条文本上限 8000 字节；内存历史 500 条；并发会话 200；每订阅者缓冲 128 | `im2agent/internal/harness/bus.go:18-24` | 容量数字已存在，但语义是“丢”不是“降级” |
| F2 | 消息 ID 是**总线全局自增**，不是 per-conversation 序号 | `bus.go:45,206-207` | 跨会话不可比；`since=` 只能当“全局水位”用 |
| F3 | 无 `clientMsgId`、无去重存储：每次 POST 都新建一条 | `bus.go:176-215` | 重试 = 重复入队；不存在“恰好一次” |
| F4 | 会话按 `(harness, agent)` 复用 id；结构里没有 principal / token / state / 心跳 | `bus.go:35-41,84-123` | 无 takeover、无 REVOKED、无 TTL，会话**永不离线**直到 DELETE |
| F5 | SSE 事件只有 `data:`，**没有 `id:` 行** | `im2agent/internal/api/harness.go:208-218` | `Last-Event-ID` 无法重放；恢复只能靠 `?since=`（profile 的 `resumeHeader` 与实现不符） |
| F6 | SSE 重放仅当 `since>0`；订阅后**不做 per-session 过滤**，任一已认证客户端收到全部事件 | `harness.go:223-245` | 冷启动丢历史 + 跨会话越权读（详见 G3） |
| F7 | HTTP 桥的读游标 `lastSeen` 是**模块级全局** | `packages/im2a-http-mcp/server.mjs:24,222-226` | 两个客户端共用一个游标，A 的读取让 B 永久丢消息 |
| F8 | 限流按 `agentId` 计（120/60s）；出口护栏只在 `payload.content` 存在时生效 | `agent-im/services/gateway/src/agent-gateway.mjs:148-164,361-387` | 委托 N 个子 Agent = N×120/min；P/C 通道若可载正文即绕过护栏 |
| F9 | 委托不得更换 principal；`revoke({cascade})` 给后代标 `CASCADE` → `PARENT_REVOKED`；`VisibilityScope` 默认 15 min | `delegation.mjs:218-220,357-378`；`visibility.mjs:39` | 撤销语义存在，但**不触碰会话**；投影是短 TTL，重放必须重算 |

---

## 2. 决策

| # | 决策 | 依据 | 被拒绝的替代 |
|---|---|---|---|
| D1 | 三通道冻结：**D** 有 seq/持久/审计/计未读；**P** 无序/不持久/不审计/TTL；**C** 无序/`stored=false`/至多一次 | 判型错误是实时系统首要事故源 | 单一通道 + 每消息标志位（会把正文漏进 P） |
| D2 | 收敛模型 = **sequenced-log**（`conversationId + seq`），不引入 CRDT/OT | 收敛对象是消息日志，不是共享文档 | CRDT（无对应问题，却新增内存与墓碑治理） |
| D3 | 恢复游标 = 四元组 `(principalUserId, agentId, conversationId, lastAppliedSeq)` | `Mcp-Session-Id` 部署即失效；`dsh-session:` 不是 bearer | 用传输会话 id 当游标（一次发布就丢数据） |
| D4 | 缺口语义：`≥floor` 重放；`<floor` → `RESYNC_REQUIRED`（快照）；`>head` → `CURSOR_INVALID`（拒绝静默修复） | 降级必须可计数，不能伪装成成功重放 | 一律“尽力补齐”（污染状态不可观测） |
| D5 | `ALREADY_ONLINE` 收窄为 **takeover**（同 principal + 同 agent + token 有效）；旧连接以 `superseded` 关闭；新增 IM2A 自有码 `SUPERSEDED`；takeover **不继承**未确认出站队列 | 断网后旧连接仍在线最长 90s，此时重连必被拒 → 表现为重试风暴 | 保留 `ALREADY_ONLINE`（等于把网络抖动变成登录失败） |
| D6 | 部署 drain 用 **HTTP 503 + `Retry-After`** 与工具结果 `serverState:"draining"`/`retryAfterMs` | 不新增 MCP 方法 | `notifications/im2a/drain`（新增未定义协议） |
| D7 | 退避：`min(cap, base·2^n)` × **full jitter**；`base=500`/`cap=30000`/`herdGuard=2000`；服务端 `retryAfterMs` 优先 | 部署后必须错峰 | 固定间隔重试（自我 DDoS） |
| D8 | 幂等键 `principalUserId:clientMsgId`，TTL 24h，命中**返回首次结果**（含 `messageId`/`seq`） | 只记键不记结果，重试者拿不到 seq，反而制造空洞 | 只做“重复检测” |
| D9 | 背压按通道分类：D `drop-push-resync`（日志不丢、推送可丢）；P `coalesce-last`；C `drop-oldest`；出站双限 256 条 / 1 MiB | 内存必须有界，且“丢弃 ≠ 成功” | 统一丢弃（D 通道静默丢失） |
| D10 | 双层限流：外层 principal 240/min，内层 agent 120/min；审计区分外层/内层 | 委托树放大速率 | 只限 agent（N 个子 = N 倍预算） |
| D11 | 权限六层：ident-code → 委托衰减 → 网关管线 → `VisibilityScope` → `ScopedGrant` → 频道成员；**A1** 重放重新投影、**A2** P/C 结构上无正文、**A3** 撤销即断连 | 缓存投影会让离线期间收紧的 scope 被绕过 | “上次算好的可见字段”直接复用 |
| D12 | 订阅即授权（presence 一次判权，踢人时主动推 `revoked` 并关订阅）；**D 通道每条都过第 3/4 层** | 10Hz×人数逐条判权不可接受 | 把同一取舍用到 D 通道 |
| D13 | 回环豁免取 `loopback-only`：明文仅允许 `127.0.0.1`/`::1`，非回环恒 `tlsRequired` | `mcp-profile.schema.json:39-45` 与本地桥 `http://127.0.0.1:18082/mcp` 冲突 | 全面放行明文 / 全面禁回环（都会卡住现场调试） |
| D14 | 不引入：CRDT/OT、新 MCP 方法、新 A2A 扩展、WebTransport、持久化总线、跨组织实时会话（`crossOrg:false`） | 无问题就不加机制 | 逐条已在上表反向登记 |

---

## 3. 消息形状

三通道字段与强约束见 `packages/protocol/schemas/realtime-message.schema.json`（`$defs.durable/presence/control`）。要点：

| 通道 | 定序 | 必须字段 | 禁止 | 结构保证 |
|---|---|---|---|---|
| D | `conversationId + seq`（服务端分配） | `messageId`、`seq`、`from`、`to`、`at`、`payload`；`TEXT`/`INTENT` 另需 `clientMsgId` | 客户端提交 `seq` | `additionalProperties:false` |
| P | 无 | `memberId`、`at`，且 `cursor`/`selection`/`typing`/`viewport` 至少一个 | **任何** `payload`/`content`/`text`；坐标为数值 | 变体无正文属性 + `additionalProperties:false` |
| C | 无 | `kind:"CMD"`、`command`、`from`、`at` | 自由文本；`args` 仅数值/布尔 | `command` 封闭枚举（`INTERRUPT`/`RESUME_HINT`） |

该 schema **不新增线上协议**：字段全部来自 `harness.Message`（`bus.go:43-51`）、`makeEnvelope`（`INTERFACES.md:366`）或设计文档已冻结项；它只做两件散文档做不到的事——拒绝 P/C 正文、拒绝缺 `clientMsgId` 的客户端文本。

---

## 4. 六方等价性

| 方 | 接入协议（既有枚举） | 会话载体 | 可恢复 | 允许差异（仅此两列） |
|---|---|---|---|---|
| DSH | `MCP_TOOLS` | `Mcp-Session-Id` + 游标 | 是 | streamable-http 主用；stdio 受沙箱禁子进程限制 |
| WorkBuddy | `MCP_TOOLS` | 进程生命周期 | **部分**（进程内 in-flight 丢失） | stdio；靠邮箱游标补，不靠内存 |
| 浏览器 | `MCP_TOOLS` | `Last-Event-ID` + 游标 | 是（**待 G2**） | SSE 需真事件投递（当前只有 keep-alive） |
| 微信 / chatops | `IM_CUSTOM` | 平台 `messageId` + 平台序 | 由平台决定 | 平台能力缺失必须 `CAPABILITY_MISSING`，不得假装 `STREAMING`/`UNREAD_SHAPING` |
| OpenClaw | `MCP_TOOLS` | 进程生命周期 | 同 WorkBuddy | stdio bundle |
| Agent 专家 | `A2A_JSONRPC` / `MCP_TOOLS` | A2A `taskId` | 是（A2A 状态机） | 跨组织仅在 Phase3 |

**X1**：微信不是 MCP 客户端，是 `IM_CUSTOM`；平台 `messageId ≠ clientMsgId`，把它画成“MCP over 微信”会导出错误的幂等键。
**X2**：差异只允许出现在“传输适配 + 会话载体”两列；游标、重放、去重必须是同一套语义，否则就是六套系统。

---

## 5. 差异登记（先补 1–4，再谈其余）

| # | 现状 | 位置 | 验收 | 阻塞的对外声明 |
|---|---|---|---|---|
| G1 | `seq` 是全局自增 | `bus.go:206-207` | 同 `conversationId` 严格递增且从 1 起，跨会话互不影响 | “可重放” |
| G2 | SSE 事件无 `id:` 行 | `harness.go:208-218` | 浏览器带 `Last-Event-ID` 重连拿到不重复的续传 | 浏览器侧“断线续传” |
| G3 | **SSE 订阅后不按 session 过滤**，已认证即收全部事件 | `harness.go:231-245` | 客户端 A 收不到只有 B 可见的 D 消息 | **六方实时协作（越权读）** |
| G4 | 无 `clientMsgId` / 无去重 | `bus.go:176-215` | 同键重发只产生一条，且返回首次 `messageId`/`seq` | “重试不重复” |
| G5 | 无心跳/TTL/REVOKED；会话永不离线 | `bus.go:35-41,84-123` | 断网 ≤90s 转 `OFFLINE`；撤销 ≤5s 转 `REVOKED` | “在线状态”“撤销即断连” |
| G6 | 背压只有 128 条缓冲 + `dropped` 计数，无字节上限、无分通道策略 | `bus.go:23,282-298` | 慢消费者内存有界；D 通道转 `resyncRequired` 而非静默丢 | “支持 N 人同房间” |
| G7 | HTTP 桥游标模块级全局 | `server.mjs:24,222-226` | 两客户端并发 `recv` 互不推进游标 | DSH 多窗口/多会话 |
| G8 | 历史是内存环形 500 条，重启清零 | `bus.go:20,208-211` | 离线 1h + 200 条积压可完整重放 | “离线不丢消息” |
| G9 | 无 principal 层限流 | `agent-gateway.mjs:148-164` | 委托 N 子后总速率 ≤240/min | 多 Agent 委托 |
| G10 | profile 有 fixture 但未接入 `validate` | `scripts/validate.mjs` | 已补：`schemaChecks` 6→7，负例进 CI | — |

> **归属提醒**：G7（per-caller 游标）与“工具按 scope 过滤”已由 `docs/design/IM2A-v2.5.0-MCP-BRIDGE-AND-PLUGINS2-PACKAGING.md` §6 定义（负控 N4/A9、`cacheScope` 必须转 `private`），并由 `scripts/check-bridge-contracts.mjs` 执行。本 ADR 只定义**游标是什么**（D3/D4），不重复定义工具面 —— 两处若冲突，以该文档为工具面权威。

---

## 6. 后果

**现在可以声明**：六方在钉版协议内闭合；D/P/C 判型与消息形状可校验；单条 8000 字节、历史 500、会话 200、缓冲 128 是实测常量。

**现在不能声明**：断线不丢消息、跨会话不可越权读、撤销即时生效、支持 N 人同房间。前三项分别被 G3/G4/G5 阻塞，第四项必须先有 p95 + `outboundQueueBytes` 实测（规则 B1）。

**必须存在的观测项**：`resume_success_rate`、`resync_required_rate`、`seq_gap_count`（>0 即事故）、`dedupe_hit_rate`、`outbound_drop{channel}`、`presence_coalesced_hz`、`session_superseded`、`state_digest_mismatch`（>0 即分歧）。

**需 owner 签字**：`retentionSeqs`/`maxBatch`（现 10000/500 为建议默认值）、`ratePerPrincipalPerMin`（240）。

---

## 7. 未决

1. `mcp-profile.schema.json:39-45` 的正式修订（加 `loopbackExempt` 还是维持“非回环才强制 TLS”）——需 owner，本 ADR 不单方面改钉版 profile。
2. `retentionSeqs`/`maxBatch` 进 `const` 前需签字。
3. 微信平台 `STREAMING`/`UNREAD_SHAPING`/`CUSTOM_MESSAGE` 未逐项实测；缺失走 `CAPABILITY_MISSING`。

## 附录 · 相关文件

```text
packages/protocol/schemas/realtime-session-profile.schema.json   常量 profile
packages/protocol/schemas/realtime-message.schema.json           D/P/C 消息形状（本次新增）
packages/protocol/fixtures/realtime-message.*.json               正例 3 + 负例 2（本次新增）
scripts/validate-realtime.mjs                                    两者耦合校验（本次新增）
tests/TEST-MATRIX-v2.5.0-REALTIME.md                             真机测试矩阵
packages/im2a-http-mcp/server.mjs                                HTTP 桥（G7）
im2agent/internal/{harness/bus.go,api/harness.go}                实时总线与 SSE（G1–G6、G8）
agent-im/services/gateway/src/{agent-gateway,delegation}.mjs     护栏/限流/委托（G9）
```
