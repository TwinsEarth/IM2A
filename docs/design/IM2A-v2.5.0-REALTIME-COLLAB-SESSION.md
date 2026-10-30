# IM2A v2.5.0 · 跨 Harness 实时协作与会话语义

| 项 | 值 |
|---|---|
| 设计对象 | IM2A 平台 v2.5.0（规划）的跨 Harness 实时协作与会话语义 |
| 代码基线 | `E:\GPT\im2a-v2.3.45` · 分支 `release/2.4` · HEAD `5975ebe` · 平台 `2.4.6` / Go `2.3.26` |
| 范围 | DSH / WorkBuddy / 浏览器 / 微信 / OpenClaw / Agent 专家 六方 |
| 协议基线（冻结，不新增） | A2A `1.0`（`v1.0.1`）· MCP `2026-07-28` · Agent Plugins profile `1.0` · `dsh-session-ref 1.0` · `ident-code 1.0` |
| 参考实现 | Hermes Agent Gateway/ACP（**仅借形状**，见 §14 未验证项 1）· 本仓 `agent-im` `AgentGateway` 契约 |
| 结论 | **DESIGN-READY / RUNTIME-NOT-READY**：语义可冻结；6 项实现缺口必须先补，否则"断线重连"会成为丢消息的合法借口 |
| 凭证 | 本文不含任何凭证、密钥、令牌；只引用环境变量名与文件位置 |

---

## 0. 结论摘要

**事实（可验证）**

1. **六方传输已在钉版协议内闭合，不需要新协议。** MCP `2026-07-28` 已 pin（`packages/protocol/versions.json:20-25`）；`AgentGateway` 已把接入方式枚举为 `AGENT_WS / IM_CUSTOM / A2A_JSONRPC / MCP_TOOLS`（`agent-im/contracts/proto/gateway.proto:46-52`），六方各自落在其中之一（§10 映射表）。**凡是本文写不进去的东西，都是缺口，不是新协议的候选。**
2. **"会话"在本项目里是三个不同的东西，混用会直接丢消息。** `Mcp-Session-Id`（传输会话，`packages/im2a-http-mcp/server.mjs:18` 为**进程级** `randomUUID()`）、`dsh-session:<id>`（运行时会话，`session-ref.schema.json:8-24`）、`conversationId`（排序域，`INTERFACES.md:385`）。当前 HTTP 桥把读游标 `lastSeen` 做成**模块级全局**（`server.mjs:23`），`im2a_recv` 直接推进它（`server.mjs:222-226`）——**两个客户端共用一个游标，A 的读取会让 B 永久丢消息。**
3. **恢复所需的三件套已经存在，缺的是接线，不是设计。** 序号 `SeqAllocator`（`INTERFACES.md:371-375`）、去重 `DedupeStore`（`:378-383`）、持久游标 `AgentMailboxStore.ack/fetch`（`:913-919`）。v2.5.0 的恢复语义是"把这三个接上"，而不是发明游标格式。
4. **`ALREADY_ONLINE` 让真机重连必然失败。** §15 规定同一 agent 重复 `connect` 返回 `ALREADY_ONLINE`（`INTERFACES.md:567`），而心跳超时是 90s（`:567`）。真机断网后旧连接在服务端仍"在线"最长 90s，客户端此刻重连必被拒——表现为重试风暴，看起来像服务端宕机。**这是 v2.5.0 必须修的语义缺陷，不是调参。**
5. **存在一条未受检的外发旁路。** 出口护栏只在 `payload.content` 存在时生效（`agent-gateway.mjs:361-387`）。若 presence / CMD 通道允许承载自由文本，且不经过 `Route()`，则等于在治理管线旁开了一条不经检查的外发通道。**结论：P/C 通道必须在结构上不可能携带正文。**
6. **委托会成倍放大限流预算。** 限流按 `agentId` 计数（`agent-gateway.mjs:148-164`），一个 principal 委托 N 个子 Agent 即获得 N×120/min。委托衰减（`delegation.mjs:234-285`）管的是权限，不管速率。
7. **本地回环 profile 自相矛盾。** `mcp-profile.schema.json:39-45` 要求 `streamable-http` 必须 `^https://` 且 `tlsRequired:true`，而当前 HTTP 桥的既定用法是明文 `http://127.0.0.1:18082/mcp`（`packages/im2a-http-mcp/README.md:27-30`）。**明文回环要么被显式豁免，要么该路径在生产中是非法的**——v2.5.0 必须二选一写死。

**判断**

v2.5.0 不新增协议，新增的是**语义契约**：把"哪个字段可丢、丢到什么程度、丢了怎么补"写成可校验的表，再把六方的传输差异收敛到同一张表上。收敛对象是**消息日志**，不是共享文档——因此**不引入 CRDT / OT**（§12）；用 `conversationId + seq` 定序、`clientMsgId` 幂等、游标重放补洞，已足够让任意两方收敛到同一字节序列。

---

## 1. 证据基线

| 判断 | 证据 | 能证明 | 不能证明 |
|---|---|---|---|
| 传输层无需新增 | `versions.json:20-25`（MCP pin）、`gateway.proto:46-52`（四协议） | 六方都有既有落点 | 各方适配器已实现 |
| HTTP 桥是单会话实现 | `server.mjs:18,23,222-226` | 多客户端共享游标 | 生产网关是否同样如此（未读 Go/远端实现） |
| 序号/去重/游标已定义 | `INTERFACES.md:371-383,913-919` | 恢复语义有原语 | 是否已在网关接线（未验证） |
| 重连会被拒 | `INTERFACES.md:567,578` | `ALREADY_ONLINE` 是冻结语义 | 实际生产是否已有绕过分支 |
| 出口护栏只看 content | `agent-gateway.mjs:361` | 旁路存在的前提成立 | 是否已有其它检查层（未见） |
| 限流按 agent | `agent-gateway.mjs:148-164` | 委托放大成本 | 生产是否另设 principal 限流 |
| DSH 需要 modern result | `P0-REPORT.md:350-398` | `server/discover`→`tools/list` 且要求 `resultType/ttlMs/cacheScope` | 其它 5 方的客户端要求 |
| 委托衰减规则冻结 | `delegation.mjs:234-285`、`INTERFACES.md:946` | scope 只能收窄 | 撤销是否触发断连（未实现） |
| 本地桥明文回环 | `packages/im2a-http-mcp/README.md:27-30` vs `mcp-profile.schema.json:39-45` | profile 与运行形态冲突 | 生产是否走 HTTPS |

---

## 2. 状态分类（协议落点由这张表决定）

先给每个字段判型，再决定它走哪条通道。**判型错误是实时系统最主要的事故源**，而不是选型错误。

| 数据 | 类型 | 承载 | 定序 | 丢失后果 | 恢复手段 |
|---|---|---|---|---|---|
| 业务消息正文 | 持久 | D 通道 / §10 envelope | `conversationId + seq` | 不可接受 | mailbox cursor 重放 |
| 委托 token / 衰减链 | 持久 | §30 + `_meta` | 签发序（`del-*`） | 越权或失联 | 重验链；撤销即断连 |
| 审批单 `tkt-*` | 持久 | §4 approval-engine | ticket 生命周期 | 决策丢失 | 重放 + 幂等重提交 |
| A2A 任务 | 持久 | A2A `taskId` | A2A 自有状态机 | 任务悬空 | A2A task 查询 |
| 已读位移 / 游标 | 持久 | §29 `ack` | 单调 | 重复投递或漏投 | `CURSOR_INVALID` 拒绝回退 |
| 在线/正在输入/光标 | **临时** | P 通道 | 无 | 无 | 心跳重建，≤1 TTL 自愈 |
| 唤醒/中止指令 | **控制** | C 通道（§14 `CMD`，`stored=false`） | 无 | 无（加速项） | D 通道状态可重建 |
| 频道成员 / 订阅 | 仲裁 | §14 `ChannelRegistry` | 变更序 | 泄漏或看不到 | 重连时重新鉴权 |

**三通道模型（冻结）**

```text
D 通道（durable）  ：有 seq、进审计、进邮箱、可重放。丢失 = 事故。
P 通道（presence） ：无 seq、不审计、不进邮箱、TTL 过期即消失、可合并。丢失 = 无感。
C 通道（control）  ：无 seq、stored=false、只投给已订阅 Agent、至多一次。丢失 = 慢一点。
```

**不变量 R1（可断言的验收条件）**：把 P 与 C 通道**全部丢弃**后，系统必须仍能仅凭 D 通道 + 游标重放到完全一致的状态。任何"只存在于 presence 里的事实"都是设计缺陷。

---

## 3. 会话身份模型（三层 ID，禁止互用）

| 层 | 标识 | 谁生成 | 生命周期 | 用途 | **禁止用途** |
|---|---|---|---|---|---|
| 传输会话 | `Mcp-Session-Id` | MCP 服务端 | 连接/进程 | HTTP 亲和、SSE 重放绑定 | **禁止做恢复游标**（部署即失效） |
| 运行时会话 | `dsh-session:<id>` | DSH | 带 `expiresAt`/`revoked` | 跨会话寻址（`session-ref 1.0`） | 禁止当 bearer token（`packages/protocol/README.md:26-30`） |
| 排序域 | `conversationId` | IM2A | 长期 | `seq` 单调域 | 禁止跨会话复用 seq |

**规则 S1**：恢复游标永远是四元组 `(principalUserId, agentId, conversationId, lastAppliedSeq)`。任何实现用 `Mcp-Session-Id` 或 `dsh-session:` 当游标，都是缺陷。

**规则 S2**：恢复时必须**重验身份**——`signed card + token`（§12）与委托链（§30）。离线期间被撤销的 token/委托不得因"上次连着"而复活。

**规则 S3**：`Mcp-Session-Id` 重新签发是**正常事件**（部署、LB 迁移、空闲回收），不得被上报为错误；只有"同一 `(principal, agent, conversation)` 的 `seq` 出现空洞"才是错误。

---

## 4. 消息模型

复用 §10 信封，**不新增信封层**。新增字段全部是 IM2A 自己的工具参数/结果字段（不是 MCP 方法，不是 A2A 扩展）。

| 字段 | 来源 | 说明 |
|---|---|---|
| `bizType` / `bizId` / `payload` / `traceId` / `trustZone` / `agentContext` / `bizSeq` | §10 `makeEnvelope`（`INTERFACES.md:366`） | 原样保留，不改语义 |
| `clientMsgId` | 客户端生成（UUID） | **幂等键**；重试必须复用同一个值 |
| `messageId`（`msg-*`） | 网关生成 | 规范 ID；同一个 `clientMsgId` 永远映射到同一个 `messageId` |
| `conversationId` | 网关/频道层 | `seq` 单调域 |
| `seq` | `SeqAllocator.next({userId, conversationId})` | **服务端唯一权威顺序**；客户端时间戳只作展示 |
| `origin` | 节点 ID | 跨节点扇出去重（§48 `:1418` 的 `id + origin`） |
| `kind` | §14 `MESSAGE_KIND` | `TEXT/CMD/AGENT_TURN/APPROVAL_CARD/INTENT` |
| `channelClass` | 本文 | `D` / `P` / `C`，决定下面所有策略分支 |

**规则 M1**：一行消息的 `channelClass` 决定它的全部行为（持久化、审计、未读、合并、溢出策略）；**不允许**在同一通道里混类。

**规则 M2**：`seq` 由服务端分配，客户端**不得**提交 `seq`。客户端时钟只用于排序展示，不参与 `seq` 判定（时钟偏移不改变收敛结果）。

**规则 M3**：跨节点扇出后，同一 `messageId` 在每个节点**最多投递一次**（§48 不变量①）。重复由 `DedupeStore` 与 `messageId` 双重兜底。

---

## 5. 会话状态机与重连

### 5.1 状态机（在 §15 四态上补两个动作，不新增状态名以外的语义）

```text
        connect(auth ok)                 accept
CONNECTING ───────────────▶ ONLINE ◀─────────────┐
     │                        │                   │
     │ auth fail              │ drain signal      │ resume(seq ok)
     ▼                        ▼                   │
  REVOKED ◀── revoke ──── DRAINING ── close ──▶ OFFLINE
     ▲                        │                   │
     └────────────────────────┴───────────────────┘
                     heartbeat timeout (90s)
```

- `DRAINING`：部署/摘流。**不再接受新 POST**（HTTP `503` + `Retry-After`，见 §5.3），允许在途请求完成，随后关闭 SSE。
- `REVOKED`：终态。仅由撤销触发（token / 委托级联 / principal 撤销）。

### 5.2 修正 `ALREADY_ONLINE`：改为 takeover

**缺陷**：§15 冻结语义是重复 `connect` → `ALREADY_ONLINE`（`INTERFACES.md:567,578`）。真机断网时旧连接在服务端仍在线最长 90s，重连必被拒。

**v2.5.0 语义（在 §15 之上收窄，不改变其它模块）**：

```text
connect(agentId, principalUserId, token):
  if 无在线会话            -> 新建，ONLINE
  if 在线会话属于同一 (principalUserId, agentId) 且 token 有效
                           -> takeover：旧 transport 以 reason='superseded' 关闭，
                              新连接继承 conversationId 集与订阅集合，
                              但**不继承任何未确认的 outbound 队列**（交回客户端重放）
  if 在线会话属于别的 principal/agent
                           -> 认证阶段即失败（UNAUTHENTICATED / TOKEN_MISMATCH），不是 ALREADY_ONLINE
```

新增错误码（IM2A 自有 `SESSION_CODE`，非 MCP/A2A 协议码）：`SUPERSEDED`。`ALREADY_ONLINE` 保留给"显式拒绝 takeover"的配置（例如管理端强制单会话）。

**规则 C1**：takeover 只转移 **D 通道游标**，不转移 in-flight 队列。客户端未确认的操作由客户端用同一 `clientMsgId` 重发。

### 5.3 部署 drain（不新增 MCP 方法）

| 层 | 手段 | 是否新协议 |
|---|---|---|
| HTTP | 新 POST 返回 `503` + `Retry-After: <s>` | 否，HTTP 既有语义 |
| 工具结果 | 心跳/接收类工具的结果里带 `serverState:"draining"`、`retryAfterMs` | 否，IM2A 自有工具 schema |
| SSE | 服务端主动关闭流；客户端按自身退避重连 | 否 |

**明确不做**：不新增 `notifications/im2a/drain` 之类方法（§12 禁止新增未定义协议）。

### 5.4 恢复算法（唯一实现，六方共用语义）

```text
resume(principalUserId, agentId, conversationId, lastAppliedSeq):
  1 传输重置：新 Mcp-Session-Id（正常），或 SSE 带 Last-Event-ID 重连
  2 重验身份：signed card + token；委托链 verify()（§30）
  3 取游标：游标以 §29 mailbox 的 ack 位移为准；客户端上报值仅用于校验
  4 服务端判定：
      lastAppliedSeq >= floor        -> 重放 (lastAppliedSeq, min(+maxBatch, head)]
      lastAppliedSeq <  floor        -> RESYNC_REQUIRED（发快照，不发日志）
      lastAppliedSeq >  head         -> CURSOR_INVALID（客户端状态被污染，拒绝静默修复）
  5 客户端按 seq 顺序应用；按 messageId 去重（双保险）
  6 未确认的上行操作：同一 clientMsgId 重发；服务端命中去重 -> 返回**原结果**
```

`floor = head - retentionSeqs`，`retentionSeqs` 与 `maxBatch` 见 profile（§7）。

**规则 R2**：`RESYNC_REQUIRED` 必须可区分于"正常重放"——它是降级路径，要计入指标；不许把它伪装成一次成功的重放。

### 5.5 退避与惊群

```text
delay = min(capMs, baseMs * 2^attempt)
delay = delay * random()                       // full jitter
delay += drainHintMs ? random(0, herdGuardMs) : 0   // 部署后额外抖动
server 提供的 retryAfterMs 优先于本地计算
```

默认 `baseMs=500`、`capMs=30000`、`herdGuardMs=2000`（§7 profile）。

**规则 R3**：服务端在 drain 窗口内应按连接批次返回**带随机偏移的** `Retry-After`，让同一批客户端错峰而不是同时回来。

---

## 6. 背压

### 6.1 出站（服务端 → 客户端）

每连接有界队列：`outboundQueueEntries` 与 `outboundQueueBytes` 双限（任一超限即触发）。溢出策略**按通道分类**，不允许统一处理：

| 通道 | 溢出策略 | 理由 |
|---|---|---|
| D | **丢弃待推送（不是丢弃日志）→ 标记 `resyncRequired` → 客户端按游标补** | 日志不可丢，推送可丢；内存必须有界 |
| P | 按 `(conversationId, memberId)` 合并，**只留最后一帧** | 光标中间态无价值 |
| C | 丢最旧 | 指令是加速项，且幂等可重发 |

这与 §48 总线"单条 64KB、超限丢弃并计入 `stats.dropped`"（`INTERFACES.md:1418`）方向一致，但**D 通道不得用"丢弃即成功"**——必须显式转成 `resyncRequired`。

### 6.2 入站与容量

| 环节 | 现有上限 | 溢出行为 | 依据 |
|---|---|---|---|
| 轮次内注入 | `capacity=50` | `CAPACITY_EXCEEDED`（不静默丢） | `INTERFACES.md:810,818` |
| 持久邮箱 | `capacityPerAgent=1000` | `CAPACITY_EXCEEDED` | `INTERFACES.md:914,922` |
| 网关限流 | 120 / 60s / agentId | `RATE_LIMITED` | `agent-gateway.mjs:32,148-164` |
| single message | 64KB（总线）/ 8000B（`im2a_send` 文本） | 拒绝 | `INTERFACES.md:1418`、`server.mjs:206` |

### 6.3 双限流（补 §12 的委托放大缺口）

```text
外层：principalUserId 天花板  ratePerPrincipalPerMin   -> 拒绝模式，含下属全部子 Agent
内层：agentId            ratePerAgentPerMin           -> 现有 §12 语义不变
```

两层都超时返回 `RATE_LIMITED`，但**审计里必须区分是外层还是内层**——否则运维无法判断是"某个 Agent 疯了"还是"某个委托树疯了"。

### 6.4 扇出天花板（诚实数字）

`§48 broadcast.mjs` 当前是**单机内存 / TCP 总线**，不持久化，无订阅者即丢弃。v2.5.0 **不承诺**跨机房扇出。目标的测量口径：

| 场景 | 必须实测的量 | 已知形态 |
|---|---|---|
| 1 个热频道 × N 在线 | 扇出 p95、内存峰值、丢弃计数 | 单进程 SSE，受 `outboundQueueBytes` 约束 |
| M 个冷频道 × 2 人 | 连接建立成本、会话表内存 | 分片问题，非扇出问题 |

**规则 B1**：任何"支持 X 人同房间"的对外声明，必须附 `outboundQueueBytes`、`presence` 合并频率与实测 p95；没有测量就不写数字。

---

## 7. 幂等

| 操作 | 幂等键 | 重复时的行为 |
|---|---|---|
| 发送消息 | `(principalUserId, clientMsgId)` | `DedupeStore.checkAndMark` 命中 → **返回首次结果**，不二次入队 |
| 上行重试 | 同上 | 同上（重放安全） |
| 委托签发 | `idempotencyKey`（可选参数，§30 允许新增可选参数） | 命中 → 返回同一 `del-*`，不新建子 token |
| 审批决策 | `tkt-*` + 决策版本 | 幂等；`ALREADY_USED` 式拒绝重复决策 |
| scoped grant 消耗 | `grantId + scope` | 单次消耗（`INTERFACES.md:1106`） |
| A2A 任务消息 | A2A `messageId` | 由 A2A 状态机裁决 |

**规则 I1**：去重键的 TTL 必须 ≥ 最大离线重试窗口。取 `864e5`（24h，§10 默认 `ttlMs`，`INTERFACES.md:379`）——与"离线 1 小时 + 200 条积压"的验收场景留足余量。

**规则 I2**：去重必须**缓存首次结果**而不只是"记住键"。只记键不记结果，会让重试者拿到"重复"却拿不到 `messageId`/`seq`，从而无法推进游标——反而制造空洞。

**规则 I3**：`clientMsgId` 由客户端生成且**永不跨 principal 复用**；键空间是 `principalUserId + ':' + clientMsgId`，天然阻止跨用户撞键。

---

## 8. 权限边界

### 8.1 六层（自上而下，任一层拒绝即终止）

| 层 | 机制 | 粒度 | 实时性相关 |
|---|---|---|---|
| 1 | `ident-code 1.0` L0–L4 | 主体级 | 决定能否建会话 |
| 2 | 委托衰减 §30 | 链级 | **恢复时重验**，撤销即断连 |
| 3 | `AgentGateway` §12 管线 | 每次外部动作 | 唯一数据平面 |
| 4 | `VisibilityScope` §13 | 读侧字段 | **重放时必须重新投影** |
| 5 | `ScopedGrant` §35 | 单条消息 × 单 scope | 单次消耗 |
| 6 | 频道成员 §14 | 订阅级 | 订阅即授权（见 8.3） |

### 8.2 三条实时专有规则

**规则 A1 —— 重放必须重新投影，禁止复用缓存投影。**
`VisibilityScope` 是短 TTL（15 min，`INTERFACES.md:150`）。若服务端缓存"上次算好的可见字段"并在重放时直接下发，离线期间收紧的 scope 会被旧投影绕过。**缓存原始 + 下发时投影**，且投影结果不得跨 scope 变更复用。

**规则 A2 —— P/C 通道在结构上不可携带正文。**
§12 出口护栏只看 `payload.content`（`agent-gateway.mjs:361`）。因此：
- P 通道 schema **没有** `text` 字段，只有 `cursor` / `selection` / `typing` / `viewport` 等定长枚举数值；
- C 通道只允许**封闭枚举**的指令码 + 参数（如 `INTERRUPT`、`RESUME_HINT`），不允许自由文本。
两者都设字段数上限与字节上限。**违反即不是"宽松"，而是绕过 `EgressGuard` 与 `SignedAuditLog` 的越权外发。**

**规则 A3 —— 撤销必须主动断连。**
§30 `revoke({cascade:true})` 返回 `revoked` 列表（`delegation.mjs:357-379`），但**不触碰会话**。v2.5.0 要求：撤销事件触发会话层 `forceOffline(reason='parent_revoked')`，并对在途轮次调 `endTurn({outcome:'revoked'})`（§25）；未消费消息**留在邮箱**（不丢），以便重新授权后继续。

### 8.3 订阅即授权 vs 每事件授权（显式取舍）

| 方案 | 成本 | 风险 |
|---|---|---|
| 每次 presence 事件都判权 | 10Hz × 人数，不可接受 | 无 |
| 订阅时判一次，之后放行（**采纳**） | 一次 | 成员在订阅期间被踢出的窗口内仍收 presence |

**决定**：采纳"订阅时判权"，并要求踢出成员时**主动推一次 `revoked` 并关闭该订阅**，把窗口收敛到一次往返，而不是靠 TTL 自然过期。presence 是临时数据，这个残留风险可接受；**D 通道不接受同一取舍**——D 通道每条消息必须过第 3/4 层。

### 8.4 跨组织

`session-ref` 冻结 `crossOrg: const false`（`session-ref.schema.json:23`）；A2A 跨组织适配只在 Phase3（`gateway.proto:50`）。v2.5.0 **不开放**跨组织实时会话，也不为它预留隐含字段。

---

## 9. 多 Agent 委托的实时语义

| 场景 | 语义 |
|---|---|
| 子 Agent 建会话 | 子 Agent 是独立 `agentId`，有独立在线态、独立订阅；但 principal 必须等于父（`delegation.mjs:218-221`） |
| 并发上限 | 每 principal 的在线连接数、每父委托的子数设上限（§7 profile），超限显式拒绝 |
| 速率 | 外层按 principal 计（§6.3），防止委托树放大 |
| 撤销 | 级联撤销 → 主动断连全部后代（A3）+ 在途轮次 `revoked` |
| 父 Agent 掉线 | 子会话**不**随之掉线（避免级联惊群）；但子会话一旦需要续签委托即失败 |
| 跨组织委托 | 走 A2A `A2A_JSONRPC`，**不签发本平台 token**（`gateway.proto:23-25` A1 裁定）；内部委托走 `del-*`。两者不混用 |

---

## 10. 六方映射矩阵

| 方 | 接入协议（既有枚举） | 传输 | 会话载体 | 可恢复 | 已知阻塞 |
|---|---|---|---|---|---|
| DSH | `MCP_TOOLS` | streamable-http（**主用**）/ stdio | `Mcp-Session-Id` + 游标 | 是 | 沙箱禁子进程管道（`P0-REPORT.md:242-288`）：stdio 不可用，只能走 HTTP |
| WorkBuddy | `MCP_TOOLS` | stdio（`adapters.json:145-158`） | 进程生命周期 | **部分**：进程内 in-flight 丢失 | 进程死即会话死；靠 §29 邮箱游标补，不靠内存 |
| 浏览器 | `MCP_TOOLS` | streamable-http + SSE | `Last-Event-ID` + 游标 | 是 | SSE 需真重放（当前 `server.mjs:250-261` 只有 keep-alive，无事件投递） |
| 微信 / chatops | `IM_CUSTOM` | IM 渠道（`im-adapter` §31） | 平台 `messageId` + 平台序 | **由平台决定** | 平台能力缺失必须 `CAPABILITY_MISSING` 显式拒绝（`INTERFACES.md:986`），不得假装支持 `STREAMING`/`UNREAD_SHAPING` |
| OpenClaw | `MCP_TOOLS` | stdio（bundle） | 进程生命周期 | 同 WorkBuddy | 同上 |
| Agent 专家 | `A2A_JSONRPC` / `MCP_TOOLS` | A2A | A2A `taskId` | 是（A2A 自有状态机） | 跨组织只在 Phase3 |

**规则 X1**：微信侧**不是 MCP 客户端**，它是 `IM_CUSTOM`。任何把它画成"MCP over 微信"的图都是错的，会导出错误的幂等键（平台 `messageId` ≠ `clientMsgId`）。

**规则 X2**：六方的差异**只允许**存在于"传输适配层 + 会话载体"两列；第 4–6 列（游标、重放、去重）必须是同一套语义。否则就是六套系统。

---

## 11. 观测与收敛证明

| 指标 | 定义 | 告警阈值（建议） |
|---|---|---|
| `resume_success_rate` | 重放成功 / 重连总数 | < 99% |
| `resync_required_rate` | 走快照降级 / 重连总数 | > 1% |
| `seq_gap_count` | 同一 `conversationId` 出现空洞次数 | **> 0 即事故** |
| `dedupe_hit_rate` | 命中数 / 上行总数 | 突增说明重试风暴 |
| `outbound_drop{channel}` | 各通道丢弃计数 | D 通道 > 0 需复盘 |
| `presence_coalesced_hz` | 每频道合并后频率 | > 10Hz |
| `session_superseded` | takeover 次数 | 与网络质量相关，非错误 |
| `state_digest_mismatch` | 抽样状态哈希不一致 | **> 0 即分歧事故** |

**收敛证明**：客户端周期性上报 `digest(conversationId, lastAppliedSeq, 可见状态摘要)`；服务端抽样比对。**没有这个，任何"已收敛"的说法都不可验证。**

---

## 12. 明确不引入（写下来是为了拒绝）

| 不引入 | 理由 |
|---|---|
| CRDT / OT | 本系统收敛对象是**消息日志**，不是共享文档。`seq` + 幂等 + 游标重放已足够；引入 CRDT 是新增机制而无对应问题 |
| 新 MCP 方法（`notifications/im2a/*` 等） | 违反"不加未定义协议"；drain 用 HTTP 503 + 工具结果字段已足 |
| 新 A2A 扩展 | A2A 钉 `v1.0.1`，扩展需新 ADR |
| WebTransport / QUIC | `AGENT_WS` 已存在但 v2.5.0 不铺开；无测量前不引入第三种传输 |
| 持久化总线 | §48 冻结"不持久化"。要持久化就用 D 通道 + §29 邮箱，不要把总线改成队列 |
| 跨组织实时会话 | `crossOrg:false` 冻结；Phase3 之前不谈 |
| 客户端时钟参与定序 | 客户端时钟只做展示 |

---

## 13. 实现缺口（按顺序，先做 1–3 再谈其它）

| # | 缺口 | 位置 | 验收 |
|---|---|---|---|
| G1 | HTTP 桥单会话 + 全局游标 | `packages/im2a-http-mcp/server.mjs:18,23,222-226` | 两客户端并发 `recv` 互不推进游标；各自断线重放无空洞 |
| G2 | SSE 不投递事件 | `server.mjs:250-261` | 浏览器侧能收到 D 通道推送并按 `Last-Event-ID` 重放 |
| G3 | `ALREADY_ONLINE` 拒绝重连 | `INTERFACES.md:567`（§15） | 断网 30s 内重连成功，旧连接以 `superseded` 关闭 |
| G4 | 去重只记键不记结果 | §10 `DedupeStore`（`:378-383`） | 重试返回首次结果（含 `messageId`/`seq`） |
| G5 | 撤销不断连 | `delegation.mjs:357-379` + §15 | 级联撤销 5s 内后代会话变 `REVOKED`，在途轮次 `revoked` |
| G6 | profile 明文回环矛盾 | `mcp-profile.schema.json:39-45` vs `im2a-http-mcp/README.md:27-30` | 二选一写死并有校验 |
| G7 | 无限流 principal 天花板 | `agent-gateway.mjs:148-164` | 委托 N 子后总速率仍 ≤ `ratePerPrincipalPerMin` |

---

## 14. 未验证项与限制

1. **Hermes Gateway/ACP 未联网核验**（本次环境 web 检索失败）。本文只借用其"网关收敛 + 会话抽象"的形状，未引用其字段名；若后续要引用具体契约，需重新抓取并补 pin。
2. 未读 Go/远端网关实现，"生产是否已按 §15 实现"未知；本文结论以仓库内 `agent-im` 契约为准。
3. 未运行任何负载测试；§6.4 的天花板只给测量口径，**不给承诺数字**。
4. `Last-Event-ID` 重放能力取决于具体 MCP 实现；本文只要求"可重放"，未核验各 Harness 客户端是否支持带游标重连。
5. 微信侧的真实平台能力（`STREAMING`/`UNREAD_SHAPING`/`CUSTOM_MESSAGE`）未逐项验证；缺失必须走 `CAPABILITY_MISSING`，不得降级。
6. profile 常量为**建议默认值**，需 owner 签字后才进 `realtime-session-profile.schema.json` 的 `const`。
7. `platform` 版本仍为 `2.4.6`（`scripts/validate.mjs:39` 硬断言），**v2.5.0 代码线不存在**；本文与时间均为相对估计。

---

## 附录 A · 相关文件

```text
docs/design/IM2A-v2.5.0-REALTIME-COLLAB-SESSION.md   本文
docs/adr/0006-realtime-session-semantics.md          决策记录
packages/protocol/schemas/realtime-session-profile.schema.json   常量 profile（草案）
packages/protocol/fixtures/realtime-session-profile.valid.json    正例
tests/TEST-MATRIX-v2.5.0-REALTIME.md                 真机测试矩阵
packages/im2a-http-mcp/server.mjs                    HTTP MCP 桥（G1/G2 所在）
packages/protocol/schemas/{mcp-profile,session-ref}.schema.json
agent-im/contracts/INTERFACES.md §10/§14/§15/§25/§29/§30/§35/§48
agent-im/contracts/proto/gateway.proto
agent-im/services/gateway/src/{agent-gateway,delegation}.mjs
```
