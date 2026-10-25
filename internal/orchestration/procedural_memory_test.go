package orchestration

import (
	"context"
	"strings"
	"testing"

	"human-agent-im/internal/memory"
)

func TestD2_RecordAndRetrieve(t *testing.T) {
	store := memory.NewMemoryStore()
	pm := NewProceduralMemory(store)
	ctx := context.Background()
	sc := memory.Scope{UserID: 1, AgentID: "procurement"}

	_ = pm.RecordDecision(ctx, sc, "purchase.amount>500", "buy:咖啡机", "approve", "")
	s, conf := pm.Suggestion(ctx, sc, "purchase.amount>500")
	if !strings.Contains(s, "用户批准") {
		t.Fatalf("expected approve hint, got %q", s)
	}
	if conf < 0.55 {
		t.Fatalf("conf too low after approve: %.2f", conf)
	}
}

func TestD2_RejectLowersConfidence(t *testing.T) {
	store := memory.NewMemoryStore()
	pm := NewProceduralMemory(store)
	ctx := context.Background()
	sc := memory.Scope{UserID: 1, AgentID: "procurement"}

	_ = pm.RecordDecision(ctx, sc, "purchase.cheap_gift", "buy:鲜花", "approve", "")
	_ = pm.RecordDecision(ctx, sc, "purchase.cheap_gift", "buy:鲜花", "reject", "太贵")
	_, conf := pm.Suggestion(ctx, sc, "purchase.cheap_gift")
	if conf > 0.4 {
		t.Fatalf("after reject conf should drop, got %.2f", conf)
	}
}

func TestD2_AutoExecuteThreshold(t *testing.T) {
	store := memory.NewMemoryStore()
	pm := NewProceduralMemory(store)
	ctx := context.Background()
	sc := memory.Scope{UserID: 1, AgentID: "procurement"}

	// 一次 approve 后 conf=0.6，应能自动执行
	_ = pm.RecordDecision(ctx, sc, "auto.buy_milk", "买牛奶", "approve", "")
	if !pm.ShouldAutoExecute(ctx, sc, "auto.buy_milk") {
		t.Fatalf("single approve should cross autoThresh")
	}
	// 一次 reject 后不该自动执行
	_ = pm.RecordDecision(ctx, sc, "auto.buy_expensive", "买贵的", "reject", "")
	if pm.ShouldAutoExecute(ctx, sc, "auto.buy_expensive") {
		t.Fatalf("rejected trigger must not auto-execute")
	}
}

func TestD2_Isolation(t *testing.T) {
	store := memory.NewMemoryStore()
	pm := NewProceduralMemory(store)
	ctx := context.Background()
	scA := memory.Scope{UserID: 1, AgentID: "procurement"}
	scB := memory.Scope{UserID: 1, AgentID: "legal"}

	_ = pm.RecordDecision(ctx, scA, "secret.trigger", "x", "approve", "")
	s, _ := pm.Suggestion(ctx, scB, "secret.trigger")
	if s != "" {
		t.Fatalf("cross-scope leak: %q", s)
	}
}

func TestD2_EmptyTriggerSafe(t *testing.T) {
	store := memory.NewMemoryStore()
	pm := NewProceduralMemory(store)
	sc := memory.Scope{UserID: 1, AgentID: "x"}
	if err := pm.RecordDecision(context.Background(), sc, "", "x", "approve", ""); err != nil {
		t.Fatal(err)
	}
	if s, _ := pm.Suggestion(context.Background(), sc, ""); s != "" {
		t.Fatalf("empty trigger should return empty")
	}
}
