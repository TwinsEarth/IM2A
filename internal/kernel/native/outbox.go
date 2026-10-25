// SPDX-License-Identifier: MIT
//
// C3 outbox worker（task-25）：
//   - Send 事务内只写 n_messages + n_outbox；**提交后** worker 才投递（禁止双写/幽灵消息）；
//   - 投递走 `connapi.Bridge.Publish`（经 MessagePublisher 抽象），即复用 B2-b 的
//     im_delivery_log/位点/补发，不另造投递游标；
//   - 失败指数退避（1s→2s→…≤60s），超过 maxOutboxAttempts 标 failed（死信计数），
//     消息行永远保留（不丢）；重复投递由客户端按 message_id 去重（B2 已允许重复帧）。
package native

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// maxOutboxAttempts 单条 outbox 最大投递尝试次数（超过 → status=failed 死信）。
const maxOutboxAttempts = 5

// MessagePublisher 把"一条已提交消息"投递给某个 recipient。
// bootstrap 适配 connapi.Bridge.Publish（会写 im_delivery_log 并投递）。
type MessagePublisher interface {
	PublishMessage(ctx context.Context, recipientUserID int64, payload []byte, critical bool) error
}

// messageEnvelope 是投递帧 payload（自包含：客户端按 message_id 去重、biz_seq 排序）。
type messageEnvelope struct {
	Type           string `json:"type"` // "n_message"
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	BizSeq         int64  `json:"biz_seq"`
	SenderUserID   int64  `json:"sender_user_id"`
	Kind           string `json:"kind"`
	Body           string `json:"body"`
	BodySize       int    `json:"body_size"`
	CreatedAt      int64  `json:"created_at"`
}

// StartOutbox 启动 worker（publisher==nil 时不启动：消息仍落库，outbox 保持 pending）。
// ctx 取消或 StopOutbox 时退出。
func (s *MessageService) StartOutbox(ctx context.Context) {
	if s.publisher == nil {
		s.log.Printf("[native/outbox] WARN: 无投递链路（embedded/无 bridge），outbox worker 未启动；消息只落库")
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-s.wake:
				s.drainOutbox(ctx)
			case <-ticker.C:
				s.drainOutbox(ctx)
			}
		}
	}()
}

// StopOutbox 停止 worker（幂等）。
func (s *MessageService) StopOutbox() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

// drainOutbox 反复取批直到没有到期 pending。
func (s *MessageService) drainOutbox(ctx context.Context) {
	for {
		n := s.processOutboxBatch(ctx)
		if n == 0 {
			return
		}
	}
}

// processOutboxBatch 处理一批（≤20）到期 outbox 行；返回本批处理条数（0=无到期）。
func (s *MessageService) processOutboxBatch(ctx context.Context) int {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.log.Printf("[native/outbox] WARN: begin tx: %v", err)
		return 0
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT id, message_id, conversation_id, attempts
		   FROM n_outbox
		  WHERE status='pending' AND next_try_at <= now()
		  ORDER BY id
		  LIMIT 20
		    FOR UPDATE SKIP LOCKED`)
	if err != nil {
		s.log.Printf("[native/outbox] WARN: select due: %v", err)
		return 0
	}
	type due struct {
		id             int64
		messageID      string
		conversationID string
		attempts       int
	}
	var batch []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.messageID, &d.conversationID, &d.attempts); err != nil {
			rows.Close()
			s.log.Printf("[native/outbox] WARN: scan due: %v", err)
			return 0
		}
		batch = append(batch, d)
	}
	rows.Close()
	if len(batch) == 0 {
		return 0
	}

	for _, d := range batch {
		if ctx.Err() != nil {
			return 0
		}
		s.processOne(ctx, tx, d.id, d.messageID, d.conversationID, d.attempts)
		s.processed.Add(1)
	}
	if err := tx.Commit(); err != nil {
		s.log.Printf("[native/outbox] WARN: commit batch: %v", err)
		return 0
	}
	return len(batch)
}

// processOne 投递单条消息给会话全部 active 成员（在 outbox 事务内；失败则退避重试）。
func (s *MessageService) processOne(ctx context.Context, tx *sql.Tx, outboxID int64, msgID, convID string, attempts int) {
	var senderPID int64
	var bizSeq int64
	var kind string
	var body []byte
	var bodySize int
	var createdAt time.Time
	err := tx.QueryRowContext(ctx,
		`SELECT sender_principal, biz_seq, kind, body, body_size, created_at
		   FROM n_messages WHERE id=$1`, msgID).
		Scan(&senderPID, &bizSeq, &kind, &body, &bodySize, &createdAt)
	if err == sql.ErrNoRows {
		// 消息行不存在（理论上 FK 保证不会）：直接死信，避免阻塞队列。
		_, _ = tx.ExecContext(ctx,
			`UPDATE n_outbox SET status='failed', last_error='message missing', updated_at=now() WHERE id=$1`, outboxID)
		s.dead.Add(1)
		return
	}
	if err != nil {
		s.retryOutbox(ctx, tx, outboxID, attempts, fmt.Errorf("load message: %w", err))
		return
	}
	var senderUID sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT user_id FROM n_principal WHERE id=$1`, senderPID).Scan(&senderUID)

	envelope := messageEnvelope{
		Type: "n_message", ConversationID: convID, MessageID: msgID, BizSeq: bizSeq,
		SenderUserID: senderUID.Int64, Kind: kind, Body: string(body), BodySize: bodySize,
		CreatedAt: createdAt.Unix(),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		s.retryOutbox(ctx, tx, outboxID, attempts, fmt.Errorf("marshal envelope: %w", err))
		return
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT p.user_id
		   FROM n_conversation_members m
		   JOIN n_principal p ON p.id = m.principal_id
		  WHERE m.conversation_id=$1 AND m.left_at IS NULL AND p.user_id IS NOT NULL`, convID)
	if err != nil {
		s.retryOutbox(ctx, tx, outboxID, attempts, fmt.Errorf("load members: %w", err))
		return
	}
	var recipients []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			rows.Close()
			s.retryOutbox(ctx, tx, outboxID, attempts, fmt.Errorf("scan member: %w", err))
			return
		}
		recipients = append(recipients, uid)
	}
	rows.Close()
	if len(recipients) == 0 {
		// 会话无 active 人类成员（只剩 agent）：标记 delivered（无 recipient 可投），不重试。
		_, _ = tx.ExecContext(ctx,
			`UPDATE n_outbox SET status='delivered', last_error='no human recipients', updated_at=now() WHERE id=$1`, outboxID)
		s.delivered.Add(1)
		return
	}
	for _, uid := range recipients {
		if err := s.publisher.PublishMessage(ctx, uid, payload, true); err != nil {
			s.retryOutbox(ctx, tx, outboxID, attempts, fmt.Errorf("publish recipient=%d: %w", uid, err))
			return
		}
	}
	_, _ = tx.ExecContext(ctx,
		`UPDATE n_outbox SET status='delivered', last_error=NULL, updated_at=now() WHERE id=$1`, outboxID)
	s.delivered.Add(1)
}

// retryOutbox 记录失败并指数退避；超过上限标 failed（死信）。
func (s *MessageService) retryOutbox(ctx context.Context, tx *sql.Tx, outboxID int64, attempts int, cause error) {
	next := attempts + 1
	if next >= maxOutboxAttempts {
		_, _ = tx.ExecContext(ctx,
			`UPDATE n_outbox SET status='failed', attempts=$2, last_error=$3, updated_at=now() WHERE id=$1`,
			outboxID, next, cause.Error())
		s.dead.Add(1)
		s.log.Printf("[native/outbox] DEAD message outbox=%d attempts=%d: %v", outboxID, next, cause)
		return
	}
	backoff := time.Duration(1<<uint(minInt(next, 6))) * time.Second
	if backoff > time.Minute {
		backoff = time.Minute
	}
	_, _ = tx.ExecContext(ctx,
		`UPDATE n_outbox SET attempts=$2, next_try_at=now() + make_interval(secs => $3),
		        last_error=$4, updated_at=now()
		  WHERE id=$1`, outboxID, next, backoff.Seconds(), cause.Error())
	s.retries.Add(1)
	s.log.Printf("[native/outbox] retry outbox=%d attempt=%d backoff=%s: %v", outboxID, next, backoff, cause)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// OutboxStats 返回 outbox 状态计数（DB 查询）+ worker 指标（原子计数）。
type OutboxStats struct {
	Pending   int64  `json:"pending"`
	Delivered int64  `json:"delivered"`
	Failed    int64  `json:"failed"`
	Processed uint64 `json:"processed"`
	Retries   uint64 `json:"retries"`
	Dead      uint64 `json:"dead"`
}

// OutboxStats 查询 DB 计数 + 返回 worker 指标（可观测；/v1/native/* 或 metrics 后续暴露）。
func (s *MessageService) OutboxStats(ctx context.Context) (OutboxStats, error) {
	var st OutboxStats
	rows, err := s.db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM n_outbox GROUP BY status`)
	if err != nil {
		return st, fmt.Errorf("native: outbox stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return st, err
		}
		switch status {
		case "pending":
			st.Pending = n
		case "delivered":
			st.Delivered = n
		case "failed":
			st.Failed = n
		}
	}
	st.Processed = s.processed.Load()
	st.Retries = s.retries.Load()
	st.Dead = s.dead.Load()
	return st, rows.Err()
}
