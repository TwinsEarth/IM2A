package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postMCP(t *testing.T, h http.Handler, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp?account=acc-im-bot", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// 协议层测试：initialize/tools/list/参数校验不依赖真实 DB（relay 传 nil）。
func TestMCPInitialize(t *testing.T) {
	h := NewMCPHandler(nil)
	code, out := postMCP(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	res, _ := out["result"].(map[string]any)
	if res == nil || res["protocolVersion"] != "2024-11-05" {
		t.Fatalf("bad init result: %v", out)
	}
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "im2a-relay" {
		t.Fatalf("bad serverInfo: %v", info)
	}
}

func TestMCPToolsList(t *testing.T) {
	h := NewMCPHandler(nil)
	_, out := postMCP(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	res, _ := out["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(tools))
	}
	tool0, _ := tools[0].(map[string]any)
	if tool0["name"] != "im2a_route" {
		t.Fatalf("want im2a_route, got %v", tool0["name"])
	}
}

func TestMCPCallMissingArgs(t *testing.T) {
	h := NewMCPHandler(nil)
	_, out := postMCP(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"im2a_route","arguments":{"text":"hi"}}}`)
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil || errObj["code"].(float64) != -32602 {
		t.Fatalf("want -32602 param error, got %v", out)
	}
}

func TestMCPUnknownTool(t *testing.T) {
	h := NewMCPHandler(nil)
	_, out := postMCP(t, h, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil || errObj["code"].(float64) != -32601 {
		t.Fatalf("want -32601, got %v", out)
	}
}

func TestMCPNotification(t *testing.T) {
	h := NewMCPHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notifications want 202, got %d", rec.Code)
	}
}

func TestMCPBatch(t *testing.T) {
	h := NewMCPHandler(nil)
	body := `[{"jsonrpc":"2.0","id":1,"method":"initialize"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("batch status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "im2a-relay") {
		t.Fatalf("batch missing init result: %s", rec.Body.String())
	}
}

func TestMCPRejectGET(t *testing.T) {
	h := NewMCPHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}
