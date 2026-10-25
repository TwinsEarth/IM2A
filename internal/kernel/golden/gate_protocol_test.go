// SPDX-License-Identifier: MIT
//
// 协议能力门禁（task-22）：现有 golden 只验证「响应形状（status/content-type/JSON）」，
// 无法发现「中间件包装器吃掉 http.Hijacker/http.Flusher」这类协议能力缺陷。
//
// 本文件用真实 gateway 子进程（bootstrap.Assemble + routes.Register 同生产装配）验证：
//  1. /v1/ws 真实 gorilla Upgrade → 101 + 收到真实广播 + 正常关闭；
//  2. /v1/sse/ 真实流式响应 → 200 + text/event-stream + 2s 内首个事件块（证明 Flush 透传）；
//  3. security.Audit / metrics.Middleware 包装后的 ResponseWriter 仍实现 Hijacker/Flusher，
//     底层不支持时 Hijack 返回 http.ErrNotSupported。
//
// 负向对照（手工，见 task-22 回报）：临时回退两处 wrapper 的 Hijack/Flush 后本文件必须红灯。
package golden

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"human-agent-im/internal/metrics"
	"human-agent-im/internal/security"
)

// TestGateWSUpgradeSmoke：真实 gorilla dial 走完整生产链（metrics→routes.wrap→/v1/ws）。
func TestGateWSUpgradeSmoke(t *testing.T) {
	root := repoRoot(t)
	bin := buildGateway(t, root)
	base, stop := startGateway(t, bin)
	defer stop()

	topic := "approval:4242"
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/v1/ws?topic=" + topic
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	c, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		status, body := 0, ""
		if resp != nil {
			status = resp.StatusCode
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
			body = string(b)
		}
		t.Fatalf("GATE FAIL: /v1/ws Upgrade 失败（status=%d body=%.200s）: %v", status, body, err)
	}
	defer c.Close()

	// 1) 升级成功首帧必须是 connected。
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, connected, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("GATE FAIL: Upgrade 成功但读 connected 失败: %v", err)
	}
	if !strings.Contains(string(connected), `"type":"connected"`) {
		t.Fatalf("GATE FAIL: 首帧不是 connected: %.200s", connected)
	}

	// 2) 触发真实业务广播（create approval → Broadcast("approval:<uid>")），断言 WS 收到。
	body := strings.NewReader(`{"userId":4242,"sourceAgentId":"gate-test","type":"SKILL_CALL","payload":{}}`)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/approvals", body)
	req.Header.Set("Content-Type", "application/json")
	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GATE FAIL: POST /v1/approvals: %v", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(rsp.Body, 200))
	_ = rsp.Body.Close()
	if rsp.StatusCode != http.StatusCreated {
		t.Fatalf("GATE FAIL: /v1/approvals status=%d body=%.200s", rsp.StatusCode, raw)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("GATE FAIL: Upgrade 成功但未收到广播: %v", err)
	}
	if !strings.Contains(string(msg), "approval_card") {
		t.Fatalf("GATE FAIL: 广播内容不含 approval_card: %.200s", msg)
	}

	// 3) 客户端主动 Close，必须快速结束（不挂死）。
	_ = c.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "gate-done"),
		time.Now().Add(time.Second))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatalf("GATE FAIL: 发送 Close 后仍收到数据")
	}
	t.Logf("GATE_WS_UPGRADE_OK: 101 + connected + approval_card broadcast + close")
}

// TestGateSSEFirstFlush：真实 HTTP 流式响应必须在 2s 内推出首个事件块。
func TestGateSSEFirstFlush(t *testing.T) {
	root := repoRoot(t)
	bin := buildGateway(t, root)
	base, stop := startGateway(t, bin)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sse/?userId=gate-sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GATE FAIL: GET /v1/sse/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		t.Fatalf("GATE FAIL: /v1/sse/ status=%d body=%.200s（Flusher 可能被中间件吃掉）",
			resp.StatusCode, raw)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("GATE FAIL: Content-Type=%q, want text/event-stream", ct)
	}
	// 2s 内读到首个事件块（event: connected + data:）。
	firstChunk := make(chan string, 1)
	go func() {
		br := bufio.NewReader(resp.Body)
		var sb strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				firstChunk <- sb.String()
				return
			}
			sb.WriteString(line)
			if strings.HasPrefix(line, "data:") {
				firstChunk <- sb.String()
				return
			}
		}
	}()
	select {
	case chunk := <-firstChunk:
		if !strings.Contains(chunk, "event: connected") || !strings.Contains(chunk, "data:") {
			t.Fatalf("GATE FAIL: 首个事件块不完整: %.200s", chunk)
		}
		t.Logf("GATE_SSE_FIRST_FLUSH_OK: 200 + text/event-stream + first event block")
	case <-time.After(2 * time.Second):
		t.Fatalf("GATE FAIL: 2s 内未收到首个 SSE 事件块（Flusher 透传/流式推失败）")
	}
}

// TestGateResponseWriterCapabilities：中间件包装后必须保留 Hijacker/Flusher 能力。
func TestGateResponseWriterCapabilities(t *testing.T) {
	// 3.1 security.Audit 包装后：Hijacker + Flusher 均成立，且 Hijack 真正接管连接。
	t.Run("security.Audit", func(t *testing.T) {
		hit := make(chan struct{}, 1)
		h := (&security.Service{}).Audit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Errorf("GATE FAIL: security.Audit 包装后丢失 http.Hijacker（/v1/ws 将无法升级）")
				return
			}
			if _, ok := w.(http.Flusher); !ok {
				t.Errorf("GATE FAIL: security.Audit 包装后丢失 http.Flusher（/v1/sse/ 将无法流式输出）")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("GATE FAIL: security.Audit Hijack: %v", err)
				return
			}
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
			_ = conn.Close()
			hit <- struct{}{}
		}))
		srv := httptest.NewServer(h)
		defer srv.Close()
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatalf("GATE FAIL: security.Audit GET: %v", err)
		}
		_ = resp.Body.Close()
		select {
		case <-hit:
		case <-time.After(2 * time.Second):
			t.Fatalf("GATE FAIL: security.Audit 未完成 Hijack（连接接管失败）")
		}
	})

	// 3.2 metrics.Middleware 包装后：Hijacker 成立（真实服务链最外层）。
	t.Run("metrics.Middleware", func(t *testing.T) {
		hit := make(chan struct{}, 1)
		h := metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Errorf("GATE FAIL: metrics.Middleware 包装后丢失 http.Hijacker（真实服务 /v1/ws 仍会失败）")
				return
			}
			if _, ok := w.(http.Flusher); !ok {
				t.Errorf("GATE FAIL: metrics.Middleware 包装后丢失 http.Flusher")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("GATE FAIL: metrics.Middleware Hijack: %v", err)
				return
			}
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
			_ = conn.Close()
			hit <- struct{}{}
		}))
		srv := httptest.NewServer(h)
		defer srv.Close()
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatalf("GATE FAIL: metrics.Middleware GET: %v", err)
		}
		_ = resp.Body.Close()
		select {
		case <-hit:
		case <-time.After(2 * time.Second):
			t.Fatalf("GATE FAIL: metrics.Middleware 未完成 Hijack")
		}
	})

	// 3.3 底层不支持 Hijacker 时必须返回 http.ErrNotSupported（而不是 panic/静默成功）。
	t.Run("unsupported returns ErrNotSupported", func(t *testing.T) {
		check := func(name string, wrap func(http.Handler) http.Handler) {
			rec := httptest.NewRecorder() // 不实现 http.Hijacker
			h := wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Errorf("GATE FAIL: %s 包装后丢失 http.Hijacker", name)
					return
				}
				if _, _, err := hj.Hijack(); err != http.ErrNotSupported {
					t.Errorf("GATE FAIL: %s Hijack(不支持底层) err=%v, want http.ErrNotSupported", name, err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			h.ServeHTTP(rec, req)
		}
		check("security.Audit", func(h http.Handler) http.Handler { return (&security.Service{}).Audit(h) })
		check("metrics.Middleware", metrics.Middleware)
	})
}
