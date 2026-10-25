package eventbus

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func v10OpenPG(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set; skip real PG integration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	db.SetMaxOpenConns(10)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestBusPersistsAuditEvent 真实落库：SetPG 后 Publish 必须异步写入 audit_events。
// 修复前异步写错误被 `_` 丢弃，审计丢失时无任何告警。
func TestBusPersistsAuditEvent(t *testing.T) {
	db := v10OpenPG(t)
	bus := New()
	bus.SetPG(db)

	uid := uint64(900000000 + time.Now().UnixNano()%99000000)
	ag := fmt.Sprintf("v10audit_%d", time.Now().UnixNano())
	action := fmt.Sprintf("v10_audit_%d", time.Now().UnixNano())
	// audit_events 是 append-only WORM（触发器禁止 DELETE/UPDATE），审计行不删除；
	// ag/action 均为本次唯一标识，测试断言只查自己的行，永久保留属合规预期。

	if err := bus.Publish(context.Background(), &Event{
		Type:    EventType(action),
		UserID:  uid,
		AgentID: ag,
		Payload: map[string]any{"k": "v", "n": 42},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		if err := db.QueryRow(
			"SELECT count(*) FROM audit_events WHERE agent_id=$1 AND action=$2",
			ag, action,
		).Scan(&n); err != nil {
			t.Fatalf("count audit_events: %v", err)
		}
		if n >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n < 1 {
		t.Fatal("audit event was not persisted to audit_events")
	}

	// 校验 detail 是合法 JSONB 且携带 payload
	var detail []byte
	if err := db.QueryRow(
		"SELECT detail FROM audit_events WHERE agent_id=$1 AND action=$2 ORDER BY id DESC LIMIT 1",
		ag, action,
	).Scan(&detail); err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if len(detail) == 0 || string(detail) == "{}" {
		t.Fatalf("detail should carry payload, got %q", string(detail))
	}
}
