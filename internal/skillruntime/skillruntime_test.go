package skillruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"human-agent-im/internal/trustzone"
)

var errNoCap = errors.New("capability not registered")

// fakeCaps 进程内能力提供者。
type fakeCaps struct {
	m map[string]*trustzone.Capability
}

func (f fakeCaps) GetCapability(_ context.Context, agentID string) (*trustzone.Capability, error) {
	if c, ok := f.m[agentID]; ok {
		return c, nil
	}
	return nil, errNoCap
}

func newTestRuntime() *Runtime {
	family := &trustzone.Capability{
		AgentID:                    "family-agent",
		TrustLevelMin:              trustzone.LevelFamily,
		AllowedSkills:              []string{"home-maintenance", "family-purchase"},
		RequiresApprovalAboveCents: 200000,
		BudgetCentsPerDay:          1000000,
	}
	return NewRuntime(DefaultRegistry(), fakeCaps{m: map[string]*trustzone.Capability{"family-agent": family}})
}

func TestInitializeProtocol(t *testing.T) {
	rt := newTestRuntime()
	res := rt.Initialize()
	if res.ProtocolVersion != "2026-07-28" {
		t.Fatalf("protocol=%s want 2026-07-28", res.ProtocolVersion)
	}
}

func TestListTools(t *testing.T) {
	rt := newTestRuntime()
	tools := rt.ListTools()
	if len(tools) < 4 {
		t.Fatalf("tools=%d want >=4", len(tools))
	}
}

func TestGateAllow(t *testing.T) {
	rt := newTestRuntime()
	r := rt.Call(context.Background(), CallParams{
		Name: "home-maintenance.request",
		Gate: GateContext{AgentID: "family-agent", TrustLevel: int(trustzone.LevelFamily)},
	})
	if r.Status != "OK" {
		t.Fatalf("status=%s want OK (gate=%s)", r.Status, gateStr(r))
	}
}

func TestGateRequiresApprovalOverAmount(t *testing.T) {
	rt := newTestRuntime()
	r := rt.Call(context.Background(), CallParams{
		Name:      "family-purchase.order",
		Arguments: map[string]any{"amountCents": float64(500000)},
		Gate:      GateContext{AgentID: "family-agent", TrustLevel: int(trustzone.LevelFamily)},
	})
	if r.Status != "REQUIRES_APPROVAL" {
		t.Fatalf("status=%s want REQUIRES_APPROVAL (gate=%s)", r.Status, gateStr(r))
	}
}

func TestGateDenyStranger(t *testing.T) {
	rt := newTestRuntime()
	r := rt.Call(context.Background(), CallParams{
		Name: "home-maintenance.request",
		Gate: GateContext{AgentID: "unknown-agent", TrustLevel: int(trustzone.LevelStranger)},
	})
	if r.Status != "DENIED" {
		t.Fatalf("status=%s want DENIED (gate=%s)", r.Status, gateStr(r))
	}
}

func TestGateDenyToolNotFound(t *testing.T) {
	rt := newTestRuntime()
	r := rt.Call(context.Background(), CallParams{
		Name: "nope.tool",
		Gate: GateContext{AgentID: "family-agent", TrustLevel: int(trustzone.LevelFamily)},
	})
	if r.Status != "DENIED" {
		t.Fatalf("status=%s want DENIED", r.Status)
	}
}

func TestDispatchJSONRPC(t *testing.T) {
	rt := newTestRuntime()
	// initialize
	resp := rt.Dispatch(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if resp.Error != nil {
		t.Fatalf("initialize error: %v", resp.Error)
	}
	var init InitializeResult
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &init); err != nil {
		t.Fatalf("unmarshal init: %v", err)
	}
	if init.ProtocolVersion != "2026-07-28" {
		t.Fatalf("protocol=%s", init.ProtocolVersion)
	}
	// 未知方法 → method not found
	resp = rt.Dispatch(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"bogus"}`))
	if resp.Error == nil || resp.Error.Code != ErrCodeMethodNotFound {
		t.Fatalf("bogus method resp=%+v", resp)
	}
	// tools/call 门控放行（家庭 Agent 调用白名单内的物业报修）
	resp = rt.Dispatch(context.Background(), []byte(`{
		"jsonrpc":"2.0","id":3,"method":"tools/call",
		"params":{"name":"home-maintenance.request","_gate":{"agentId":"family-agent","trustLevel":1}}
	}`))
	if resp.Error != nil {
		t.Fatalf("tools/call error: %v", resp.Error)
	}
}

func gateStr(r CallResult) string {
	if r.Gate == nil {
		return "<nil>"
	}
	return r.Gate.Decision + ":" + r.Gate.Reason
}
