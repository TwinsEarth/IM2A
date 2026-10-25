package agentdir

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// db 返回 db 与本次唯一 suf；cleanup 用同一 suf，保证零残留。
func db(t *testing.T) (*sql.DB, string) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("open: %v", err)
	}
	_, err = d.Exec(`CREATE TABLE IF NOT EXISTS agent_directory (
		agent_id TEXT PRIMARY KEY, owner_user TEXT NOT NULL, display_name TEXT NOT NULL,
		alias TEXT NOT NULL DEFAULT '', attention_level SMALLINT NOT NULL DEFAULT 1,
		tags JSONB NOT NULL DEFAULT '[]'::jsonb, monthly_budget_cents BIGINT NOT NULL DEFAULT 0,
		trust_level SMALLINT NOT NULL DEFAULT 4, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Skipf("migrate: %v", err)
	}
	suf := fmt.Sprintf("v181_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, err := d.Exec(`DELETE FROM agent_directory WHERE agent_id LIKE $1 OR owner_user LIKE $1`, suf+"%")
		if err != nil {
			t.Errorf("cleanup agentdir: %v", err)
		}
		d.Close()
	})
	return d, suf
}

func TestV181Directory(t *testing.T) {
	d, suf := db(t)
	s := New(d)
	ctx := context.Background()
	owner := suf + "_user"
	a := &Agent{AgentID: suf + "_boss", OwnerUser: owner, DisplayName: "家庭管家", Alias: "butler@im2a",
		AttentionLevel: 2, Tags: []string{"日程", "采购"}, MonthlyBudgetCents: 50000, TrustLevel: 1} // 继承家庭档
	if err := s.Upsert(ctx, a); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	a.Alias = "butler2@im2a"
	if err := s.Upsert(ctx, a); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	got, err := s.Get(ctx, a.AgentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Alias != "butler2@im2a" {
		t.Fatalf("upsert 未更新 alias: %s", got.Alias)
	}
	if got.TrustLevel != 1 {
		t.Fatalf("信任档应为继承 1（家庭），实际 %d", got.TrustLevel)
	}
	if len(got.Tags) != 2 {
		t.Fatalf("tags 应为2，实际 %v", got.Tags)
	}
	list, err := s.ListByOwner(ctx, owner)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("owner 应有1个Agent，实际 %d", len(list))
	}
	if other, _ := s.ListByOwner(ctx, suf+"_other"); len(other) != 0 {
		t.Fatalf("越权：other 通讯录 %d（应0）", len(other))
	}
	if err := s.SetAttention(ctx, a.AgentID, 3); err != nil {
		t.Fatalf("attention: %v", err)
	}
	got2, _ := s.Get(ctx, a.AgentID)
	if got2.AttentionLevel != 3 {
		t.Fatalf("attention 未更新: %d", got2.AttentionLevel)
	}
	if err := s.SetAttention(ctx, a.AgentID, 9); err == nil {
		t.Fatal("attention=9 应报错")
	}
}
