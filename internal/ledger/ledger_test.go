package ledger

import (
	"errors"
	"testing"
)

// TestSendChargeIdempotent 发送即扣费：重复回调不二次扣费。
func TestSendChargeIdempotent(t *testing.T) {
	l := New(10)
	if _, err := l.Reserve("m1", 100, 200, ModeSendCharge, 100); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	e, err := l.Charge("m1", "")
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if e.Status != StatusCharged {
		t.Fatalf("status=%s want CHARGED", e.Status)
	}
	if _, err := l.Charge("m1", ""); !errors.Is(err, ErrAlreadyProcessed) {
		t.Fatalf("duplicate charge err=%v want ErrAlreadyProcessed", err)
	}
}

// TestReplyCharge 回复才扣费：先占位，回复后才扣费。
func TestReplyCharge(t *testing.T) {
	l := New(10)
	e, err := l.Reserve("m2", 100, 200, ModeReplyCharge, 5000)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if e.Status != StatusPending {
		t.Fatalf("after reserve status=%s want PENDING", e.Status)
	}
	e, err = l.Charge("m2", "t000009")
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if e.Status != StatusCharged || e.ApprovalCardID != "t000009" {
		t.Fatalf("charged entry status=%s card=%s", e.Status, e.ApprovalCardID)
	}
}

// TestRefund 退款只能从已扣费迁移，且不能重复。
func TestRefund(t *testing.T) {
	l := New(10)
	_, _ = l.Reserve("m3", 1, 2, ModeSendCharge, 100)
	// 未扣费先退款应非法
	if _, err := l.Refund("m3", "not charged"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("refund pending err=%v want ErrInvalidTransition", err)
	}
	_, _ = l.Charge("m3", "")
	e, err := l.Refund("m3", "user requested")
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if e.Status != StatusRefunded {
		t.Fatalf("status=%s want REFUNDED", e.Status)
	}
	if _, err := l.Charge("m3", ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("recharge refunded err=%v want ErrInvalidTransition", err)
	}
}

// TestDailyLimit 单日发送上限。
func TestDailyLimit(t *testing.T) {
	l := New(3)
	for i := 0; i < 3; i++ {
		if _, err := l.Reserve("ok"+string(rune('a'+i)), 100, 200, ModeSendCharge, 10); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}
	if _, err := l.Reserve("overflow", 100, 200, ModeSendCharge, 10); !errors.Is(err, ErrDailyLimitExceeded) {
		t.Fatalf("4th reserve err=%v want ErrDailyLimitExceeded", err)
	}
	// 不同发送方不受影响
	if _, err := l.Reserve("other-sender", 101, 200, ModeSendCharge, 10); err != nil {
		t.Fatalf("other sender reserve: %v", err)
	}
	sent, limit := l.DailyUsage(100)
	if sent != 3 || limit != 3 {
		t.Fatalf("daily usage sent=%d limit=%d want 3/3", sent, limit)
	}
}

// TestChargeNotFound 未占位直接扣费应 NotFound。
func TestChargeNotFound(t *testing.T) {
	l := New(10)
	if _, err := l.Charge("missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("charge missing err=%v want ErrNotFound", err)
	}
}
