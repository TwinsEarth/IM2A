package memory

import (
	"context"
	"testing"
)

func TestDecayConfidence(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	sc := Scope{UserID: 1, AgentID: "sub"}

	_ = s.UpsertFact(ctx, sc, &Fact{Key: "high", Value: "x", Confidence: 0.9})
	_ = s.UpsertFact(ctx, sc, &Fact{Key: "low", Value: "y", Confidence: 0.3})

	// 每次衰减到 90%：high 0.9→0.81 保留；low 0.3→0.27 <0.5 删除
	decayed, removed, err := s.DecayConfidence(ctx, sc, 0.9, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if decayed != 1 || removed != 1 {
		t.Fatalf("after 1st decay: decayed=%d removed=%d", decayed, removed)
	}
	// high 继续衰减
	for i := 0; i < 20; i++ {
		_, _, _ = s.DecayConfidence(ctx, sc, 0.9, 0.5)
	}
	facts, _ := s.ListFacts(ctx, sc)
	if len(facts) != 0 {
		t.Fatalf("all facts should decay below min, got %d", len(facts))
	}
}

func TestEpisodeIdempotentAppend(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	sc := Scope{UserID: 1, AgentID: "sub"}
	e := &Episode{ID: "fixed-id", Kind: "task", Summary: "x"}
	_ = s.AppendEpisode(ctx, sc, e)
	_ = s.AppendEpisode(ctx, sc, e) // 重复
	eps, _ := s.ListEpisodes(ctx, sc, EpisodeQuery{})
	if len(eps) != 1 {
		t.Fatalf("idempotent append should keep 1, got %d", len(eps))
	}
}

func TestSearchAllFacts(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.UpsertFact(ctx, Scope{UserID: 1, AgentID: "sub-a"}, &Fact{Key: "采购.平台", Value: "京东", Confidence: 0.9})
	_ = s.UpsertFact(ctx, Scope{UserID: 1, AgentID: "sub-b"}, &Fact{Key: "法律.偏好", Value: "书面", Confidence: 0.7})
	_ = s.UpsertFact(ctx, Scope{UserID: 2, AgentID: "other"}, &Fact{Key: "采购.平台", Value: "淘宝", Confidence: 0.8})

	// 用户 1 全局搜"采购"应只命中本用户
	hits, _ := s.SearchAllFacts(ctx, 1, "采购")
	if len(hits) != 1 || hits[0].Value != "京东" {
		t.Fatalf("cross-agent search wrong: %+v", hits)
	}
	// 用户 2 搜"采购"
	hits2, _ := s.SearchAllFacts(ctx, 2, "采购")
	if len(hits2) != 1 || hits2[0].Value != "淘宝" {
		t.Fatalf("user2 search wrong: %+v", hits2)
	}
}
