# 序号实例审计（task-11 缺陷 2，诊断用，未改造）

> **v3 决策 D8=B（2026-10-25）补充**：人类消息的投递序与断线续传由腾讯云 IM PaaS 负责；
> 本文件 §4 的 `deliverySeq` 方案只适用于**本地消息与 Agent 消息**，不再用于人类消息续传。
>
> 日期：2026-10-25 ｜ 口径：【实测】= 本次在 `E:\GPT\im2a-work\im2agent` 读码/远端起 httptest 复现；【推断】= 依据代码语义推出，未跑生产。
> 结论先行：**是正确性 bug（P0 级隐患，当前部署形态下表现为 P1）**，但成因与任务描述略有出入 —— 见 §1 的实测更正。

## 1. 三处实例 & 一处实测更正

全仓 `grep 'msgseq.New('`【实测】只有 3 处：

| # | 位置 | 承载路由 | 用途 | 持久化 | 重启后 |
|---|---|---|---|---|---|
| A | `internal/api/msg.go:36`（`NewMsgMux`） | `POST /v1/msg/seq`、`/v1/msg/seq/batch`、`GET /v1/msg/stats` | 单独发号/批量预留 | ❌ 纯内存 map | 从 0 重新开始 |
| B | `internal/api/msg.go:84`（`NewMsgMuxWithStore`） | `POST /v1/msg/send`（分配 seq 并落 `messages`） | **生产主路径** | ❌ 纯内存 | 从 0 重新开始 |
| C | `services/msg-svc/main.go:42` | 独立服务，注册 `NewMsgMux` 全套 | 微服务形态的发号 | ❌ 纯内存 | 从 0 重新开始 |

**更正**：任务里写的第三处 `services/gateway/main.go:42` 实际是 `"human-agent-im/internal/api"` 这行 import；`services/gateway/main.go` **全文没有 `msgseq` 引用**【实测 `grep`】。

### 1.1 更严重的实测发现：`/v1/msg/seq` 在生产网关上是 404

`services/gateway/main.go:551` 只挂载了 B：`mux.Handle("/v1/msg/", api.NewMsgMuxWithStore(...))`。
而 A 是被 `NewMsgMuxWithStore` 以**绝对路径**再挂到 `/v1/seq`、`/v1/seq/`、`/v1/stats` 上的（`msg.go:90-92`）——外层 `ServeMux` **不会剥离前缀**，所以请求 `/v1/msg/seq` 到了 B 的内层 mux 后匹配不到任何 pattern → 404。

远端用同构 httptest 复现（Go 1.27）【实测】：

```
POST /v1/msg/send      -> 200 "send"
POST /v1/msg/seq       -> 404 "404 page not found"
POST /v1/msg/seq/batch -> 404 "404 page not found"
```

对照：`README.md:312-313` 仍把 `/v1/msg/seq`、`/v1/msg/seq/batch` 列为公开 API；而 `services/gateway/main.go:992` 的 `-selftest` 直接把 A 挂在 `/v1/msg/` 上、并在 `:1012` 断言它返回 200 —— **selftest 用另一种装配方式，所以绿；生产是 404**。这就是"测试通过但功能不可用"的典型结构。

## 2. 当前序号语义（B 路径）

- 键是 `senderID`（`msg.go:105` `gen.Next(req.SenderID)`），即**发送方维度的单调序号**，不是 `proto/msg.proto` 注释写的"会话内单调"。
- `messages.seq BIGINT NOT NULL` 落库，但 `002_v1.sql` **没有 `(sender_id, seq)` 唯一约束**，只有 `idx_messages_sender` 普通索引 → 重复 seq 会被静默接受。
- 客户端续传依据 `PullMessagesRequest.last_ack_seq`（`proto/msg.proto`），网关/服务端**没有任何代码按它做补发**（`internal/api/msg.go` 只有 seq 分配与 save；`PullMessages` 尚未实现）。

## 3. 是否"同一会话被两处实例混用"？—— 分两种情形

| 情形 | 判定 |
|---|---|
| **当前单进程网关（只有 B 可达）** | 同一会话只有一个发号器 → **不混用**。但重启即从 0 重发，与库里既有 `messages.seq` **直接撞号**（无唯一约束兜底）→ 同一会话出现重复 seq，客户端按 seq 排序/去重会丢消息。**这是现状下的真实 bug（P1）** |
| **msg-svc 与 gateway 同时部署（C + B 都可达）** | 同一 `senderID` 在两条路径上各有一个计数器 → **同一会话必然撞号**（`POST /v1/msg/seq` 预留 1，`POST /v1/msg/send` 又发 1）。C 目前只在 `Makefile SERVICES` 里可 `make run SVC=msg-svc` 启动，未进 docker-compose/systemd → 目前是**潜在** bug，一旦按微服务部署即触发 |

→ 所以"两处实例混用同一会话"的**结构性条件已经具备**（同一份代码、同一张表、两套计数器），只是当前部署形态让它尚未同时暴露。**结论：正确性 bug，必须修；优先级随 msg-svc 上线变为 P0。**

### 最小修复建议（不实施，Wave B 前必须做）

1. 把生成器收敛为**进程内单例**，经 `api.Deps` 注入（删掉 `msg.go:36/84` 两处 `New`，`msg-svc` 改为依赖同一实现而非另建）。
2. 启动时**播种**：`SELECT sender_id, MAX(seq) FROM messages GROUP BY sender_id` 灌回内存 map（用户量小，一次性可接受）；长期改为 DB 发号。
3. 立刻补唯一约束（先清重）：`CREATE UNIQUE INDEX uq_messages_sender_seq ON messages(sender_id, seq)` —— 让撞号从"静默错序"变成"显式 500"。
4. `PullMessages` 必须实现（现在 proto 有契约、代码没有），否则 `last_ack_seq` 无从谈起。

## 4. 若做 DB 单调的 per-recipient `deliverySeq`（Wave B），需要什么

三个对象，缺一不可：

```sql
-- 1) 收件方投递序号（权威、跨实例、重启不归零）
CREATE TABLE IF NOT EXISTS im_user_delivery_seq (
    user_id    BIGINT PRIMARY KEY,
    last_seq   BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 发号（单语句原子）：UPDATE ... SET last_seq=last_seq+1 RETURNING last_seq
-- 首次插入用 INSERT ... ON CONFLICT DO UPDATE 覆盖

-- 2) 投递日志（续传补发数据源；与序号同事务写入）
CREATE TABLE IF NOT EXISTS im_delivery_log (
    user_id    BIGINT  NOT NULL,
    seq        BIGINT  NOT NULL,
    msg_type   SMALLINT NOT NULL,
    payload    BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_delivery_log_expire ON im_delivery_log (created_at);

-- 3) 设备侧游标（重连时上报 last_ack_seq，内核据此决定补发起点）
CREATE TABLE IF NOT EXISTS im_device_cursor (
    device_id    TEXT PRIMARY KEY,
    user_id      BIGINT NOT NULL,
    last_ack_seq BIGINT NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

与现有资产的关系：`messages.seq`（发送方维度，业务展示）**保留不动**；`deliverySeq`（收件方维度，传输层）新增；两者不合并 —— 合并会破坏现有 `/v1/msg/list` 与 golden 快照。`im_device_cursor.device_id` 与本次新建的 `devices.id` 同源（`devices` 表此前缺失，已在 `035_devices.sql` 修复，`user_id` 为 TEXT，故此处 `user_id` 建议同样用 TEXT 保持一致）。

## 5. 顺带发现的第二个 P1（与序号无关，但同源：代码与 schema 脱节）

`internal/api/payment_ledger.go` 全部 SQL 写在 `payment_ledger_legacy_v09` 上（INSERT/SELECT/UPDATE 共 8 处），而该表在根 `migrations/20261023_*.sql` 里被定义为**退役归档**（`ALTER TABLE payment_ledger RENAME TO payment_ledger_legacy_v09`）；新的权威账本 `paid_message_ledger` 只被 admin 统计、privacy 导出和测试读取，**没有任何生产写入方**。
→ 扣费事实落在 legacy 表，`/v1/admin/stats` 的 `paid_total_cents/paid_charged` 恒为 0。建议：`payment_ledger.go` 改写 `paid_message_ledger`（或并入 `internal/ledger` 的 DB 实现），**不要**为了让它跑通而补建 legacy 表（见 `036_missing_tables.sql` 末尾说明）。
