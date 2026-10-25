# 双账本合并：数据流与字段差异（task-12 / v2.3.27）

> 日期：2026-10-25 ｜ 口径：【实测】= 读码 + 远端编译/测试；【未验证】= 需有 PG 环境确认。
> 结论：**目标表是 `paid_message_ledger`**；`/v1/ledger/*` 五个端点已从退役表改指过来，
> 统计侧（admin/privacy/ops）**一行未改**。新增 1 个迁移（038，纯增量列）。

## 0. 先更正 task-11 回报里的一处错误

task-11 我写"`paid_message_ledger` 无任何生产写入方"——**这是错的**，原因是我当时 grep 输出
被 `Select-Object -First 12` 截断，漏看了 `internal/store/postgres.go` 的 `pgLedger`。
实际状态是：

- `paid_message_ledger` **有完整生产写入方**：`internal/store/postgres.go:221-380` 的 `pgLedger`
  （Reserve/Charge/Refund/Get/DailyUsage，含状态机、`paid_message_events` 事件、日限、`$1::uuid`）。
- 真正的问题是**同一个业务存在两套并存实现**：新链路（`/v1/payment/*`、`paid_flow.go`）走
  `d.Store.Ledger` → `pgLedger` → 写 `paid_message_ledger`；而老的 `/v1/ledger/*`
  （`payment_ledger.go`）自己手写 SQL 写**退役表** `payment_ledger_legacy_v09`。
- 因此"统计恒为 0"只在**只走老端点**的部署里成立；只要走新链路，统计是能看到的。修正后
  这个 bug 的准确定性是：**老端点写死表 → 数据落在统计/privacy 都读不到的地方（并且全新库
  直接 42P01）**，而不是"整条计费链路的统计恒 0"。

## 1. 两张表的全部读写点【实测】

### 1.1 `paid_message_ledger`（权威表，001_init.sql:103）

| 角色 | 位置 |
|---|---|
| 写入（状态机） | `internal/store/postgres.go:242` Reserve INSERT(status=0)<br>`:283` Charge UPDATE(status 0→1, charged_at, approval_card_id)<br>`:317` Refund UPDATE(status 1→2, refunded_at, refund_reason) |
| 读取 | `internal/store/postgres.go:232` 日限 count、`:269`/`:306` 状态读、`:343` Get、`:366` DailyUsage |
| 事件 | `:254`/`:291`/`:324` INSERT `paid_message_events` |
| 统计 | `internal/api/admin.go:33-35`（sum/count，status=1/2）、`:113` 时间序列（charged_at） |
| 列表 | `internal/api/admin.go:136`（id/message_id/sender/receiver/billing_mode/amount/status/created_at/charged_at） |
| 隐私 | `internal/privacy/privacy.go:151`/`:157`（按 sender/receiver 导出，注释说明为"财税凭证"） |
| 测试 | `internal/api/v176_real_test.go:62`、`v177_real_test.go:68`、`internal/store/pgledger_real_test.go` |
| **HTTP 老端点（本次改指）** | `internal/api/payment_ledger.go`（charge/list/open/refund 全部 SQL） |

### 1.2 `payment_ledger_legacy_v09`（退役表）

| 角色 | 位置 |
|---|---|
| 来源 | 根 `migrations/20261023_migrate_payment_ledger_to_paid_message_ledger.sql`：`ALTER TABLE payment_ledger RENAME TO payment_ledger_legacy_v09`，注释「老表只读归档」；**该文件不在 `internal/store/migrations/` 内嵌目录**，`store.Migrate` 不会执行它 |
| 原写入 | `internal/api/payment_ledger.go:50`（charge）、`:121`（open→charged）、`:147`（refund） |
| 原读取 | `:34`（日限）、`:45`（幂等）、`:67`（列表）、`:107`（open 查单）、`:142`（退款查单） |
| 原统计 | `internal/api/ops.go:34`（`im2a_ledger_total`） |
| 现状 | 改指后**代码零引用**；`internal/store/schema_contract_test.go` 的白名单已清空，若有人写回去会立刻红灯 |

## 2. 字段差异表【实测：001_init.sql:103-117 vs 根迁移 + payment_ledger.go 的 INSERT 列】

| 语义 | `paid_message_ledger`（目标） | `payment_ledger_legacy_v09`（退役） | 兼容处理 |
|---|---|---|---|
| 主键 | `id BIGSERIAL PK` | `id BIGSERIAL PK` | 一致 |
| 业务消息 id | `message_id UUID NOT NULL`，`UNIQUE(message_id)` | `message_id TEXT`（无唯一约束） | **不兼容**：HTTP 入参是任意字符串 → 用 `md5('im2a:ledger:biz:'||id)::uuid` 确定性派生；原串存新增列 `biz_message_id`（038） |
| 发送方 | `sender_id BIGINT NOT NULL` | `sender_id TEXT` | **不兼容**：非数字入参 → 400（不做隐式哈希，账本必须可对账） |
| 接收方 | `receiver_id BIGINT NOT NULL` | `receiver_id TEXT` | 同上 |
| 计费模式 | `billing_mode SMALLINT NOT NULL CHECK (1,2)` | `mode TEXT`（'send'/'pay_open'） | `send/空→1`，`pay_open→2`；响应仍回字符串 |
| 金额 | `amount_cents BIGINT NOT NULL CHECK (>=0)` | `amount_cents`（无 CHECK） | 直接对应；入参 ≤0 仍 400 |
| 状态 | `status SMALLINT NOT NULL DEFAULT 0 CHECK (0,1,2)` | `status TEXT`（'pending'/'charged'/'refunded'/'blocked'） | 写：直接 0/1/2；读：`1→charged,2→refunded,else pending` |
| 扣费时间 | `charged_at TIMESTAMPTZ NULL` | 无 | charge/open 时 `now()` |
| 退款时间 | `refunded_at TIMESTAMPTZ NULL` | 无 | refund 时 `now()` |
| 退款原因 | `refund_reason VARCHAR(256) NULL` | 无（丢弃） | refund 时写入（**新增能力**） |
| 审批卡 | `approval_card_id VARCHAR(64) NULL` | 无 | 老端点留 NULL |
| 创建时间 | `created_at TIMESTAMPTZ NOT NULL DEFAULT now()` | `created_at TIMESTAMPTZ` | 一致 |
| 事件溯源 | `paid_message_events`（0/1/2 迁移各写一条） | 无 | 老端点的 charge/refund 现在补写事件（审计更完整） |
| 原串检索 | 新增 `biz_message_id TEXT` + 部分唯一索引（038） | `message_id TEXT` | 列表回显原串，历史行 `COALESCE(biz_message_id, message_id::text)` |

## 3. 需要的新增 DDL（已加迁移 `038_ledger_biz_message_id.sql`，纯增量）

```sql
ALTER TABLE paid_message_ledger ADD COLUMN IF NOT EXISTS biz_message_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_biz_message
    ON paid_message_ledger (biz_message_id) WHERE biz_message_id IS NOT NULL;
COMMENT ON COLUMN paid_message_ledger.biz_message_id IS
    'v2.3.27：/v1/ledger/* 的原始业务 messageId（message_id 列存派生 UUID，二者一一对应）';
```

为什么必须加这一列：`paid_message_ledger.message_id` 是 UUID，而对外契约（`/v1/ledger/` 列表、
退款、查单）需要按**原始字符串**寻址与展示；只存派生 UUID 会让老客户端拿到哈希。
不加列、改用视图的替代方案被否决：视图无法让 INSERT 同时写两列，需要 INSTEAD OF 触发器，
比一列 + 索引复杂且更难解释。

## 4. 不变量（改完必须仍成立，已由测试覆盖）

1. **幂等键 = messageId**：同一 messageId 只落一行；重复 charge 返回 `idempotent:true` 且不二次计钱
   （实现从「SELECT 再 INSERT」升级为 `ON CONFLICT (message_id) DO NOTHING`，关掉了并发窗口）。
   注意：`(message_id)` 与 `(biz_message_id)` 两个唯一约束**同时存在**，任一冲突都会挡住重复。
2. **状态机**：写路径只产生 `0→1`（open）、`1→2`（refund）；非法迁移显式 409
   （pending 退款不再被静默改成 refunded —— 老实现会）。
3. **日限**：每 sender 每日 10 次，统计口径与老实现一致（当日全部落账行，不看 status）。
4. **金额**：`amount_cents` 直传，CHECK `>=0` 兜底；统计口径 `status=1` 求和，退款后自动从总和里退出。

## 5. 未验证 / 需在有 PG 的环境确认

- 迁移 037/038 与两张表的真实 DDL 执行：本机与服务器**都没有 PG**，`go test` 的 PG 用例全部
  `SKIP`。需在 CI/测试库跑一次：
  `PG_DSN=... go test ./internal/api -run 'TestLedgerConsolidation|TestSeqPersistence' -v`
  以及 `PG_DSN=... go test ./internal/store -run TestMigrationContract -v`。
- 历史数据：若生产 `payment_ledger_legacy_v09` 里已有真实流水，**本改动不做数据迁移**
  （把 TEXT message_id 映射成 UUID 需要业务决策：用 `md5('legacy:v0.9:'||msg)` 还是重新分配）。
  如需要，可参照根迁移的确定性映射写一个一次性回填脚本（未实现，因缺少生产样本）。
- 老客户端若传非数字 wxid：现在会 400。当前仓库内没有任何生产调用方传非数字 id
  （唯一调用方是测试，且用 DB=nil 路径），但外部客户端需确认。
