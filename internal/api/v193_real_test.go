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
	"human-agent-im/internal/envelope"
)

// TestV193ContextPortableExportImport：A 导出加密包 → B 用同一把 key 导入 → B 能还原 A 的明文。
func TestV193ContextPortableExportImport(t *testing.T) {
	db := v19OpenDB(t)
	t.Cleanup(func() { _ = db.Close() })
	deps := &Deps{DB: db}
	const secret = "v193-secret"
	const staticTok = "static-193"

	identA := "v193-A-" + fmt.Sprint(time.Now().UnixNano())
	identB := "v193-B-" + fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() {
		db.Exec("DELETE FROM encrypted_context WHERE user_id IN (SELECT id FROM users WHERE identifier LIKE 'v193-%')")
		db.Exec("DELETE FROM users WHERE identifier LIKE 'v193-%'")
	})
	uidA, _ := deps.upsertUserByIdentifier(identA)
	uidB, _ := deps.upsertUserByIdentifier(identB)

	keyA := make([]byte, 32)
	keyB := make([]byte, 32)
	rand.Read(keyA)
	rand.Read(keyB)
	keyAB64 := base64.StdEncoding.EncodeToString(keyA)

	aTok, _ := auth.Issue(secret, uidA, time.Hour, "im2a")
	bTok, _ := auth.Issue(secret, uidB, time.Hour, "im2a")

	putH := auth.Authenticate(http.HandlerFunc(deps.PutEncryptedContext), staticTok, secret)
	expH := auth.Authenticate(http.HandlerFunc(deps.ExportEncryptedContext), staticTok, secret)
	impH := auth.Authenticate(http.HandlerFunc(deps.ImportEncryptedContext), staticTok, secret)

	call := func(h http.Handler, tok, keyH, method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		}
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		if keyH != "" {
			r.Header.Set("X-Data-Key", keyH)
		}
		h.ServeHTTP(rec, r)
		return rec
	}

	for _, pt := range []string{"MSG-ONE-银行", "MSG-TWO-物业"} {
		rec := call(putH, aTok, keyAB64, "POST", "/v1/memory/encrypted",
			fmt.Sprintf(`{"plaintext":%q,"realm":"migrate"}`, pt))
		if rec.Code != 200 {
			t.Fatalf("A 写入失败 %d %s", rec.Code, rec.Body.String())
		}
	}

	rec := call(expH, aTok, "", "GET", "/v1/memory/encrypted/export", "")
	if rec.Code != 200 {
		t.Fatalf("export %d %s", rec.Code, rec.Body.String())
	}
	pkgBytes := rec.Body.Bytes()
	var pkg struct {
		Entries []struct {
			Nonce  string `json:"nonce_b64"`
			Cipher string `json:"ciphertext_b64"`
		} `json:"entries"`
		Count int `json:"count"`
	}
	json.NewDecoder(bytes.NewBuffer(pkgBytes)).Decode(&pkg)
	if pkg.Count != 2 {
		t.Fatalf("应导出 2 条，实际 %d", pkg.Count)
	}
	for _, pt := range []string{"MSG-ONE", "MSG-TWO"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(pt)) {
			t.Fatal("导出包泄露明文")
		}
	}


	// 密文快照迁移：服务端原样落库、不强校验 key → 200 imported=2
	rec = call(impH, bTok, "", "POST", "/v1/memory/encrypted/import", string(pkgBytes))
	if rec.Code != 200 {
		t.Fatalf("导入 %d %s", rec.Code, rec.Body.String())
	}
	impBytes := rec.Body.Bytes()
	var impResp struct {
		Imported int `json:"imported"`
	}
	json.NewDecoder(bytes.NewBuffer(impBytes)).Decode(&impResp)
	if impResp.Imported != 2 {
		t.Fatalf("应导入 2 条，实际 %d", impResp.Imported)
	}

	rec = call(expH, bTok, "", "GET", "/v1/memory/encrypted/export", "")
	bpkgBytes := rec.Body.Bytes()
	var bpkg struct {
		Entries []struct {
			Nonce  string `json:"nonce_b64"`
			Cipher string `json:"ciphertext_b64"`
		} `json:"entries"`
	}
	json.NewDecoder(bytes.NewBuffer(bpkgBytes)).Decode(&bpkg)
	seen := map[string]bool{}
	for i, e := range bpkg.Entries {
		n, _ := base64.StdEncoding.DecodeString(e.Nonce)
		c, _ := base64.StdEncoding.DecodeString(e.Cipher)
		pt, err := envelope.Decrypt(keyA, n, c)
		if err != nil {
			t.Fatalf("B 用 keyA 解密迁移记录失败: %v", err)
		}
		seen[string(pt)] = true
		if i == 0 {
			if _, err := envelope.Decrypt(keyB, n, c); err == nil {
				t.Fatal("错 key 不应解出明文")
			}
		}
	}
	if !seen["MSG-ONE-银行"] || !seen["MSG-TWO-物业"] {
		t.Fatalf("迁移后明文不完整: %v", seen)
	}
}
