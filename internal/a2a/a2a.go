package a2a

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Envelope struct {
	MsgID     string         `json:"msgId"`
	ConvID    string         `json:"convId"`
	FromAgent string         `json:"fromAgent"`
	ToAgent   string         `json:"toAgent"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	Delivered bool           `json:"delivered"`
	CreatedAt time.Time      `json:"createdAt,omitempty"`
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

// Send 幂等投递（ON CONFLICT DO NOTHING）。返回是否为已存在(already)。
func (s *Service) Send(ctx context.Context, e *Envelope) (already bool, err error) {
	if e.MsgID == "" || e.ConvID == "" || e.FromAgent == "" || e.ToAgent == "" {
		return false, errors.New("a2a: msgId/convId/fromAgent/toAgent required")
	}
	if e.Type == "" {
		e.Type = "proposal"
	}
	pj, _ := json.Marshal(e.Payload)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO a2a_envelopes(msg_id,conv_id,from_agent,to_agent,msg_type,payload)
		 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(msg_id) DO NOTHING`,
		e.MsgID, e.ConvID, e.FromAgent, e.ToAgent, e.Type, pj)
	if err != nil {
		return false, err
	}
	aff, _ := res.RowsAffected()
	return aff == 0, nil
}

func (s *Service) Inbox(ctx context.Context, agent string, limit int) ([]*Envelope, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT msg_id,conv_id,from_agent,to_agent,msg_type,payload,delivered,created_at
		 FROM a2a_envelopes WHERE to_agent=$1 AND delivered=false
		 ORDER BY created_at ASC LIMIT $2`, agent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Envelope, 0, limit)
	for rows.Next() {
		e := &Envelope{}
		var pj []byte
		if err := rows.Scan(&e.MsgID, &e.ConvID, &e.FromAgent, &e.ToAgent, &e.Type, &pj, &e.Delivered, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(pj, &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) Deliver(ctx context.Context, msgID string) (noRows bool, err error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE a2a_envelopes SET delivered=true, delivered_at=now() WHERE msg_id=$1 AND delivered=false`, msgID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 0, nil
}
