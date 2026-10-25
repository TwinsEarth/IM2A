package openclaw

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"human-agent-im/internal/wechatbot"
	"human-agent-im/internal/wechatid"
)

func TestOpenClawBindFlow(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	suf := fmt.Sprintf("oc_%d", time.Now().UnixNano())
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// 先跑 migration
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS openclaw_qr_sessions (
  qr_id TEXT PRIMARY KEY, owner_user BIGINT NOT NULL,
  device_type TEXT NOT NULL DEFAULT 'unknown', status TEXT NOT NULL DEFAULT 'pending',
  wxid TEXT, user_id BIGINT, group_id TEXT, error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), scanned_at TIMESTAMPTZ)`,
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}

	ids := wechatid.New(db)
	bots := wechatbot.New(db)
	svc := New(db, ids, bots)

	ctx := context.Background()
	owner := suf + "_owner"
	qrID := suf + "_qr"

	// 1) 发起
	se, err := svc.NewSession(ctx, qrID, owner, "ios")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if se.Status != "pending" || se.OwnerUser != owner {
		t.Fatalf("session=%+v", se)
	}

	// 2) 回调
	wxid := suf + "_wxid"
	se2, err := svc.Callback(ctx, qrID, wxid)
	if err != nil {
		t.Fatalf("Callback: %v", err)
	}
	if se2.Status != "bound" {
		t.Fatalf("status=%s err=%s", se2.Status, se2.Error)
	}
	if se2.UserID <= 0 {
		t.Fatalf("no userId: %+v", se2)
	}
	if se2.GroupID == "" {
		t.Fatalf("no groupId")
	}

	// 3) 重复回调应失败
	_, err = svc.Callback(ctx, qrID, wxid)
	if err == nil {
		t.Fatal("expected duplicate callback error")
	}

	// 4) 第二台设备 ios 已存在，应 ErrDeviceExists
	qr2 := suf + "_qr2"
	if _, err := svc.NewSession(ctx, qr2, owner, "ios"); err != nil {
		t.Fatal(err)
	}
	se3, err := svc.Callback(ctx, qr2, suf+"_wx2")
	if err == nil {
		t.Fatalf("expected device-exists, got bound=%+v", se3)
	}
	if se3.Status != "failed" || se3.Error == "" {
		t.Fatalf("expected failed state with error, got %+v", se3)
	}

	// 5) android 端应成功（复用同组）
	qr3 := suf + "_qr3"
	if _, err := svc.NewSession(ctx, qr3, owner, "android"); err != nil {
		t.Fatal(err)
	}
	se4, err := svc.Callback(ctx, qr3, suf+"_wx3")
	if err != nil || se4.Status != "bound" {
		t.Fatalf("android bind: %v se=%+v", err, se4)
	}
	if se4.GroupID != se2.GroupID {
		t.Fatalf("reuse group failed: %s vs %s", se4.GroupID, se2.GroupID)
	}

	// cleanup
	db.Exec(`DELETE FROM openclaw_qr_sessions WHERE qr_id LIKE $1`, suf+"%")
	db.Exec(`DELETE FROM wechat_bindings WHERE id LIKE $1 OR wxid LIKE $1`, "ocb_"+qrID, suf+"%")
	db.Exec(`DELETE FROM wechat_bindings WHERE wxid LIKE $1`, suf+"%")
	db.Exec(`DELETE FROM wechat_bot_groups WHERE id LIKE $1 OR id=$2`, "%"+suf+"%", se2.GroupID)
	db.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
}
