package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 主通道返回 200 → 直接用主，不碰备。
func TestChainPrimaryOK(t *testing.T) {
	var fallbackHit bool
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHit = true
	}))
	defer fb.Close()
	pri := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from-primary"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer pri.Close()

	chain := NewChain(
		NewOpenAICompatible(pri.URL, "k1", "deepseek-chat"),
		NewOpenAICompatible(fb.URL, "k2", "kimi-k2.6"),
	)
	resp, err := chain.Chat(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Content != "from-primary" || resp.RoutedTo != "primary" {
		t.Fatalf("want primary content, got %q routed=%q", resp.Content, resp.RoutedTo)
	}
	if fallbackHit {
		t.Fatal("fallback must not be called when primary OK")
	}
	pok, fok, _, both := chain.Stats()
	if pok != 1 || fok != 0 || both != 0 {
		t.Fatalf("stats wrong: pok=%d fok=%d both=%d", pok, fok, both)
	}
}

// 主通道 500 → 自动切备，备 200 返回。
func TestChainFallbackOnPrimary500(t *testing.T) {
	pri := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer pri.Close()
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from-kimi"}}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`))
	}))
	defer fb.Close()

	chain := NewChain(
		NewOpenAICompatible(pri.URL, "k1", "deepseek-chat"),
		NewOpenAICompatible(fb.URL, "k2", "kimi-k2.6"),
	)
	resp, err := chain.Chat(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Content != "from-kimi" || resp.RoutedTo != "fallback" {
		t.Fatalf("want fallback content, got %q routed=%q", resp.Content, resp.RoutedTo)
	}
	pok, fok, pfail, both := chain.Stats()
	if pok != 0 || fok != 1 || pfail != 1 || both != 0 {
		t.Fatalf("stats wrong: pok=%d fok=%d pfail=%d both=%d", pok, fok, pfail, both)
	}
}

// 主备都失败 → 返回 ChainError。
func TestChainBothFail(t *testing.T) {
	pri := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer pri.Close()
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer fb.Close()

	chain := NewChain(
		NewOpenAICompatible(pri.URL, "k1", "deepseek-chat"),
		NewOpenAICompatible(fb.URL, "k2", "kimi-k2.6"),
	)
	_, err := chain.Chat(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("want ChainError, got nil")
	}
	ce, ok := err.(*ChainError)
	if !ok {
		t.Fatalf("want *ChainError, got %T", err)
	}
	if ce.PrimaryErr == nil || ce.FallbackErr == nil {
		t.Fatal("ChainError must carry both errors")
	}
	_, _, _, both := chain.Stats()
	if both != 1 {
		t.Fatalf("want bothFail=1, got %d", both)
	}
}

// 无备通道且主失败 → 直接返回主错误。
func TestChainNilFallback(t *testing.T) {
	pri := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer pri.Close()
	chain := NewChain(NewOpenAICompatible(pri.URL, "k1", "deepseek-chat"), nil)
	if _, err := chain.Chat(context.Background(), &ChatRequest{Messages: []Message{{Role: "user"}}}); err == nil {
		t.Fatal("want error, got nil")
	}
}
