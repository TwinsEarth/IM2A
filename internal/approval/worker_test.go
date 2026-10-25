package approval

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeSink struct {
	mu    sync.Mutex
	calls []fakeCall
}
type fakeCall struct {
	userID  uint64
	ticket  string
	channel string
}

func (f *fakeSink) Notify(_ context.Context, userID uint64, t *Ticket, ch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{userID, t.ID, ch})
	return nil
}

func (f *fakeSink) snapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestB4_TickEscalatesAfter30min(t *testing.T) {
	e := New(NewMemoryStore())
	sink := &fakeSink{}
	w := NewWorker(e, sink, time.Minute)

	// deadline 已过 31 分钟
	deadline := time.Now().Add(-31 * time.Minute)
	tk, _ := e.CreateTicket(100, "procurement", TicketPayment, nil, deadline)

	if err := w.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	calls := sink.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 escalation, got %+v", calls)
	}
	if calls[0].channel != "watch_push" || calls[0].ticket != tk.ID {
		t.Fatalf("bad call: %+v", calls[0])
	}
}

func TestB4_TickEscalatesToSMSAfter2h(t *testing.T) {
	e := New(NewMemoryStore())
	sink := &fakeSink{}
	w := NewWorker(e, sink, time.Minute)

	deadline := time.Now().Add(-3 * time.Hour)
	_, _ = e.CreateTicket(100, "procurement", TicketPayment, nil, deadline)

	// 已经过 3h，直接进入 sms 通道（Escalate 按 over>=2h 直接置档位 2）
	_ = w.Tick(context.Background(), time.Now())
	_ = w.Tick(context.Background(), time.Now())

	calls := sink.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 sms escalation (already past 2h), got %+v", calls)
	}
	if calls[0].channel != "sms" {
		t.Fatalf("must be sms, got %+v", calls[0])
	}
}

func TestB4_NoEscalationIfOnTime(t *testing.T) {
	e := New(NewMemoryStore())
	sink := &fakeSink{}
	w := NewWorker(e, sink, time.Minute)

	// deadline 在未来
	_, _ = e.CreateTicket(100, "procurement", TicketPayment, nil, time.Now().Add(time.Hour))
	_ = w.Tick(context.Background(), time.Now())
	if len(sink.snapshot()) != 0 {
		t.Fatalf("on-time ticket must not escalate: %+v", sink.snapshot())
	}
}

func TestB4_CompleteIdempotent(t *testing.T) {
	e := New(NewMemoryStore())
	tk, _ := e.CreateTicket(100, "procurement", TicketPayment, nil, time.Now().Add(time.Hour))
	_, _ = e.Approve(tk.ID, "user", "ok")
	_, _ = e.BeginExecuting(tk.ID, "agent")
	if err := CompleteIdempotent(e, tk.ID, "agent"); err != nil {
		t.Fatal(err)
	}
	// 第二次重复调用不报错
	if err := CompleteIdempotent(e, tk.ID, "agent"); err != nil {
		t.Fatalf("idempotent must not error: %v", err)
	}
	tk2, _ := e.Get(tk.ID)
	if tk2.State != StateCompleted {
		t.Fatalf("want completed, got %s", tk2.State)
	}
}

func TestB4_FailIdempotentOnRejected(t *testing.T) {
	e := New(NewMemoryStore())
	tk, _ := e.CreateTicket(100, "procurement", TicketPayment, nil, time.Now().Add(time.Hour))
	_, _ = e.Reject(tk.ID, "user", "太贵")
	// 已 rejected，FailIdempotent 应该吞掉不报错
	if err := FailIdempotent(e, tk.ID, "agent", "never ran"); err != nil {
		t.Fatalf("already rejected must be idempotent, got %v", err)
	}
}
