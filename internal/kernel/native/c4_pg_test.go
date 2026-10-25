// SPDX-License-Identifier: MIT
//
// PG-gated C4 判定性测试：Resolver（negotiation 多播 / subagent owner）+ agent token 闭环。
package native

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/negotiation"
	"human-agent-im/internal/store"
)

func openC4PG(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set; PG-gated C4 test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// fakeNego 固定返回带 turns 的会话（不需要真 PG 协商表也能测 Resolver 多播映射）。
type fakeNego struct{ sess *negotiation.Session }

func (f fakeNego) LoadSession(context.Context, string) (*negotiation.Session, error) {
	if f.sess == nil {
		return nil, store.ErrNotFound
	}
	return f.sess, nil
}

// c4InsertUser 建一个 users 行（account_identities FK 需要）。
func c4InsertUser(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func c4CleanupUsers(t *testing.T, db *sql.DB, ids []int64) {
	t.Helper()
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = db.Exec(`DELETE FROM account_identities WHERE user_id=$1`, id)
			_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, id)
		}
	})
}

func TestC4ResolverMultiTopic(t *testing.T) {
	db := openC4PG(t)
	ctx := context.Background()
	base := time.Now().UnixNano()%100_000_000*10 + 6_000_000
	wxA, wxB := fmt.Sprintf("c4-wx-a-%d", base), fmt.Sprintf("c4-wx-b-%d", base)
	uidA, uidB := c4InsertUser(t, db), c4InsertUser(t, db)
	c4CleanupUsers(t, db, []int64{uidA, uidB})
	if _, err := db.Exec(`INSERT INTO account_identities(user_id, provider, open_id) VALUES ($1,'wechat',$2),($3,'wechat',$4)`,
		uidA, wxA, uidB, wxB); err != nil {
		t.Fatalf("insert identities: %v", err)
	}
	agentA, agentB := "c4-sub-a-"+fmt.Sprintf("%d", base), "c4-sub-b-"+fmt.Sprintf("%d", base)
	if _, err := db.Exec(`INSERT INTO agent_registry(user_wxid, agent_id) VALUES ($1,$2),($3,$4)`,
		wxA, agentA, wxB, agentB); err != nil {
		t.Fatalf("insert agent_registry: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM agent_registry WHERE agent_id IN ($1,$2)`, agentA, agentB)
	})

	// subagent → owner A；approval/trust 单播；negotiation → 双方 owners 多播。
	nego := fakeNego{sess: &negotiation.Session{
		ID: "c4-sess",
		Turns: []negotiation.Turn{
			{FromAgentID: agentA, ToAgentID: agentB},
		},
	}}
	r := NewResolver(db, nego)
	r.SetTTL(time.Minute, time.Minute)

	if users, ok := r.Resolve(ctx, "subagent:"+agentA); !ok || len(users) != 1 || users[0] != uidA {
		t.Fatalf("subagent resolve = (%v,%v), want ([%d],true)", users, ok, uidA)
	}
	if users, ok := r.Resolve(ctx, "negotiation:c4-sess"); !ok || len(users) != 2 {
		t.Fatalf("negotiation resolve = (%v,%v), want 2 users", users, ok)
	} else {
		seen := map[int64]bool{users[0]: true, users[1]: true}
		if !seen[uidA] || !seen[uidB] {
			t.Fatalf("negotiation 多播错了: %v (want %d,%d)", users, uidA, uidB)
		}
	}
	if users, ok := r.Resolve(ctx, "approval:42"); !ok || len(users) != 1 || users[0] != 42 {
		t.Fatalf("approval resolve = (%v,%v)", users, ok)
	}
	if _, ok := r.Resolve(ctx, "unknown:zzz"); ok {
		t.Fatal("unknown topic 不应 mapped")
	}
	t.Logf("C4_RESOLVER_OK: subagent→owner(%d)，negotiation→双方 owners 多播，approval 单播，unknown=unmapped", uidA)
}

func TestC4AgentTokenLifecycle(t *testing.T) {
	db := openC4PG(t)
	ctx := context.Background()
	svc := NewAgentTokenService(db, "c4-jwt-secret", 5*time.Minute)
	owner := time.Now().UnixNano()%1_000_000_000 + 7_000_000
	agentID := "c4-agent-" + fmt.Sprintf("%d", owner)

	res, err := svc.Issue(ctx, owner, agentID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM n_agent_token WHERE jti=$1`, res.Jti)
		_, _ = db.Exec(`DELETE FROM n_principal WHERE kind='agent' AND agent_id=$1`, agentID)
	})
	claims, err := auth.Parse("c4-jwt-secret", res.Token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if claims.Sub != res.Principal || claims.Iss != AgentIssuer || claims.Sub <= 0 {
		t.Fatalf("claims = %+v, want sub=%d iss=%s", claims, res.Principal, AgentIssuer)
	}
	if err := svc.ValidateClaims(ctx, claims); err != nil {
		t.Fatalf("未吊销 token 应通过: %v", err)
	}
	// 人类 token（iss 非 agent）不查吊销表 → 放行。
	if err := svc.ValidateClaims(ctx, &auth.Claims{Sub: 1, Iss: "im2a-test"}); err != nil {
		t.Fatalf("人类 token 不应被 agent 校验拦截: %v", err)
	}
	// 错误 owner 不能吊销。
	if err := svc.Revoke(ctx, owner+1, res.Jti); CodeOf(err) != CodeNotFound {
		t.Fatalf("错误 owner 吊销 code=%q", CodeOf(err))
	}
	if err := svc.Revoke(ctx, owner, res.Jti); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := svc.ValidateClaims(ctx, claims); err == nil {
		t.Fatal("吊销后 ValidateClaims 应失败")
	}
	t.Logf("C4_AGENT_TOKEN_OK: sub=%d iss=%s；吊销后校验失败（owner 作用域）", res.Principal, claims.Iss)
}
