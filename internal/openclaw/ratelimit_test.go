package openclaw

import (
	"context"
	"testing"
	"time"
)

func TestWaitOutbound_FirstCallNoWait(t *testing.T) {
	ResetRateLimiter("acc_test_1")
	start := time.Now()
	if err := WaitOutbound(context.Background(), "acc_test_1"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("first call should not block")
	}
}

func TestWaitOutbound_SecondCallWaits(t *testing.T) {
	ResetRateLimiter("acc_test_2")
	_ = WaitOutbound(context.Background(), "acc_test_2")
	start := time.Now()
	if err := WaitOutbound(context.Background(), "acc_test_2"); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 2*time.Second {
		t.Fatalf("second call should wait ~3s, got %v", elapsed)
	}
}

func TestWaitOutbound_ContextCancel(t *testing.T) {
	ResetRateLimiter("acc_test_3")
	_ = WaitOutbound(context.Background(), "acc_test_3")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := WaitOutbound(ctx, "acc_test_3")
	if err == nil {
		t.Fatal("expected context error")
	}
	if time.Since(start) > 800*time.Millisecond {
		t.Fatal("should cancel quickly")
	}
}

func TestWaitOutbound_IndependentAccounts(t *testing.T) {
	ResetRateLimiter("acc_a")
	ResetRateLimiter("acc_b")
	_ = WaitOutbound(context.Background(), "acc_a")
	start := time.Now()
	if err := WaitOutbound(context.Background(), "acc_b"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("different accounts should not block each other")
	}
}
