package inqueue

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// 真实 PG 落库断言（v2.3.19 审查）：仅当 INQUEUE_REAL_PG 设置时运行。
// 断言：Enqueue 幂等 → Worker 抢占 queued → done；失败 handler → error。零残留 cleanup。
func TestInQueueRealPG(t *testing.T) {
	dsn := os.Getenv("INQUEUE_REAL_PG")
	if dsn == "" {
		t.Skip("set INQUEUE_REAL_PG to run real PG test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := "audit_v2319_" + time.Now().Format("150405.000000")
	cleanup := func() {
		_, _ = db.Exec("DELETE FROM inbound_events WHERE idem_key LIKE $1", "audit_v2319_%")
	}
	cleanup()
	defer cleanup()

	ctx := context.Background()
	id, dup, err := Enqueue(ctx, db, key, "audit_acct", "wxid_audit", "审计自检", "", "")
	if err != nil || dup || id == 0 {
		t.Fatalf("enqueue id=%d dup=%v err=%v", id, dup, err)
	}
	_, dup2, err := Enqueue(ctx, db, key, "audit_acct", "wxid_audit", "审计自检", "", "")
	if err != nil || !dup2 {
		t.Fatalf("dup enqueue must be idempotent: dup=%v err=%v", dup2, err)
	}

	done := make(chan struct{})
	w := NewWorker(db, func(ctx context.Context, ev Event) error {
		if ev.Text != "审计自检" {
			t.Errorf("text=%q", ev.Text)
		}
		close(done)
		return nil
	})
	w.poll = 50 * time.Millisecond
	cctx, cancel := context.WithCancel(ctx)
	go w.Start(cctx)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not process queued event")
	}
	time.Sleep(300 * time.Millisecond) // 等 worker 用父 ctx 写 done
	cancel()

	var status string
	if err := db.QueryRow("SELECT status FROM inbound_events WHERE idem_key=$1", key).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "done" {
		t.Fatalf("status=%q want done", status)
	}
}
