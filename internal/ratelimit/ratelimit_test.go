package ratelimit

import (
	"net/http"
	"testing"
)

func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		xri        string
		want       string
	}{
		{"direct public strips port", "203.0.113.9:51234", "", "", "203.0.113.9"},
		{"public direct ignores spoofed XFF", "203.0.113.9:51234", "1.2.3.4", "", "203.0.113.9"},
		{"behind loopback proxy trusts XFF", "127.0.0.1:8080", "203.0.113.20, 10.0.0.1", "", "203.0.113.20"},
		{"behind docker private proxy trusts XFF", "172.17.0.1:4000", "203.0.113.21", "", "203.0.113.21"},
		{"loopback proxy falls back to X-Real-IP", "127.0.0.1:8080", "", "203.0.113.22", "203.0.113.22"},
		{"loopback no forward headers", "[::1]:5000", "", "", "::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xri != "" {
				r.Header.Set("X-Real-IP", tc.xri)
			}
			if got := ClientIP(r); got != tc.want {
				t.Errorf("ClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIPLimitBurstAndRefill(t *testing.T) {
	h := IPLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), 100, 5) // 便于测试：burst 仅 5
	// 同一真实 IP（同源端口，聚合到一个桶）连续 6 次：前 5 放行，第 6 个 429
	for i := 0; i < 5; i++ {
		r, _ := http.NewRequest("GET", "/", nil)
		r.RemoteAddr = "127.0.0.1:1000" // 同一连接，同一端口
		rec := &spyRW{header: http.Header{}}
		h.ServeHTTP(rec, r)
		if rec.code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i+1, rec.code)
		}
	}
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1000"
	rec := &spyRW{header: http.Header{}}
	h.ServeHTTP(rec, r)
	if rec.code != http.StatusTooManyRequests {
		t.Fatalf("6th request over burst: want 429, got %d", rec.code)
	}
}

type spyRW struct {
	header http.Header
	code   int
	body   []byte
}

func (s *spyRW) Header() http.Header         { return s.header }
func (s *spyRW) Write(b []byte) (int, error) { s.body = append(s.body, b...); return len(b), nil }
func (s *spyRW) WriteHeader(code int)        { s.code = code }
