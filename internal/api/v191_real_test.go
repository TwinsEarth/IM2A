package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"human-agent-im/internal/auth"
)

// TestV191HorizontalIsolation（v1.9.1）：JWT 主体优先于 query user_id；
// A 不能冒充/读取 B 的数据；静态共享 token 向后兼容；无/错 token 401。
func TestV191HorizontalIsolation(t *testing.T) {
	db := v19OpenDB(t)
	t.Cleanup(func() { _ = db.Close() })
	deps := &Deps{DB: db}
	const secret = "v191-test-secret"
	const staticTok = "demo-static-token"

	identA := "v191-A-" + fmt.Sprint(time.Now().UnixNano())
	identB := "v191-B-" + fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE identifier LIKE 'v191-%'") })

	uidA, err := deps.upsertUserByIdentifier(identA)
	if err != nil {
		t.Fatalf("upsert A: %v", err)
	}
	uidB, err := deps.upsertUserByIdentifier(identB)
	if err != nil {
		t.Fatalf("upsert B: %v", err)
	}
	if uidA == 0 || uidB == 0 || uidA == uidB {
		t.Fatalf("两个不同用户应落到不同 id: A=%d B=%d", uidA, uidB)
	}

	aTok, _ := auth.Issue(secret, uidA, time.Hour, "im2a")
	bTok, _ := auth.Issue(secret, uidB, time.Hour, "im2a")

	// 待保护的资源：listPushTasks（按 user_id 查 im_push_tasks）
	h := auth.Authenticate(http.HandlerFunc(deps.listPushTasks), staticTok, secret)

	do := func(tok, url string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", url, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// 1) A 带 JWT 却请求 B 的数据 → 403（横向越权拦截）
	if c := do(aTok, "/v1/push/tasks?user_id="+strconv.FormatInt(uidB, 10)); c != http.StatusForbidden {
		t.Fatalf("A 读 B 应 403，实际 %d", c)
	}
	// 2) B 带 JWT 却请求 A 的数据 → 403
	if c := do(bTok, "/v1/push/tasks?user_id="+strconv.FormatInt(uidA, 10)); c != http.StatusForbidden {
		t.Fatalf("B 读 A 应 403，实际 %d", c)
	}
	// 3) A 读自己（query 与 JWT 一致）→ 200
	if c := do(aTok, "/v1/push/tasks?user_id="+strconv.FormatInt(uidA, 10)); c != http.StatusOK {
		t.Fatalf("A 读自己应 200，实际 %d", c)
	}
	// 4) A 带 JWT 但不带 user_id → 自动用 JWT 主体（uidA）→ 200
	if c := do(aTok, "/v1/push/tasks"); c != http.StatusOK {
		t.Fatalf("A 不带 user_id 应落到自己并 200，实际 %d", c)
	}
	// 5) 静态共享 token：无 JWT 身份，回退 query → 200（向后兼容）
	if c := do(staticTok, "/v1/push/tasks?user_id=999999"); c != http.StatusOK {
		t.Fatalf("静态 token 兼容应 200，实际 %d", c)
	}
	// 6) 无 token → 401
	if c := do("", "/v1/push/tasks?user_id=1"); c != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401，实际 %d", c)
	}
	// 7) 错签 token → 401
	badTok, _ := auth.Issue("wrong-secret", uidA, time.Hour, "im2a")
	if c := do(badTok, "/v1/push/tasks?user_id=1"); c != http.StatusUnauthorized {
		t.Fatalf("错签 token 应 401，实际 %d", c)
	}

}
