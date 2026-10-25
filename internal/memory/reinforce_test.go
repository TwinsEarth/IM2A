package memory

import (
	"context"
	"math"
	"testing"
)

func diff(a, b float64) float64 { return math.Abs(a - b) }

func TestReinforceFact(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	sc := Scope{UserID: 1, AgentID: "sub"}
	_ = s.UpsertFact(ctx, sc, &Fact{Key: "k", Value: "v", Confidence: 0.5})

	// 强化 +0.2
	f, err := s.ReinforceFact(ctx, sc, "k", 0.2)
	if err != nil || f.Confidence != 0.7 {
		t.Fatalf("reinforce up: %v %v", f, err)
	}
	// 弱化 -0.3
	f, _ = s.ReinforceFact(ctx, sc, "k", -0.3)
	if diff(f.Confidence, 0.4) > 0.001 {
		t.Fatalf("reinforce down: %.2f", f.Confidence)
	}
	// 裁剪到 [0,1]
	_, _ = s.ReinforceFact(ctx, sc, "k", 10)
	f, _ = s.GetFact(ctx, sc, "k")
	if f.Confidence != 1 {
		t.Fatalf("should clamp to 1, got %.2f", f.Confidence)
	}
	// 不存在
	if _, err := s.ReinforceFact(ctx, sc, "missing", 0.1); err != ErrNotFound {
		t.Fatalf("missing key should ErrNotFound, got %v", err)
	}
}

func TestEpisodeTagFilter(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	sc := Scope{UserID: 1, AgentID: "sub"}
	_ = s.AppendEpisode(ctx, sc, &Episode{Kind: "task", Summary: "采购", Tags: []string{"proc"}})
	_ = s.AppendEpisode(ctx, sc, &Episode{Kind: "task", Summary: "法律", Tags: []string{"legal"}})
	_ = s.AppendEpisode(ctx, sc, &Episode{Kind: "task", Summary: "杂事", Tags: []string{"proc", "misc"}})

	eps, _ := s.ListEpisodes(ctx, sc, EpisodeQuery{Tag: "proc"})
	if len(eps) != 2 {
		t.Fatalf("tag=proc want 2, got %d", len(eps))
	}
	eps, _ = s.ListEpisodes(ctx, sc, EpisodeQuery{Tag: "legal"})
	if len(eps) != 1 || eps[0].Summary != "法律" {
		t.Fatalf("tag=legal wrong: %+v", eps)
	}
}

func TestGCWorking(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	sc := Scope{UserID: 1, AgentID: "sub"}

	origNow := now
	defer func() { now = origNow }()
	cur := int64(2000000)
	now = func() int64 { return cur }

	_ = s.PutWorkingWithTTL(ctx, sc, "t1", "x", 60)
	_ = s.PutWorkingWithTTL(ctx, sc, "t2", "y", 60)
	_ = s.PutWorking(ctx, sc, "perm", "z")

	// 未过期：GC 清理 0
	if n, _ := s.GCWorking(ctx, sc); n != 0 {
		t.Fatalf("no expired, want 0, got %d", n)
	}
	// 推进 120 秒
	now = func() int64 { return cur + 120 }
	n, _ := s.GCWorking(ctx, sc)
	if n != 2 {
		t.Fatalf("want 2 cleaned, got %d", n)
	}
	// perm 仍在
	if _, err := s.GetWorking(ctx, sc, "perm"); err != nil {
		t.Fatalf("perm should survive GC: %v", err)
	}
}
