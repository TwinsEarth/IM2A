package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIssueParseRoundtrip(t *testing.T) {
	tok, err := Issue("s3cret", 42, time.Hour, "im2a")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	c, err := Parse("s3cret", tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Sub != 42 {
		t.Fatalf("sub=%d want 42", c.Sub)
	}
	if c.Iss != "im2a" {
		t.Fatalf("iss=%q", c.Iss)
	}
	if c.Exp <= c.Iat {
		t.Fatalf("exp<=iat")
	}
}

func TestParseBadSignature(t *testing.T) {
	tok, _ := Issue("secret-A", 1, time.Hour, "im2a")
	if _, err := Parse("secret-B", tok); err != ErrBadSig {
		t.Fatalf("want ErrBadSig got %v", err)
	}
}

func TestParseExpired(t *testing.T) {
	tok, err := Issue("s", 1, -time.Minute, "im2a")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := Parse("s", tok); err != ErrExpired {
		t.Fatalf("want ErrExpired got %v", err)
	}
}

func TestParseMalformed(t *testing.T) {
	for _, s := range []string{"", "a.b", "a.b.c.d"} {
		if _, err := Parse("s", s); err == nil {
			t.Fatalf("expected error for %q", s)
		}
	}
}

func TestParseWrongAlg(t *testing.T) {
	hdr, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	pay, _ := json.Marshal(Claims{Sub: 1, Iat: 1, Exp: time.Now().Add(time.Hour).Unix()})
	tok := b64url(hdr) + "." + b64url(pay) + "." + b64url([]byte("xx"))
	if _, err := Parse("s", tok); err != ErrBadAlg {
		t.Fatalf("want ErrBadAlg got %v", err)
	}
}

func TestRequireIdentityMiddleware(t *testing.T) {
	secret := "k"

	// 无 token -> 401
	var gotUID int64
	var gotOK bool
	h := RequireIdentity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUID, gotOK = UserIDFromContext(r)
		w.WriteHeader(200)
	}), secret)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/auth/me", nil))
	if rec.Code != 401 {
		t.Fatalf("无 token 应 401，实际 %d", rec.Code)
	}

	// 合法 token -> 200 且 context 带 uid=7
	tok, _ := Issue(secret, 7, time.Hour, "im2a")
	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec2, req)
	if rec2.Code != 200 || !gotOK || gotUID != 7 {
		t.Fatalf("合法 token 期望 200 uid=7，实际 code=%d uid=%d ok=%v", rec2.Code, gotUID, gotOK)
	}

	// 错签 token -> 401
	rec3 := httptest.NewRecorder()
	bad, _ := Issue("other", 9, time.Hour, "im2a")
	req3 := httptest.NewRequest("GET", "/v1/auth/me", nil)
	req3.Header.Set("Authorization", "Bearer "+bad)
	h.ServeHTTP(rec3, req3)
	if rec3.Code != 401 {
		t.Fatalf("错签 token 应 401，实际 %d", rec3.Code)
	}
}
