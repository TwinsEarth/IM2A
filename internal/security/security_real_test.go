package security

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// db 返回连接与唯一前缀；cleanup 用同一前缀保证零残留。
func db(t *testing.T) (*sql.DB, string) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("open: %v", err)
	}
	_, err = d.Exec(`CREATE TABLE IF NOT EXISTS security_events (
		id BIGSERIAL PRIMARY KEY, occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		event_type TEXT NOT NULL, method TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '',
		status_code INT NOT NULL DEFAULT 0, client_ip TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT '', duration_ms BIGINT NOT NULL DEFAULT 0, detail TEXT NOT NULL DEFAULT '')`)
	if err != nil {
		t.Skipf("migrate: %v", err)
	}
	suf := fmt.Sprintf("v183_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := d.Exec(`DELETE FROM security_events WHERE path LIKE $1`, "/"+suf+"%"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		d.Close()
	})
	return d, suf
}

func TestV183Headers(t *testing.T) {
	h := Headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); !strings.Contains(got, v) {
			t.Errorf("安全头 %s=%q 应含 %q", k, got, v)
		}
	}
}

func TestV183CORSWhitelist(t *testing.T) {
	h := CORS([]string{"https://app.geefish.cn"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	// 合法来源
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/x", nil)
	req.Header.Set("Origin", "https://app.geefish.cn")
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.geefish.cn" {
		t.Fatalf("白名单来源应回 ACAO，实际 %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	// 非法来源预检必须 403
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("OPTIONS", "/v1/x", nil)
	req2.Header.Set("Origin", "https://evil.com")
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("非法来源预检应 403，实际 %d", rec2.Code)
	}
	if rec2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("非法来源不得回 ACAO")
	}
}

func TestV183AuditPersistsAndClassifies(t *testing.T) {
	d, suf := db(t)
	s := New(d)
	ctx := context.Background()

	// 用一个会返回 401 的内层 handler，验证归类 auth_failed
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+suf+"/unauth" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := s.Audit(inner)

	// 200 请求
	r1 := httptest.NewRequest("POST", "/"+suf+"/ok", nil)
	r1.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	r1.Header.Set("User-Agent", "im2a-test")
	srv.ServeHTTP(httptest.NewRecorder(), r1)
	// 401 请求
	r2 := httptest.NewRequest("GET", "/"+suf+"/unauth", nil)
	r2.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	srv.ServeHTTP(httptest.NewRecorder(), r2)

	// 断言落库：共 2 条
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM security_events WHERE path LIKE $1`, "/"+suf+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应落库 2 条安全事件，实际 %d", n)
	}

	// 断言 401 归类 auth_failed，且 XFF 首段取真实 IP
	var etype, ip string
	err := d.QueryRow(`SELECT event_type, client_ip FROM security_events WHERE path=$1`, "/"+suf+"/unauth").
		Scan(&etype, &ip)
	if err != nil {
		t.Fatal(err)
	}
	if etype != "auth_failed" {
		t.Fatalf("401 应归类 auth_failed，实际 %s", etype)
	}
	if ip != "203.0.113.9" {
		t.Fatalf("应取 XFF 首段真实 IP，实际 %s", ip)
	}

	// 断言 Service.List 可查
	list, err := s.List(ctx, "auth_failed", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("List(auth_failed) 不应为空")
	}
}

func TestV183Classify(t *testing.T) {
	cases := map[int]string{200: "request", 401: "auth_failed", 403: "blocked", 429: "rate_limited", 500: "request"}
	for code, want := range cases {
		if got := Classify(code); got != want {
			t.Errorf("Classify(%d)=%s want %s", code, got, want)
		}
	}
}

func TestV184MaxBody(t *testing.T) {
	readAll := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			// MaxBytesReader 超限会在 writer 上置 413
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := MaxBody(16, readAll) // 16 字节上限，便于构造

	// 1) 显式 ContentLength 超限 → 413（不读 body）
	big := strings.Repeat("A", 100)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/x", strings.NewReader(big))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("ContentLength 超限应 413，实际 %d", rec.Code)
	}

	// 2) 小 body 正常通过
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/v1/x", strings.NewReader("tiny"))
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("小 body 应 200，实际 %d", rec2.Code)
	}

	// 3) 无 ContentLength（chunked）的超大 body：MaxBytesReader 兜底，
	// handler 无法读到超过上限的字节（内存被限制），Read 必然报错。
	var readErr error
	var readN int
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		readN = len(b)
		readErr = err
	})
	h3 := MaxBody(16, capture)
	req3 := httptest.NewRequest("POST", "/v1/x", strings.NewReader(big))
	req3.ContentLength = -1 // 模拟 chunked/未知长度
	h3.ServeHTTP(httptest.NewRecorder(), req3)
	if readErr == nil {
		t.Fatalf("chunked 超大 body 读取应返回 MaxBytesError")
	}
	if readN > 16 {
		t.Fatalf("chunked 兜底失败：handler 读到 %d 字节，应 <=16", readN)
	}
}
