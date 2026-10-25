package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func ok() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
}

func TestRequireTokenEmptyDevMode(t *testing.T) {
	h := RequireToken(ok(), "") // 开发模式（未配置 token）放行
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 200 {
		t.Fatalf("开发模式应放行，实际 %d", rec.Code)
	}
}

func TestRequireTokenHeader(t *testing.T) {
	h := RequireToken(ok(), "secret")
	for _, tc := range []struct {
		name string
		auth string
		want int
	}{
		{"缺失", "", 401},
		{"错误", "Bearer nope", 401},
		{"非 Bearer", "Token secret", 401},
		{"正确", "Bearer secret", 200},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x", nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s: 期望 %d 实际 %d", tc.name, tc.want, rec.Code)
		}
	}
}

// v1.1：WebSocket 无法设置 Authorization 头，必须支持 query access_token。
func TestRequireTokenQueryForWebSocket(t *testing.T) {
	h := RequireToken(ok(), "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/ws?topic=x&access_token=secret", nil))
	if rec.Code != 200 {
		t.Fatalf("query token 正确应放行，实际 %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/v1/ws?topic=x&access_token=bad", nil))
	if rec2.Code != 401 {
		t.Fatalf("query token 错误应 401，实际 %d", rec2.Code)
	}
}
