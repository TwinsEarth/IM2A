package agentregistry

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openReal(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "postgres://hai:hai_dev@127.0.0.1:5432/hai_platform?sslmode=disable"
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skip("no pg:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skip("no pg:", err)
	}
	return db
}

func TestRegistry_RealDB(t *testing.T) {
	db := openReal(t)
	defer db.Close()
	s := New(db)
	ctx := context.Background()
	wx := "probe_wx_registry"

	// cleanup
	_, _ = db.ExecContext(ctx, `DELETE FROM agent_registry WHERE user_wxid=$1`, wx)
	defer func() { _, _ = db.ExecContext(ctx, `DELETE FROM agent_registry WHERE user_wxid=$1`, wx) }()

	// 1. Ensure master
	a, err := s.Ensure(ctx, wx, "master", "master", "管家", 4, 999)
	if err != nil {
		t.Fatal(err)
	}
	if a.AgentID != "master" || a.TrustLevel != 4 {
		t.Fatalf("bad agent %+v", a)
	}

	// 2. Ensure sub agent with quota=2
	_, err = s.Ensure(ctx, wx, "calendar", "sub", "日程", 2, 2)
	if err != nil {
		t.Fatal(err)
	}

	// 3. List
	list, err := s.ListByUser(ctx, wx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 agents, got %d", len(list))
	}

	// 4. EnsureAllowed twice ok, third exceeds
	for i := 0; i < 2; i++ {
		if _, err := s.EnsureAllowed(ctx, wx, "calendar"); err != nil {
			t.Fatalf("call %d should pass: %v", i, err)
		}
	}
	if _, err := s.EnsureAllowed(ctx, wx, "calendar"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("want quota exceeded, got %v", err)
	}

	// 5. master unlimited still ok
	if _, err := s.EnsureAllowed(ctx, wx, "master"); err != nil {
		t.Fatalf("master should always pass: %v", err)
	}

	// 6. unknown agent
	if _, err := s.EnsureAllowed(ctx, wx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}

	// 7. pause
	if err := s.SetStatus(ctx, wx, "calendar", "paused"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureAllowed(ctx, wx, "calendar"); !errors.Is(err, ErrPaused) {
		t.Fatalf("want paused, got %v", err)
	}
}
