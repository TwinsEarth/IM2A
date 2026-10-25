package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// v10OpenStore 走 store.Open（含连接池治理），未设置 PG_DSN 时跳过。
func v10OpenStore(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set; skip real PG integration test")
	}
	db, err := Open("pgx", dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestOpenPoolConfig 校验连接池被显式收敛（修复前 database/sql 默认 MaxOpenConns=0 无限）。
func TestOpenPoolConfig(t *testing.T) {
	db := v10OpenStore(t)
	st := db.Stats()
	if st.MaxOpenConnections != defaultMaxOpenConns {
		t.Fatalf("MaxOpenConnections want %d, got %d", defaultMaxOpenConns, st.MaxOpenConnections)
	}
}

// TestMigrateIdempotent 内嵌迁移连续执行两次必须成功（幂等），新环境可自动建表。
func TestMigrateIdempotent(t *testing.T) {
	db := v10OpenStore(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second migrate (must be idempotent): %v", err)
	}
}

// TestPGMessageBizTypeAndContext 真实落库：业务类型 + Agent 上下文写读往返；空 biz_type 默认 text。
func TestPGMessageBizTypeAndContext(t *testing.T) {
	db := v10OpenStore(t)
	ms := NewPGMessage(db)
	ctx := context.Background()
	uid := uint64(900000000 + time.Now().UnixNano()%99000000)
	now := time.Now().Unix()
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM messages WHERE sender_id=$1 OR recipient_id=$1", uid); err != nil {
			t.Errorf("cleanup messages: %v", err)
		}
	})

	// 1) 带 biz_type + agent_context
	id1 := fmt.Sprintf("v10msg_%d_a", now)
	m1 := &Message{
		MessageID:    id1,
		SenderID:     uid,
		ReceiverID:   uid + 1,
		BizType:      "approval_card",
		Content:      "请审批",
		AgentContext: map[string]any{"ticketId": "t1", "amount": float64(100)},
		Seq:          1,
		CreatedAt:    now,
	}
	if err := ms.SaveMessage(ctx, m1); err != nil {
		t.Fatalf("save m1: %v", err)
	}
	got1, err := ms.LoadMessage(ctx, id1)
	if err != nil {
		t.Fatalf("load m1: %v", err)
	}
	if got1.BizType != "approval_card" {
		t.Fatalf("bizType want approval_card, got %q", got1.BizType)
	}
	if got1.AgentContext["ticketId"] != "t1" {
		t.Fatalf("agentContext round-trip mismatch: %v", got1.AgentContext)
	}

	// 2) 空 biz_type 落库必须为 text
	id2 := fmt.Sprintf("v10msg_%d_b", now)
	m2 := &Message{MessageID: id2, SenderID: uid, ReceiverID: uid + 1,
		Content: "hi", Seq: 2, CreatedAt: now}
	if err := ms.SaveMessage(ctx, m2); err != nil {
		t.Fatalf("save m2: %v", err)
	}
	got2, err := ms.LoadMessage(ctx, id2)
	if err != nil {
		t.Fatalf("load m2: %v", err)
	}
	if got2.BizType != "text" {
		t.Fatalf("default bizType want text, got %q", got2.BizType)
	}
	if got2.AgentContext != nil {
		t.Fatalf("empty agentContext should be nil, got %v", got2.AgentContext)
	}

	// 3) ListMessages 必须带回 biz_type
	list, err := ms.ListMessages(ctx, uid, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, mm := range list {
		if mm.MessageID == id1 && mm.BizType == "approval_card" && mm.AgentContext["ticketId"] == "t1" {
			found = true
		}
	}
	if !found {
		t.Fatal("ListMessages did not return approval_card biz_type/agent_context")
	}
}
