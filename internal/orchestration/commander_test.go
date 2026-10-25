package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakePlanner struct {
	out string
	err error
	delay time.Duration
}

func (f fakePlanner) Plan(ctx context.Context, text, summary string) (string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.out, f.err
}

func TestParsePlan_Valid(t *testing.T) {
	raw := `{"activation":"respond","intent":"task","confidence":0.9,
	"subtasks":[{"sub_agent":"schedule","goal":"明天9点提醒","steps":[{"tool":"calendar.add","risk":"low"}]}]}`
	p, ok := ParsePlan(raw)
	if !ok || p.Intent != IntentTask || p.Confidence < 0.8 {
		t.Fatalf("parse failed: %+v ok=%v", p, ok)
	}
	if len(p.Subtasks) != 1 || p.Subtasks[0].SubAgent != AgentSchedule {
		t.Fatalf("subtasks wrong: %+v", p.Subtasks)
	}
}

func TestParsePlan_WrappedInCodeFence(t *testing.T) {
	raw := "好的，这是结果：\n```json\n{\"activation\":\"respond\",\"intent\":\"chat\",\"confidence\":0.8}\n```"
	p, ok := ParsePlan(raw)
	if !ok || p.Intent != IntentChat {
		t.Fatalf("fence parse failed: %+v ok=%v", p, ok)
	}
}

func TestParsePlan_InvalidJSON(t *testing.T) {
	if _, ok := ParsePlan("不是JSON"); ok {
		t.Fatal("expected fail on garbage")
	}
}

func TestParsePlan_BadIntent(t *testing.T) {
	if _, ok := ParsePlan(`{"intent":"hack","confidence":0.9}`); ok {
		t.Fatal("expected fail on unknown intent")
	}
}

func TestCommander_FallbackOnPlannerError(t *testing.T) {
	c := New(fakePlanner{err: errors.New("boom")})
	p := c.Plan(context.Background(), "hi", "")
	if p.Intent != IntentChat {
		t.Fatalf("should fallback to chat, got %+v", p)
	}
}

func TestCommander_FallbackOnTimeout(t *testing.T) {
	c := New(fakePlanner{out: `{"intent":"task","confidence":0.9}`, delay: 8 * time.Second})
	p := c.Plan(context.Background(), "提醒我", "")
	if p.Intent != IntentChat {
		t.Fatalf("timeout should fallback chat, got %+v", p)
	}
}

func TestCommander_LowConfidenceFallback(t *testing.T) {
	c := New(fakePlanner{out: `{"intent":"task","confidence":0.2}`})
	p := c.Plan(context.Background(), "随便", "")
	if p.Intent != IntentChat {
		t.Fatalf("low confidence should fallback, got %+v", p)
	}
}

func TestDispatcher_CircuitOnTooManySubtasks(t *testing.T) {
	d := NewDispatcher(DefaultBounds(), nil)
	p := Plan{Intent: IntentTask}
	for i := 0; i < DefaultBounds().MaxSubtasks+1; i++ {
		p.Subtasks = append(p.Subtasks, SubTask{SubAgent: AgentMaster, Goal: "x"})
	}
	o := d.Dispatch(context.Background(), p)
	if !o.Circuit || o.OK {
		t.Fatalf("should circuit, got %+v", o)
	}
}

func TestDispatcher_ChatPassesThrough(t *testing.T) {
	d := NewDispatcher(DefaultBounds(), nil)
	if o := d.Dispatch(context.Background(), Plan{Intent: IntentChat}); !o.OK {
		t.Fatalf("chat should pass through: %+v", o)
	}
}

func TestSafeChatPlan_Defaults(t *testing.T) {
	p := SafeChatPlan("hi")
	if p.Intent != IntentChat || p.ReplyHint != "hi" || p.Confidence != 0 {
		t.Fatalf("safe plan wrong: %+v", p)
	}
	_ = strings.Contains
}
