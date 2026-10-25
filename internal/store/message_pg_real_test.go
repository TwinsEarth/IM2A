package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestPGMessageIdempotentConflict 真实落库：同一 message_id 第二次 Save 必须
// 走 ON CONFLICT DO NOTHING（不报错、不覆盖、行数不变）。补齐 v0.5.2 唯一缺口。
func TestPGMessageIdempotentConflict(t *testing.T) {
	db := v10OpenStore(t)
	ms := NewPGMessage(db)
	ctx := context.Background()
	uid := uint64(910000000 + time.Now().UnixNano()%99000000)
	id := fmt.Sprintf("v052idem_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM messages WHERE sender_id=$1 OR recipient_id=$1", uid); err != nil {
			t.Errorf("cleanup messages: %v", err)
		}
	})

	m1 := &Message{
		MessageID:  id,
		SenderID:   uid,
		ReceiverID: uid + 1,
		BizType:    "text",
		Content:    "first",
		Seq:        1,
		CreatedAt:  time.Now().Unix(),
	}
	if err := ms.SaveMessage(ctx, m1); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// 第二次同 message_id，但内容不同：必须被吞掉（DO NOTHING），不报错不覆盖。
	m2 := &Message{
		MessageID:  id,
		SenderID:   uid,
		ReceiverID: uid + 1,
		BizType:    "approval_card",
		Content:    "second-should-not-land",
		Seq:        2,
		CreatedAt:  time.Now().Unix(),
	}
	if err := ms.SaveMessage(ctx, m2); err != nil {
		t.Fatalf("conflicting save must not error (ON CONFLICT DO NOTHING): %v", err)
	}

	// 行数必须仍为 1。
	var n int
	if err := db.QueryRow("SELECT count(*) FROM messages WHERE message_id=$1", id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("row count want 1 (conflict must be dropped), got %d", n)
	}

	// 落库内容必须是第一次的，不能被第二次覆盖。
	got, err := ms.LoadMessage(ctx, id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Content != "first" || got.BizType != "text" || got.Seq != 1 {
		t.Fatalf("conflicting save overwrote the row: %+v", got)
	}

	// LoadMessage 未命中必须返回 (nil, error)，供上层映射 404。
	if _, err := ms.LoadMessage(ctx, id+"-nope"); err == nil {
		t.Fatal("load missing id must return error")
	}
}
