package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// PGMessage 基于 PostgreSQL 的消息存储（v1.0.5；v1.0.5 修复补齐 biz_type/agent_context 落库）。
type PGMessage struct {
	db *sql.DB
}

// NewPGMessage 创建 PG 消息存储。
func NewPGMessage(db *sql.DB) *PGMessage {
	return &PGMessage{db: db}
}

// SaveMessage 保存消息（含业务类型与 Agent 上下文；message_id 幂等）。
func (p *PGMessage) SaveMessage(ctx context.Context, m *Message) error {
	biz := m.BizType
	if biz == "" {
		biz = "text"
	}
	var ac interface{}
	if len(m.AgentContext) > 0 {
		b, err := json.Marshal(m.AgentContext)
		if err != nil {
			return err
		}
		ac = string(b)
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO messages (message_id, sender_id, recipient_id, biz_type, content, agent_context, seq)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (message_id) DO NOTHING
	`, m.MessageID, m.SenderID, m.ReceiverID, biz, m.Content, ac, m.Seq)
	return err
}

const msgCols = `message_id, sender_id, recipient_id, biz_type, content, agent_context, seq,
	EXTRACT(EPOCH FROM created_at)::bigint`

// rowScanner 同时兼容 *sql.Row 与 *sql.Rows。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(s rowScanner, m *Message) error {
	var bizType string
	var acJSON []byte
	if err := s.Scan(&m.MessageID, &m.SenderID, &m.ReceiverID, &bizType, &m.Content,
		&acJSON, &m.Seq, &m.CreatedAt); err != nil {
		return err
	}
	m.BizType = bizType
	if len(acJSON) > 0 {
		var ac map[string]any
		if err := json.Unmarshal(acJSON, &ac); err == nil {
			m.AgentContext = ac
		}
	}
	return nil
}

// LoadMessage 按 message_id 读取消息。
func (p *PGMessage) LoadMessage(ctx context.Context, id string) (*Message, error) {
	var m Message
	err := scanMessage(
		p.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE message_id = $1`, id),
		&m,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("store: message not found")
		}
		return nil, err
	}
	return &m, nil
}

// ListMessages 列出用户相关消息（按时间倒序）。
func (p *PGMessage) ListMessages(ctx context.Context, userID uint64, limit int) ([]*Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+msgCols+` FROM messages
		WHERE sender_id = $1 OR recipient_id = $1
		ORDER BY created_at DESC LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		var m Message
		if err := scanMessage(rows, &m); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}
