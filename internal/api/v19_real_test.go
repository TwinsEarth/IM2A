package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"human-agent-im/internal/auth"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// v19OpenDB 连真实 PG；PG_DSN 未设置则跳过（CI 无 PG 也能过）。
func v19OpenDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

// TestV190LoginUpsertAndMe：login 真实落 users、同 identifier 复用、me 返回 token 主体、401 矩阵。
func TestV190LoginUpsertAndMe(t *testing.T) {
	db := v19OpenDB(t)
	t.Cleanup(func() { _ = db.Close() })
	deps := &Deps{DB: db}
	const secret = "v190-test-secret"
	ident := "v190-" + fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE identifier LIKE 'v190-%'") })

	// 第一次 login：建新用户
	body, _ := json.Marshal(LoginRequest{Identifier: ident})
	rec := httptest.NewRecorder()
	deps.AuthLogin(secret, time.Hour)(rec, httptest.NewRequest("POST", "/v1/auth/login", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("login 期望 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
		UserID      int64  `json:"user_id"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.UserID == 0 || resp.AccessToken == "" {
		t.Fatalf("bad login resp: %s", rec.Body.String())
	}

	// 真实落库断言
	var dbIdent string
	if err := db.QueryRow("SELECT identifier FROM users WHERE id=$1", resp.UserID).Scan(&dbIdent); err != nil || dbIdent != ident {
		t.Fatalf("未真实落库 id=%d err=%v ident=%q", resp.UserID, err, dbIdent)
	}

	// 第二次 login：同 identifier 复用同一 id
	rec2 := httptest.NewRecorder()
	deps.AuthLogin(secret, time.Hour)(rec2, httptest.NewRequest("POST", "/v1/auth/login", bytes.NewReader(body)))
	var resp2 struct {
		UserID int64 `json:"user_id"`
	}
	json.NewDecoder(rec2.Body).Decode(&resp2)
	if resp2.UserID != resp.UserID {
		t.Fatalf("同 identifier 应复用 id %d，实际 %d", resp.UserID, resp2.UserID)
	}
	var cnt int
	if err := db.QueryRow("SELECT count(*) FROM users WHERE identifier=$1", ident).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("应只 1 行，实际 cnt=%d err=%v", cnt, err)
	}

	// me：合法 token 返回主体
	meHandler := auth.RequireIdentity(http.HandlerFunc(deps.AuthMe), secret)
	meReq := httptest.NewRequest("GET", "/v1/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+resp.AccessToken)
	meRec := httptest.NewRecorder()
	meHandler.ServeHTTP(meRec, meReq)
	if meRec.Code != 200 {
		t.Fatalf("me 期望 200，实际 %d body=%s", meRec.Code, meRec.Body.String())
	}
	var meResp struct {
		UserID int64 `json:"user_id"`
	}
	json.NewDecoder(meRec.Body).Decode(&meResp)
	if meResp.UserID != resp.UserID {
		t.Fatalf("me 返回 %d，期望 %d", meResp.UserID, resp.UserID)
	}

	// me 无 token -> 401
	meRecNo := httptest.NewRecorder()
	meHandler.ServeHTTP(meRecNo, httptest.NewRequest("GET", "/v1/auth/me", nil))
	if meRecNo.Code != 401 {
		t.Fatalf("me 无 token 应 401，实际 %d", meRecNo.Code)
	}

	// me 错签 token -> 401
	bad, _ := auth.Issue("wrong-secret", 99, time.Hour, "im2a")
	meRecBad := httptest.NewRecorder()
	badReq := httptest.NewRequest("GET", "/v1/auth/me", nil)
	badReq.Header.Set("Authorization", "Bearer "+bad)
	meHandler.ServeHTTP(meRecBad, badReq)
	if meRecBad.Code != 401 {
		t.Fatalf("me 错签应 401，实际 %d", meRecBad.Code)
	}
}
