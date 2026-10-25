package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// v2.3.24：门控读生产注册表真实信任档，而不是固定 TrustLevel=3。

// 一条 master 高风险动作（im.send），需要 trust>=3。
type hookSendPlanner struct{}

func (hookSendPlanner) Plan(ctx context.Context, s, r string) (string, error) {
	p := Plan{
		Activation: ActivationRespond,
		Intent:     IntentTask,
		Confidence: 0.95,
		Subtasks: []SubTask{
			{SubAgent: AgentMaster, Goal: "代发消息", Steps: []ToolStep{{Tool: "im.send", Args: map[string]any{"text": "在吗"}}}},
		},
		ReplyHint: "已帮你发出去了。",
	}
	b, _ := json.Marshal(p)
	return string(b), nil
}

type fakeTrust struct {
	trust int
	err   error
}

func (f fakeTrust) TrustLevel(ctx context.Context, wxid string) (int, error) {
	return f.trust, f.err
}

func TestL4Hook_GateUsesRealTrust(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")

	// 信任档 2（低于高风险门槛 3）：必须被门控拒绝，不能放行。
	h := newOnHook(hookSendPlanner{}).WithTrust(fakeTrust{trust: 2})
	reply, handled := h.MaybeHandle(context.Background(), "wx_low", "帮我发条消息")
	if !handled {
		t.Fatal("task should be handled")
	}
	if !strings.Contains(reply, "不能直接办") {
		t.Fatalf("low trust must be denied, reply=%q", reply)
	}

	// 信任档 3（达标）：放行，回复 ReplyHint。
	h2 := newOnHook(hookSendPlanner{}).WithTrust(fakeTrust{trust: 3})
	reply2, handled2 := h2.MaybeHandle(context.Background(), "wx_high", "帮我发条消息")
	if !handled2 || !strings.Contains(reply2, "发出去了") {
		t.Fatalf("trust>=3 should auto-allow, handled=%v reply=%q", handled2, reply2)
	}
}

func TestL4Hook_TrustProviderErrorFallsBack(t *testing.T) {
	os.Setenv("L4_ENABLED", "1")
	defer os.Unsetenv("L4_ENABLED")
	// Provider 出错时回退默认档（L4Hook.TrustLevel=3），高风险动作仍应放行，不阻断。
	h := newOnHook(hookSendPlanner{}).WithTrust(fakeTrust{trust: 0, err: errors.New("registry down")})
	reply, handled := h.MaybeHandle(context.Background(), "wx_err", "帮我发条消息")
	if !handled || !strings.Contains(reply, "发出去了") {
		t.Fatalf("provider error must fall back to default trust, handled=%v reply=%q", handled, reply)
	}
}
