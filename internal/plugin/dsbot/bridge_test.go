// SPDX-License-Identifier: MIT

package dsbotplugin

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"human-agent-im/internal/plugin"
)

func newBridgeTestPlugin(t *testing.T, endpoint string) *Plugin {
	t.Helper()
	t.Setenv(EnvEndpoint, endpoint)
	t.Setenv(EnvTimeout, "2000")
	return New(Deps{Logger: log.New(io.Discard, "", 0), Enabled: true})
}

func TestBridgeReachableDeclaresCapability(t *testing.T) {
	var gotPost struct {
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotPost)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	p := newBridgeTestPlugin(t, srv.URL)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := p.Health(context.Background())
	if h.State != plugin.StateOK {
		t.Fatalf("state = %q, want ok (bridge reachable)", h.State)
	}
	if !strings.Contains(h.Detail, "bridge reachable") || !strings.Contains(h.Detail, "probe took") {
		t.Fatalf("detail = %q, want bridge reachable + probe duration", h.Detail)
	}
	bridge, ok := capByID(p.Capabilities(), CapBridge)
	if !ok {
		t.Fatalf("bridge capability missing when healthy: %v", capIDs(p.Capabilities()))
	}
	if bridge.Kind != plugin.KindChannel || len(bridge.Modes) != 1 || bridge.Modes[0] != "send" {
		t.Fatalf("bridge capability = %+v, want channel/send", bridge)
	}
	if got := p.BridgeEndpoint(); got != srv.URL {
		t.Fatalf("BridgeEndpoint() = %q, want %q", got, srv.URL)
	}

	// 桥接投递：真实 POST 到本地 httptest（loopback，不访问外部网络）。
	if err := p.BridgeSend(context.Background(), "来自 IM2A 的长消息"); err != nil {
		t.Fatalf("BridgeSend: %v", err)
	}
	if gotPost.Text != "来自 IM2A 的长消息" || gotPost.Source != "im2a" {
		t.Fatalf("bridge POST body = %+v", gotPost)
	}
}

func TestBridgeProbeHTTPErrorDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := newBridgeTestPlugin(t, srv.URL)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil (degraded only), got %v", err)
	}
	h := p.Health(context.Background())
	if h.State != plugin.StateDegraded {
		t.Fatalf("state = %q, want degraded", h.State)
	}
	if !strings.Contains(h.Detail, "bridge degraded") || !strings.Contains(h.Detail, "HTTP 503") {
		t.Fatalf("detail = %q, want bridge degraded + HTTP status", h.Detail)
	}
	if _, ok := capByID(p.Capabilities(), CapBridge); ok {
		t.Fatalf("bridge capability must NOT be declared when probe fails: %v", capIDs(p.Capabilities()))
	}
	// 移植能力仍可用。
	for _, id := range []string{CapWebSearch, CapLongMessage, CapRosterSpec} {
		if _, ok := capByID(p.Capabilities(), id); !ok {
			t.Fatalf("migrated capability %s must remain available when bridge degrades", id)
		}
	}
}

func TestBridgeProbeUnreachableDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	endpoint := srv.URL
	srv.Close() // 关闭后连接被拒，模拟 endpoint 不可达

	p := newBridgeTestPlugin(t, endpoint)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil, got %v", err)
	}
	if h := p.Health(context.Background()); h.State != plugin.StateDegraded {
		t.Fatalf("state = %q, want degraded", h.State)
	}
	if err := p.BridgeSend(context.Background(), "hi"); err == nil {
		t.Fatal("BridgeSend must fail when bridge is unavailable")
	}
}

func TestBridgeSendHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newBridgeTestPlugin(t, srv.URL)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err := p.BridgeSend(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("BridgeSend error = %v, want HTTP 500", err)
	}
}

func TestBridgeSendEmptyText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newBridgeTestPlugin(t, srv.URL)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.BridgeSend(context.Background(), "   "); err == nil {
		t.Fatal("BridgeSend empty text should error")
	}
}

func TestBridgeBadEndpointConfig(t *testing.T) {
	// 非法 URL：探测失败 → degraded，但不能 panic，Start 仍返回 nil。
	p := newBridgeTestPlugin(t, "://not a url")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil, got %v", err)
	}
	if h := p.Health(context.Background()); h.State != plugin.StateDegraded {
		t.Fatalf("state = %q, want degraded", h.State)
	}
}
