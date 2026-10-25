// SPDX-License-Identifier: MIT
//
// C2 判定性 HTTP E2E（task-23）：真 PG + 真 bootstrap.Assemble + routes.Register + JWT，
// 覆盖：幂等创建 / 上限 409 且事务回滚 / 非成员 404 / 归档 410 / 鉴权 401。
//
// 默认 skip：IM2A_E2E=1 + PG_DSN。复跑：
//
//	source /root/im2a-work/e2e-env.sh
//	cd /root/im2a-work/build
//	IM2A_E2E=1 PG_DSN="$PG_DSN" go test -p 1 ./internal/kernel/bootstrap -run TestC2ConversationE2E -v -count=1 -timeout 300s
package bootstrap_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/kernel/routes"
	"human-agent-im/internal/store"
)

const c2JWTSecret = "c2-e2e-jwt"

// c2Do 发 JSON 请求（token 为空则不设 Authorization），返回 status + body map。
func c2Do(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func c2ErrCode(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return ""
}

func TestC2ConversationE2E(t *testing.T) {
	dsn := b2cGate(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	b2cEnv(t, map[string]string{
		"OPENCLAW_ENABLED": "off",
		"DSBOT_ENABLED":    "off",
		"PG_DSN":           dsn,
		"GATEWAY_TOKEN":    "",
		"IM2A_ADMIN_TOKEN": "c2-admin",
		"IM2A_JWT_SECRET":  c2JWTSecret,
		"NATIVE_ENABLED":   "on",
		"CONN_MODE":        "",
		"CONN_UDS_PATH":    "",
	})
	k, err := bootstrap.Assemble(bootstrap.Load(0))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = k.Close(context.Background())
		}
	}()
	if k.NativeConv == nil {
		t.Fatalf("NATIVE_ENABLED=on + PG 但 NativeConv 未装配")
	}
	mux := http.NewServeMux()
	routes.Register(mux, k)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	base := srv.URL
	baseID := time.Now().UnixNano()%100_000_000*10 + 2_000_000
	owner := baseID + 1
	members := make([]int64, 0, 11)
	for i := 0; i < 11; i++ {
		members = append(members, baseID+2+int64(i))
	}
	thirteenth := baseID + 20
	stranger := baseID + 30
	users := append([]int64{owner, thirteenth, stranger}, members...)
	tokenOf := func(uid int64) string {
		tok, err := auth.Issue(c2JWTSecret, uid, time.Hour, "c2-e2e")
		if err != nil {
			t.Fatalf("issue jwt: %v", err)
		}
		return tok
	}
	ownerTok, memberTok, strangerTok := tokenOf(owner), tokenOf(members[0]), tokenOf(stranger)

	// 401：无 JWT。
	if status, _ := c2Do(t, http.MethodGet, base+"/v1/native/conversations", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("无 JWT GET conversations status=%d, want 401", status)
	}
	t.Logf("C2_AUTH_OK: 无 JWT → 401")

	// 1) 创建 GROUP（1+11=12 满员），idempotent client_conv_id。
	createBody := map[string]any{
		"kind": "GROUP", "title": "C2 E2E", "client_conv_id": "c2-client-1",
		"member_user_ids": members,
	}
	status, body := c2Do(t, http.MethodPost, base+"/v1/native/conversations", ownerTok, createBody)
	if status != http.StatusCreated || body["created"] != true {
		t.Fatalf("create status=%d body=%v", status, body)
	}
	conv := body["conversation"].(map[string]any)
	convID := conv["id"].(string)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM n_conversation_members WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_conversations WHERE id=$1`, convID)
		for _, uid := range users {
			_, _ = db.Exec(`DELETE FROM n_principal WHERE kind='human' AND user_id=$1`, uid)
		}
	})
	if got := int(conv["max_members"].(float64)); got != 12 {
		t.Fatalf("max_members=%d, want 12", got)
	}

	// 2) 幂等：同 client_conv_id 再 POST → 200 created=false 同 id。
	status, body = c2Do(t, http.MethodPost, base+"/v1/native/conversations", ownerTok, createBody)
	if status != http.StatusOK || body["created"] != false {
		t.Fatalf("idempotent create status=%d body=%v", status, body)
	}
	if got := body["conversation"].(map[string]any)["id"]; got != convID {
		t.Fatalf("idempotent 返回不同会话: %v vs %v", got, convID)
	}
	t.Logf("C2_IDEMPOTENT_OK: 第二次 created=false，id=%s", convID)

	// 3) 成员可读；非成员 404（fail-closed）。
	if status, _ := c2Do(t, http.MethodGet, base+"/v1/native/conversations/"+convID, memberTok, nil); status != 200 {
		t.Fatalf("成员 GET status=%d", status)
	}
	status, body = c2Do(t, http.MethodGet, base+"/v1/native/conversations/"+convID, strangerTok, nil)
	if status != http.StatusNotFound || c2ErrCode(body) != "CONVERSATION_NOT_FOUND" {
		t.Fatalf("非成员 GET status=%d code=%q body=%v", status, c2ErrCode(body), body)
	}
	t.Logf("C2_VISIBILITY_OK: 成员=200，非成员=404 CONVERSATION_NOT_FOUND")

	// 4) 第 13 个成员：409 MEMBER_LIMIT_REACHED 且成员数仍 12。
	status, body = c2Do(t, http.MethodPost, base+"/v1/native/conversations/"+convID+"/members", ownerTok,
		map[string]any{"user_id": thirteenth})
	if status != http.StatusConflict || c2ErrCode(body) != "MEMBER_LIMIT_REACHED" {
		t.Fatalf("第13成员 status=%d code=%q body=%v", status, c2ErrCode(body), body)
	}
	status, body = c2Do(t, http.MethodGet, base+"/v1/native/conversations/"+convID, ownerTok, nil)
	if status != 200 {
		t.Fatalf("GET after limit status=%d", status)
	}
	membersNow := body["conversation"].(map[string]any)["members"].([]any)
	if len(membersNow) != 12 {
		t.Fatalf("超限后成员数=%d, want 12（必须回滚）", len(membersNow))
	}
	t.Logf("C2_LIMIT_OK: 第13成员 → 409 MEMBER_LIMIT_REACHED；回滚后 members=12")

	// 5) 归档 410：owner archive → 200；再 add member → 410。
	status, body = c2Do(t, http.MethodPost, base+"/v1/native/conversations/"+convID+"/archive", ownerTok, nil)
	if status != 200 {
		t.Fatalf("archive status=%d body=%v", status, body)
	}
	if st := body["conversation"].(map[string]any)["status"]; st != "archived" {
		t.Fatalf("archive status=%v", st)
	}
	status, body = c2Do(t, http.MethodPost, base+"/v1/native/conversations/"+convID+"/members", ownerTok,
		map[string]any{"user_id": thirteenth})
	if status != http.StatusGone || c2ErrCode(body) != "CONVERSATION_ARCHIVED" {
		t.Fatalf("归档后加成员 status=%d code=%q body=%v", status, c2ErrCode(body), body)
	}
	// 归档会话仍可读。
	if status, _ := c2Do(t, http.MethodGet, base+"/v1/native/conversations/"+convID, memberTok, nil); status != 200 {
		t.Fatalf("归档会话成员 GET status=%d", status)
	}
	t.Logf("C2_ARCHIVE_OK: archive=200 status=archived；归档后加成员=410 CONVERSATION_ARCHIVED；读=200")

	// 6) 列表。
	status, body = c2Do(t, http.MethodGet, base+"/v1/native/conversations", ownerTok, nil)
	if status != 200 || int(body["count"].(float64)) < 1 {
		t.Fatalf("list status=%d body=%v", status, body)
	}
	t.Logf("C2_LIST_OK: count=%v", body["count"])

	_ = k.Close(context.Background())
	closed = true
	t.Logf("C2_E2E PASS: 401/幂等/409+回滚/404/410 全链路")
}

// 供失败信息扩展。
var _ = fmt.Sprintf
