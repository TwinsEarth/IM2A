package tempgroup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var (
	ErrClosed   = errors.New("tempgroup: group closed")
	ErrNotFound = errors.New("tempgroup: not found")
	ErrMember   = errors.New("tempgroup: not a member")
)

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) Create(ctx context.Context, id, topic, createdBy string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO temp_groups(id, topic, created_by) VALUES ($1,$2,$3)`,
		id, topic, createdBy)
	if err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO temp_group_members(group_id, agent_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
		id, createdBy)
	return nil
}

func (s *Service) Join(ctx context.Context, groupID, agentID string) error {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM temp_groups WHERE id=$1`, groupID).Scan(&status)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return ErrClosed
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO temp_group_members(group_id, agent_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
		groupID, agentID)
	return err
}

func (s *Service) Post(ctx context.Context, groupID, fromAgent string, payload any) error {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM temp_groups WHERE id=$1`, groupID).Scan(&status)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return ErrClosed
	}
	var c int
	_ = s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM temp_group_members WHERE group_id=$1 AND agent_id=$2`,
		groupID, fromAgent).Scan(&c)
	if c == 0 {
		return ErrMember
	}
	pj, _ := json.Marshal(payload)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO temp_messages(group_id, from_agent, payload) VALUES ($1,$2,$3)`,
		groupID, fromAgent, pj)
	return err
}

func (s *Service) History(ctx context.Context, groupID string, limit int) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_agent, payload FROM temp_messages WHERE group_id=$1 ORDER BY id DESC LIMIT $2`,
		groupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var from string
		var pj []byte
		_ = rows.Scan(&from, &pj)
		out = append(out, map[string]any{"from": from, "payload": string(pj)})
	}
	return out, nil
}

func (s *Service) Destroy(ctx context.Context, groupID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE temp_groups SET status='closed', closed_at=now() WHERE id=$1 AND status='active'`, groupID)
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, _ = tx.ExecContext(ctx, `DELETE FROM temp_messages WHERE group_id=$1`, groupID)
	_, _ = tx.ExecContext(ctx, `DELETE FROM temp_group_members WHERE group_id=$1`, groupID)
	return tx.Commit()
}

func (s *Service) Members(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM temp_group_members WHERE group_id=$1`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		out = append(out, a)
	}
	return out, nil
}
