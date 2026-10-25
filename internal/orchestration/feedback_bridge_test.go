package orchestration

import (
	"context"
	"testing"

	"human-agent-im/internal/approval"
)

type fakeSink struct {
	calls []fakeCall
}
type fakeCall struct {
	userID uint64
	agent  string
	key    string
	delta  float64
}

func (f *fakeSink) Record(userID uint64, agent, key string, delta float64) {
	f.calls = append(f.calls, fakeCall{userID, agent, key, delta})
}

func TestD1_ApproveStrengthens(t *testing.T) {
	sink := &fakeSink{}
	b := NewApprovalFeedbackBridge(sink)
	tk := &approval.Ticket{
		UserID:        100,
		SourceAgentID: "procurement",
		Type:          approval.TicketPayment,
		State:         approval.StateApproved,
	}
	b.OnTicketResolved(context.Background(), tk)
	if len(sink.calls) != 1 {
		t.Fatalf("want 1 call, got %+v", sink.calls)
	}
	c := sink.calls[0]
	if c.userID != 100 || c.agent != "procurement" || c.key != "PAYMENT" || c.delta <= 0 {
		t.Fatalf("bad call: %+v", c)
	}
}

func TestD1_RejectWeakens(t *testing.T) {
	sink := &fakeSink{}
	b := NewApprovalFeedbackBridge(sink)
	tk := &approval.Ticket{
		UserID:        100,
		SourceAgentID: "procurement",
		Type:          approval.TicketPayment,
		State:         approval.StateRejected,
	}
	b.OnTicketResolved(context.Background(), tk)
	if sink.calls[0].delta >= 0 {
		t.Fatalf("reject must be negative, got %+v", sink.calls[0])
	}
}

func TestD1_ModifyWeakensMildly(t *testing.T) {
	sink := &fakeSink{}
	b := NewApprovalFeedbackBridge(sink)
	tk := &approval.Ticket{
		UserID:        100,
		SourceAgentID: "schedule",
		Type:          approval.TicketNegotiation,
		State:         approval.StateModified,
	}
	b.OnTicketResolved(context.Background(), tk)
	if sink.calls[0].delta >= 0 {
		t.Fatalf("modify must be mildly negative, got %+v", sink.calls[0])
	}
}

func TestD1_FailedNotScored(t *testing.T) {
	sink := &fakeSink{}
	b := NewApprovalFeedbackBridge(sink)
	tk := &approval.Ticket{
		UserID:        100,
		SourceAgentID: "x",
		Type:          approval.TicketPayment,
		State:         approval.StateFailed,
	}
	b.OnTicketResolved(context.Background(), tk)
	if len(sink.calls) != 0 {
		t.Fatalf("failed must not score, got %+v", sink.calls)
	}
}

func TestD1_NilSafe(t *testing.T) {
	b := NewApprovalFeedbackBridge(nil)
	b.OnTicketResolved(context.Background(), nil) // must not panic
}

func TestD1_EmptyAgentFallback(t *testing.T) {
	sink := &fakeSink{}
	b := NewApprovalFeedbackBridge(sink)
	tk := &approval.Ticket{UserID: 1, State: approval.StateApproved}
	b.OnTicketResolved(context.Background(), tk)
	if sink.calls[0].agent != "unknown" || sink.calls[0].key != "general" {
		t.Fatalf("fallback bad: %+v", sink.calls[0])
	}
}
