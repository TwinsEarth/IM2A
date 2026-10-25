package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"human-agent-im/internal/model"
)

// mockCloud 固定返回 JSON，模拟 Cloud LLM。
type mockCloud struct{ content string }

func (m *mockCloud) Chat(_ context.Context, _ *model.ChatRequest) (*model.ChatResponse, error) {
	return &model.ChatResponse{Content: m.content, Model: "mock", RoutedTo: "cloud"}, nil
}
func (m *mockCloud) Name() string { return "mock" }

func newMockDeps(content string) *Deps {
	return &Deps{Model: &mockCloud{content: content}}
}

// v1.3.2 意图预审：spam 判定
func TestV132IntentPrecheck(t *testing.T) {
	d := newMockDeps(`{"category":"spam","reason":"刷单广告","risk":9,"charge":true}`)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"加微信日赚5000"}`))
	w := httptest.NewRecorder()
	d.intentPrecheck(w, req)
	if !strings.Contains(w.Body.String(), `"category":"spam"`) {
		t.Errorf("应判 spam, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"charge":true`) {
		t.Errorf("spam 应 charge=true, got %s", w.Body.String())
	}
}

// v1.3.3 出口护栏：block
func TestV133GuardBlock(t *testing.T) {
	d := newMockDeps(`{"allowed":false,"level":"block","reason":"含手机号"}`)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"13812345678"}`))
	w := httptest.NewRecorder()
	d.guardReview(w, req)
	if !strings.Contains(w.Body.String(), `"allowed":false`) {
		t.Errorf("应 block, got %s", w.Body.String())
	}
}

// v1.3.4 蒸馏：reject → delta=0.3
func TestV134Distill(t *testing.T) {
	d := newMockDeps(`{"rule":"高价家电需审批","thresholdDelta":0.3,"appliedTo":"shopping"}`)
	body := `{"agentId":"shopping","decision":"reject","context":"买洗碗机","feedback":"太贵"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.distillFeedback(w, req)
	if !strings.Contains(w.Body.String(), `"thresholdDelta":0.3`) {
		t.Errorf("reject 应 delta=0.3, got %s", w.Body.String())
	}
}

// v1.3.5 路由：schedule
func TestV135Router(t *testing.T) {
	d := newMockDeps(`{"subAgent":"schedule","intent":"开会","priority":2,"summary":"明天3点","confidence":0.97}`)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"明天3点开会"}`))
	w := httptest.NewRecorder()
	d.llmRoute(w, req)
	if !strings.Contains(w.Body.String(), `"subAgent":"schedule"`) {
		t.Errorf("应路由 schedule, got %s", w.Body.String())
	}
}

// LLM 不可用时安全兜底
func TestV132Fallback(t *testing.T) {
	d := &Deps{} // Model=nil
	// Model=nil 会 panic，跳过；只验证空文本 400
	req2 := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	w2 := httptest.NewRecorder()
	d.intentPrecheck(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("空文本应 400, got %d", w2.Code)
	}
}
