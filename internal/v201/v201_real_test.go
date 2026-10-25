package v201

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"human-agent-im/internal/agentquota"
	"human-agent-im/internal/wechatid"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func db(t *testing.T) (*sql.DB, string) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("open: %v", err)
	}
	suf := fmt.Sprintf("v201_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
		d.Close()
	})
	return d, suf
}

func TestIdentifyReusesSameUser(t *testing.T) {
	d, suf := db(t)
	s := wechatid.New(d)
	ctx := context.Background()
	wx := suf + "_wx"
	uid1, isNew1, err := s.Identify(ctx, wx)
	if err != nil || !isNew1 {
		t.Fatalf("first identify: uid=%d isNew=%v err=%v", uid1, isNew1, err)
	}
	uid2, isNew2, err := s.Identify(ctx, wx)
	if err != nil || isNew2 {
		t.Fatalf("second identify should reuse: uid=%d isNew=%v err=%v", uid2, isNew2, err)
	}
	if uid1 != uid2 {
		t.Fatalf("wxid changed user: %d -> %d", uid1, uid2)
	}
}

func TestQuotaFreeLimits(t *testing.T) {
	d, suf := db(t)
	idp := wechatid.New(d)
	qp := agentquota.New(d)
	ctx := context.Background()
	uid, _, err := idp.Identify(ctx, suf+"_q")
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if err := qp.Occupy(ctx, uid, "main"); err != nil {
		t.Fatalf("first main should pass: %v", err)
	}
	if err := qp.Occupy(ctx, uid, "main"); err != agentquota.ErrQuotaExceeded {
		t.Fatalf("second main should be ErrQuotaExceeded, got %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := qp.Occupy(ctx, uid, "manager"); err != nil {
			t.Fatalf("manager %d: %v", i, err)
		}
	}
	if err := qp.Occupy(ctx, uid, "manager"); err != agentquota.ErrQuotaExceeded {
		t.Fatalf("3rd manager should exceed, got %v", err)
	}
}

func TestUpgradeRaisesLimits(t *testing.T) {
	d, suf := db(t)
	idp := wechatid.New(d)
	qp := agentquota.New(d)
	ctx := context.Background()
	uid, _, err := idp.Identify(ctx, suf+"_u")
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := qp.Occupy(ctx, uid, "sub"); err != nil {
			t.Fatalf("fill free sub %d: %v", i, err)
		}
	}
	if err := qp.Occupy(ctx, uid, "sub"); err != agentquota.ErrQuotaExceeded {
		t.Fatalf("11th sub should exceed on free, got %v", err)
	}
	if err := qp.Upgrade(ctx, uid, "pro"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if err := qp.Occupy(ctx, uid, "sub"); err != nil {
		t.Fatalf("after upgrade 11th sub should pass: %v", err)
	}
}
