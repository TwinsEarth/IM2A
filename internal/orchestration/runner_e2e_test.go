package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// v2.3.16 D4：端到端 smoke 测试。
// Planner 接口返回原始 JSON，Commander 自己 ParsePlan。

type stubPlanner struct {
	plan Plan
}

func (s *stubPlanner) Plan(ctx context.Context, userText, recentSummary string) (string, error) {
	b, _ := json.Marshal(s.plan)
	return string(b), nil
}

// v2.3.21：纯闲聊必须透传回旧链路（由完整 chat 模型回答），L4 不吞。
func TestD4_ChatPassthrough(t *testing.T) {
	cmd := New(&stubPlanner{plan: SafeChatPlan("你好呀")})
	r := NewRunner(cmd, DefaultGate(), NewExitGuard(), NewDistillStats())
	res := r.Run(context.Background(), "你好", "", 0)
	if !res.Passthrough {
		t.Fatalf("chat must passthrough to old chain: %+v", res)
	}
	if res.Reply != "" {
		t.Fatalf("passthrough must not fabricate reply, got %q", res.Reply)
	}
}

// v2.3.21：信息不全时回澄清问题（L4 接管，不透传）。
func TestD4_ClarifyQuestion(t *testing.T) {
	p := Plan{
		Activation:      ActivationRespond,
		Intent:          IntentTask,
		Confidence:      0.9,
		NeedClarify:     true,
		ClarifyQuestion: "你想约周几的几点？",
	}
	cmd := New(&stubPlanner{plan: p})
	r := NewRunner(cmd, DefaultGate(), NewExitGuard(), NewDistillStats())
	res := r.Run(context.Background(), "帮我约个时间", "", 3)
	if res.Passthrough {
		t.Fatalf("clarify must be handled by L4")
	}
	if !strings.Contains(res.Reply, "周几") {
		t.Fatalf("reply should be the clarify question: %q", res.Reply)
	}
}

// v2.3.21：转发/协商/系统意图透传回旧链路（L4 只接管办事）。
func TestD4_RelayNegoSystemPassthrough(t *testing.T) {
	for _, intent := range []Intent{IntentRelay, IntentNegoReply, IntentSystem} {
		p := Plan{Activation: ActivationRespond, Intent: intent, Confidence: 0.9, ReplyHint: "x"}
		r := NewRunner(New(&stubPlanner{plan: p}), DefaultGate(), NewExitGuard(), NewDistillStats())
		res := r.Run(context.Background(), "发给小王", "", 3)
		if !res.Passthrough {
			t.Fatalf("intent %s must passthrough", intent)
		}
	}
}

// v2.3.21：群聊未 @ 静默（handled=true、reply 为空）。
func TestD4_IgnoreSilent(t *testing.T) {
	p := Plan{Activation: ActivationIgnore, Intent: IntentChat, Confidence: 0.9}
	r := NewRunner(New(&stubPlanner{plan: p}), DefaultGate(), NewExitGuard(), NewDistillStats())
	res := r.Run(context.Background(), "群里闲聊", "", 3)
	if res.Passthrough || res.Reply != "" {
		t.Fatalf("ignore must be handled silently: %+v", res)
	}
}

func TestD4_AllowedTool(t *testing.T) {
	p := Plan{
		Activation: ActivationRespond,
		Intent:     IntentTask,
		Confidence: 0.9,
		Subtasks: []SubTask{
			{
				SubAgent: "schedule",
				Goal:     "订会议",
				Steps: []ToolStep{
					{Tool: "calendar.add", Args: map[string]any{"title": "周会"}},
				},
			},
		},
		ReplyHint: "已帮你订好周会。",
	}
	cmd := New(&stubPlanner{plan: p})
	r := NewRunner(cmd, DefaultGate(), NewExitGuard(), NewDistillStats())
	res := r.Run(context.Background(), "帮我订周会", "", 3)
	if !res.Allowed || res.NeedApprove || res.Passthrough {
		t.Fatalf("calendar.add should be allowed: %+v", res)
	}
	if !strings.Contains(res.Reply, "周会") {
		t.Fatalf("reply: %q", res.Reply)
	}
}

func TestD4_HighRiskDenied(t *testing.T) {
	p := Plan{
		Activation: ActivationRespond,
		Intent:     IntentTask,
		Confidence: 0.9,
		Subtasks: []SubTask{
			{
				SubAgent: "procurement",
				Goal:     "下单",
				Steps: []ToolStep{
					{Tool: "purchase.checkout", Args: map[string]any{"amount": 100}},
				},
			},
		},
	}
	cmd := New(&stubPlanner{plan: p})
	r := NewRunner(cmd, DefaultGate(), NewExitGuard(), NewDistillStats())
	res := r.Run(context.Background(), "买", "", 1)
	if res.Allowed || res.Passthrough {
		t.Fatalf("high-risk with low trust must be denied: %+v", res)
	}
	if strings.Contains(res.Reply, "wxid") || strings.Contains(res.Reply, "ticket_") {
		t.Fatalf("reply leaks internals: %q", res.Reply)
	}
}

func TestD4_NeedsApprovalOverBudget(t *testing.T) {
	p := Plan{
		Activation: ActivationRespond,
		Intent:     IntentTask,
		Confidence: 0.9,
		Subtasks: []SubTask{
			{
				SubAgent: "procurement",
				Goal:     "买大件",
				Steps: []ToolStep{
					{Tool: "purchase.checkout", Args: map[string]any{"amount": 800}},
				},
			},
		},
	}
	cmd := New(&stubPlanner{plan: p})
	r := NewRunner(cmd, DefaultGate(), NewExitGuard(), NewDistillStats())
	res := r.Run(context.Background(), "买个 800 块的东西", "", 4)
	if !res.NeedApprove || res.Allowed {
		t.Fatalf("over budget should need approval: %+v", res)
	}
	if !strings.Contains(res.Reply, "批准") {
		t.Fatalf("reply should ask approval: %q", res.Reply)
	}
}

// v2.3.21：checkout 缺金额 → 必须上抛审批（不能默认放行）；负数 → 拒绝。
func TestD4_CheckoutMissingAmountNeedsApproval(t *testing.T) {
	p := Plan{
		Activation: ActivationRespond, Intent: IntentTask, Confidence: 0.9,
		Subtasks: []SubTask{{SubAgent: AgentProcurement, Goal: "下单",
			Steps: []ToolStep{{Tool: "purchase.checkout", Args: map[string]any{}}}}},
	}
	r := NewRunner(New(&stubPlanner{plan: p}), DefaultGate(), NewExitGuard(), NewDistillStats())
	res := r.Run(context.Background(), "帮我下单", "", 4)
	if !res.NeedApprove || res.Allowed {
		t.Fatalf("missing amount must require approval: %+v", res)
	}

	p2 := p
	p2.Subtasks = []SubTask{{SubAgent: AgentProcurement, Goal: "下单",
		Steps: []ToolStep{{Tool: "purchase.checkout", Args: map[string]any{"amount": -5}}}}}
	r2 := NewRunner(New(&stubPlanner{plan: p2}), DefaultGate(), NewExitGuard(), NewDistillStats())
	res2 := r2.Run(context.Background(), "帮我下单", "", 4)
	if res2.Allowed || res2.NeedApprove {
		t.Fatalf("negative amount must be denied, not approval: %+v", res2)
	}
}

// v2.3.21：只有办事（自动放行）才计入 approved；透传的闲聊不计入蒸馏统计。
func TestD4_StatsCountOnlyHandled(t *testing.T) {
	stats := NewDistillStats()
	// 5 条闲聊：透传，不计入
	chatRunner := NewRunner(New(&stubPlanner{plan: SafeChatPlan("hi")}), DefaultGate(), NewExitGuard(), stats)
	for i := 0; i < 5; i++ {
		chatRunner.Run(context.Background(), "hi", "", 0)
	}
	if got := stats.Snapshot().Total; got != 0 {
		t.Fatalf("chat passthrough must not count, total=%d", got)
	}
	// 3 个自动放行任务：计 3 approved
	okPlan := Plan{Activation: ActivationRespond, Intent: IntentTask, Confidence: 0.9,
		Subtasks: []SubTask{{SubAgent: AgentSchedule, Steps: []ToolStep{{Tool: "calendar.add"}}}}}
	taskRunner := NewRunner(New(&stubPlanner{plan: okPlan}), DefaultGate(), NewExitGuard(), stats)
	for i := 0; i < 3; i++ {
		taskRunner.Run(context.Background(), "提醒", "", 3)
	}
	snap := stats.Snapshot()
	if snap.Total != 3 || snap.Approved != 3 {
		t.Fatalf("want 3 approved, got %+v", snap)
	}
}

func TestD4_NilSafe(t *testing.T) {
	var r *Runner
	res := r.Run(context.Background(), "x", "", 0)
	if !res.Passthrough {
		t.Fatalf("nil runner should passthrough rather than swallow")
	}
}
