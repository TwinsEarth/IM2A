package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"human-agent-im/internal/approval"
	"human-agent-im/internal/audit"
	"human-agent-im/internal/ledger"
	"human-agent-im/internal/negotiation"
	"human-agent-im/internal/trustzone"
)

func TestRelationContract(t *testing.T) {
	s := NewMemoryStore(10)
	ctx := context.Background()
	if _, err := s.Relation.GetTrust(ctx, 1, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing trust err=%v want ErrNotFound", err)
	}
	if err := s.Relation.GrantTrust(ctx, 1, 2, trustzone.LevelFamily); err != nil {
		t.Fatalf("grant: %v", err)
	}
	lvl, err := s.Relation.GetTrust(ctx, 1, 2)
	if err != nil || lvl != trustzone.LevelFamily {
		t.Fatalf("get level=%v err=%v", lvl, err)
	}
	// upsert 改档位
	if err := s.Relation.GrantTrust(ctx, 1, 2, trustzone.LevelTrusted); err != nil {
		t.Fatalf("regrant: %v", err)
	}
	lvl, _ = s.Relation.GetTrust(ctx, 1, 2)
	if lvl != trustzone.LevelTrusted {
		t.Fatalf("updated level=%v want Trusted", lvl)
	}
	list, err := s.Relation.ListTrust(ctx, 1)
	if err != nil || len(list) != 1 || list[2] != trustzone.LevelTrusted {
		t.Fatalf("list=%v err=%v", list, err)
	}
	if err := s.Relation.RevokeTrust(ctx, 1, 2); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.Relation.GetTrust(ctx, 1, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after revoke err=%v want ErrNotFound", err)
	}
}

func TestCapabilityContract(t *testing.T) {
	s := NewMemoryStore(10)
	ctx := context.Background()
	cap := trustzone.Capability{
		AgentID: "family-agent", TrustLevelMin: trustzone.LevelFamily,
		AllowedSkills: []string{"home-maintenance"}, RequiresApprovalAboveCents: 200000,
	}
	if err := s.Capability.UpsertCapability(ctx, cap); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Capability.GetCapability(ctx, "family-agent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RequiresApprovalAboveCents != 200000 || len(got.AllowedSkills) != 1 {
		t.Fatalf("got cap=%+v", got)
	}
	all, err := s.Capability.ListCapabilities(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list=%d err=%v", len(all), err)
	}
	if _, err := s.Capability.GetCapability(ctx, "missing"); err == nil {
		t.Fatal("missing capability should error")
	}
}

func TestLedgerContract(t *testing.T) {
	s := NewMemoryStore(2)
	ctx := context.Background()
	_ = ctx
	if _, err := s.Ledger.Reserve("m1", 10, 20, ledger.ModeSendCharge, 100); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	e, err := s.Ledger.Charge("m1", "")
	if err != nil || e.Status != ledger.StatusCharged {
		t.Fatalf("charge e=%v err=%v", e, err)
	}
	if _, err := s.Ledger.Charge("m1", ""); !errors.Is(err, ledger.ErrAlreadyProcessed) {
		t.Fatalf("dup charge err=%v", err)
	}
}

func TestApprovalContract(t *testing.T) {
	s := NewMemoryStore(10)
	eng := approval.New(s.Approval)
	tk, err := eng.CreateTicket(1, "agent", approval.TicketPayment, map[string]any{"k": "v"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	loaded, err := s.Approval.Load(tk.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ID != tk.ID || loaded.State != approval.StatePending {
		t.Fatalf("loaded=%+v", loaded)
	}
}

func TestNegotiationContract(t *testing.T) {
	s := NewMemoryStore(10)
	ctx := context.Background()
	sess := negotiation.NewSession("sess-x", 5, 0)
	if _, err := sess.Apply(negotiation.Turn{
		SessionID: "sess-x", FromAgentID: "a", ToAgentID: "b",
		Intent: negotiation.IntentPropose, Payload: map[string]any{"why": "合作"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := s.Negotiation.SaveSession(ctx, sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Negotiation.LoadSession(ctx, "sess-x")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.State != negotiation.StateWorking {
		t.Fatalf("state=%s want WORKING", got.State)
	}
}

func TestAuditSink(t *testing.T) {
	s := NewMemoryStore(10)
	err := s.Audit.Write(audit.Entry{
		At: time.Now(), MessageID: "m", AgentID: "a",
		Action: audit.ActionContentAllow, Decision: "ok",
	})
	if err != nil {
		t.Fatalf("audit write: %v", err)
	}
	mem, ok := s.Audit.(*audit.MemorySink)
	if !ok {
		t.Fatal("audit sink not memory")
	}
	if mem.Count()[audit.ActionContentAllow] != 1 {
		t.Fatalf("count=%v", mem.Count())
	}
}

// TestPostgresSkipped 真实 PostgreSQL 用例：设置 PG_DSN 时才运行（CI/资源闸门）。
// 驱动由服务主程序 blank import 注册；当前离线环境无驱动时 Open 会明确报错。
func TestPostgresSkipped(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set; skipping PostgreSQL-backed store test")
	}
	driver := os.Getenv("PG_DRIVER")
	if driver == "" {
		driver = "pgx"
	}
	db, err := Open(driver, dsn)
	if err != nil {
		t.Skipf("postgres driver/db unavailable: %v", err)
	}
	// t.Cleanup 按 LIFO 执行：先注册的连接关闭最后执行，确保数据清理运行时连接仍可用。
	t.Cleanup(func() { _ = db.Close() })
	// users.id 为 identity 列，须先建用户（RETURNING id）；trust_levels 外键 ON DELETE CASCADE，删用户即清信任行。
	mkUser := func(tag int) uint64 {
		var id uint64
		phone := fmt.Sprintf("ENC_PG_STORE_%d_%d", time.Now().UnixNano(), tag)
		if err := db.QueryRow(`INSERT INTO users(phone_encrypted,status) VALUES($1,1) RETURNING id`, phone).Scan(&id); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		return id
	}
	uid := mkUser(1)
	target := mkUser(2)
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM users WHERE id IN ($1,$2)", uid, target); err != nil {
			t.Errorf("cleanup users: %v", err)
		}
		// 前缀兜底：phone_encrypted 以 ENC_PG_STORE_ 开头的行均为本测试数据，
		// trust_levels 外键 ON DELETE CASCADE 会一并清理。精确 id 覆盖正常路径，
		// 此前缀仅在测试二进制被重复/并发触发时防御性兜底，确保零残留。
		if _, err := db.Exec("DELETE FROM users WHERE phone_encrypted LIKE 'ENC_PG_STORE_%'"); err != nil {
			t.Errorf("cleanup store prefix: %v", err)
		}
	})
	s := NewPostgresStore(db, 10)
	ctx := context.Background()
	if err := s.Relation.GrantTrust(ctx, uid, target, trustzone.LevelFamily); err != nil {
		t.Fatalf("pg grant: %v", err)
	}
	lvl, err := s.Relation.GetTrust(ctx, uid, target)
	if err != nil || lvl != trustzone.LevelFamily {
		t.Fatalf("pg get level=%v err=%v", lvl, err)
	}
}
