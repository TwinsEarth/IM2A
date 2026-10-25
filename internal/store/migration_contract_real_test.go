package store

// 迁移契约测试（真 PG）：PG_DSN 未设置时自动 skip（沿用 pgledger_real_test.go 的 newRealDB）。
//
// 覆盖三个此前**没有任何测试覆盖**的失败模式：
//   1) 全新库能否从 001 一路跑到 040（含 040_seq_user_id；历史上 004_im.sql 的 ALTER TABLE
//      im_webhook_events 会因缺表直接 42P01，导致 Migrate 返回 error、配置 PG_DSN 的服务起不来）；
//   2) Migrate 重复执行是否幂等（服务每次启动都会全量重跑迁移）；
//   3) devices / ticket_state 等表是否真的可写可读（此前只有测试夹具手工建表，
//      生产 DDL 缺失被长期掩盖）；
//   4) 序列 seq_user_id 是否真实可发号（v2.3.28 真库首验缺陷 3：表差集看不见序列）。
//
// 运行方式：
//   PG_DSN='postgres://user:pass@127.0.0.1:5432/im2a_test?sslmode=disable' \
//     go test ./internal/store -run TestMigrationContract -v

import (
	"context"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// requiredTables 是「代码在读写、迁移必须建」的表清单（task-11 的差集修复结果）。
// 新增条目意味着新增一处生产依赖，请同时补迁移。
var requiredTables = []string{
	// 本次修复（曾经缺失）
	"devices",
	"im_webhook_events",
	"im_groups",
	"im_group_members",
	"ticket_state",
	"offline_approvals",
	"trust_relations",
	"blacklist",
	// 抽样回归（已有迁移，防止将来被误删）
	"users",
	"messages",
	"im_messages",
	"im_relations",
	"paid_message_ledger",
	"audit_events",
	"inbound_events",
}

// requiredSequences 是「源码 nextval 在读、迁移必须创建」的序列清单。
// 序列不在 schema_contract_test 的 CREATE TABLE 差集里，故这里单独列
// （v2.3.28 真库首验缺陷 3：seq_user_id 从未被任何迁移创建，identify 直接 42P01）。
var requiredSequences = []string{
	"seq_user_id", // internal/wechatid/wechatid.go:28、internal/api/wechat_webhook.go:217
}

func TestMigrationContract_FreshSchemaRunsToCompletion(t *testing.T) {
	db := newRealDB(t)
	ctx := context.Background()

	// 关键回归：这一步在修复前会在 004_im.sql 处失败（im_webhook_events 不存在）。
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("首次 Migrate 失败（全新库无法从零建 schema）: %v", err)
	}
	// 服务每次启动都会重跑全部迁移 → 必须幂等。
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("第二次 Migrate 失败（迁移非幂等，服务重启会崩）: %v", err)
	}

	for _, table := range requiredTables {
		var reg *string
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, table).Scan(&reg); err != nil {
			t.Fatalf("to_regclass(%s): %v", table, err)
		}
		if reg == nil || *reg == "" {
			t.Errorf("表 %q 在迁移后仍不存在", table)
		}
	}
	for _, seq := range requiredSequences {
		var reg *string
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, seq).Scan(&reg); err != nil {
			t.Fatalf("to_regclass(%s): %v", seq, err)
		}
		if reg == nil || *reg == "" {
			t.Errorf("序列 %q 在迁移后仍不存在（nextval 会 42P01）", seq)
		}
	}
}

// TestMigrationContract_WechatIdentitySequence 证明 040 的三件事：
//  1. seq_user_id 真实可发号（INSERT wechat_identity ... nextval 与生产语句一致）；
//  2. 重跑 Migrate 不会回退序列（否则服务每次重启都会重用号码）；
//  3. 发号单调递增（同一序列两次取号严格变大）。
func TestMigrationContract_WechatIdentitySequence(t *testing.T) {
	db := newRealDB(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	suf := "test-seq-" + time.Now().Format("20060102150405.000000")
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
	})

	insertWithNextval := func(wxid string) int64 {
		t.Helper()
		var uid int64
		// 与 internal/wechatid/wechatid.go:27-28 的生产语句一致。
		if err := db.QueryRowContext(ctx,
			`INSERT INTO wechat_identity(wxid,user_id) VALUES($1, nextval('seq_user_id')) RETURNING user_id`,
			wxid).Scan(&uid); err != nil {
			t.Fatalf("INSERT wechat_identity(nextval('seq_user_id')) 失败（040 未生效？）: %v", err)
		}
		return uid
	}

	uid1 := insertWithNextval(suf + "_a")
	if uid1 <= 0 {
		t.Fatalf("seq_user_id 发出的 user_id = %d, want > 0", uid1)
	}
	// 服务每次启动都会重跑迁移：重跑后序列不得回退。
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("第二次 Migrate: %v", err)
	}
	uid2 := insertWithNextval(suf + "_b")
	if uid2 <= uid1 {
		t.Fatalf("重跑迁移后序列回退/重用：uid1=%d uid2=%d（不得回退）", uid1, uid2)
	}
}

// TestMigrationContract_DevicesRoundTrip 证明 devices 不再是「只能靠测试夹具建」的表。
func TestMigrationContract_DevicesRoundTrip(t *testing.T) {
	db := newRealDB(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	devID := "test-dev-" + time.Now().Format("20060102150405.000000")
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, `DELETE FROM devices WHERE id=$1`, devID) })

	// 与 internal/api/multidevice.go:34 的生产语句一致（upsert）。
	if _, err := db.ExecContext(ctx, `
		INSERT INTO devices(id, user_id, type, last_seen) VALUES($1,$2,$3,$4)
		ON CONFLICT (id) DO UPDATE SET user_id=$2, type=$3, last_seen=$4`,
		devID, "10086", "ios", time.Now()); err != nil {
		t.Fatalf("INSERT devices（生产同款 upsert）失败: %v", err)
	}
	// 与 internal/privacy/privacy.go:40 一致：按 user_id 归属查询。
	var got string
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM devices WHERE id=$1`, devID).Scan(&got); err != nil {
		t.Fatalf("SELECT devices: %v", err)
	}
	if got != "10086" {
		t.Fatalf("user_id 回读 = %q, want 10086", got)
	}
	// privacy 的导出路径（privacy.go:126 动态 SQL 形态）也必须能跑到这张表。
	var cnt int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM devices WHERE user_id = $1`, "10086").Scan(&cnt); err != nil {
		t.Fatalf("privacy 形态 count: %v", err)
	}
	if cnt == 0 {
		t.Fatal("按 user_id 统计为 0，索引/字段类型可能不对")
	}
}

// TestMigrationContract_WebhookEventsIdempotency 证明 003a 与 004 的配合成立：
// 表由 003a 建，唯一的 msg_key 部分索引由 004 建，生产 INSERT 用的
// `ON CONFLICT (msg_key) WHERE msg_key IS NOT NULL` 只有两者都在时才能成功。
func TestMigrationContract_WebhookEventsIdempotency(t *testing.T) {
	db := newRealDB(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	msgKey := "test-msgkey-" + time.Now().Format("20060102150405.000000")
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, `DELETE FROM im_webhook_events WHERE msg_key=$1`, msgKey) })

	insert := `INSERT INTO im_webhook_events
		(callback_type, from_user, to_user, group_id, msg_key, sdk_app_id, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (msg_key) WHERE msg_key IS NOT NULL DO NOTHING`

	res, err := db.ExecContext(ctx, insert, "C2C.CallbackAfterSendMsg", "u1", "u2", "", msgKey, 1400000001, `{"k":"v"}`)
	if err != nil {
		t.Fatalf("首次 INSERT webhook 事件失败（003a+004 组合有问题）: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("首次 INSERT 影响行数 = %d, want 1", n)
	}
	// 腾讯云会重投同一回调：必须被部分唯一索引吸收。
	res2, err := db.ExecContext(ctx, insert, "C2C.CallbackAfterSendMsg", "u1", "u2", "", msgKey, 1400000001, `{"k":"v"}`)
	if err != nil {
		t.Fatalf("重复 INSERT 报错（幂等失效）: %v", err)
	}
	if n, _ := res2.RowsAffected(); n != 0 {
		t.Fatalf("重复 INSERT 影响行数 = %d, want 0（msg_key 未去重）", n)
	}
}
