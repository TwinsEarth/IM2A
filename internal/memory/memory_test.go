package memory

import (
	"context"
	"errors"
	"testing"
)

func TestThreeLayerMemory(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	sc := Scope{UserID: 1, AgentID: "master"}

	// Working
	if err := s.PutWorking(ctx, sc, "current_task", "买牛奶"); err != nil {
		t.Fatal(err)
	}
	v, err := s.GetWorking(ctx, sc, "current_task")
	if err != nil || v != "买牛奶" {
		t.Fatalf("working get: v=%v err=%v", v, err)
	}
	if _, err := s.GetWorking(ctx, sc, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// Episodic
	_ = s.AppendEpisode(ctx, sc, &Episode{Kind: "task", Summary: "物业报修", Result: "success"})
	_ = s.AppendEpisode(ctx, sc, &Episode{Kind: "approval", Summary: "采购审批", Result: "pending"})
	eps, err := s.ListEpisodes(ctx, sc, EpisodeQuery{Kind: "task"})
	if err != nil || len(eps) != 1 || eps[0].Summary != "物业报修" {
		t.Fatalf("list episodes: %d %v", len(eps), err)
	}
	if eps[0].ID == "" || eps[0].Timestamp == 0 {
		t.Error("episode id/timestamp not set")
	}

	// Semantic
	_ = s.UpsertFact(ctx, sc, &Fact{Key: "采购.偏好平台", Value: "京东", Confidence: 0.9, Source: "manual"})
	_ = s.UpsertFact(ctx, sc, &Fact{Key: "采购.夜间阈值", Value: "500", Confidence: 0.6, Source: "distilled"})
	got, err := s.GetFact(ctx, sc, "采购.偏好平台")
	if err != nil || got.Value != "京东" {
		t.Fatalf("get fact: %v %v", got, err)
	}
	hits, _ := s.SearchFacts(ctx, sc, "采购")
	if len(hits) != 2 {
		t.Fatalf("search want 2 hits, got %d", len(hits))
	}
	// 置信度高的排前面
	if hits[0].Confidence < hits[1].Confidence {
		t.Error("facts should be ordered by confidence desc")
	}
}

func TestNamespaceIsolation(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	a := Scope{UserID: 1, AgentID: "sub-procurement"}
	b := Scope{UserID: 1, AgentID: "sub-legal"}

	_ = s.PutWorking(ctx, a, "secret", "A的采购清单")
	_ = s.UpsertFact(ctx, a, &Fact{Key: "k", Value: "A-only"})

	// B 读不到 A 的 working（不同命名空间）
	if _, err := s.GetWorking(ctx, b, "secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("B should not see A working, got %v", err)
	}
	// B 读不到 A 的 fact
	if _, err := s.GetFact(ctx, b, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("B should not see A fact, got %v", err)
	}
}

func TestGuardCrossAgentDeny(t *testing.T) {
	g := NewGuarded(NewMemoryStore())
	ctx := context.Background()
	proc := Scope{UserID: 1, AgentID: "sub-procurement"}
	legal := Scope{UserID: 1, AgentID: "sub-legal"}

	_ = g.UpsertFact(ctx, proc, &Fact{Key: "供应商", Value: "内部价"})
	// legal 未授权，越权读取被拒
	if _, err := g.ReadFactsOf(ctx, legal, proc); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("unauthorized cross-agent read should be denied, got %v", err)
	}
}

func TestGuardMasterAccess(t *testing.T) {
	g := NewGuarded(NewMemoryStore())
	ctx := context.Background()
	master := Scope{UserID: 1, AgentID: "master"}
	proc := Scope{UserID: 1, AgentID: "sub-procurement"}

	g.RegisterMaster(1, "master")
	_ = g.UpsertFact(ctx, proc, &Fact{Key: "供应商", Value: "内部价"})

	// 总 Agent 可读取子 Agent 记忆
	facts, err := g.ReadFactsOf(ctx, master, proc)
	if err != nil {
		t.Fatalf("master should read sub-agent facts, got %v", err)
	}
	if len(facts) != 1 || facts[0].Value != "内部价" {
		t.Fatalf("master read wrong facts: %v", facts)
	}
	// 总 Agent 可读取子 Agent 工作记忆（写权限）
	_ = g.PutWorking(ctx, proc, "note", "x")
	if _, err := g.ReadWorkingFrom(ctx, master, proc, "note"); err != nil {
		t.Fatalf("master read sub working: %v", err)
	}
}

func TestGuardExplicitGrant(t *testing.T) {
	g := NewGuarded(NewMemoryStore())
	ctx := context.Background()
	proc := Scope{UserID: 1, AgentID: "sub-procurement"}
	legal := Scope{UserID: 1, AgentID: "sub-legal"}

	_ = g.AppendEpisode(ctx, proc, &Episode{Kind: "task", Summary: "合同审核", Result: "success"})

	// 授权前拒绝
	if _, err := g.ListEpisodesOf(ctx, legal, proc, EpisodeQuery{}); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("before grant should deny, got %v", err)
	}
	// 总 Agent 给 legal 读权限
	g.Grant(proc, "sub-legal", AccessRead)
	eps, err := g.ListEpisodesOf(ctx, legal, proc, EpisodeQuery{})
	if err != nil {
		t.Fatalf("after grant should allow, got %v", err)
	}
	if len(eps) != 1 {
		t.Fatalf("want 1 episode, got %d", len(eps))
	}
	// 撤销后再次拒绝
	g.Revoke(proc, "sub-legal")
	if _, err := g.ListEpisodesOf(ctx, legal, proc, EpisodeQuery{}); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("after revoke should deny, got %v", err)
	}
}

func TestWorkingTTL(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	sc := Scope{UserID: 1, AgentID: "master"}

	// 临时回退 now 以模拟过期
	origNow := now
	defer func() { now = origNow }()
	cur := int64(1000000)
	now = func() int64 { return cur }

	_ = s.PutWorkingWithTTL(ctx, sc, "session_token", "abc", 60)
	v, err := s.GetWorking(ctx, sc, "session_token")
	if err != nil || v != "abc" {
		t.Fatalf("before expire: v=%v err=%v", v, err)
	}
	// 推进 61 秒
	now = func() int64 { return cur + 61 }
	if _, err := s.GetWorking(ctx, sc, "session_token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after expire should be ErrNotFound, got %v", err)
	}

	// 永不过期
	_ = s.PutWorking(ctx, sc, "perm", "x")
	now = func() int64 { return cur + 1000000 }
	v, err = s.GetWorking(ctx, sc, "perm")
	if err != nil || v != "x" {
		t.Fatalf("no-TTL key should persist, v=%v err=%v", v, err)
	}
}

func TestGuardCrossUserAlwaysDeny(t *testing.T) {
	g := NewGuarded(NewMemoryStore())
	ctx := context.Background()
	// 用户 1 的 master
	master1 := Scope{UserID: 1, AgentID: "master"}
	// 用户 2 的子 Agent
	proc2 := Scope{UserID: 2, AgentID: "sub-procurement"}

	g.RegisterMaster(1, "master")
	g.RegisterMaster(2, "master")
	_ = g.UpsertFact(ctx, proc2, &Fact{Key: "k", Value: "用户2隐私"})

	// 即使是用户1的 master，也绝不能访问用户2的记忆
	if _, err := g.ReadFactsOf(ctx, master1, proc2); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("cross-user access must be denied, got %v", err)
	}
	if _, err := g.ReadWorkingFrom(ctx, master1, proc2, "x"); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("cross-user working read must be denied, got %v", err)
	}
}
