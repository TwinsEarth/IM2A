package memory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// v10OpenPG 打开真实 PostgreSQL；未设置 PG_DSN 时跳过（禁止 mock 桩）。
func v10OpenPG(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set; skip real PG integration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	db.SetMaxOpenConns(10)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func v10Scope(t *testing.T, db *sql.DB) Scope {
	t.Helper()
	uid := uint64(900000000 + time.Now().UnixNano()%99000000)
	ag := fmt.Sprintf("v10mem_%d", time.Now().UnixNano())
	sc := Scope{UserID: uid, AgentID: ag}
	// 零残留：真实落库测试结束必须删除本 scope 写入的所有行。
	t.Cleanup(func() {
		for _, tb := range []string{"working_memory", "episodes", "facts"} {
			if _, err := db.Exec(fmt.Sprintf("DELETE FROM %s WHERE user_id=$1 AND agent_id=$2", tb), uid, ag); err != nil {
				t.Errorf("cleanup %s: %v", tb, err)
			}
		}
	})
	return sc
}

// TestPGWorkingMemoryCRUDAndTTL 真实落库：工作记忆写/读/删 + TTL 过期。
func TestPGWorkingMemoryCRUDAndTTL(t *testing.T) {
	db := v10OpenPG(t)
	st := NewPGStore(db)
	ctx := context.Background()
	sc := v10Scope(t, db)

	if err := st.PutWorkingWithTTL(ctx, sc, "k1", map[string]any{"x": 1}, 0); err != nil {
		t.Fatalf("put working: %v", err)
	}
	if _, err := st.GetWorking(ctx, sc, "k1"); err != nil {
		t.Fatalf("get after put: %v", err)
	}
	// TTL=1s，过期后应被清理
	if err := st.PutWorkingWithTTL(ctx, sc, "kttl", "v", 1); err != nil {
		t.Fatalf("put ttl: %v", err)
	}
	if _, err := st.GetWorking(ctx, sc, "kttl"); err != nil {
		t.Fatalf("ttl key should exist before expiry: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := st.GetWorking(ctx, sc, "kttl"); err != ErrNotFound {
		t.Fatalf("expect ErrNotFound after TTL, got %v", err)
	}
	if err := st.DeleteWorking(ctx, sc, "k1"); err != nil {
		t.Fatalf("delete working: %v", err)
	}
	if _, err := st.GetWorking(ctx, sc, "k1"); err != ErrNotFound {
		t.Fatalf("expect ErrNotFound after delete, got %v", err)
	}
}

// TestPGEpisodesTagOnlyFilter 回归 ListEpisodes 仅 tag 过滤时 $3/$4 占位符错乱的真实缺陷。
func TestPGEpisodesTagOnlyFilter(t *testing.T) {
	db := v10OpenPG(t)
	st := NewPGStore(db)
	ctx := context.Background()
	sc := v10Scope(t, db)
	ts := time.Now().Unix()
	eps := []*Episode{
		{ID: fmt.Sprintf("ep_%d_a", ts), Timestamp: ts, Kind: "task", Summary: "a", Tags: []string{"alpha"}},
		{ID: fmt.Sprintf("ep_%d_b", ts), Timestamp: ts + 1, Kind: "approval", Summary: "b", Tags: []string{"beta"}},
	}
	for _, e := range eps {
		if err := st.AppendEpisode(ctx, sc, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// 仅 tag（Kind 为空）——修复前 PG 必然报绑定错误
	got, err := st.ListEpisodes(ctx, sc, EpisodeQuery{Tag: "alpha", Limit: 10})
	if err != nil {
		t.Fatalf("tag-only filter broken (placeholder bug): %v", err)
	}
	if len(got) != 1 || len(got[0].Tags) == 0 || got[0].Tags[0] != "alpha" {
		t.Fatalf("tag-only want 1 alpha, got %d", len(got))
	}
	// 仅 kind
	got2, err := st.ListEpisodes(ctx, sc, EpisodeQuery{Kind: "approval", Limit: 10})
	if err != nil {
		t.Fatalf("kind filter: %v", err)
	}
	if len(got2) != 1 || got2[0].Kind != "approval" {
		t.Fatalf("kind filter want 1 approval, got %d", len(got2))
	}
	// 无 kind 无 tag（LIMIT 占位符必须正确）
	got3, err := st.ListEpisodes(ctx, sc, EpisodeQuery{Limit: 10})
	if err != nil {
		t.Fatalf("no filter: %v", err)
	}
	if len(got3) != 2 {
		t.Fatalf("no filter want 2, got %d", len(got3))
	}
}

// TestPGFactsSearchAndEmbedding 真实落库：ILIKE 关键词检索 + embedding 向量存取往返。
func TestPGFactsSearchAndEmbedding(t *testing.T) {
	db := v10OpenPG(t)
	st := NewPGStore(db)
	ctx := context.Background()
	sc := v10Scope(t, db)
	facts := []*Fact{
		{Key: "coffee", Value: "偏好美式咖啡", Confidence: 0.9, Source: "manual"},
		{Key: "meeting", Value: "周四下午开会", Confidence: 0.7, Source: "approval"},
		{Key: "emb", Value: "带向量事实", Confidence: 0.8, Source: "distilled", Embedding: []float32{0.1, 0.2, 0.3}},
	}
	for _, f := range facts {
		if err := st.UpsertFact(ctx, sc, f); err != nil {
			t.Fatalf("upsert fact %s: %v", f.Key, err)
		}
	}
	// v1.9.4：向量余弦检索返回 topK 并按相似度排序（不再 ILIKE 硬过滤）。
	got, err := st.SearchFacts(ctx, sc, "咖啡")
	if err != nil {
		t.Fatalf("search facts: %v", err)
	}
	if len(got) == 0 || got[0].Key != "coffee" {
		t.Fatalf("向量检索「咖啡」近邻第一应是 coffee, got %d keys=%v", len(got), keysOf(got))
	}
	f, err := st.GetFact(ctx, sc, "emb")
	if err != nil {
		t.Fatalf("get fact emb: %v", err)
	}
	if len(f.Embedding) != 3 || f.Embedding[0] != 0.1 || f.Embedding[2] != 0.3 {
		t.Fatalf("embedding round-trip mismatch: %v", f.Embedding)
	}
}

func keysOf(fs []*Fact) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Key)
	}
	return out
}
