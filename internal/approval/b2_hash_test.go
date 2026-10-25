package approval

import (
	"errors"
	"testing"
	"time"
)

func TestB2_ActionHashBinding(t *testing.T) {
	e := New(NewMemoryStore())
	deadline := time.Now().Add(30 * time.Minute)

	// 创建时声明 action_hash
	tk, err := e.CreateTicket(100, "procurement", TicketPayment,
		map[string]any{"amount": 1200, "__action_hash__": "sha256:abc"}, deadline)
	if err != nil {
		t.Fatal(err)
	}
	if tk.ActionHash != "sha256:abc" {
		t.Fatalf("ActionHash not picked up: %+v", tk)
	}

	// 批准
	if _, err := e.Approve(tk.ID, "user", "同意"); err != nil {
		t.Fatal(err)
	}

	// 错误 hash 被拒
	_, err = e.BeginExecutingWithHash(tk.ID, "agent", "sha256:WRONG")
	if !errors.Is(err, ErrActionHashMismatch) {
		t.Fatalf("want hash mismatch, got %v", err)
	}

	// 正确 hash 进入 executing
	tk2, err := e.BeginExecutingWithHash(tk.ID, "agent", "sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if tk2.State != StateExecuting {
		t.Fatalf("want executing, got %s", tk2.State)
	}

	// 完成
	if _, err := e.Complete(tk2.ID, "agent"); err != nil {
		t.Fatal(err)
	}
}

func TestB2_LegacyTicketNoHashPasses(t *testing.T) {
	e := New(NewMemoryStore())
	tk, _ := e.CreateTicket(100, "procurement", TicketPayment, map[string]any{"amount": 1}, time.Now().Add(time.Hour))
	_, _ = e.Approve(tk.ID, "user", "ok")
	// 老票据没有 action_hash，传入空字符串也放行（向后兼容）
	if _, err := e.BeginExecutingWithHash(tk.ID, "agent", ""); err != nil {
		t.Fatalf("legacy ticket should pass: %v", err)
	}
}

func TestB2_StillNoBypass(t *testing.T) {
	e := New(NewMemoryStore())
	tk, _ := e.CreateTicket(100, "procurement", TicketPayment, map[string]any{"__action_hash__": "h"}, time.Now().Add(time.Hour))
	// pending 直接 executing 仍被状态机拒绝（不能绕开审批）
	if _, err := e.BeginExecutingWithHash(tk.ID, "agent", "h"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pending->executing must be rejected, got %v", err)
	}
}
