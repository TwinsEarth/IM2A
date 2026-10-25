// SPDX-License-Identifier: MIT
//
// PG-gated C3 判定性测试（真 PG13.23）：
//  1. 幂等：同 client_msg_id 两次 → 1 行消息 + 1 行 outbox（不重复投递）；
//  2. bizSeq 连续：并发 10 writer × 10 条 → biz_seq = 1..100 无空洞无重复（抓 TOCTOU）；
//  3. 32KiB 边界：恰好通过、+1 字节 → MESSAGE_TOO_LARGE(413)；
//  4. 非成员/归档：404 fail-closed / 410；
//  5. 读位点：GREATEST 单调不倒退；
//  6. outbox 崩溃恢复：Publish 先失败 → 消息已落库 → 恢复后补投成功（不丢、消息不重复）。
package native

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// createTestConversation 建会话（owner + members），返回 convID 与 userIDs。
func createTestConversation(t *testing.T, convs *ConversationStore, kind ConvKind, nMembers int) (string, []int64) {
	t.Helper()
	users := uniqueUsers(1 + nMembers)
	v, _, err := convs.Create(context.Background(), CreateRequest{
		OwnerUserID: users[0], Kind: kind, MemberUserIDs: users[1:],
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	return v.ID, users
}

// cleanupC3 删除测试会话及其消息/outbox/principal。
func cleanupC3(t *testing.T, db *sql.DB, convID string, users []int64) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM n_outbox WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_messages WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_read_cursor WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_conversation_members WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_conversations WHERE id=$1`, convID)
		for _, uid := range users {
			_, _ = db.Exec(`DELETE FROM n_principal WHERE kind='human' AND user_id=$1`, uid)
		}
	})
}

func TestC3SendIdempotent(t *testing.T) {
	db := openConvPG(t)
	convs := NewConversationStore(db)
	msgs := NewMessageService(db, nil, nil)
	convID, users := createTestConversation(t, convs, ConvDirect, 1)
	cleanupC3(t, db, convID, users)
	ctx := context.Background()

	first, dedup, err := msgs.Send(ctx, SendRequest{
		ConversationID: convID, SenderUserID: users[0],
		ClientMsgID: "client-1", Body: []byte("hello"),
	})
	if err != nil || dedup {
		t.Fatalf("first send: dedup=%v err=%v", dedup, err)
	}
	second, dedup2, err := msgs.Send(ctx, SendRequest{
		ConversationID: convID, SenderUserID: users[0],
		ClientMsgID: "client-1", Body: []byte("hello"),
	})
	if err != nil || !dedup2 {
		t.Fatalf("second send: dedup=%v err=%v", dedup2, err)
	}
	if second.ID != first.ID || second.BizSeq != first.BizSeq {
		t.Fatalf("幂等返回不同消息: %+v vs %+v", first, second)
	}
	// created_at 必须来自 DB 同一行（C5b 回滚"同一行"断言的判定点；Send 不得用 time.Now() 伪造）。
	var dbCreated time.Time
	if err := db.QueryRow(`SELECT created_at FROM n_messages WHERE id=$1`, first.ID).Scan(&dbCreated); err != nil {
		t.Fatalf("query created_at: %v", err)
	}
	if !first.CreatedAt.Equal(dbCreated) {
		t.Fatalf("Send 返回 created_at=%v, DB=%v（必须取 DB 值，否则重开/回读不一致）", first.CreatedAt, dbCreated)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("幂等第二次 created_at=%v != 第一次 %v", second.CreatedAt, first.CreatedAt)
	}
	var msgRows, outboxRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_messages WHERE conversation_id=$1`, convID).Scan(&msgRows); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_outbox WHERE conversation_id=$1`, convID).Scan(&outboxRows); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if msgRows != 1 || outboxRows != 1 {
		t.Fatalf("幂等后 messages=%d outbox=%d, want 1/1", msgRows, outboxRows)
	}
	t.Logf("C3_IDEMPOTENT_OK: messages=1 outbox=1，第二次 dedup=true 同 id=%s", first.ID)
}

func TestC3BizSeqConcurrent(t *testing.T) {
	db := openConvPG(t)
	convs := NewConversationStore(db)
	msgs := NewMessageService(db, nil, nil)
	convID, users := createTestConversation(t, convs, ConvGroup, 0)
	cleanupC3(t, db, convID, users)
	ctx := context.Background()

	const writers, perWriter = 10, 10
	var wg sync.WaitGroup
	var failed atomic.Int64
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, _, err := msgs.Send(ctx, SendRequest{
					ConversationID: convID, SenderUserID: users[0],
					ClientMsgID: fmt.Sprintf("w%d-%d", w, i),
					Body:        []byte(fmt.Sprintf("m-%d-%d", w, i)),
				}); err != nil {
					failed.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d 次发送失败", failed.Load())
	}
	rows, err := db.Query(`SELECT biz_seq FROM n_messages WHERE conversation_id=$1 ORDER BY biz_seq`, convID)
	if err != nil {
		t.Fatalf("query biz_seq: %v", err)
	}
	defer rows.Close()
	var seqs []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan biz_seq: %v", err)
		}
		seqs = append(seqs, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(seqs) != writers*perWriter {
		t.Fatalf("biz_seq 条数=%d, want %d", len(seqs), writers*perWriter)
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("biz_seq 不连续/有重复：索引 %d = %d（前 10 个=%v）", i, s, seqs[:minInt(10, len(seqs))])
		}
	}
	t.Logf("C3_BIZSEQ_OK: %d 条 biz_seq = 1..%d 无空洞无重复（并发 %d writer）", len(seqs), len(seqs), writers)
}

func TestC3SizeLimit(t *testing.T) {
	db := openConvPG(t)
	convs := NewConversationStore(db)
	msgs := NewMessageService(db, nil, nil)
	convID, users := createTestConversation(t, convs, ConvGroup, 0)
	cleanupC3(t, db, convID, users)
	ctx := context.Background()

	// 恰好 32KiB：通过。
	exact := make([]byte, MaxMessageBody)
	for i := range exact {
		exact[i] = 'a'
	}
	if _, _, err := msgs.Send(ctx, SendRequest{
		ConversationID: convID, SenderUserID: users[0], Body: exact, ClientMsgID: "size-ok",
	}); err != nil {
		t.Fatalf("恰好 32KiB 应通过: %v", err)
	}
	// +1 字节 → 413 MESSAGE_TOO_LARGE，且不落库。
	over := make([]byte, MaxMessageBody+1)
	_, _, err := msgs.Send(ctx, SendRequest{
		ConversationID: convID, SenderUserID: users[0], Body: over, ClientMsgID: "size-over",
	})
	if CodeOf(err) != CodeMessageTooLarge || StatusForCode(CodeMessageTooLarge) != 413 {
		t.Fatalf("超限错误 code=%q status=%d, want %q/413 (err=%v)",
			CodeOf(err), StatusForCode(CodeOf(err)), CodeMessageTooLarge, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_messages WHERE conversation_id=$1`, convID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("超限消息落库了（count=%d, want 1=仅 32KiB 那条）", n)
	}
	t.Logf("C3_SIZE_OK: 32768B 通过、32769B → 413 MESSAGE_TOO_LARGE 且不落库")
}

func TestC3NonMemberAndArchived(t *testing.T) {
	db := openConvPG(t)
	convs := NewConversationStore(db)
	msgs := NewMessageService(db, nil, nil)
	convID, users := createTestConversation(t, convs, ConvDirect, 1)
	cleanupC3(t, db, convID, users)
	ctx := context.Background()
	stranger := users[1] + 1000
	// stranger 无 principal：发送/列表都应 404 fail-closed（不泄露会话存在性）。
	if _, _, err := msgs.Send(ctx, SendRequest{ConversationID: convID, SenderUserID: stranger, Body: []byte("x")}); CodeOf(err) != CodeNotFound {
		t.Fatalf("非成员发送 code=%q, want %q", CodeOf(err), CodeNotFound)
	}
	if _, err := msgs.List(ctx, convID, stranger, 0, 10); CodeOf(err) != CodeNotFound {
		t.Fatalf("非成员列表 code=%q, want %q", CodeOf(err), CodeNotFound)
	}
	// 归档后发消息 → 410 CONVERSATION_ARCHIVED。
	if _, err := convs.Archive(ctx, convID, users[0]); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, _, err := msgs.Send(ctx, SendRequest{ConversationID: convID, SenderUserID: users[0], Body: []byte("x")}); CodeOf(err) != CodeArchived {
		t.Fatalf("归档后发送 code=%q, want %q", CodeOf(err), CodeArchived)
	}
	t.Logf("C3_VISIBILITY_OK: 非成员 404 fail-closed；归档后发送 410")
}

func TestC3ReadCursorMonotonic(t *testing.T) {
	db := openConvPG(t)
	convs := NewConversationStore(db)
	msgs := NewMessageService(db, nil, nil)
	convID, users := createTestConversation(t, convs, ConvDirect, 1)
	cleanupC3(t, db, convID, users)
	ctx := context.Background()
	owner, peer := users[0], users[1]
	for i := 1; i <= 3; i++ {
		if _, _, err := msgs.Send(ctx, SendRequest{ConversationID: convID, SenderUserID: owner, Body: []byte(fmt.Sprintf("m%d", i))}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	// 乱序上报：3 然后 1 → GREATEST 保持 3。
	cur, err := msgs.MarkRead(ctx, convID, peer, 3)
	if err != nil {
		t.Fatalf("mark read 3: %v", err)
	}
	cur, err = msgs.MarkRead(ctx, convID, peer, 1)
	if err != nil {
		t.Fatalf("mark read 1: %v", err)
	}
	if cur.ReadBizSeq != 3 {
		t.Fatalf("乱序 ack 后 read_biz_seq=%d, want 3（GREATEST 单调）", cur.ReadBizSeq)
	}
	// 未读数 = next(3) - read(3) = 0。
	if cur.Unread != 0 {
		t.Fatalf("unread=%d, want 0", cur.Unread)
	}
	// 非成员推进读位点 → 404。
	if _, err := msgs.MarkRead(ctx, convID, users[1]+2000, 1); CodeOf(err) != CodeNotFound {
		t.Fatalf("非成员 mark read code=%q", CodeOf(err))
	}
	t.Logf("C3_READ_CURSOR_OK: 3 后 1 → 仍 3（GREATEST 单调），unread=0")
}

// fakePublisher 注入失败 N 次后成功，并记录每次投递。
type fakePublisher struct {
	mu        sync.Mutex
	failFirst int
	calls     int
	payloads  []string
	users     []int64
}

func (f *fakePublisher) PublishMessage(_ context.Context, recipientUserID int64, payload []byte, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failFirst > 0 {
		f.failFirst--
		return errors.New("injected publish failure")
	}
	f.payloads = append(f.payloads, string(payload))
	f.users = append(f.users, recipientUserID)
	return nil
}

func TestC3OutboxCrashRecovery(t *testing.T) {
	db := openConvPG(t)
	convs := NewConversationStore(db)
	pub := &fakePublisher{failFirst: 1}
	msgs := NewMessageService(db, pub, nil)
	msgs.interval = 20 * time.Millisecond
	convID, users := createTestConversation(t, convs, ConvDirect, 1)
	cleanupC3(t, db, convID, users)
	// 测试隔离：outbox worker 是**全局批量取**（生产语义），历史残留的 pending 行会混进本用例的
	// 投递计数/重试计数。先把空队列清成 failed（只影响测试库里的陈旧行），再启动 worker。
	if _, err := db.Exec(`UPDATE n_outbox SET status='failed', last_error='test-isolation'
	                       WHERE status='pending'`); err != nil {
		t.Fatalf("isolate outbox: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgs.StartOutbox(ctx)
	defer msgs.StopOutbox()

	// 发送后 worker 第一次投递会失败：消息必须已落库（事务先提交），outbox 重试。
	msg, _, err := msgs.Send(ctx, SendRequest{
		ConversationID: convID, SenderUserID: users[0], ClientMsgID: "crash-1", Body: []byte("recover me"),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	var msgRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_messages WHERE id=$1`, msg.ID).Scan(&msgRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if msgRows != 1 {
		t.Fatalf("投递失败时消息必须已落库，count=%d", msgRows)
	}
	// 等待恢复后补投成功（不丢）：按 message_id 精确断言（OutboxStats 是全局计数，测试不用它判本消息）。
	deadline := time.Now().Add(5 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT status FROM n_outbox WHERE message_id=$1`, msg.ID).Scan(&status); err == nil && status == "delivered" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != "delivered" {
		t.Fatalf("outbox 未在 5s 内 delivered（status=%q），消息可能丢", status)
	}
	st, err := msgs.OutboxStats(ctx)
	if err != nil {
		t.Fatalf("outbox stats: %v", err)
	}
	if st.Retries == 0 {
		t.Fatal("应至少发生一次重试（注入失败）")
	}
	// outbox 行唯一（幂等）：该消息只有 1 行。
	var outboxRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_outbox WHERE message_id=$1`, msg.ID).Scan(&outboxRows); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxRows != 1 {
		t.Fatalf("outbox 行数=%d, want 1（不重复）", outboxRows)
	}
	// 消息只有一行（不重复）；每次成功投递恰好一次/recipient（2 个成员 → 2 次）。
	// 只统计**本消息**的投递（worker 全局队列，防历史残留行干扰）。
	own := 0
	for _, p := range pub.payloads {
		if strings.Contains(p, `"message_id":"`+msg.ID+`"`) {
			own++
		}
	}
	if own != 2 {
		t.Fatalf("本消息成功投递次数=%d, want 2（owner+peer 各一次；总投递=%d）", own, len(pub.payloads))
	}
	t.Logf("C3_OUTBOX_OK: 注入失败时消息已落库；重试 %d 次后 outbox=delivered，本消息投递 2 recipient，outbox 行数 1", st.Retries)
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
