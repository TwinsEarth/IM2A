package negotiation

import (
	"errors"
	"strings"
	"testing"
)

func mkTurn(session, from, to string, intent Intent, payload map[string]any) Turn {
	return Turn{SessionID: session, FromAgentID: from, ToAgentID: to, Intent: intent, Payload: payload}
}

// TestCompleteInThreeRounds 三轮达成一致 → COMPLETED。
func TestCompleteInThreeRounds(t *testing.T) {
	s := NewSession("s1", 5, 0)
	turns := []Turn{
		mkTurn("s1", "a", "b", IntentPropose, map[string]any{"why": "合作"}),
		mkTurn("s1", "b", "a", IntentCounter, map[string]any{"offer": "周三 15:00"}),
		mkTurn("s1", "a", "b", IntentAccept, map[string]any{"agree": "周三 15:00"}),
	}
	for _, tn := range turns {
		if _, err := s.Apply(tn); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	if s.State != StateCompleted {
		t.Fatalf("state=%s want COMPLETED", s.State)
	}
	if !strings.Contains(s.SummaryJSON(), "agreed_at_round") {
		t.Fatalf("summary missing agreed_at_round: %s", s.SummaryJSON())
	}
}

// TestRoundLimit 超过最大轮次熔断为 INPUT_REQUIRED。
func TestRoundLimit(t *testing.T) {
	s := NewSession("s2", 5, 0)
	var lastErr error
	// 6 轮反提案（不 Accept），第 6 轮应触发熔断
	for i := 0; i < 6; i++ {
		intent := IntentPropose
		if i%2 == 1 {
			intent = IntentCounter
		}
		_, lastErr = s.Apply(mkTurn("s2", "a", "b", intent, map[string]any{"n": i}))
	}
	if !errors.Is(lastErr, ErrRoundLimit) {
		t.Fatalf("6th round err=%v want ErrRoundLimit", lastErr)
	}
	if s.State != StateInputRequired {
		t.Fatalf("state=%s want INPUT_REQUIRED", s.State)
	}
}

// TestRequestAuth 挂起等待人工审批 → AUTH_REQUIRED 并绑定审批单。
func TestRequestAuth(t *testing.T) {
	s := NewSession("s3", 5, 0)
	if _, err := s.Apply(mkTurn("s3", "a", "b", IntentRequestAuth, nil)); err != nil {
		t.Fatalf("request auth: %v", err)
	}
	if s.State != StateAuthRequired {
		t.Fatalf("state=%s want AUTH_REQUIRED", s.State)
	}
	s.BindApprovalTicket("t000042")
	if s.ApprovalTicketID != "t000042" {
		t.Fatalf("ticket=%s want t000042", s.ApprovalTicketID)
	}
}

// TestReject 预审拒绝 → REJECTED，终态不可再写。
func TestReject(t *testing.T) {
	s := NewSession("s4", 5, 0)
	if _, err := s.Apply(mkTurn("s4", "a", "b", IntentReject, nil)); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if s.State != StateRejected {
		t.Fatalf("state=%s want REJECTED", s.State)
	}
	if _, err := s.Apply(mkTurn("s4", "a", "b", IntentPropose, nil)); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("write after reject err=%v want ErrSessionEnded", err)
	}
}

// TestTokenBudget 超 token 预算熔断。
func TestTokenBudget(t *testing.T) {
	// 预算极小：任何非空 payload 都超限
	s := NewSession("s5", 5, 1)
	_, err := s.Apply(mkTurn("s5", "a", "b", IntentPropose, map[string]any{"why": "这是一段较长的来意描述，会超出极小预算"}))
	if !errors.Is(err, ErrTokenBudget) {
		t.Fatalf("err=%v want ErrTokenBudget", err)
	}
	if s.State != StateInputRequired {
		t.Fatalf("state=%s want INPUT_REQUIRED", s.State)
	}
}
