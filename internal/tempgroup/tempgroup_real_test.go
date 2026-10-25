package tempgroup

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

func TestTempGroupLifecycle(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" { t.Skip() }
	d, _ := sql.Open("pgx", dsn)
	t.Cleanup(func() { d.Close() })
	suf := fmt.Sprintf("v208_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM temp_messages WHERE group_id LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM temp_group_members WHERE group_id LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM temp_groups WHERE id LIKE $1`, suf+"%")
	})
	ctx := context.Background()
	s := New(d)

	gid := suf + "_g1"
	if err := s.Create(ctx, gid, "采购协商", "agent-main"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 加两个成员
	if err := s.Join(ctx, gid, "agent-a"); err != nil { t.Fatalf("join a: %v", err) }
	if err := s.Join(ctx, gid, "agent-b"); err != nil { t.Fatalf("join b: %v", err) }
	// 成员发消息
	if err := s.Post(ctx, gid, "agent-a", map[string]any{"offer": 100}); err != nil {
		t.Fatalf("post: %v", err)
	}
	if err := s.Post(ctx, gid, "agent-b", map[string]any{"counter": 95}); err != nil {
		t.Fatalf("post2: %v", err)
	}
	// 非成员发消息应拒绝
	if err := s.Post(ctx, gid, "agent-unknown", map[string]any{"x": 1}); !errors.Is(err, ErrMember) {
		t.Fatalf("non-member should be ErrMember, got %v", err)
	}
	// 历史应 2 条
	h, err := s.History(ctx, gid, 10)
	if err != nil || len(h) != 2 {
		t.Fatalf("history=%d err=%v", len(h), err)
	}
	// 成员应 3 个（main+a+b）
	mems, _ := s.Members(ctx, gid)
	if len(mems) != 3 {
		t.Fatalf("members=%d", len(mems))
	}

	// 销毁
	if err := s.Destroy(ctx, gid); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	// 销毁后发消息应 ErrClosed
	if err := s.Post(ctx, gid, "agent-a", map[string]any{"x": 1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("after destroy should be ErrClosed, got %v", err)
	}
	// 销毁后成员为空
	mems2, _ := s.Members(ctx, gid)
	if len(mems2) != 0 {
		t.Fatalf("members after destroy=%d", len(mems2))
	}
	// 销毁后历史为空（物理删消息）
	h2, _ := s.History(ctx, gid, 10)
	if len(h2) != 0 {
		t.Fatalf("history after destroy=%d", len(h2))
	}
	// 群记录留审计（status=closed）
	var status string
	_ = d.QueryRow(`SELECT status FROM temp_groups WHERE id=$1`, gid).Scan(&status)
	if status != "closed" {
		t.Fatalf("status=%s", status)
	}
	// 二次 destroy 应 ErrNotFound/已 closed
	if err := s.Destroy(ctx, gid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second destroy=%v", err)
	}
}
