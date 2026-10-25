package orchestration

import (
	"context"
	"encoding/json"
	"testing"
)

type benchPlanner struct{}

func (benchPlanner) Plan(ctx context.Context, s, r string) (string, error) {
	p := SafeChatPlan("好的，我记下了")
	b, _ := json.Marshal(p)
	return string(b), nil
}

func benchRunner() *Runner {
	return NewRunner(New(benchPlanner{}), DefaultGate(), NewExitGuard(), NewDistillStats())
}

func BenchmarkRunnerChat(b *testing.B) {
	r := benchRunner()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Run(ctx, "你好", "", 3)
	}
}

func BenchmarkGateCheck(b *testing.B) {
	g := DefaultGate()
	step := ToolStep{Tool: "purchase.checkout", Risk: "high", Args: map[string]any{"amount": 100}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Check(AgentProcurement, step, 3)
	}
}

func BenchmarkGuardSanitize(b *testing.B) {
	g := NewExitGuard()
	in := "这是一段正常回复，帮你安排下午三点的会议，记得带材料。"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Sanitize(in)
	}
}
