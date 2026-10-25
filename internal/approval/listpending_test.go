package approval

import (
	"testing"
	"time"
)

// v2.3.22：MemoryStore.byUser 历史上只建 key 不追加 id，导致 ListPending 永远返回空。
// 本测试锁死"按用户列出待审、排除终态、用户隔离"的真实行为。
func TestListPending_RealIndex(t *testing.T) {
	e := New(NewMemoryStore())
	dl := time.Now().Add(time.Hour)

	p1, _ := e.CreateTicket(1, "a", TicketPayment, nil, dl)
	p2, _ := e.CreateTicket(1, "a", TicketSkillCall, nil, dl)
	other, _ := e.CreateTicket(2, "b", TicketPayment, nil, dl)
	// p1 被批准（非终态链路上仍可能待执行，但 Approve 后不是 pending 列表关注的"待审"；
	// 这里直接把一张驳回为终态，验证终态被排除）。
	rej, _ := e.CreateTicket(1, "a", TicketNegotiation, nil, dl)
	if _, err := e.Reject(rej.ID, "user", "no"); err != nil {
		t.Fatal(err)
	}

	got, err := e.ListPending(1)
	if err != nil {
		t.Fatal(err)
	}
	// 用户 1 有 3 张票（p1,p2,rej），其中 rej 终态排除 → 期望 p1,p2 两张
	ids := map[string]bool{}
	for _, tk := range got {
		ids[tk.ID] = true
	}
	if !ids[p1.ID] || !ids[p2.ID] {
		t.Fatalf("pending must contain %s,%s, got %v", p1.ID, p2.ID, ids)
	}
	if ids[rej.ID] {
		t.Fatalf("rejected ticket must not be pending")
	}
	if len(got) != 2 {
		t.Fatalf("user1 pending = %d want 2", len(got))
	}

	// 用户隔离：用户 2 只看到自己的票
	got2, _ := e.ListPending(2)
	if len(got2) != 1 || got2[0].ID != other.ID {
		t.Fatalf("user2 pending isolation broken: %+v", got2)
	}
}
