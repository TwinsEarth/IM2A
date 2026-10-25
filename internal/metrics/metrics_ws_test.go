// SPDX-License-Identifier: MIT
//
// P1 回归（task-20 收尾）：metrics.Middleware 的 statusWriter 必须透传 http.Hijacker，
// 否则真实服务链 metrics.Middleware → mux → routes.wrap → /v1/ws 的 gorilla Upgrade 必然失败。
package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Middleware 包装后仍是 http.Hijacker，且真实 gorilla Upgrade 成功收发一条消息。
func TestMiddlewarePreservesHijacker(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var upgraded atomic.Bool
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("gorilla upgrade 经 metrics.Middleware 失败: %v", err)
			return
		}
		defer c.Close()
		upgraded.Store(true)
		_ = c.WriteMessage(websocket.TextMessage, []byte("ws-ok"))
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(msg) != "ws-ok" || !upgraded.Load() {
		t.Fatalf("msg=%q upgraded=%v", msg, upgraded.Load())
	}
}

// 底层不支持 Hijacker 时返回 http.ErrNotSupported（不 panic）。
func TestStatusWriterHijackUnsupported(t *testing.T) {
	rec := httptest.NewRecorder() // 不实现 http.Hijacker
	sw := &statusWriter{ResponseWriter: rec, code: 200}
	conn, rw, err := sw.Hijack()
	if err != http.ErrNotSupported {
		t.Fatalf("Hijack err=%v, want http.ErrNotSupported", err)
	}
	if conn != nil || rw != nil {
		t.Fatalf("Hijack returned conn=%v rw=%v", conn, rw)
	}
}
