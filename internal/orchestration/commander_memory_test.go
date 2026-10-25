package orchestration

import (
	"context"
	"strings"
	"testing"

	"human-agent-im/internal/memory"
)

func TestC2_CoordinationRoundtrip(t *testing.T) {
	store := memory.NewMemoryStore()
	cm := NewCommanderMemory(store)
	ctx := context.Background()
	if got := cm.GetCoordination(ctx, 1); got != "" {
		t.Fatalf("empty should be empty, got %q", got)
	}
	if err := cm.SetCoordination(ctx, 1, "用户最近在忙采购和日程"); err != nil {
		t.Fatal(err)
	}
	if got := cm.GetCoordination(ctx, 1); got != "用户最近在忙采购和日程" {
		t.Fatalf("roundtrip failed: %q", got)
	}
}

func TestC2_CoordinationDoesNotLeakToSubagent(t *testing.T) {
	store := memory.NewMemoryStore()
	cm := NewCommanderMemory(store)
	ctx := context.Background()
	_ = cm.SetCoordination(ctx, 1, "总Agent内部摘要")
	// 子 Agent 用自己的 Scope 读 working，看不到 commander 的 key
	sub := memory.Scope{UserID: 1, AgentID: "procurement"}
	v, err := store.GetWorking(ctx, sub, "coordination")
	if err == nil && v != nil {
		t.Fatalf("subagent must not see commander coordination: %v", v)
	}
}

func TestC2_UserDigestAggregatesAcrossAgents(t *testing.T) {
	store := memory.NewMemoryStore()
	cm := NewCommanderMemory(store)
	ctx := context.Background()
	_ = store.AppendEpisode(ctx, memory.Scope{UserID: 1, AgentID: "procurement"}, &memory.Episode{Kind: "task", Summary: "买了咖啡豆"})
	_ = store.AppendEpisode(ctx, memory.Scope{UserID: 1, AgentID: "schedule"}, &memory.Episode{Kind: "task", Summary: "排了周三会"})
	out := cm.UserDigest(ctx, 1, []string{"procurement", "schedule"})
	if !strings.Contains(out, "procurement") || !strings.Contains(out, "买了咖啡豆") {
		t.Fatalf("missing procurement: %q", out)
	}
	if !strings.Contains(out, "schedule") || !strings.Contains(out, "排了周三会") {
		t.Fatalf("missing schedule: %q", out)
	}
}

func TestC2_UserDigestSkipsCommanderSelf(t *testing.T) {
	store := memory.NewMemoryStore()
	cm := NewCommanderMemory(store)
	ctx := context.Background()
	_ = store.AppendEpisode(ctx, memory.Scope{UserID: 1, AgentID: CommanderAgentID}, &memory.Episode{Kind: "sys", Summary: "commander自身记录"})
	out := cm.UserDigest(ctx, 1, []string{CommanderAgentID})
	if strings.Contains(out, "commander自身记录") {
		t.Fatalf("commander self must not be included: %q", out)
	}
}

func TestC2_CoordinationTruncated(t *testing.T) {
	store := memory.NewMemoryStore()
	cm := NewCommanderMemory(store)
	ctx := context.Background()
	long := strings.Repeat("很长。", 400) // 1600 字
	_ = cm.SetCoordination(ctx, 1, long)
	got := cm.GetCoordination(ctx, 1)
	if r := []rune(got); len(r) > 1005 {
		t.Fatalf("coordination too long: %d", r)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("should be ellipsized: %q", got)
	}
}

func TestC2_NilSafe(t *testing.T) {
	cm := NewCommanderMemory(nil)
	if got := cm.GetCoordination(context.Background(), 1); got != "" {
		t.Fatalf("nil store should return empty")
	}
	if got := cm.UserDigest(context.Background(), 1, []string{"x"}); got != "" {
		t.Fatalf("nil digest should be empty")
	}
}
