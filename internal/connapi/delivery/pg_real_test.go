// SPDX-License-Identifier: MIT
//
// PG-gated 真实库测试：需要 PG_DSN（如 postgres://user:pass@host:5432/db?sslmode=disable）。
// 本机/CI 无 PG 时自动 Skip —— 这部分是 B2B-DESIGN §6 判定性测试的"真 PG"要求，
// 待有 PG 的环境执行（当前构建服务器无 PG，见 task-17 回报的已知限制）。
package delivery

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/store"
)

// openPG 打开 PG、跑迁移并返回 store；PG_DSN 空则 Skip。
func openPG(t *testing.T) (*PGStore, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set; PG-gated test (B2B 判定性 E2E 待真库验)")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewPGStore(db, Options{Retention: time.Hour}), db
}

// testUserID 用时间戳生成不冲突的 userID。
func testUserID() int64 { return time.Now().UnixNano() }

func cleanupPG(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM im_delivery_log WHERE user_id = $1`,
		`DELETE FROM im_user_delivery_seq WHERE user_id = $1`,
		`DELETE FROM im_device_cursor WHERE user_id = $1`,
	} {
		if _, err := db.Exec(q, userID); err != nil {
			t.Fatalf("cleanup %q: %v", q, err)
		}
	}
}

func TestPGMigrateCreatesTables(t *testing.T) {
	_, db := openPG(t)
	ctx := context.Background()
	for _, table := range []string{"im_device_cursor", "im_delivery_log", "im_user_delivery_seq"} {
		var reg sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)`, table).Scan(&reg); err != nil {
			t.Fatalf("to_regclass(%s): %v", table, err)
		}
		if !reg.Valid || reg.String == "" {
			t.Fatalf("table %s missing after migration", table)
		}
	}
	// 迁移重复执行必须幂等（迁移器每次启动都重跑）。
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate twice: %v", err)
	}
}

func TestPGAckGreatestIdempotent(t *testing.T) {
	s, db := openPG(t)
	userID := testUserID()
	deviceID := "pg-ios-" + time.Now().Format("150405.000000")
	cleanupPG(t, db, userID)
	t.Cleanup(func() { cleanupPG(t, db, userID) })
	ctx := context.Background()

	for _, seq := range []uint64{5, 3, 7, 7, 1, 6} {
		if _, err := s.Ack(ctx, userID, deviceID, seq); err != nil {
			t.Fatalf("Ack(%d): %v", seq, err)
		}
	}
	cur, ok, err := s.Cursor(ctx, deviceID)
	if err != nil || !ok {
		t.Fatalf("Cursor ok=%v err=%v", ok, err)
	}
	if cur.AckedSeq != 7 {
		t.Fatalf("acked_seq = %d, want 7 (GREATEST)", cur.AckedSeq)
	}
}

func TestPGPlanReplayAndTrim(t *testing.T) {
	s, db := openPG(t)
	userID := testUserID()
	deviceA := "pg-a-" + time.Now().Format("150405.000000")
	deviceB := "pg-b-" + time.Now().Format("150405.000000")
	cleanupPG(t, db, userID)
	t.Cleanup(func() { cleanupPG(t, db, userID) })
	ctx := context.Background()

	payload := make([]byte, 64)
	entries, err := s.Append(ctx, userID, []AppendFrame{
		{MsgType: MsgTypeEVT, Payload: payload},
		{MsgType: MsgTypeEVT, Payload: payload},
		{MsgType: MsgTypeEVT, Payload: payload},
	})
	if err != nil || len(entries) != 3 {
		t.Fatalf("Append = (%d,%v)", len(entries), err)
	}
	if entries[0].Seq != 1 || entries[2].Seq != 3 {
		t.Fatalf("seqs = %+v, want 1..3", entries)
	}
	plan, err := s.PlanReplay(ctx, ReplayRequest{UserID: userID, Limits: DefaultReplayLimits()})
	if err != nil || !plan.OK || plan.Truncated {
		t.Fatalf("PlanReplay = %+v err=%v", plan, err)
	}
	if plan.From != 1 || plan.To != 3 || plan.Replayed != 3 {
		t.Fatalf("plan = %+v, want from=1 to=3 replayed=3", plan)
	}
	// epoch 不匹配拒绝 resume。
	if _, err := s.BumpEpoch(ctx, userID, deviceA); err != nil {
		t.Fatalf("BumpEpoch: %v", err)
	}
	stale, err := s.PlanReplay(ctx, ReplayRequest{UserID: userID, DeviceID: deviceA, ConnEpoch: 0})
	if err == nil || stale.OK || stale.Reason != "epoch_mismatch" {
		t.Fatalf("stale epoch plan = %+v err=%v, want epoch_mismatch", stale, err)
	}
	// 位点：A=3、B=1 → Trim 只能删 seq<=1 且超过保留期；测试里日志刚写（未过保留期）→ 不裁。
	if _, err := s.Ack(ctx, userID, deviceA, 3); err != nil {
		t.Fatalf("Ack A: %v", err)
	}
	if _, err := s.Ack(ctx, userID, deviceB, 1); err != nil {
		t.Fatalf("Ack B: %v", err)
	}
	if deleted, err := s.Trim(ctx, userID, 0); err != nil || deleted != 0 {
		t.Fatalf("Trim before retention = (%d,%v), want (0,nil)", deleted, err)
	}
}
