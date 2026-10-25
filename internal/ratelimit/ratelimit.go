// Package ratelimit 提供 IP 维度的令牌桶限流中间件。
//
// v1.8.7 安全加固：
//   - 限流键剥离端口，按真实客户端 IP 聚合（原先用 RemoteAddr（含源端口），
//     连接池每连接一个桶，导致突发被分摊、限流失效）；
//   - 在受信反向代理（loopback/内网，如 nginx）之后信任 X-Forwarded-For / X-Real-IP；
//   - 直连来源（公网 IP）不信任 XFF，防止伪造头绕过限流。
package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type bucket struct {
	tokens int
	last   time.Time
}

// ClientIP 返回真实客户端 IP（剥离端口；受信代理后取 XFF/XRI）。
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if isTrustedProxy(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			if xff = strings.TrimSpace(xff); xff != "" {
				return xff
			}
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			return xri
		}
	}
	return host
}

func isTrustedProxy(host string) bool {
	if host == "127.0.0.1" || host == "::1" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
	}
	return false
}

// IPLimit 按真实客户端 IP 限流：每秒最多 rps 个请求，突发上限 burst。
func IPLimit(next http.Handler, rps int, burst int) http.Handler {
	if rps <= 0 {
		return next
	}
	mu := &sync.Mutex{}
	buckets := map[string]*bucket{}
	refill := time.Second / time.Duration(rps)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		mu.Lock()
		b, ok := buckets[ip]
		if !ok {
			b = &bucket{tokens: burst, last: time.Now()}
			buckets[ip] = b
		}
		now := time.Now()
		elapsed := now.Sub(b.last)
		b.tokens += int(elapsed / refill)
		if b.tokens > burst {
			b.tokens = burst
		}
		b.last = now
		if b.tokens <= 0 {
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
		b.tokens--
		mu.Unlock()
		next.ServeHTTP(w, r)
	})
}
