package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// 一个会被 L4 接管的"办事"计划（低风险日程，直接放行）。
type hookTaskPlanner struct{}

func (hookTaskPlanner) Plan(ctx context.Context, s, r string) (string, error) {
	p := Plan{
		Activation: ActivationRespond,
		Intent:     IntentTask,
		Confidence: 0.95,
		Subtasks: []SubTask{
			{SubAgent: AgentSchedule, Goal: "提醒", Steps: []ToolStep{{Tool: "calendar.add", Args: map[string]any{"title": "开会"}}}},
		},
		ReplyHint: "已帮你记下开会提醒。",
	}
	b, _ := json.Marshal(p)
	return string(b), nil
}

// 纯闲聊计划：必须透传回旧链路，不能被 planner 吞掉。
type hookChatPlanner struct{}

func (hookChatPlanner) Plan(ctx context.Context, s, r string) (string, error) {
	b, _ := json.Marshal(SafeChatPlan("你好呀"))
	return string(b), nil
}

func newOnHook(p Planner) *L4Hook {
	return NewL4Hook(NewRunner(New(p), DefaultGate(), NewExitGuard(), NewDistillStats()))
}

func TestL4Hook_DefaultOff(t *testing.T) {
	os.Unsetenv("L4_ENABLED")
	if h := newOnHook(hookTaskPlanner{}); h != nil {
		t.Fatalf("default off should return nil hook")
	}
}

func TestL4Hook_TaskHandled(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")
	h := newOnHook(hookTaskPlanner{})
	if h == nil {
		t.Fatalf("on should return hook")
	}
	reply, handled := h.MaybeHandle(context.Background(), "wx_task", "提醒我开会")
	if !handled {
		t.Fatalf("task should be handled by L4")
	}
	if !strings.Contains(reply, "提醒") {
		t.Fatalf("reply: %q", reply)
	}
}

func TestL4Hook_ChatPassthrough(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")
	h := newOnHook(hookChatPlanner{})
	reply, handled := h.MaybeHandle(context.Background(), "wx_chat", "你好")
	if handled {
		t.Fatalf("pure chat must passthrough to old chat chain, got handled reply=%q", reply)
	}
}

func TestL4Hook_NilSafe(t *testing.T) {
	var h *L4Hook
	reply, handled := h.MaybeHandle(context.Background(), "wx_nil", "x")
	if handled || reply != "" {
		t.Fatalf("nil hook must not handle")
	}
}

func TestL4Hook_OnAliasAccepted(t *testing.T) {
	for _, v := range []string{"on", "ON", " true ", "yes"} {
		os.Setenv("L4_ENABLED", v)
		if newOnHook(hookTaskPlanner{}) == nil {
			t.Fatalf("L4_ENABLED=%q must activate hook", v)
		}
	}
	os.Unsetenv("L4_ENABLED")
	for _, v := range []string{"", "off", "0", "no"} {
		os.Setenv("L4_ENABLED", v)
		if h := newOnHook(hookTaskPlanner{}); h != nil {
			t.Fatalf("L4_ENABLED=%q must be off", v)
		}
	}
	os.Unsetenv("L4_ENABLED")
}
