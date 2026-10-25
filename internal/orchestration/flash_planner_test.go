package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"human-agent-im/internal/model"
)

// 实现一个 stub provider，记录请求并返回预设 JSON。
type stubFlash struct {
	out string
	err error
	got []model.Message
}

func (s *stubFlash) Name() string { return "stub-flash" }
func (s *stubFlash) Chat(_ context.Context, req *model.ChatRequest) (*model.ChatResponse, error) {
	s.got = req.Messages
	if s.err != nil {
		return nil, s.err
	}
	return &model.ChatResponse{Content: s.out, Model: "stub"}, nil
}

func TestFlashPlanner_RealJSON(t *testing.T) {
	want := `{"activation":"respond","intent":"task","confidence":0.8,
"subtasks":[{"sub_agent":"schedule","goal":"明天 10 点提醒我开会"}]}`
	p := NewFlashPlanner(&stubFlash{out: want})
	c := New(p)
	plan := c.Plan(context.Background(), "明天10点提醒我开会", "")
	if plan.Intent != IntentTask || plan.Confidence < 0.7 {
		t.Fatalf("bad plan %+v", plan)
	}
	if len(plan.Subtasks) != 1 || plan.Subtasks[0].SubAgent != AgentSchedule {
		t.Fatalf("bad subtasks %+v", plan.Subtasks)
	}
}

func TestFlashPlanner_LowConfidenceFallsBack(t *testing.T) {
	p := NewFlashPlanner(&stubFlash{out: `{"intent":"task","confidence":0.2}`})
	plan := New(p).Plan(context.Background(), "在吗", "")
	if plan.Intent != IntentChat {
		t.Fatalf("low conf should fall back to chat, got %+v", plan)
	}
}

func TestFlashPlanner_MalformedJSONFallsBack(t *testing.T) {
	p := NewFlashPlanner(&stubFlash{out: "我觉得你应该..."})
	plan := New(p).Plan(context.Background(), "随便聊", "")
	if plan.Intent != IntentChat {
		t.Fatalf("malformed should fall back, got %+v", plan)
	}
}

func TestFlashPlanner_ProviderErrorFallsBack(t *testing.T) {
	p := NewFlashPlanner(&stubFlash{err: errors.New("timeout")})
	plan := New(p).Plan(context.Background(), "hi", "")
	if plan.Intent != IntentChat {
		t.Fatalf("provider error should fall back, got %+v", plan)
	}
}

func TestFlashPlanner_RecentSummaryInjected(t *testing.T) {
	cli := &stubFlash{out: `{"intent":"chat","confidence":0.9}`}
	p := NewFlashPlanner(cli)
	plan := New(p).Plan(context.Background(), "买咖啡", "用户昨天说美式")
	_ = plan
	if len(cli.got) < 2 || !strings.Contains(cli.got[1].Content, "美式") {
		t.Fatalf("recent summary not injected: %+v", cli.got)
	}
}

func TestFlashPlanner_Timeout(t *testing.T) {
	cli := &stubFlash{} // 永远不返回
	_ = cli
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Commander 自己有 6s timeout，这里只验证超时 ctx 能正常降级
	plan := New(NewFlashPlanner(&slowFlash{})).Plan(ctx, "x", "")
	if plan.Intent != IntentChat {
		t.Fatalf("timeout should fall back, got %+v", plan)
	}
}

type slowFlash struct{}

func (s *slowFlash) Name() string { return "slow" }
func (s *slowFlash) Chat(ctx context.Context, _ *model.ChatRequest) (*model.ChatResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
