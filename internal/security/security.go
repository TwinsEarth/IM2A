package security

// v1.8.3 安全合规基线：
//  - Headers：统一安全响应头（防点击劫持/MIME 嗅探/降级，CSP 收紧）
//  - CORS：白名单来源（默认不放行 *，非法来源不回 ACAO）
//  - Auditor：请求级安全审计落库（状态码归类 request/auth_failed/rate_limited），供等保审计与监控
import (
	"bufio"
	"context"
	"database/sql"
	"net"
	"net/http"
	"strings"
	"time"
)

// SecurityEvent 一条安全审计事件。
type SecurityEvent struct {
	ID         int64     `json:"id"`
	OccurredAt time.Time `json:"occurredAt"`
	EventType  string    `json:"eventType"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	StatusCode int       `json:"statusCode"`
	ClientIP   string    `json:"clientIp"`
	Actor      string    `json:"actor"`
	UserAgent  string    `json:"userAgent"`
	DurationMS int64     `json:"durationMs"`
	Detail     string    `json:"detail"`
}

// Service 安全事件落库与查询。
type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

// RecordEvent 真实落一条安全事件。
func (s *Service) RecordEvent(ctx context.Context, e *SecurityEvent) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO security_events(event_type,method,path,status_code,client_ip,actor,user_agent,duration_ms,detail)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		e.EventType, e.Method, e.Path, e.StatusCode, e.ClientIP, e.Actor, e.UserAgent, e.DurationMS, e.Detail)
	return err
}

// Classify 按状态码归类安全事件类型。
func Classify(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "auth_failed"
	case status == http.StatusForbidden:
		return "blocked"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	default:
		return "request"
	}
}

// ClientIP 从 X-Forwarded-For 首段取真实 IP（nginx 反代场景），否则取 RemoteAddr。
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// MaxBody 限制请求体大小，缓解超大 body 内存放大（DoS）。
// 先按 ContentLength 直接拦截，再用 MaxBytesReader 兜底无 ContentLength 的 chunked 请求。
func MaxBody(max int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > max {
			w.Header().Set("Connection", "close")
			w.WriteHeader(http.StatusRequestEntityTooLarge) // 413
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		next.ServeHTTP(w, r)
	})
}

// Headers 统一安全响应头。
func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-XSS-Protection", "1; mode=block")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'self'")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		next.ServeHTTP(w, r)
	})
}

// CORS 白名单来源中间件。allowed 为精确来源列表（含 scheme）；空列表等于禁止跨域。
func CORS(allowed []string, next http.Handler) http.Handler {
	allow := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		allow[strings.TrimRight(o, "/")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allow[strings.TrimRight(origin, "/")] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		} else if origin != "" && r.Method == http.MethodOptions {
			// 非白名单来源的预检直接拒绝，不放行
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Hijack 透传 http.Hijacker（2026-10-25 P1 修复，task-20 收尾）：
// gorilla/websocket 的 Upgrade 直接断言 http.Hijacker，而 Audit 中间件此前只包装了
// WriteHeader/Write —— 导致 c.wrap 之后的 /v1/ws 升级必然失败：
// "websocket: response does not implement http.Hijacker"（deps.DB 是否为 nil 都一样，
// Audit 无条件包装；该缺陷先于 Wave A 存在）。底层不支持时返回 http.ErrNotSupported，
// 与标准库 ResponseController 的语义一致。
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	// WebSocket 升级成功 = 101；在 Hijack 前记状态，否则 Audit 会把连接记为 200。
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

// Flush 透传 http.Flusher：SSE（/v1/sse/*、chat 流式）依赖显式 Flush，
// 中间件包装不能把它吃掉（同样先于 Wave A 的缺陷）。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Audit 记录每个请求的安全事件；只在响应结束后异步落库（不阻塞主链路），失败不影响业务。
func (s *Service) Audit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 0}
		next.ServeHTTP(sw, r)
		// 健康检查/预检不审计，降低噪音
		if r.URL.Path == "/healthz" || r.Method == http.MethodOptions {
			return
		}
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		ev := &SecurityEvent{
			EventType:  Classify(sw.status),
			Method:     r.Method,
			Path:       r.URL.Path,
			StatusCode: sw.status,
			ClientIP:   ClientIP(r),
			Actor:      r.Header.Get("X-Actor"),
			UserAgent:  truncate(r.UserAgent(), 256),
			DurationMS: time.Since(start).Milliseconds(),
		}
		// 同步落库但限时，避免拖累请求；测试依赖即时可见，故用请求 context
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		_ = s.RecordEvent(ctx, ev)
	})
}

// List 查询安全事件（运营后台/审计可视化用）。
func (s *Service) List(ctx context.Context, eventType string, limit int) ([]*SecurityEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		rows *sql.Rows
		err  error
	)
	if eventType == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id,occurred_at,event_type,method,path,status_code,client_ip,actor,user_agent,duration_ms,detail
			 FROM security_events ORDER BY id DESC LIMIT $1`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id,occurred_at,event_type,method,path,status_code,client_ip,actor,user_agent,duration_ms,detail
			 FROM security_events WHERE event_type=$1 ORDER BY id DESC LIMIT $2`, eventType, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*SecurityEvent, 0, limit)
	for rows.Next() {
		e := &SecurityEvent{}
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.EventType, &e.Method, &e.Path, &e.StatusCode,
			&e.ClientIP, &e.Actor, &e.UserAgent, &e.DurationMS, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
