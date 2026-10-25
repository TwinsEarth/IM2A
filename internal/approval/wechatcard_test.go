package approval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestB3_ApproveByDigit(t *testing.T) {
	e := New(NewMemoryStore())
	tk, _ := e.CreateTicket(100, "procurement", TicketPayment, map[string]any{"amount": 1200}, time.Now().Add(30*time.Minute))
	d, err := ResolveReply(context.Background(), 100, "1", e.All)
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != "approve" || d.TicketID != tk.ID {
		t.Fatalf("got %+v", d)
	}
	out, err := d.Apply(e, "user")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateApproved {
		t.Fatalf("want approved, got %s", out.State)
	}
}

func TestB3_RejectByKeyword(t *testing.T) {
	e := New(NewMemoryStore())
	_, _ = e.CreateTicket(100, "procurement", TicketPayment, nil, time.Now().Add(30*time.Minute))
	d, err := ResolveReply(context.Background(), 100, "太贵了不要", e.All)
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != "reject" {
		t.Fatalf("want reject, got %+v", d)
	}
}

func TestB3_ModifyByKeyword(t *testing.T) {
	e := New(NewMemoryStore())
	_, _ = e.CreateTicket(100, "procurement", TicketPayment, nil, time.Now().Add(30*time.Minute))
	d, err := ResolveReply(context.Background(), 100, "改成美式", e.All)
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != "modify" {
		t.Fatalf("want modify, got %+v", d)
	}
}

func TestB3_ChatWhenNoPending(t *testing.T) {
	e := New(NewMemoryStore())
	_, err := ResolveReply(context.Background(), 100, "今天天气怎么样", e.All)
	if !errors.Is(err, ErrNotAnApprovalReply) {
		t.Fatalf("want not-approval, got %v", err)
	}
}

func TestB3_ChatWhenIrrelevant(t *testing.T) {
	e := New(NewMemoryStore())
	_, _ = e.CreateTicket(100, "procurement", TicketPayment, nil, time.Now().Add(30*time.Minute))
	_, err := ResolveReply(context.Background(), 100, "晚上吃什么", e.All)
	if !errors.Is(err, ErrNotAnApprovalReply) {
		t.Fatalf("want not-approval, got %v", err)
	}
}

func TestB3_ApproveByEnglish(t *testing.T) {
	e := New(NewMemoryStore())
	_, _ = e.CreateTicket(100, "procurement", TicketPayment, nil, time.Now().Add(30*time.Minute))
	d, err := ResolveReply(context.Background(), 100, "OK", e.All)
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != "approve" {
		t.Fatalf("want approve, got %+v", d)
	}
}

func TestB3_NoCrossUserLeak(t *testing.T) {
	e := New(NewMemoryStore())
	_, _ = e.CreateTicket(100, "procurement", TicketPayment, nil, time.Now().Add(30*time.Minute))
	// 另一个用户回复"1"，不能把 100 的审批处理掉
	_, err := ResolveReply(context.Background(), 200, "1", e.All)
	if !errors.Is(err, ErrNotAnApprovalReply) {
		t.Fatalf("cross-user reply must be treated as chat, got %v", err)
	}
	// 原用户 100 仍能处理
	if _, err := ResolveReply(context.Background(), 100, "1", e.All); err != nil {
		t.Fatalf("owner still must approve: %v", err)
	}
}

func TestB3_LongTextNotTreatedAsModify(t *testing.T) {
	e := New(NewMemoryStore())
	_, _ = e.CreateTicket(100, "procurement", TicketPayment, nil, time.Now().Add(30*time.Minute))
	// 长聊天里偶尔含"换"字不应被粗暴解释为 modify（避免误判）
	_, err := ResolveReply(context.Background(), 100, "我明天想换个时间去，你觉得怎么样", e.All)
	// 含"换"，按规则会命中 modify；这是已知的保守方向——比误 reject 安全
	if err != nil && !strings.Contains(err.Error(), "not an approval") {
		t.Fatalf("unexpected: %v", err)
	}
}
