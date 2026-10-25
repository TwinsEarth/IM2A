// SPDX-License-Identifier: MIT
//
// C3（task-25）：原生消息模型（n_messages）+ 读位点（n_read_cursor）。
//
// ===== 两个"序"必须分清（本文件最重要的一段注释）=====
//
//	bizSeq  = n_messages.biz_seq：**会话内连续业务序号**，由 n_conversations.next_biz_seq
//	          在同一事务内原子 +1 分配；客户端按它排序/去重/算未读。
//	deliverySeq = im_delivery_log.seq：**每 recipient 的投递序号**（B2-b），由
//	          connapi.Bridge.Publish 的发号事务分配；客户端重连 resume 用它补发。
//
// 两者语义、生命周期、归属完全不同：bizSeq 属于会话（C3），deliverySeq 属于投递（B2-b）。
//
// ===== 硬约束 =====
//   - 事务内只写 n_messages + n_outbox；**提交后**才由 outbox worker 投递（outbox.go）；
//   - 幂等：UNIQUE(conversation_id, client_msg_id)，重复提交返回同一行且不重复入 outbox；
//   - 正文 >32KiB → MESSAGE_TOO_LARGE(413)，不截断；
//   - 不另造投递游标：Offline 补发完全交给 B2-b 的 im_device_cursor + im_delivery_log。
package native

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// MaxMessageBody 单条消息正文上限（32KiB；> 上限返回 MESSAGE_TOO_LARGE 413，不截断）。
const MaxMessageBody = 32 * 1024

// 消息域错误码（补进 StatusForCode）。
const (
	CodeMessageTooLarge = "MESSAGE_TOO_LARGE" // 413
)

// Message 是 n_messages 一行。
type Message struct {
	ID              string
	ConversationID  string
	SenderPrincipal int64
	SenderUserID    int64
	BizSeq          int64
	ClientMsgID     string
	Kind            string
	Body            []byte
	BodySize        int
	CreatedAt       time.Time
}

// MessageView 是对外 JSON 形状。
type MessageView struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	SenderUserID   int64     `json:"sender_user_id"`
	BizSeq         int64     `json:"biz_seq"`
	ClientMsgID    string    `json:"client_msg_id,omitempty"`
	Kind           string    `json:"kind"`
	Body           string    `json:"body"`
	BodySize       int       `json:"body_size"`
	CreatedAt      time.Time `json:"created_at"`
}

// SendRequest 发送一条消息。
type SendRequest struct {
	ConversationID string
	SenderUserID   int64
	ClientMsgID    string // 幂等键（可空）
	Kind           string // 缺省 text
	Body           []byte
}

// ReadCursorView 是 n_read_cursor 对外形状。
type ReadCursorView struct {
	ConversationID string `json:"conversation_id"`
	ReadBizSeq     int64  `json:"read_biz_seq"`
	Unread         int64  `json:"unread"`
}

// messageID 生成消息 id（"m_"+16字节随机 hex）。
func messageID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("m_%d", time.Now().UnixNano())
	}
	return "m_" + hex.EncodeToString(b[:])
}

// MessageService 是 C3 领域层。
type MessageService struct {
	db         *sql.DB
	principals *PrincipalStore
	publisher  MessagePublisher // nil = 无投递链路（embedded 无 bridge；只落库/outbox）
	log        *log.Logger

	wake     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	interval time.Duration

	// outbox 指标（原子；OutboxStats 另查 DB 计数）
	processed atomic.Uint64
	delivered atomic.Uint64
	retries   atomic.Uint64
	dead      atomic.Uint64
}

// NewMessageService 构造消息服务（不自动启动 outbox；调用 StartOutbox）。
func NewMessageService(db *sql.DB, publisher MessagePublisher, logger *log.Logger) *MessageService {
	if logger == nil {
		logger = log.Default()
	}
	return &MessageService{
		db:         db,
		principals: NewPrincipalStore(db),
		publisher:  publisher,
		log:        logger,
		wake:       make(chan struct{}, 1),
		stop:       make(chan struct{}),
		interval:   250 * time.Millisecond,
	}
}

// senderPrincipal 返回发送者 principal（只查不建；非成员 → ErrNotFound）。
func (s *MessageService) senderPrincipal(ctx context.Context, q queryer, convID string, userID int64) (int64, *Member, error) {
	pid, ok, err := principalForUser(ctx, q, userID)
	if err != nil {
		return 0, nil, err
	}
	if !ok {
		return 0, nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	m, isMember, err := (&ConversationStore{db: s.db}).activeMember(ctx, q, convID, pid)
	if err != nil {
		return 0, nil, err
	}
	if !isMember {
		return 0, nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	return pid, m, nil
}

// Send 发送消息：事务内分配 bizSeq + 写 n_messages + 写 n_outbox；提交后唤醒 worker。
// 返回 (view, dedup, error)；dedup=true 表示同一 client_msg_id 已存在（未重复投递）。
func (s *MessageService) Send(ctx context.Context, req SendRequest) (*MessageView, bool, error) {
	if req.ConversationID == "" || req.SenderUserID <= 0 {
		return nil, false, newError(CodeInvalidArgument, "conversation_id/sender required")
	}
	if len(req.Body) == 0 {
		return nil, false, newError(CodeInvalidArgument, "body required")
	}
	if len(req.Body) > MaxMessageBody {
		return nil, false, newError(CodeMessageTooLarge, "正文 %d 字节 > 上限 %d（不截断）", len(req.Body), MaxMessageBody)
	}
	kind := req.Kind
	if kind == "" {
		kind = "text"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("native: begin send message: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 锁会话行：bizSeq 分配与成员校验在同一事务内（并发安全、无 TOCTOU）。
	var status string
	var nextSeq int64
	err = tx.QueryRowContext(ctx,
		`SELECT status, next_biz_seq FROM n_conversations WHERE id=$1 FOR UPDATE`,
		req.ConversationID).Scan(&status, &nextSeq)
	if err == sql.ErrNoRows {
		return nil, false, newError(CodeNotFound, "conversation %q not found", req.ConversationID)
	}
	if err != nil {
		return nil, false, fmt.Errorf("native: lock conversation: %w", err)
	}
	if status == string(ConvStatusArchived) {
		return nil, false, newError(CodeArchived, "conversation %q 已归档，不可发消息", req.ConversationID)
	}
	senderPID, _, err := s.senderPrincipal(ctx, tx, req.ConversationID, req.SenderUserID)
	if err != nil {
		return nil, false, err
	}
	// 幂等：同 client_msg_id 已存在 → 返回同一行（不写新行、不写新 outbox）。
	if req.ClientMsgID != "" {
		var existID string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM n_messages WHERE conversation_id=$1 AND client_msg_id=$2`,
			req.ConversationID, req.ClientMsgID).Scan(&existID)
		if err == nil {
			if err := tx.Commit(); err != nil {
				return nil, false, fmt.Errorf("native: commit send(dedup): %w", err)
			}
			v, err := s.getByID(ctx, existID)
			return v, true, err
		}
		if err != sql.ErrNoRows {
			return nil, false, fmt.Errorf("native: dedup lookup: %w", err)
		}
	}
	// bizSeq：会话内连续（行锁保证无空洞/无重复）。
	var bizSeq int64
	if err := tx.QueryRowContext(ctx,
		`UPDATE n_conversations SET next_biz_seq = next_biz_seq + 1, updated_at = now()
		  WHERE id=$1 RETURNING next_biz_seq`, req.ConversationID).Scan(&bizSeq); err != nil {
		return nil, false, fmt.Errorf("native: alloc biz_seq: %w", err)
	}
	id := messageID()
	var clientID any
	if req.ClientMsgID != "" {
		clientID = req.ClientMsgID
	}
	// 返回 DB 的 created_at（不能用 time.Now() 伪造：重开/回读必须与同一行一致，
	// 这是 C5b 回滚演练"同一行"断言的判定点）。
	var createdAt time.Time
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO n_messages
		   (id, conversation_id, sender_principal, biz_seq, client_msg_id, kind, body, body_size)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 RETURNING created_at`,
		id, req.ConversationID, senderPID, bizSeq, clientID, kind, req.Body, len(req.Body)).Scan(&createdAt); err != nil {
		if req.ClientMsgID != "" && isUniqueViolation(err) {
			// 并发同 client id：回滚后回读已存在行（幂等语义）。
			_ = tx.Rollback()
			var existID string
			if e := s.db.QueryRowContext(ctx,
				`SELECT id FROM n_messages WHERE conversation_id=$1 AND client_msg_id=$2`,
				req.ConversationID, req.ClientMsgID).Scan(&existID); e == nil {
				v, verr := s.getByID(ctx, existID)
				return v, true, verr
			}
		}
		return nil, false, fmt.Errorf("native: insert message: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO n_outbox (message_id, conversation_id) VALUES ($1,$2)`, id, req.ConversationID); err != nil {
		return nil, false, fmt.Errorf("native: insert outbox: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("native: commit send message: %w", err)
	}
	// 提交成功后才唤醒投递 worker（事务内绝不投递）。
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return &MessageView{
		ID: id, ConversationID: req.ConversationID, SenderUserID: req.SenderUserID,
		BizSeq: bizSeq, ClientMsgID: req.ClientMsgID, Kind: kind,
		Body: string(req.Body), BodySize: len(req.Body), CreatedAt: createdAt,
	}, false, nil
}

// getByID 读一条消息并映射对外形状。
func (s *MessageService) getByID(ctx context.Context, id string) (*MessageView, error) {
	m := &Message{}
	var client sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, sender_principal, biz_seq, client_msg_id, kind, body, body_size, created_at
		   FROM n_messages WHERE id=$1`, id).
		Scan(&m.ID, &m.ConversationID, &m.SenderPrincipal, &m.BizSeq, &client, &m.Kind, &m.Body, &m.BodySize, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, newError(CodeNotFound, "message %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("native: get message: %w", err)
	}
	m.ClientMsgID = client.String
	if uid, ok, err := s.principals.UserIDOf(ctx, m.SenderPrincipal); err != nil {
		return nil, err
	} else if ok {
		m.SenderUserID = uid
	}
	return &MessageView{
		ID: m.ID, ConversationID: m.ConversationID, SenderUserID: m.SenderUserID,
		BizSeq: m.BizSeq, ClientMsgID: m.ClientMsgID, Kind: m.Kind,
		Body: string(m.Body), BodySize: m.BodySize, CreatedAt: m.CreatedAt,
	}, nil
}

// List 按 bizSeq 游标拉消息（afterBizSeq exclusive；非成员 404 fail-closed）。
func (s *MessageService) List(ctx context.Context, convID string, viewerUserID int64, afterBizSeq int64, limit int) ([]MessageView, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	pid, ok, err := principalForUser(ctx, s.db, viewerUserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	if _, isMember, err := (&ConversationStore{db: s.db}).activeMember(ctx, s.db, convID, pid); err != nil {
		return nil, err
	} else if !isMember {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, m.conversation_id, m.sender_principal, p.user_id, m.biz_seq, m.client_msg_id,
		        m.kind, m.body, m.body_size, m.created_at
		   FROM n_messages m
		   LEFT JOIN n_principal p ON p.id = m.sender_principal
		  WHERE m.conversation_id=$1 AND m.biz_seq > $2
		  ORDER BY m.biz_seq LIMIT $3`, convID, afterBizSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("native: list messages: %w", err)
	}
	defer rows.Close()
	out := make([]MessageView, 0, limit)
	for rows.Next() {
		var m Message
		var client sql.NullString
		var senderUID sql.NullInt64
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.SenderPrincipal, &senderUID, &m.BizSeq,
			&client, &m.Kind, &m.Body, &m.BodySize, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("native: scan message: %w", err)
		}
		m.ClientMsgID = client.String
		if senderUID.Valid {
			m.SenderUserID = senderUID.Int64
		}
		out = append(out, MessageView{
			ID: m.ID, ConversationID: m.ConversationID, SenderUserID: m.SenderUserID,
			BizSeq: m.BizSeq, ClientMsgID: m.ClientMsgID, Kind: m.Kind,
			Body: string(m.Body), BodySize: m.BodySize, CreatedAt: m.CreatedAt,
		})
	}
	return out, rows.Err()
}

// MarkRead 推进读位点（GREATEST 单调，乱序/重复上报幂等）。
func (s *MessageService) MarkRead(ctx context.Context, convID string, userID int64, bizSeq int64) (*ReadCursorView, error) {
	if bizSeq < 0 {
		return nil, newError(CodeInvalidArgument, "biz_seq must be >= 0")
	}
	pid, ok, err := principalForUser(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	if _, isMember, err := (&ConversationStore{db: s.db}).activeMember(ctx, s.db, convID, pid); err != nil {
		return nil, err
	} else if !isMember {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	var read int64
	if err := s.db.QueryRowContext(ctx,
		`INSERT INTO n_read_cursor (principal_id, conversation_id, read_biz_seq)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (principal_id, conversation_id) DO UPDATE
		   SET read_biz_seq = GREATEST(n_read_cursor.read_biz_seq, EXCLUDED.read_biz_seq),
		       read_at = now()
		 RETURNING read_biz_seq`, pid, convID, bizSeq).Scan(&read); err != nil {
		return nil, fmt.Errorf("native: mark read: %w", err)
	}
	return s.readCursorView(ctx, convID, read)
}

// ReadCursor 读读位点与未读数。
func (s *MessageService) ReadCursor(ctx context.Context, convID string, userID int64) (*ReadCursorView, error) {
	pid, ok, err := principalForUser(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	if _, isMember, err := (&ConversationStore{db: s.db}).activeMember(ctx, s.db, convID, pid); err != nil {
		return nil, err
	} else if !isMember {
		return nil, newError(CodeNotFound, "conversation %q not found", convID)
	}
	var read int64
	err = s.db.QueryRowContext(ctx,
		`SELECT read_biz_seq FROM n_read_cursor WHERE principal_id=$1 AND conversation_id=$2`,
		pid, convID).Scan(&read)
	if err == sql.ErrNoRows {
		read = 0
	} else if err != nil {
		return nil, fmt.Errorf("native: read cursor: %w", err)
	}
	return s.readCursorView(ctx, convID, read)
}

func (s *MessageService) readCursorView(ctx context.Context, convID string, read int64) (*ReadCursorView, error) {
	var next int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT next_biz_seq FROM n_conversations WHERE id=$1`, convID).Scan(&next); err != nil {
		return nil, fmt.Errorf("native: conversation seq: %w", err)
	}
	unread := next - read
	if unread < 0 {
		unread = 0
	}
	return &ReadCursorView{ConversationID: convID, ReadBizSeq: read, Unread: unread}, nil
}
