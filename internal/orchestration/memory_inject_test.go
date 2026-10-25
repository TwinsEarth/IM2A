package orchestration

import (
	"context"
	"strings"
	"testing"

	"human-agent-im/internal/memory"
)

func TestC3_InjectsFactsAndEpisodes(t *testing.T) {
	store := memory.NewMemoryStore()
	ctx := context.Background()
	sc := memory.Scope{UserID: 1, AgentID: "procurement"}

	_ = store.UpsertFact(ctx, sc, &memory.Fact{Key: "coffee", Value: "美式", Confidence: 0.9})
	_ = store.UpsertFact(ctx, sc, &memory.Fact{Key: "milk", Value: "全脂", Confidence: 0.9})
	_ = store.AppendEpisode(ctx, sc, &memory.Episode{Kind: "task", Summary: "上周买过咖啡豆"})

	out := BuildSubagentContext(ctx, store, sc, "帮我买咖啡")
	if !strings.Contains(out, "美式") {
		t.Fatalf("fact not injected: %q", out)
	}
	if !strings.Contains(out, "上周买过咖啡豆") {
		t.Fatalf("episode not injected: %q", out)
	}
	if !strings.Contains(out, "【你的记忆】") {
		t.Fatalf("missing header: %q", out)
	}
}

func TestC3_DoesNotLeakOtherScope(t *testing.T) {
	store := memory.NewMemoryStore()
	ctx := context.Background()
	scA := memory.Scope{UserID: 1, AgentID: "procurement"}
	scB := memory.Scope{UserID: 1, AgentID: "legal"}

	_ = store.UpsertFact(ctx, scA, &memory.Fact{Key: "secret", Value: "采购内幕", Confidence: 0.9})
	_ = store.UpsertFact(ctx, scB, &memory.Fact{Key: "secret", Value: "法律内幕", Confidence: 0.9})

	out := BuildSubagentContext(ctx, store, scB, "合同")
	if strings.Contains(out, "采购内幕") {
		t.Fatalf("cross-scope leak: %q", out)
	}
	if !strings.Contains(out, "法律内幕") {
		t.Fatalf("own fact missing: %q", out)
	}
}

func TestC3_EmptyMemoryReturnsEmpty(t *testing.T) {
	store := memory.NewMemoryStore()
	sc := memory.Scope{UserID: 1, AgentID: "schedule"}
	if out := BuildSubagentContext(context.Background(), store, sc, "明天"); out != "" {
		t.Fatalf("should be empty, got %q", out)
	}
}

func TestC3_NilStoreSafe(t *testing.T) {
	sc := memory.Scope{UserID: 1, AgentID: "x"}
	if out := BuildSubagentContext(context.Background(), nil, sc, "hi"); out != "" {
		t.Fatalf("nil store should return empty, got %q", out)
	}
}

func TestC3_LowConfidenceFiltered(t *testing.T) {
	store := memory.NewMemoryStore()
	ctx := context.Background()
	sc := memory.Scope{UserID: 1, AgentID: "p"}
	_ = store.UpsertFact(ctx, sc, &memory.Fact{Key: "noise", Value: "垃圾", Confidence: 0.2})
	out := BuildSubagentContext(ctx, store, sc, "noise")
	if strings.Contains(out, "垃圾") {
		t.Fatalf("low-conf fact leaked: %q", out)
	}
}
