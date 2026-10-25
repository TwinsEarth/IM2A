package skillbridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"human-agent-im/internal/orchestration"
	"human-agent-im/internal/skillruntime"
)

func newExec() *Executor {
	return New(skillruntime.NewRuntime(skillruntime.DefaultRegistry(), nil))
}

func TestBridge_RealPurchaseCompare(t *testing.T) {
	e := newExec()
	out, err := e.Execute(context.Background(), orchestration.AgentProcurement,
		orchestration.ToolStep{Tool: "purchase.search", Args: map[string]any{"item": "酱油"}}, 3)
	if err != nil {
		t.Fatalf("compare should execute: %v", err)
	}
	if !strings.Contains(out, "比价") {
		t.Fatalf("real compare output missing, got %q", out)
	}
}

func TestBridge_RealPurchaseCheckout(t *testing.T) {
	e := newExec()
	out, err := e.Execute(context.Background(), orchestration.AgentProcurement,
		orchestration.ToolStep{Tool: "purchase.checkout", Args: map[string]any{"item": "酱油", "amount": 95.0}}, 3)
	if err != nil {
		t.Fatalf("checkout should execute: %v", err)
	}
	if !strings.Contains(out, "已下单") {
		t.Fatalf("real order output missing, got %q", out)
	}
}

func TestBridge_RealPropertyReport(t *testing.T) {
	e := newExec()
	out, err := e.Execute(context.Background(), orchestration.AgentProperty,
		orchestration.ToolStep{Tool: "property.report", Args: map[string]any{"category": "water", "description": "漏水"}}, 3)
	if err != nil {
		t.Fatalf("report should execute: %v", err)
	}
	if !strings.Contains(out, "报修工单") {
		t.Fatalf("real work-order output missing, got %q", out)
	}
}

func TestBridge_Calendar(t *testing.T) {
	e := newExec()
	if _, err := e.Execute(context.Background(), orchestration.AgentSchedule,
		orchestration.ToolStep{Tool: "calendar.list", Args: map[string]any{}}, 3); err != nil {
		t.Fatalf("calendar query: %v", err)
	}
}

func TestBridge_ChannelActionNoSkill(t *testing.T) {
	e := newExec()
	// im.send 由出站通道完成，不经过领域 Skill：不报错、不产生领域输出。
	out, err := e.Execute(context.Background(), orchestration.AgentMaster,
		orchestration.ToolStep{Tool: "im.send", Args: map[string]any{"text": "在吗"}}, 3)
	if err != nil || out != "" {
		t.Fatalf("channel action should be noop, got out=%q err=%v", out, err)
	}
}

func TestBridge_NotWired(t *testing.T) {
	e := newExec()
	// legal.draft 在编排层是合法低风险工具，但尚无真实 Skill → 必须如实报未接通，不假装成功。
	_, err := e.Execute(context.Background(), orchestration.AgentLegal,
		orchestration.ToolStep{Tool: "legal.draft", Args: map[string]any{}}, 3)
	if !errors.Is(err, orchestration.ErrToolNotWired) {
		t.Fatalf("legal.draft must be ErrToolNotWired, got %v", err)
	}
}
