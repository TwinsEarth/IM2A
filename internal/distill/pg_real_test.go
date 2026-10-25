package distill

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"human-agent-im/internal/eventbus"

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

func v10Count(t *testing.T, db *sql.DB, table, agent string, uid uint64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		fmt.Sprintf("SELECT count(*) FROM %s WHERE user_id=$1 AND agent_id=$2", table),
		uid, agent,
	).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func v10Wait(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout waiting for: " + msg)
}

// TestDistillPersistsImprovementAndPatch 真实落库：
// 正反馈 -> improvements 行；连续负反馈达到 NegStreakThreshold(2) -> threshold_patches 行。
// 修复前异步写错误被静默吞掉且无任何测试，threshold_patches 长期为 0。
func TestDistillPersistsImprovementAndPatch(t *testing.T) {
	db := v10OpenPG(t)
	eng := New(eventbus.New())
	eng.SetPG(db)

	uid := uint64(900000000 + time.Now().UnixNano()%99000000)
	ag := fmt.Sprintf("v10distill_%d", time.Now().UnixNano())
	key := "calendar.book"
	t.Cleanup(func() {
		for _, tb := range []string{"improvements", "threshold_patches"} {
			if _, err := db.Exec(fmt.Sprintf("DELETE FROM %s WHERE user_id=$1 AND agent_id=$2", tb), uid, ag); err != nil {
				t.Errorf("cleanup %s: %v", tb, err)
			}
		}
	})

	// 1) 正反馈 -> improvements
	eng.Record(uid, ag, key, 1.0)
	v10Wait(t, func() bool {
		return v10Count(t, db, "improvements", ag, uid) >= 1
	}, 3*time.Second, "improvement row after positive feedback")

	// 2) 连续 2 次负反馈（defaultNegStreakThreshold=2）-> threshold_patches
	eng.Record(uid, ag, key, -1.0)
	eng.Record(uid, ag, key, -1.0)
	v10Wait(t, func() bool {
		return v10Count(t, db, "threshold_patches", ag, uid) >= 1
	}, 3*time.Second, "threshold_patch row after negative streak")

	// 校验阈值收紧行确实 auto_applied=true
	var auto bool
	if err := db.QueryRow(
		"SELECT auto_applied FROM threshold_patches WHERE user_id=$1 AND agent_id=$2 ORDER BY id DESC LIMIT 1",
		uid, ag,
	).Scan(&auto); err != nil {
		t.Fatalf("read threshold_patch: %v", err)
	}
	if !auto {
		t.Fatal("threshold_patch.auto_applied should be true")
	}
}
