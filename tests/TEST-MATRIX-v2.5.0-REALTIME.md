# v2.5.0 实时协作 · 真机测试矩阵

| 项 | 值 |
|---|---|
| 状态 | **矩阵已定义，真机未跑通**：25 行中 0 行完成完整真机验收 |
| 基线 | `E:\GPT\im2a-v2.3.45` · platform `2.4.6` / Go `2.3.26` |
| 语义依据 | `docs/adr/0006-realtime-session-semantics.md` · `docs/design/IM2A-v2.5.0-REALTIME-COLLAB-SESSION.md` |
| 常量依据 | `packages/protocol/schemas/realtime-session-profile.schema.json`（`realtime-session-profile.valid.json` 取值） |
| 形状依据 | `packages/protocol/schemas/realtime-message.schema.json` |

## 0. 状态口径（先看这张表再读矩阵）

| 状态 | 含义 | 能否对外声明 |
|---|---|---|
| `PASS(local)` | 本机可判定，已在 `npm test` 内执行 | 只可声明“契约可校验” |
| `PARTIAL` | 一部分已记录通过，另一部分未验证 | 必须逐条说明哪一半 |
| `FAIL(code)` | 代码级已确认与目标语义相反（当下缺陷） | 不可声明，且需缺陷单 |
| `GAP(code)` | v2.5.0 目标语义在现网实现里不存在（**预期内未落地，非回归**） | 不可声明 |
| `UNVERIFIED` | 缺真机/账号/安装，未执行 | 不可声明 |
| `OPEN` | 需 owner 决策后才能定判据 | 不可声明 |

> 除 `RT-21` 外，`GAP` 行都是“v2.5.0 待落地”，不是 2.4.6 的回归。`RT-21` 是**当下就该修**的越权读（见 ADR 0006 · G3）。

## 1. 前置条件

| 项 | 值 | 来源 |
|---|---|---|
| 网关 | `http://127.0.0.1:18080`，`Authorization: Bearer $IM2A_HARNESS_TOKEN` | `packages/im2a-http-mcp/server.mjs:16-17` |
| HTTP MCP 桥 | `http://127.0.0.1:18082/mcp`（回环明文，见 RT-23） | `server.mjs:14-15` |
| DSH | `0.2.1-rc.5`；stdlib 子进程被禁 → 只走 streamable-http | `version-matrix.json:73-79`；`P0-REPORT.md:242-288` |
| OpenClaw | `2026.10.7`（external-pinned） | `version-matrix.json:81-87` |
| MCP / A2A | `2026-07-28` / `1.0`（`v1.0.1`） | `packages/protocol/versions.json` |
| 证据文件 | `tests/evidence/rt-<NN>-<party>.json` | 本文件 §5 |

## 2. 矩阵

### 组 A · 会话（载体、接管、撤销、上限）

| ID | 场景 | 适用方 | 操作 | 通过判据 | 现状 |
|---|---|---|---|---|---|
| RT-01 | 六方各自注册并互相可见 | 全部 | 六端各起一次 `POST /v1/harness/sessions`，再 `GET /v1/harness/sessions` + 订阅 SSE | 六条在线记录；每端 SSE 收到 `type=session`；`count` 单调增 | `UNVERIFIED`（WorkBuddy/微信/OpenClaw 未装） |
| RT-02 | 同一 `(harness,agent)` 断网重连 | DSH/OpenClaw/WorkBuddy | 杀连接后 30s 内重连，同 token | 旧 transport 收到 `reason=superseded` 关闭；`session_superseded` +1；无 `ALREADY_ONLINE` 报错 | `GAP(code)`：`Register` 只按 `(harness,agent)` 复用 id，无 takeover/`SUPERSEDED`（`bus.go:84-123`） |
| RT-03 | 心跳 30s / 服务端 90s 超时 | 全部 | 静默丢包 120s 不重连 | ≤90s 该会话转 `OFFLINE`，presence 消失 | `GAP(code)`：`Session` 无 `lastSeen`、无 `sweepStale`（`bus.go:35-41`） |
| RT-04 | 撤销 → 断连 | Agent 专家/子 Agent | `revoke({cascade:true})` 后对后代发一条 D 消息 | ≤5s 后代会话 `REVOKED`；在途轮次 `revoked`；未消费消息仍在邮箱 | `GAP(code)`：`delegation.revoke` 不触碰会话（`delegation.mjs:357-378`） |
| RT-05 | 每 principal 连接上限 8 | 全部 | 同 principal 起第 9 条连接 | 第 9 条被显式拒绝（非静默踢旧） | `GAP(code)`：只有全局 200（`bus.go:21,114-117`），无 principal 维度 |

### 组 B · 重连（恢复、缺口、drain）

| ID | 场景 | 适用方 | 操作 | 通过判据 | 现状 |
|---|---|---|---|---|---|
| RT-06 | socket 在发送中被杀 | 全部 | 发 D 消息后立刻断网，恢复后**同 `clientMsgId`** 重发 | 该消息只出现一次；返回首次 `messageId`+`seq` | `GAP(code)`：无 `clientMsgId`/去重（`bus.go:176-215`） |
| RT-07 | 离线 1h + 200 条积压 | DSH/浏览器/OpenClaw | 断开 1h，期间他人发 200 条，重连带游标 | 200 条按 `seq` 无空洞重放；`seq_gap_count=0` | `GAP(code)`：历史为内存环形 500 且全局 id（`bus.go:20,206-211`） |
| RT-08 | 游标超前（客户端状态被污染） | 全部 | 上报 `lastAppliedSeq > head` | 返回 `CURSOR_INVALID`，**不**静默降级为全量 | `GAP(code)`：无游标校验路径 |
| RT-09 | 游标低于保留窗口 | 全部 | 上报 `lastAppliedSeq < head-10000` | 返回 `RESYNC_REQUIRED` 并发快照；计入 `resync_required_rate` | `GAP(code)`：无 `retentionSeqs` 概念 |
| RT-10 | 部署 drain | 全部 | 滚动重启网关 | 新 POST 得 `503`+`Retry-After`；客户端 ≤5s 重连且抖动分散；零丢失 | `GAP(code)`：无 drain 状态、无 503 分支 |

### 组 C · 幂等

| ID | 场景 | 适用方 | 操作 | 通过判据 | 现状 |
|---|---|---|---|---|---|
| RT-11 | 同键双发 | 全部 | 1s 内两次 `POST /v1/harness/messages`，`meta.clientMsgId` 相同 | 一条消息、同一 `messageId`/`seq`；`dedupe_hit_rate` +1 | `GAP(code)` |
| RT-12 | 24h 内重放 | DSH/OpenClaw | 次日重放同一 `clientMsgId` | 仍命中首次结果（TTL 86400000） | `GAP(code)` |
| RT-13 | 跨 principal 撞键 | 两个 principal | 两端用同一个 `clientMsgId` | 视为两条不同消息（键空间 `principalUserId:clientMsgId`） | `GAP(code)` |

### 组 D · 背压

| ID | 场景 | 适用方 | 操作 | 通过判据 | 现状 |
|---|---|---|---|---|---|
| RT-14 | 慢消费者（不读 SSE） | 浏览器/DSH | 读到 100 条后停读 60s，期间灌 2000 条 | 服务端内存有界（≤1 MiB/连接）；D 通道转 `resyncRequired` 而**非**静默丢；补发后状态一致 | `GAP(code)`：仅 128 条缓冲 + `dropped` 计数（`bus.go:23,282-298`） |
| RT-15 | presence 洪泛 | 浏览器 | 以 50Hz 推光标 10s | 合并后 ≤10Hz，最后位置胜出；无正文可外发 | `GAP(code)`：无 P 通道 |
| RT-16 | control 洪泛 | Agent 专家 | 连发 500 条 `INTERRUPT` | 丢最旧、不阻塞 D 通道；可重发不重复生效 | `GAP(code)`：无 C 通道分类 |
| RT-17 | 超限输入 | 全部 | 发 8001 字节文本；再发第 1001 条积压 | 单条被拒；邮箱满返回 `CAPACITY_EXCEEDED`（不静默丢） | `PARTIAL`：8000 字节上限已在（`bus.go:192`、`server.mjs:206`）；邮箱容量未实现（`G8`） |

### 组 E · 委托与权限边界

| ID | 场景 | 适用方 | 操作 | 通过判据 | 现状 |
|---|---|---|---|---|---|
| RT-18 | 委托不得换 principal | Agent 专家 | `delegate({principalUserId: 其他})` | `DELEGATION_DENIED`；`verify` 返回收窄后的 `effectiveScope` | `UNVERIFIED`：单测存在（`agent-im/services/gateway/test/delegation.test.mjs`），真机未跑 |
| RT-19 | 级联撤销 | 子 Agent / OpenClaw | 撤销父 token（cascade） | 后代 `verify` → `PARENT_REVOKED`；会话 `REVOKED`；未消费消息留在邮箱 | `GAP(code)`（同 RT-04） |
| RT-20 | 委托树限流放大 | 多 Agent | 1 principal 起 4 子，各发 120/min | 总速率 ≤240/min；审计区分外层/内层 | `GAP(code)`：仅按 `agentId` 计（`agent-gateway.mjs:148-164`） |
| RT-21 | **越权读：跨会话监听** | 浏览器/DSH/全部 | A 订阅自己的 SSE，B 另发一条只给 C 的 D 消息 | A 收不到 B→C 的消息 | `FAIL(code)`：SSE 订阅后不做 per-session 过滤，已认证者收到全部事件（`harness.go:231-245`） |

### 组 F · 平台差异（不能假装）

| ID | 场景 | 适用方 | 操作 | 通过判据 | 现状 |
|---|---|---|---|---|---|
| RT-22 | 平台能力缺失 | 微信 / chatops | 要求 `STREAMING` 或 `UNREAD_SHAPING`，而适配器未声明 | 返回 `CAPABILITY_MISSING`；**不降级**为伪流式 | `UNVERIFIED`：平台真实能力未逐项实测 |
| RT-23 | 回环 vs TLS | DSH/浏览器 | 非回环地址用明文 `http://` 连 `/mcp` | 非回环明文被拒（`tlsRequired`）；回环按 `loopbackExempt:"loopback-only"` 放行 | `OPEN`：`mcp-profile.schema.json:39-45` 与本地桥用法冲突，需 owner 定（ADR 0006 D13） |
| RT-24 | 沙箱禁子进程 | DSH | 走 stdio 启动 MCP server | stdio 失败是**预期**；streamable-http 下 `server/discover`→`tools/list` 携带 `resultType/ttlMs/cacheScope` | `PARTIAL`：握手 + `tools/list` 有记录（`P0-REPORT.md:350-398`）；DSH 侧真实 `tools/call` 未验证 |

### 组 0 · 契约（本机可判定）

| ID | 场景 | 适用方 | 操作 | 通过判据 | 现状 |
|---|---|---|---|---|---|
| RT-00 | 三通道形状与常量一致 | 契约层 | `npm test` | profile 正/负例、D/P/C 正例、P 夹带正文与 TEXT 缺 `clientMsgId` 两个负例全部按预期判定 | `PASS(local)`：`tests/p0.test.mjs` · `scripts/validate-realtime.mjs` |

## 3. 汇总

```text
总计 25 行
PASS(local)   1   RT-00
PARTIAL       2   RT-17, RT-24
FAIL(code)    1   RT-21   ← 当下缺陷，需缺陷单
GAP(code)     17  RT-02..16, RT-19, RT-20   ← v2.5.0 待落地，非回归
UNVERIFIED    3   RT-01, RT-18, RT-22
OPEN          1   RT-23
```

六方覆盖：会话行覆盖全部六方；重连/幂等行覆盖 DSH、WorkBuddy、浏览器、OpenClaw；委托与权限行覆盖 Agent 专家与子 Agent；平台差异行覆盖微信与 DSH。**没有任何一方拿到“实时协作已就绪”的判据。**

## 4. 复跑命令（本机，最短路径）

```bash
# RT-00 契约
cd E:/GPT/im2a-v2.3.45 && npm test && node scripts/validate-realtime.mjs

# RT-21 反例：A 订阅自己的流，B 发一条只给 C 的消息
GW=http://127.0.0.1:18080; TOKEN=$IM2A_HARNESS_TOKEN; A=hs_a; B=hs_b; C=hs_c
curl -sN "$GW/v1/harness/stream?session=$A" -H "authorization: Bearer $TOKEN" &
curl -s -X POST "$GW/v1/harness/messages" -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' -d "{\"from\":\"$B\",\"to\":\"$C\",\"text\":\"only-for-C\"}"
# 判据：A 的流里**不得**出现 only-for-C。当前会出现 → RT-21 复现。
```

## 5. 证据格式

每个用例落地一个文件，缺文件即视为未执行（不接受口头结论）：

```json
{
  "schemaVersion": "1.0",
  "case": "RT-21",
  "party": ["browser", "dsh"],
  "checkedAt": "2026-10-30T15:00:00.000Z",
  "env": { "gateway": "http://127.0.0.1:18080", "platform": "2.4.6", "go": "2.3.26" },
  "command": "curl -sN $GW/v1/harness/stream?session=hs_a ...",
  "observed": { "a_stream_saw_only_for_c": true, "events": 12 },
  "verdict": "FAIL",
  "gap": "G3"
}
```

## 6. 本矩阵禁止的声明（写下来是为了拒绝）

```text
“六方实时协作已就绪”           —— RT-01/02/03/04/06/07 任一未通过即不成立
“断线不丢消息”                 —— 被 RT-06/07/08 阻塞
“跨会话隔离”                   —— 被 RT-21 直接否决
“撤销立即生效”                 —— 被 RT-04/19 阻塞
“支持 N 人同房间”              —— 必须先给出 outboundQueueBytes、presence 合并频率与 p95（规则 B1），本矩阵不含压测行
“微信支持流式/未读塑形”        —— 未做 RT-22 前不得写
```
