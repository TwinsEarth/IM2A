package memory

import (
	"context"
	"testing"
)

func TestExportImportUser(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	// 用户 1 的两个 Agent 写记忆
	sc1 := Scope{UserID: 1, AgentID: "master"}
	sc2 := Scope{UserID: 1, AgentID: "sub-proc"}
	_ = s.PutWorking(ctx, sc1, "current", "taskA")
	_ = s.UpsertFact(ctx, sc2, &Fact{Key: "采购.平台", Value: "京东", Confidence: 0.9})
	_ = s.AppendEpisode(ctx, sc2, &Episode{Kind: "task", Summary: "买牛奶", Result: "success"})

	// 导出用户 1
	exp := s.ExportUser(ctx, 1)
	if exp.UserID != 1 {
		t.Fatalf("export user mismatch")
	}
	if len(exp.Scopes) != 2 {
		t.Fatalf("want 2 scopes, got %d", len(exp.Scopes))
	}
	if exp.Scopes["sub-proc"].Facts[0].Value != "京东" {
		t.Fatalf("fact not exported: %+v", exp.Scopes["sub-proc"])
	}

	// 新 store 导入
	s2 := NewMemoryStore()
	if err := s2.ImportUser(ctx, exp); err != nil {
		t.Fatal(err)
	}
	f, err := s2.GetFact(ctx, sc2, "采购.平台")
	if err != nil || f.Value != "京东" {
		t.Fatalf("import fact: %v %v", f, err)
	}
	eps, _ := s2.ListEpisodes(ctx, sc2, EpisodeQuery{})
	if len(eps) != 1 || eps[0].Summary != "买牛奶" {
		t.Fatalf("import episodes: %+v", eps)
	}
}

func TestStatsUser(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	sc := Scope{UserID: 1, AgentID: "sub-proc"}
	_ = s.UpsertFact(ctx, sc, &Fact{Key: "a", Value: "1"})
	_ = s.UpsertFact(ctx, sc, &Fact{Key: "b", Value: "2"})
	_ = s.AppendEpisode(ctx, sc, &Episode{Kind: "task", Summary: "x"})
	_ = s.PutWorking(ctx, sc, "w", "1")

	st := s.StatsUser(ctx, 1)
	if st.AgentCount != 1 || st.FactCount != 2 || st.EpisodeCount != 1 || st.WorkingCount != 1 {
		t.Fatalf("stats wrong: %+v", st)
	}
	// 用户 2 无数据
	st2 := s.StatsUser(ctx, 2)
	if st2.AgentCount != 0 {
		t.Fatalf("user2 should be empty: %+v", st2)
	}
}

func TestGuardExportOnlyMaster(t *testing.T) {
	g := NewGuarded(NewMemoryStore())
	ctx := context.Background()
	g.RegisterMaster(1, "master")
	sc := Scope{UserID: 1, AgentID: "sub-proc"}
	_ = g.UpsertFact(ctx, sc, &Fact{Key: "k", Value: "v"})

	// 非 master 导出被拒
	_, err := g.ExportUser(ctx, sc)
	if err != ErrAccessDenied {
		t.Fatalf("non-master export should be denied, got %v", err)
	}
	// master 导出成功
	exp, err := g.ExportUser(ctx, Scope{UserID: 1, AgentID: "master"})
	if err != nil {
		t.Fatalf("master export: %v", err)
	}
	if len(exp.Scopes) == 0 {
		t.Fatal("export should contain scopes")
	}
}
