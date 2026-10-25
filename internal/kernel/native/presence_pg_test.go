// SPDX-License-Identifier: MIT
//
// PG-gated：C1 迁移 042（n_principal/n_presence）存在性 + presence 快照 upsert。
// 无 PG_DSN 时 Skip（服务器默认构建环境无 PG）。
package native

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/store"
)

func TestPGPresenceSnapshot(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set; PG-gated presence snapshot test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, table := range []string{"n_principal", "n_presence"} {
		var reg sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)`, table).Scan(&reg); err != nil {
			t.Fatalf("to_regclass(%s): %v", table, err)
		}
		if !reg.Valid || reg.String == "" {
			t.Fatalf("迁移 042 缺表 %s", table)
		}
	}
	uid := time.Now().UnixNano()%1_000_000_000 + 500000
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM n_presence WHERE user_id = $1`, uid) })

	st := NewPGStore(db)
	now := time.Now()
	if err := st.UpsertPresence(ctx, []PresenceRow{{UserID: uid, State: "online", DeviceCount: 2, LastSeen: now}}); err != nil {
		t.Fatalf("upsert online: %v", err)
	}
	var state string
	var devices int
	if err := db.QueryRowContext(ctx,
		`SELECT state, device_count FROM n_presence WHERE user_id = $1`, uid).
		Scan(&state, &devices); err != nil {
		t.Fatalf("select: %v", err)
	}
	if state != "online" || devices != 2 {
		t.Fatalf("n_presence = (%s,%d), want (online,2)", state, devices)
	}
	// 幂等 upsert：同 user 更新而非重复插入。
	if err := st.UpsertPresence(ctx, []PresenceRow{{UserID: uid, State: "away", DeviceCount: 0, LastSeen: now}}); err != nil {
		t.Fatalf("upsert away: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT state, device_count FROM n_presence WHERE user_id = $1`, uid).
		Scan(&state, &devices); err != nil {
		t.Fatalf("select after upsert: %v", err)
	}
	if state != "away" || devices != 0 {
		t.Fatalf("n_presence after upsert = (%s,%d), want (away,0)", state, devices)
	}
}
