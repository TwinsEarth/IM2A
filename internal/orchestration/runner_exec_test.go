package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// v2.3.25：Runner 门控放行后必须真正执行工具，消除"假已办"。

type execPlan struct {
	agent SubAgentID
	tool  string
	args  map[string]any
	hint  string
}

type execPlanner struct{ p execPlan }

func (pl execPlanner) Plan(ctx context.Context, s, r string) (string, error) {
	plan := Plan{
		Activation: ActivationRespond, Intent: IntentTask, Confidence: 0.95,
		Subtasks: []SubTask{{SubAgent: pl.p.agent, Goal: pl.p.tool,
			Steps: []ToolStep{{Tool: pl.p.tool, Args: pl.p.args}}}},
		ReplyHint: pl.p.hint,
	}
	b, _ := json.Marshal(plan)
	return string(b), nil
}

type fakeExec struct {
	out    string
	err    error
	called bool
}

func (f *fakeExec) Execute(ctx context.Context, sub SubAgentID, step ToolStep, trust int) (string, error) {
	f.called = true
	return f.out, f.err
}

func newRunnerWithExec(p Planner, exec ToolExecutor) *Runner {
	return NewRunner(New(p), DefaultGate(), NewExitGuard(), NewDistillStats()).WithExecutor(exec)
}

func TestRunner_RealExecuteReplacesFakeHint(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")
	exec := &fakeExec{out: "「酱油」三家比价，最低 ¥9.50"}
	h := NewL4Hook(newRunnerWithExec(execPlanner{execPlan{
		agent: AgentProcurement, tool: "purchase.search", args: map[string]any{"item": "酱油"}},
	}, exec))
	reply, handled := h.MaybeHandle(context.Background(), "wx_exec", "帮我比下酱油价格")
	if !handled {
		t.Fatal("should be handled")
	}
	if !exec.called {
		t.Fatal("executor must be called (no fake completion)")
	}
	if !strings.Contains(reply, "比价") {
		t.Fatalf("reply must be the real skill output, got %q", reply)
	}
}

func TestRunner_NotWiredIsHonest(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")
	exec := &fakeExec{err: ErrToolNotWired}
	h := NewL4Hook(newRunnerWithExec(execPlanner{execPlan{
		agent: AgentLegal, tool: "legal.draft", args: map[string]any{}},
	}, exec))
	reply, handled := h.MaybeHandle(context.Background(), "wx_legal", "帮我写个合同")
	if !handled {
		t.Fatal("should be handled")
	}
	if !strings.Contains(reply, "还没接通") {
		t.Fatalf("not-wired must be honest, got %q", reply)
	}
}

func TestRunner_ExecFailureHonest(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")
	exec := &fakeExec{err: errors.New("downstream 500")}
	h := NewL4Hook(newRunnerWithExec(execPlanner{execPlan{
		agent: AgentSchedule, tool: "calendar.add", args: map[string]any{"title": "x"}},
	}, exec))
	reply, handled := h.MaybeHandle(context.Background(), "wx_fail", "提醒我")
	if !handled || !strings.Contains(reply, "没办成") {
		t.Fatalf("exec failure must be reported honestly, handled=%v reply=%q", handled, reply)
	}
}

func TestRunner_NoExecutorKeepsPlanHint(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")
	// 无执行器时维持旧行为（回 ReplyHint），但 Executed=false，骨架期可接受。
	h := NewL4Hook(newRunnerWithExec(execPlanner{execPlan{
		agent: AgentSchedule, tool: "calendar.add", hint: "已帮你记下。"}}, nil))
	reply, handled := h.MaybeHandle(context.Background(), "wx_plan", "提醒我")
	if !handled || !strings.Contains(reply, "记下") {
		t.Fatalf("plan-only fallback, got handled=%v reply=%q", handled, reply)
	}
}
