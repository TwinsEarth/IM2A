// SPDX-License-Identifier: MIT
//
// P1 回归：Audit 中间件的 statusWriter 必须透传 http.Hijacker / http.Flusher。
// 背景（task-20 收尾）：c.wrap 之后 /v1/ws 的 gorilla Upgrade 直接断言 http.Hijacker，
// 旧实现只包装 WriteHeader/Write → 生产 PG/内存模式都必然失败（先于 Wave A 存在）。
package security

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 1) Audit 包装后仍是 http.Hijacker，且 Hijack 真正拿到 net.Conn。
func TestAuditPreservesHijacker(t *testing.T) {
	var upgraded atomic.Bool
	h := (&Service{}).Audit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("Audit 包装后丢失 http.Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		upgraded.Store(true)
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
		_ = conn.Close()
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if !upgraded.Load() {
		t.Fatal("handler 未能 Hijack（Audit 包装破坏了连接接管）")
	}
}

// 2) 底层不支持 Hijacker 时返回 http.ErrNotSupported（不 panic）。
func TestStatusWriterHijackUnsupported(t *testing.T) {
	rec := httptest.NewRecorder() // *httptest.ResponseRecorder 不实现 http.Hijacker
	sw := &statusWriter{ResponseWriter: rec}
	conn, rw, err := sw.Hijack()
	if err != http.ErrNotSupported {
		t.Fatalf("Hijack on non-hijackable writer: err=%v, want http.ErrNotSupported", err)
	}
	if conn != nil || rw != nil {
		t.Fatalf("Hijack returned conn=%v rw=%v, want nil,nil", conn, rw)
	}
}

// 3) 判定性证据：真实 gorilla Upgrade 穿过完整中间件链（MaxBody→Headers→CORS→Audit）成功。
func TestAuditGorillaUpgradeThroughMiddlewareChain(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	core := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("gorilla upgrade 经中间件链失败: %v", err)
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte("hello-ws"))
	})
	// 与 routes.routeCtx.wrap 相同的由内到外顺序（不含 auth/limit，与本缺陷无关）。
	auditor := &Service{}
	var h http.Handler = core
	h = auditor.Audit(h)
	h = CORS([]string{"https://geefish.cn"}, h)
	h = Headers(h)
	h = MaxBody(2<<20, h)
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
	if string(msg) != "hello-ws" {
		t.Fatalf("msg = %q", msg)
	}
}

// 4) Audit 包装后仍是 http.Flusher：SSE 流式不能被中间件吃掉。
func TestAuditPreservesFlusher(t *testing.T) {
	flushed := make(chan struct{}, 1)
	h := (&Service{}).Audit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("Audit 包装后丢失 http.Flusher")
			return
		}
		_, _ = io.WriteString(w, "data: x\n\n")
		f.Flush()
		flushed <- struct{}{}
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("handler 未能 Flush（Audit 包装破坏了 SSE）")
	}
}
