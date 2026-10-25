package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"human-agent-im/internal/auth"
)

// TestV192EnvelopeE2E：端到端加密——库里只存密文、密钥在客户端、错 key/篡改不可解、横向越权。
func TestV192EnvelopeE2E(t *testing.T) {
	db := v19OpenDB(t)
	t.Cleanup(func() { _ = db.Close() })
	deps := &Deps{DB: db}
	const secret = "v192-secret"
	const staticTok = "static-192"

	identA := "v192-A-" + fmt.Sprint(time.Now().UnixNano())
	identB := "v192-B-" + fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() {
		db.Exec("DELETE FROM encrypted_context WHERE user_id IN (SELECT id FROM users WHERE identifier LIKE 'v192-%')")
		db.Exec("DELETE FROM users WHERE identifier LIKE 'v192-%'")
	})
	uidA, _ := deps.upsertUserByIdentifier(identA)
	uidB, _ := deps.upsertUserByIdentifier(identB)

	keyA := make([]byte, 32)
	keyB := make([]byte, 32)
	rand.Read(keyA)
	rand.Read(keyB)
	keyAB64 := base64.StdEncoding.EncodeToString(keyA)
	keyBB64 := base64.StdEncoding.EncodeToString(keyB)

	aTok, _ := auth.Issue(secret, uidA, time.Hour, "im2a")

	putH := auth.Authenticate(http.HandlerFunc(deps.PutEncryptedContext), staticTok, secret)
	getH := auth.Authenticate(http.HandlerFunc(deps.GetEncryptedContext), staticTok, secret)

	put := func(tok, keyHeader string, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/memory/encrypted", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if keyHeader != "" {
			req.Header.Set("X-Data-Key", keyHeader)
		}
		putH.ServeHTTP(rec, req)
		return rec
	}
	get := func(tok, keyHeader, id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/memory/encrypted?id="+id, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if keyHeader != "" {
			req.Header.Set("X-Data-Key", keyHeader)
		}
		getH.ServeHTTP(rec, req)
		return rec
	}

	const secretPT = "SECRET-PLAINTEXT-银行卡-6222"

	// 1) 带 A 的 token + 正确 key 写入 → 200
	rec := put(aTok, keyAB64, fmt.Sprintf(`{"plaintext":%q,"realm":"finance"}`, secretPT))
	if rec.Code != 200 {
		t.Fatalf("put 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var putResp struct{ ID int64 `json:"id"` }
	json.NewDecoder(rec.Body).Decode(&putResp)
	if putResp.ID == 0 {
		t.Fatalf("put 未返回 id: %s", rec.Body.String())
	}

	// 2) 直接查库：ciphertext 列绝不能包含明文
	var storedCT []byte
	if err := db.QueryRow("SELECT ciphertext FROM encrypted_context WHERE id=$1", putResp.ID).Scan(&storedCT); err != nil {
		t.Fatalf("查库: %v", err)
	}
	if bytes.Contains(storedCT, []byte(secretPT)) {
		t.Fatal("严重：密文列包含明文，端到端加密失效")
	}

	// 3) A 带正确 key 读回 → 200 且明文还原
	rec = get(aTok, keyAB64, fmt.Sprint(putResp.ID))
	if rec.Code != 200 {
		t.Fatalf("get 正确 key 应 200，实际 %d", rec.Code)
	}
	var getResp struct{ Plaintext string `json:"plaintext"` }
	json.NewDecoder(rec.Body).Decode(&getResp)
	if getResp.Plaintext != secretPT {
		t.Fatalf("解密还原失败：%q", getResp.Plaintext)
	}

	// 4) A 带错误 key 读 → 解密失败（422）
	rec = get(aTok, keyBB64, fmt.Sprint(putResp.ID))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("错 key 应 422，实际 %d %s", rec.Code, rec.Body.String())
	}

	// 5) A 不带 key 读 → 只返回密文元信息，不返回明文
	rec = get(aTok, "", fmt.Sprint(putResp.ID))
	if rec.Code != 200 || bytes.Contains(rec.Body.Bytes(), []byte(secretPT)) {
		t.Fatalf("无 key 不应泄露明文 body=%s", rec.Body.String())
	}

	// 6) 写入不带 key → 400
	rec = put(aTok, "", `{"plaintext":"x","realm":"finance"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("无 key 写入应 400，实际 %d", rec.Code)
	}

	// 7) 横向越权：B 的 token 读 A 的记录 → 403
	bTok, _ := auth.Issue(secret, uidB, time.Hour, "im2a")
	rec = get(bTok, keyAB64, fmt.Sprint(putResp.ID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("B 读 A 应 403，实际 %d", rec.Code)
	}
}
