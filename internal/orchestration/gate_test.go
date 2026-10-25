package orchestration

import "testing"

func TestGate_AllowLowRisk(t *testing.T) {
	g := DefaultGate()
	d := g.Check(AgentSchedule, ToolStep{Tool: "calendar.add", Args: map[string]any{"time": "tomorrow"}}, 1)
	if !d.Allow || d.NeedsApproval {
		t.Fatalf("calendar.add should auto-allow: %+v", d)
	}
}

func TestGate_UnknownToolDenied(t *testing.T) {
	g := DefaultGate()
	d := g.Check(AgentSchedule, ToolStep{Tool: "rm -rf /"}, 5)
	if d.Allow {
		t.Fatalf("unknown tool must be denied, got %+v", d)
	}
}

func TestGate_CapabilityDenied(t *testing.T) {
	g := DefaultGate()
	// schedule agent 不能调 purchase.checkout
	d := g.Check(AgentSchedule, ToolStep{Tool: "purchase.checkout", Args: map[string]any{"amount": 10}}, 5)
	if d.Allow {
		t.Fatalf("capability mismatch must be denied: %+v", d)
	}
}

func TestGate_HighRiskLowTrustDenied(t *testing.T) {
	g := DefaultGate()
	d := g.Check(AgentMaster, ToolStep{Tool: "im.send"}, 1)
	if d.Allow {
		t.Fatalf("high-risk with trust=1 must be denied: %+v", d)
	}
}

func TestGate_HighRiskHighTrustAllowed(t *testing.T) {
	g := DefaultGate()
	d := g.Check(AgentMaster, ToolStep{Tool: "im.send"}, 4)
	if !d.Allow {
		t.Fatalf("high-risk with trust=4 should be allowed: %+v", d)
	}
}

func TestGate_BudgetOverThresholdNeedsApproval(t *testing.T) {
	g := DefaultGate()
	d := g.Check(AgentProcurement,
		ToolStep{Tool: "purchase.checkout", Args: map[string]any{"amount": 1200.0}},
		4)
	if !d.Allow || !d.NeedsApproval {
		t.Fatalf("over-budget should allow but flag approval: %+v", d)
	}
}

func TestGate_BudgetUnderThresholdAuto(t *testing.T) {
	g := DefaultGate()
	d := g.Check(AgentProcurement,
		ToolStep{Tool: "purchase.checkout", Args: map[string]any{"amount": 99.0}},
		4)
	if !d.Allow || d.NeedsApproval {
		t.Fatalf("under-budget should auto: %+v", d)
	}
}
