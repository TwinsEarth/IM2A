package approval

import (
	"errors"
	"testing"
	"time"
)

func newEngine() *Engine { return New(NewMemoryStore()) }

// TestApproveHappyPath 一键通过链路 PENDING→APPROVED→EXECUTING→COMPLETED。
func TestApproveHappyPath(t *testing.T) {
	e := newEngine()
	now := time.Now()
	tk, err := e.CreateTicket(1, "procurement-agent", TicketPayment,
		map[string]any{"amount_cents": 300000}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != StatePending {
		t.Fatalf("new state=%s want PENDING", tk.State)
	}
	if _, err := e.Approve(tk.ID, "user", "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := e.BeginExecuting(tk.ID, "procurement-agent"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	done, err := e.Complete(tk.ID, "procurement-agent")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.State != StateCompleted {
		t.Fatalf("state=%s want COMPLETED", done.State)
	}
	// 终态后应能在事件轨迹中看到完整链
	if len(done.Events) < 4 {
		t.Fatalf("events=%d want >=4", len(done.Events))
	}
}

// TestInvalidTransition 完成态不允许再审批。
func TestInvalidTransition(t *testing.T) {
	e := newEngine()
	tk, _ := e.CreateTicket(1, "a", TicketPayment, nil, time.Now().Add(time.Hour))
	_, _ = e.Approve(tk.ID, "user", "ok")
	_, _ = e.BeginExecuting(tk.ID, "a")
	_, _ = e.Complete(tk.ID, "a")
	if _, err := e.Approve(tk.ID, "user", "again"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("approve completed err=%v want ErrInvalidTransition", err)
	}
}

// TestModifyThenExecute 修改后执行。
func TestModifyThenExecute(t *testing.T) {
	e := newEngine()
	tk, _ := e.CreateTicket(1, "a", TicketPayment, map[string]any{"provider": "A"}, time.Now().Add(time.Hour))
	if _, err := e.Modify(tk.ID, map[string]any{"provider": "B"}, "user", "换 B"); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if got, _ := e.Get(tk.ID); got.State != StateModified {
		t.Fatalf("state=%s want MODIFIED", got.State)
	}
	if _, err := e.BeginExecuting(tk.ID, "a"); err != nil {
		t.Fatalf("begin after modify: %v", err)
	}
}

// TestReject 拒绝后终态。
func TestReject(t *testing.T) {
	e := newEngine()
	tk, _ := e.CreateTicket(1, "a", TicketNegotiation, nil, time.Now().Add(time.Hour))
	if _, err := e.Reject(tk.ID, "user", "不接受"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	pending, _ := e.ListPending(1)
	if len(pending) != 0 {
		t.Fatalf("pending after reject = %d want 0", len(pending))
	}
}

// TestTimeoutEscalation 超时升级：30 分钟手表、2 小时短信。
func TestTimeoutEscalation(t *testing.T) {
	e := newEngine()
	now := time.Now()
	watch, _ := e.CreateTicket(1, "a", TicketPayment, nil, now.Add(-35*time.Minute))
	sms, _ := e.CreateTicket(1, "a", TicketPayment, nil, now.Add(-3*time.Hour))
	fresh, _ := e.CreateTicket(1, "a", TicketPayment, nil, now.Add(time.Hour))

	escalated, err := e.CheckTimeouts(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(escalated) != 2 {
		t.Fatalf("escalated=%d want 2", len(escalated))
	}
	w, _ := e.Get(watch.ID)
	if w.Escalation != 1 {
		t.Fatalf("watch ticket escalation=%d want 1", w.Escalation)
	}
	s, _ := e.Get(sms.ID)
	if s.Escalation != 2 {
		t.Fatalf("sms ticket escalation=%d want 2", s.Escalation)
	}
	f, _ := e.Get(fresh.ID)
	if f.Escalation != 0 {
		t.Fatalf("fresh ticket escalation=%d want 0", f.Escalation)
	}
}

func TestNotFound(t *testing.T) {
	e := newEngine()
	if _, err := e.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing err=%v want ErrNotFound", err)
	}
}
