# 付费注意力状态机（从代码精确抽取）

> 用途：给 `@im2a/openclaw-channel-relation` 插件实现与一致性测试提供**唯一事实来源**。
> 原则：每条结论带 `file:line`；无法确认标 `未验证`。
>
> 关键前提：**"付费状态"在本仓库有三层表示**，不是单一状态机：
> 1) **账本状态**（权威，`paid_message_ledger.status`：0/1/2，或内存 `ledger.Status` 同值）；
> 2) **付费消息副本状态**（`paid_flow.go` 内存 `paidMessage.Status`：`charged/delivered/rejected/expired` 等字符串）；
> 3) **HTTP 响应状态**（`blocked/below_price/auto_rejected/opened/already_charged` 等，部分**不落库**）。
> 三层之间**没有同步约束**，下面逐层说明。

---

## 0. 代码入口与三层状态的关系

| 面 | 代码 | 状态来源 | 说明 |
|---|---|---|---|
| `/v1/paidflow/*` | `internal/api/paid_flow.go`（662 行，`PaidFlowMux` :196-642） | 账本 `d.Store.Ledger`（PG 时是 `pgLedger`，无 PG 时内存 `ledger.Ledger`）+ 内存 `paidMessageStore`（:60-112） | 同进程内两份状态：账本权威 + 消息副本 |
| `/v1/paid/*` | `internal/api/payment.go`（`:34-112`，`PaymentMux`） | `d.Store.Ledger`（内存或 pgLedger） | reserve/charge/refund/usage/get；幂等键 = messageId |
| `/v1/ledger/*` | `internal/api/payment_ledger.go`（`:307-316`，`PaidMux`） | **直接 SQL** 读写 `paid_message_ledger` / `paid_message_events` / `blacklist` | 持久层 HTTP 面；UUID 映射见 :37-44 |
| 存储实现 | `internal/store/postgres.go`（`pgLedger` :221-374） | `paid_message_ledger` + `paid_message_events` | 生产 PG 路径 |
| 内存实现 | `internal/ledger/ledger.go`（:74-178） | map 内存 | 无 PG/测试/`payment-svc` 独立进程 |
| 路由挂载 | `internal/kernel/routes/routes_payment.go:6-10` | — | 全部走 `c.wrap`（认证/限流/审计/安全头，见 `routes.go:58-67`） |

> **实际生产路径**：网关有 PG 时 `deps.Store.Ledger` 是 `pgLedger`（`store/postgres.go:83-90`），
> 所以 `/v1/paidflow` 的账本操作也落 `paid_message_ledger`；`paidMessageStore` 仍是进程内副本（重启丢失）。
> 这意味着：**账本行 != 消息副本行**；重启后 `paidflow` 的 inbox/outbox/content 会空，但账本仍在。

---

## 1. 全部状态定义（代码为准）

### 1.1 账本状态（`internal/ledger/ledger.go:27-34`，持久层同值 `001_init.sql:110`）

| 值 | 名称 | 含义 | 证据 |
|---|---|---|---|
| 0 | `PENDING` | 已预审/已占位，待扣费 | `ledger.go:31`（`StatusPending`） |
| 1 | `CHARGED` | 已扣费（`charged_at` 非空） | `ledger.go:32`、`001_init.sql:111` |
| 2 | `REFUNDED` | 已退款（`refunded_at` 非空） | `ledger.go:33`、`001_init.sql:112` |

### 1.2 付费消息副本状态（`paid_flow.go:53` 注释 + 实际写入点）

`paidMessage.Status` 实际只会被写成：`charged`（:290）、`delivered`（:284）、`rejected`（:409）、`expired`（:552）。
HTTP 响应里还会出现 `blocked`/`below_price`/`auto_rejected`（:230/238/247/261/271）——**这些不创建 paidMessage 行**。

### 1.3 HTTP 响应状态（`PaidSendResponse` / open 返回）

| 状态 | 出现位置 | 是否落库 |
|---|---|---|
| `charged` | send（billingMode=1）:290/:309 | 副本状态 charged + 账本 CHARGED |
| `delivered` | send（billingMode=2）:284/:309 | 副本状态 delivered + 账本 PENDING |
| `blocked`（3 种 reason） | compliance :230；内存黑名单 :247；PG 黑名单 :261 | 否（无 messageId 返回） |
| `below_price` | :238 | 否 |
| `auto_rejected` | :271 | 否 |
| `opened` / `charged` | open :334/:356 | 副本状态**不更新** |
| `already_charged` | open :342 | 副本状态**不更新** |
| `rejected` | reject :411 | 副本状态 rejected |
| `expiredCount`（批） | expire :559 | 副本状态 expired |
| `ledgerStatus`（int） | export :585 | 账本 0/1/2，缺失 -1 |

---

## 2. 发送决策流水线（`/v1/paidflow/send`，顺序即代码顺序）

请求：`PaidSendRequest{senderId, receiverId, content, billingMode(1|2), amountCents}`（`paid_flow.go:175-181`）。
入口校验（:206-222）：四字段必填、`billingMode∈{1,2}`、`amountCents>0`，否则 400。

按顺序：

| 步 | 条件 | 结果状态 | 副作用 | 证据 |
|---|---|---|---|---|
| 0 | 生成 `msgID := id.UUIDv4()` | — | 每次请求**新 ID**（见 §5 幂等） | :225 |
| 1 | compliance 预审：禁词（诈骗/汇款到/中奖/兼职刷单/赌博）、手机号、外链 | `blocked` + reason | 写审计（`ActionContentReject`） | :224-233；`compliance/guard.go:26-57` |
| 2 | 接收方最低价：`amount < minPrice` | `below_price` | 无 | :235-242 |
| 3 | 内存黑名单（本进程 `block` 接口写入） | `blocked`（"you are blocked by receiver"） | 无 | :244-250、:472-488 |
| 4 | **PG 黑名单**（`blacklist WHERE user_id=$sender`） | `blocked`（"sender is on blacklist"） | 无 | :252-265；表 DDL `036_missing_tables.sql:113-120` |
| 5 | 接收方偏好：`amount >= autoRejectAboveCents`（仅 >0 时生效） | `auto_rejected` | 无 | :267-274 |
| 6 | `Ledger.Reserve(msgID,...)` | 失败→HTTP 映射 | 账本 PENDING（+日限计数/事件） | :276-281 |
| 7 | billingMode=1：`Ledger.Charge(msgID,"")` | `charged` | 账本 CHARGED、`charged_at` | :283-291 |
| 8 | 写审计 `PAID_MESSAGE_SENT` | — | audit | :293-300 |
| 9 | 写内存副本 `paidMessage{Status}` | — | `paidMessageStore.Put` | :302-306 |

> **autoAccept 未实现**：请求里只有 `autoRejectAboveCents` 被读（:269）；`AutoAcceptBelowCents` 只在 preferences 接口写入（:504-528），
> **全代码没有读取点**。README :129 与 CHANGELOG v0.9.8 声称"自动接受/自动拒绝"后者成立、前者不成立（见 §7）。

---

## 3. 状态转移表（from → event → to + side effects）

### 3.1 账本（权威状态，内存/PG 语义一致；PG 额外写 events）

| from | event | to | side effects / 错误 | 证据 |
|---|---|---|---|---|
| ∅ | `Reserve(messageID,...)` | PENDING(0) | 日限检查；内存 `dailySent++`；PG INSERT + `paid_message_events(0→0,reserved)` | 内存 `ledger.go:90-115`；PG `postgres.go:226-262` |
| PENDING | `Charge(id, approvalCardID)` | CHARGED(1) | 置 `charged_at`；内存条目原地改；PG UPDATE+事件 0→1 | `ledger.go:117-140`；`postgres.go:264-299` |
| CHARGED | `Refund(id, reason)` | REFUNDED(2) | 置 `refunded_at`/`refund_reason`；PG 事件 1→2 | `ledger.go:142-156`；`postgres.go:301-332` |
| CHARGED | `Charge` again | 不变 | `ErrAlreadyProcessed` → HTTP 409 | `ledger.go:128-130`；`payment.go:117-133` |
| REFUNDED | `Charge` | 不变 | `ErrInvalidTransition` → 409 | `ledger.go:131-133` |
| PENDING/REFUNDED/∅ | `Refund` | 不变 | PENDING→409；缺行→404 | `ledger.go:151-152,162-164` |
| 任意 | `Get` 缺失 | — | `ErrNotFound` → 404 | `ledger.go:158-167` |

### 3.2 付费消息副本（内存 `paidMessageStore`，**仅影响 inbox/outbox/export**）

| from | event | to | side effects | 证据 |
|---|---|---|---|---|
| ∅ | send mode1 | `charged` | 副本创建 | :284,302-306 |
| ∅ | send mode2 | `delivered` | 副本创建 | :284,302-306 |
| `charged`/`delivered` | `POST /paidflow/reject`（任意人可调） | `rejected` | 若账本已 charged → Refund；写审计 `PAID_MESSAGE_REJECTED` | :378-413 |
| `delivered` | `POST /paidflow/expire`（默认 >86400s） | `expired` | 若账本已 charged → Refund | :530-560 |
| `charged` | `POST /paidflow/open` | **不变** | 返回 `opened`（mode1 charged=false） | :331-337 |
| `delivered` | `POST /paidflow/open` | **不变** | 账本 Charge；返回 `opened` charged=true | :339-357 |
| `delivered` | 再次 open | 不变 | 账本已 charged → `already_charged` | :340-345 |
| 任意 | `open` 后 `expire` | `expired` | 若已 charged → **退款**（唯一实际触发 expired 退款的分支） | :546-557 |

### 3.3 `/v1/ledger/*`（直接 SQL 状态机，与 3.1 同状态值但独立实现）

| 接口 | 行为 | 证据 |
|---|---|---|
| `POST /v1/ledger/charge` | 幂等插入 status=1：`ON CONFLICT(message_id) DO NOTHING`；冲突→读当前 status 返回 `idempotent:true`；日限 10（COUNT sender, `created_at>=CURRENT_DATE`，含已退款） | `payment_ledger.go:90-152` |
| `POST /v1/ledger/open` | `replied!=true` → 200 `{charged:false,reason:"not replied"}`；messageId 不存在 404；`receiverId` 不匹配 403；status=1 → 200 idempotent；status=2 → 409；PENDING→UPDATE status=1,billing_mode=2 + 事件 0→1 | `payment_ledger.go:189-243` |
| `POST /v1/ledger/refund` | 不存在 404；status=2 → 200 idempotent；非 CHARGED → 409；CHARGED→REFUNDED + 事件 1→2 | `payment_ledger.go:245-283` |
| `POST /v1/ledger/blacklist` | `INSERT blacklist(user_id TEXT,...) ON CONFLICT DO NOTHING` | `payment_ledger.go:285-305` |
| `GET /v1/ledger/` | 按 sender/receiver 查 50 行，展示 `biz_message_id` 优先 | `payment_ledger.go:154-187` |

> 注意：`/v1/ledger/charge` 的 `messageId` 是**任意字符串**，内部用 md5 确定性映射为 UUID（`payment_ledger.go:37-44`），
> 原串写 `biz_message_id`（038 迁移），所以对外仍可用业务单号。

---

## 4. 非法转移与错误码（供一致性测试直接引用）

| 场景 | 账本错误 | HTTP | 证据 |
|---|---|---|---|
| 同 messageId 重复 Reserve | `ErrAlreadyProcessed` | 409 | `ledger.go:96-98`；`payment.go:117-133` |
| 同 messageId 重复 Charge | `ErrAlreadyProcessed` | 409 | `ledger.go:128-130` |
| 对 REFUNDED 再 Charge | `ErrInvalidTransition` | 409 | `ledger.go:131-133` |
| 对 PENDING 直接 Refund | `ErrInvalidTransition` | 409 | `ledger.go:151-152` |
| 对 REFUNDED 再 Refund | `ErrInvalidTransition` | 409 | 同上 |
| messageId 不存在 | `ErrNotFound` | 404 | `ledger.go:162-164` |
| 日限超限（内存/PG/ledger charge） | `ErrDailyLimitExceeded` | 429（`/v1/ledger/charge` 也 429） | `ledger.go:99-103`；`postgres.go:230-239`；`payment_ledger.go:115-124` |
| `/v1/ledger/open` 已退款 | —（SQL 分支） | 409 "message already refunded" | `payment_ledger.go:226-229` |
| `/v1/ledger/refund` 非 CHARGED | — | 409 | `payment_ledger.go:269-272` |
| `/v1/paidflow/reject` 已退款消息 | Refund 返回 invalid | 409 | `paid_flow.go:397-403` + `mapLedgerErr` |

`mapLedgerErr` 的完整映射（`payment.go:117-141`）：AlreadyProcessed→409、DailyLimit→429、InvalidTransition→409、
NotFound→404、其它→500；并写审计 `"<op>_ERROR"`。

> 注意：`/v1/paidflow/reject` 与 `/v1/paidflow/expire` **没有** HTTP 幂等分支：
> 第二次 reject 会走到 Refund → 409（不是 200 idempotent）；重复 expire 不会重复处理（状态已 expired，:546 跳过）。

---

## 5. 幂等键与"到底能重试什么"（最容易坑插件实现）

| 接口 | 幂等键 | 可安全重试？ | 证据 |
|---|---|---|---|
| `POST /v1/paid/reserve` | 调用方给 `messageId`；为空则自动生成 | **可**（同 ID 重试 → 409 而非双扣） | `payment.go:39-58`（空 ID 自动生成 :49-50） |
| `POST /v1/paid/charge` / `refund` | `messageId` | 可（按账本状态机返回 409/幂等） | `payment.go:61-94` |
| `POST /v1/paidflow/send` | **无客户端幂等键**：每次请求生成新 UUID | **不可**：重试=新消息/新扣费 | `paid_flow.go:225`（`msgID := id.UUIDv4()`） |
| `POST /v1/paidflow/open` | `messageId` | 可（二次 open：mode2 返回 already_charged，不双扣） | `paid_flow.go:339-345` |
| `POST /v1/paidflow/reject` | `messageId`（但无幂等分支） | 不可：二次 409（非 200） | `paid_flow.go:378-413` |
| `POST /v1/paidflow/expire` | 消息状态 | 可：只处理 delivered 一次 | `paid_flow.go:546-557` |
| `POST /v1/ledger/charge` | `messageId`（md5→UUID；`biz_message_id` 唯一） | 可：ON CONFLICT 返回当前状态 + `idempotent:true` | `payment_ledger.go:37-44,126-149` |
| `POST /v1/ledger/open` / `refund` | `messageId` | 可：已 charged/refunded 返回幂等 200 | `payment_ledger.go:222-229,265-268` |
| 黑名单 | `blacklist.user_id` 主键 | 可：ON CONFLICT DO NOTHING | `036_missing_tables.sql:113-120` |

---

## 6. 条件参数（billing mode / 定价 / 日限 / 黑白名单 / 预算）

| 维度 | 事实 | 证据 |
|---|---|---|
| billingMode | 1=发送即扣（Reserve 后立即 Charge）；2=打开/回复时扣 | `ledger.go:20-25`；`paid_flow.go:283-291`、`payment_ledger.go:69-76`（send→1 / pay_open→2） |
| 最低定价 | 接收方通过 `POST /paidflow/price` 设置，`amount < min` → below_price；**minCents 无下限校验（可传负数，等于无限制）** | `paid_flow.go:235-242,360-376` |
| 自动拒绝 | `amount >= autoRejectAboveCents` 且该值>0 → auto_rejected | `paid_flow.go:267-274`、:504-528 |
| 自动接受 | **字段存在但从未被读取** | `paid_flow.go:149-150`（定义）、:519-522（写入）、全文无读取 |
| 黑名单 | 两层：内存 blocklist（接收方维度，进程内）+ PG `blacklist`（平台/用户维度，`user_id TEXT`） | `paid_flow.go:114-145,244-265`；`036_missing_tables.sql:113-120` |
| 日限 | 默认 10/天/sender；内存按进程内计数（重启清零）；PG 按 `sender_id + created_at::date = current_date`（**含已退款**）；`/v1/ledger/charge` 另有一份硬编码 10 | `ledger.go:83-88,99-103`；`postgres.go:83-90,230-239`；`payment_ledger.go:114-124` |
| 余额/预算 | **付费流程里不存在余额/预算/钱包检查**（无 wallet/balance/budget 字段或读取）；README v1.8.1 的"月预算钱包"是 Agent 侧能力，与本流程无关 | grep 结果；`README.md:269`（另一能力） |
| 审计 | 每条发送写 `PAID_MESSAGE_SENT`；open 成功写 `RECEIVER_OPEN_AND_CHARGED`；reject 写 `PAID_MESSAGE_REJECTED`；失败写 `<op>_ERROR` | `paid_flow.go:293-300,350-354,404-408`；`payment.go:117-120` |

---

## 7. 代码 vs 文档不一致清单（最有价值部分）

| # | 文档声称 | 代码实际 | 证据 |
|---|---|---|---|
| 1 | "接收方偏好（autoReject / **autoAccept**）"（`README.md:129`；v0.9.8 "自动接受/自动拒绝" `README.md:193`） | **autoAccept 从不生效**：`AutoAcceptBelowCents` 只写不读 | `paid_flow.go:149-150,519-522`；读取仅 `autoRejectAboveCents` :269 |
| 2 | "账本不可变"（`README.md:104`） | 内存账本/`pgLedger` 都**原地 UPDATE 状态**（非 append-only）；append-only 的是 `paid_message_events` 事件表 | `ledger.go:134-135,154`；`postgres.go:282-286,316-319`；`001_init.sql:123-132` |
| 3 | 状态机画 `opened` 为消息状态（`README.md:115`） | `open` **不更新** `paidMessage.Status`（仍是 delivered/charged）；`opened` 只是响应值 | `paid_flow.go:331-357`（无 `msgs.SetStatus`） |
| 4 | "rejected → 自动退款"（`README.md:116`） | 仅当账本已 CHARGED 才退款；mode2 `delivered`（未扣费）拒绝 → **无退款**；且 reject 无调用者身份校验 | `paid_flow.go:397-403` |
| 5 | "expired（超时未打开 → 自动关闭/退款）"（`README.md:117`） | expire 只扫 `delivered`；而 delivered 在 open 之前**未扣费**→退款分支不触发；只有"先 open 扣费（状态仍 delivered！）再 expire"才会退款；mode1 `charged` 永不 expires | `paid_flow.go:544-558` |
| 6 | "message_id 幂等"（`README.md:104` 泛指付费消息） | `/paidflow/send` **每次生成新 UUID**，重试=新消息/双扣；幂等只对显式 messageId 的 `/v1/paid/*`、`/v1/ledger/*` 成立 | `paid_flow.go:225` vs `payment.go:39-58`、`payment_ledger.go:126-149` |
| 7 | "单日上限"（`README.md:104` 泛指） | 三处上限语义不同：内存账本（进程内，重启清零）/PG 账本（含退款计数）/`/v1/ledger/charge`（硬编码 10 独立计数） | `ledger.go:83-88,99-103`；`postgres.go:230-239`；`payment_ledger.go:114-124` |
| 8 | "查看内容需 receiverId 或 senderId 匹配"（`paid_flow.go:457` 注释） | 实现**只校验 messageId**，任何人拿到 messageId 即可读付费内容 | `paid_flow.go:457-470` |
| 9 | `billingMode=2` 注释"查看并回复才扣费"（`ledger.go:23`、`paid_flow.go:179`） | `/paidflow/open` **仅打开就扣费**；`/v1/ledger/open` 才有 `replied` 参数（未回复不扣） | `paid_flow.go:339-357` vs `payment_ledger.go:202-206` |
| 10 | 拦截顺序"3. 黑名单检查"（`README.md:128`） | 实际有**两层黑名单**（内存 + PG），且检查顺序为 compliance→价格→内存黑名单→PG 黑名单→autoReject | `paid_flow.go:224-274` |
| 11 | 统计"平均单价"直觉 | `sentAvgCents = sentSpent / sentCount`，但 `sentSpent` 排除 rejected、`sentCount` 包含 rejected → 平均被拉低 | `paid_flow.go:603-629` |

---

## 8. 判定性场景（供 task-35 一致性套件直接实现）

> 统一约定：内存/无 PG 路径用 `d.Store.Ledger`；PG 路径需 `PG_DSN`。测试应分别覆盖两条路径（状态值相同、落库不同）。

**S1 — billingMode=1 发送即扣 → 拒收退款（终态 REFUNDED/rejected）**
1) `POST /paidflow/price {receiverId:R, minCents:100}`；
2) `POST /paidflow/send {senderId:S, receiverId:R, content:"hi", billingMode:1, amountCents:100}` → **200 status=charged**，账本 status=1；
3) `POST /paidflow/reject {messageId:<上一步>, reason:"no"}` → 200 status=rejected，账本 status=**2(REFUNDED)**；
4) 再次 reject 同 messageId → **409**（非法转移，不是幂等 200）。

**S2 — billingMode=2 打开才扣 → 重复打开不双扣**
1) `POST /paidflow/send {... billingMode:2, amountCents:100}` → status=**delivered**，账本 0；
2) `POST /paidflow/open {messageId}` → 200 `{status:"opened", charged:true}`，账本 1；
3) 再次 open → 200 `{status:"already_charged", charged:true}`，账本仍 1（只扣一次）。

**S3 — compliance / below_price / auto_rejected 三类"不落账"拒绝**
1) content 含"汇款到" → 200 `blocked` + reason 含 `prohibited keyword`，无账本行；
2) `minCents=100` 时 amount=99 → 200 `below_price`，无账本行；
3) `preferences{autoRejectAboveCents:100}` 后 amount=100（>=）→ 200 `auto_rejected`，无账本行（注意在 reserve 之前）。

**S4 — 显式 messageId 的幂等（`/v1/paid/*`）**
1) `POST /paid/reserve {messageId:"m1",...}` → 200 status=0；
2) 重复 reserve 同 m1 → **409**；
3) `POST /paid/charge {messageId:"m1"}` → 200 status=1；
4) 重复 charge → **409**；
5) `POST /paid/refund {messageId:"m1"}` → 200 status=2；重复 refund → 409。

**S5 — `/v1/ledger/*` 的幂等与状态机（持久层，需 PG）**
1) `POST /v1/ledger/charge {messageId:"biz-1", senderId:"1", receiverId:"2", amountCents:100, mode:"send"}` → 200 charged；
2) 重复同 messageId → 200 `idempotent:true`（不是 409）；
3) `POST /v1/ledger/open {messageId:"biz-1", receiverId:"2", replied:true}` → 200 `charged:true,idempotent:true`（已 charged）；
4) `POST /v1/ledger/refund {messageId:"biz-1"}` → 200；重复 refund → 200 idempotent；
5) 对已退款消息再次 open → **409 "message already refunded"**。

**S6 — 日限（11th 拒绝）**
1) 同一 sender 连续成功 10 次（billingMode=2 reserve 不扣也计数）→ 第 11 次 `send` 返回 **429**（`ErrDailyLimitExceeded`）；
2) 换 sender 或换自然日（需注入时钟）→ 恢复；
3) 注意：PG 路径把已退款行也计入当日计数（`postgres.go:232-233` 无 status 过滤）。

---

## 9. 未验证 / 风险（插件实现前需产品确认）

1. **actor 授权缺失**：`/v1/paidflow/*` 没有任何"调用者 = sender/receiver"校验；只靠 `c.wrap` 的网关 token/JWT
   （`routes.go:58-67`）+ IP 限流。任何持网关凭据者可替任意 sender 发送、替任意 receiver 拒收/退款/过期。
   证据：`paid_flow.go:205-310`（send 直接用 body senderId）、:378-413（reject 只用 messageId）、:530-560（expire 无身份）。
2. `/v1/paidflow/content` 泄漏风险（见 §7 #8）：有 messageId 即可读正文。
3. `expire` 可被外部任意触发，且没有"接收方/系统任务"身份要求。
4. 内存副本 `paidMessageStore` 与账本分离：重启/多实例下 inbox/outbox 与账本不一致（PG 路径账本在，副本丢）。
5. 日限的"自然日"依赖 DB/server 时区（`created_at::date=current_date` / `created_at>=CURRENT_DATE`），跨时区部署需统一 TZ。
6. `paid_flow.go` 的 `price`/`preferences`/`block` 等配置**均是进程内存**（重启丢失）；生产需持久化（当前未实现）。
7. 本文件只覆盖**网关进程内**代码路径；`payment-svc` 独立进程使用内存 `ledger.New(10)`（`services/payment-svc/main.go:41`），与网关 PG 账本**不共享**（多实例部署时需确认路由/流量归属）。

---

## 10. 证据索引（抽样 20+ 条）

| # | 结论 | file:line |
|---|---|---|
| 1 | 账本三状态 | `internal/ledger/ledger.go:27-34`；`internal/store/migrations/001_init.sql:110-116` |
| 2 | Reserve 幂等/日限 | `internal/ledger/ledger.go:90-115` |
| 3 | Charge 状态机 | `internal/ledger/ledger.go:117-140` |
| 4 | Refund 状态机 | `internal/ledger/ledger.go:142-156` |
| 5 | send 流水线 | `internal/api/paid_flow.go:204-311` |
| 6 | compliance 规则 | `internal/compliance/guard.go:24-58` |
| 7 | 价格/黑名单/autoReject | `internal/api/paid_flow.go:235-274`；`036_missing_tables.sql:113-120` |
| 8 | autoAccept 只写不读 | `internal/api/paid_flow.go:149-150,519-522`（无读取） |
| 9 | open 语义 | `internal/api/paid_flow.go:313-358` |
| 10 | reject/expire | `internal/api/paid_flow.go:378-413,530-560` |
| 11 | content 无归属校验 | `internal/api/paid_flow.go:457-470` |
| 12 | 统计平均口径 | `internal/api/paid_flow.go:595-640` |
| 13 | PG 账本实现 | `internal/store/postgres.go:221-374` |
| 14 | PG 日限含退款行 | `internal/store/postgres.go:230-239` |
| 15 | 事件表 append-only | `internal/store/migrations/001_init.sql:123-132`；`postgres.go:253-255,290-292,323-325` |
| 16 | `/v1/ledger/*` 状态机 | `internal/api/payment_ledger.go:90-283` |
| 17 | UUID 映射 + biz_message_id | `internal/api/payment_ledger.go:37-44`；`internal/store/migrations/038_ledger_biz_message_id.sql` |
| 18 | 日限硬编码 10（ledger/charge） | `internal/api/payment_ledger.go:114-124` |
| 19 | 错误码映射 | `internal/api/payment.go:117-133` |
| 20 | 路由与认证链 | `internal/kernel/routes/routes_payment.go:6-10`；`internal/kernel/routes/routes.go:58-67`；`internal/auth/identity.go:59` |
| 21 | 文档状态机/拦截顺序 | `README.md:99-131` |
| 22 | 文档"不可变/幂等/单日上限" | `README.md:104` |
| 23 | 文档 v0.9 能力清单 | `CHANGELOG.md:765`；`README.md:345-357`（端点表） |
| 24 | 双账本历史（供参考） | `docs/notes-ledger-consolidation.md:24-64` |
