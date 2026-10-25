package model

import (
	"context"
	"strings"
	"testing"
)

func TestStubIntentRouting(t *testing.T) {
	s := NewStub()
	tests := []struct {
		input string
		want  string
	}{
		{"你好", "管家"},
		{"帮我安排个会议", "日程"},
		{"我要买个咖啡机", "采购"},
		{"帮我审一下合同", "法律"},
		{"随便说点什么", "未识别"},
	}
	for _, tt := range tests {
		resp, err := s.Chat(context.Background(), &ChatRequest{
			Messages: []Message{{Role: "user", Content: tt.input}},
		})
		if err != nil {
			t.Fatalf("input %q: %v", tt.input, err)
		}
		if !strings.Contains(resp.Content, tt.want) {
			t.Errorf("input %q: expected %q in %q", tt.input, tt.want, resp.Content)
		}
		if resp.Model != s.Name() {
			t.Errorf("model name mismatch: %s", resp.Model)
		}
	}
}

func TestStubEmptyMessages(t *testing.T) {
	s := NewStub()
	_, err := s.Chat(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error on empty messages")
	}
}

func TestOpenAICompatibleNoClientFallsBackToStub(t *testing.T) {
	// 无 key/baseURL/model 时回退桩（离线可跑）
	o := NewOpenAICompatible("", "", "")
	resp, err := o.Chat(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "你好"}},
	})
	if err != nil {
		t.Fatalf("expected fallback, got %v", err)
	}
	if !strings.Contains(resp.Model, "stub") {
		t.Errorf("expected stub model name, got %s", resp.Model)
	}
	if !strings.Contains(resp.Content, "管家") {
		t.Errorf("expected stub reply, got %s", resp.Content)
	}
}

func TestOpenAICompatibleUpstreamError(t *testing.T) {
	// 配置了 key 但 baseURL 不可达 → 返回错误（不回退桩）
	o := NewOpenAICompatible("http://127.0.0.1:1", "sk-test", "fake-model")
	_, err := o.Chat(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected upstream error for unreachable baseURL")
	}
}

func TestHybridName(t *testing.T) {
	h := NewHybrid(NewStub(), NewOpenAICompatible("", "", ""))
	if !strings.Contains(h.Name(), "hybrid") {
		t.Fatalf("hybrid name should contain 'hybrid', got %s", h.Name())
	}
	resp, err := h.Chat(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "帮我安排会议"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "日程") {
		t.Fatalf("hybrid should route via edge, got %s", resp.Content)
	}
}

func TestHybridRoutesLowConfidenceToCloud(t *testing.T) {
	// 端侧桩对模糊输入置信度 0.3 → 应走云端（云端无 key 回退桩，但 RoutedTo 应是 cloud 或 edge fallback）
	h := NewHybrid(NewStub(), NewOpenAICompatible("", "", ""))
	resp, err := h.Chat(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "今天天气怎么样随便聊聊"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RoutedTo == "edge" {
		t.Fatalf("low confidence should not route to edge directly, got %s", resp.RoutedTo)
	}
}

func TestHybridHighConfidenceStaysEdge(t *testing.T) {
	h := NewHybrid(NewStub(), NewOpenAICompatible("", "", ""))
	resp, err := h.Chat(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "你好"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RoutedTo != "edge" {
		t.Fatalf("high confidence should stay edge, got %s", resp.RoutedTo)
	}
}

func TestQuotaLimitFallsBackToEdge(t *testing.T) {
	h := NewHybrid(NewStub(), NewOpenAICompatible("", "", ""))
	h.SetQuota(2)
	req := &ChatRequest{
		Messages: []Message{{Role: "user", Content: "随便聊聊"}},
		AgentID:  "test-agent",
	}
	r1, _ := h.Chat(context.Background(), req)
	r2, _ := h.Chat(context.Background(), req)
	if r1.RoutedTo != "cloud" || r2.RoutedTo != "cloud" {
		t.Fatalf("first 2 should be cloud, got %s, %s", r1.RoutedTo, r2.RoutedTo)
	}
	r3, _ := h.Chat(context.Background(), req)
	if r3.RoutedTo != "edge(quota)" {
		t.Fatalf("3rd should be quota-limited edge, got %s", r3.RoutedTo)
	}
	q := h.Quota()
	if q.PerMinute != 2 {
		t.Fatalf("perMinute should be 2, got %d", q.PerMinute)
	}
}

func TestStubExtendedIntents(t *testing.T) {
	s := NewStub()
	tests := []struct{ input, want string }{
		{"我孩子作业不会做", "亲子"},
		{"帮我报销这个月账单", "财务"},
		{"家里水管坏了找物业", "物业"},
		{"我感觉不舒服想约医生", "健康"},
		{"紧急情况马上报警", "紧急"},
	}
	for _, tt := range tests {
		resp, err := s.Chat(context.Background(), &ChatRequest{
			Messages: []Message{{Role: "user", Content: tt.input}},
		})
		if err != nil {
			t.Fatalf("%q: %v", tt.input, err)
		}
		if !strings.Contains(resp.Content, tt.want) {
			t.Errorf("%q: want %q in %q", tt.input, tt.want, resp.Content)
		}
		if resp.Confidence < 0.7 {
			t.Errorf("%q: confidence too low: %.2f", tt.input, resp.Confidence)
		}
	}
}
