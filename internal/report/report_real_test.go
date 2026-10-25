package report

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestReportGenerateApprove(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" { t.Skip() }
	d, _ := sql.Open("pgx", dsn)
	t.Cleanup(func() { d.Close() })
	suf := fmt.Sprintf("v210_%d", time.Now().UnixNano())
	uid := suf + "_user"
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM agent_reports WHERE user_id=$1`, uid)
	})
	ctx := context.Background()
	s := New(d)

	r, err := s.Generate(ctx, uid)
	if err != nil { t.Fatalf("generate: %v", err) }
	if r.Status != "draft" {
		t.Fatalf("status=%s", r.Status)
	}
	if r.Summary["date"] == nil {
		t.Fatalf("summary empty")
	}
	// 落库可查
	g, err := s.Get(ctx, r.ID)
	if err != nil || g.Status != "draft" {
		t.Fatalf("get: %v", err)
	}
	// 审批
	if err := s.Approve(ctx, r.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	g2, _ := s.Get(ctx, r.ID)
	if g2.Status != "approved" {
		t.Fatalf("after approve=%s", g2.Status)
	}
	// 二次审批应 ErrNotFound（已非 draft）
	if err := s.Approve(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second approve=%v", err)
	}
	// 同日重复 Generate 走 ON CONFLICT 不报错
	r2, err := s.Generate(ctx, uid)
	if err != nil || r2.ID != r.ID {
		t.Fatalf("regen: id=%d err=%v", r2.ID, err)
	}
}
