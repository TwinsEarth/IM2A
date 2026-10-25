package report

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("report: not found")

type Report struct {
	ID      int64
	UserID  string
	Summary map[string]any
	Status  string
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

// Generate 聚合该用户今日活动，落库为 draft。
func (s *Service) Generate(ctx context.Context, userID string) (*Report, error) {
	// 聚合 a2a 发出的消息数
	var sent, inTemp int
	_ = s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM a2a_envelopes WHERE from_agent LIKE $1 AND created_at::date = current_date`,
		userID+"%").Scan(&sent)
	_ = s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM temp_messages m JOIN temp_groups g ON m.group_id=g.id
		 WHERE g.created_by LIKE $1 AND m.created_at::date = current_date`,
		userID+"%").Scan(&inTemp)

	summary := map[string]any{
		"date":      time.Now().Format("2006-01-02"),
		"sent":      sent,
		"temp_msgs": inTemp,
		"approved":  false,
	}
	pj, _ := json.Marshal(summary)
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO agent_reports(user_id, summary) VALUES ($1,$2)
		 ON CONFLICT (user_id, report_date) DO UPDATE SET summary=EXCLUDED.summary
		 RETURNING id`, userID, pj).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &Report{ID: id, UserID: userID, Summary: summary, Status: "draft"}, nil
}

// Approve 用户审批今日报告。
func (s *Service) Approve(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_reports SET status='approved', approved_at=now() WHERE id=$1 AND status='draft'`, id)
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return err
}

// Get 查报告。
func (s *Service) Get(ctx context.Context, id int64) (*Report, error) {
	var (
		uid, status string
		pj []byte
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, summary, status FROM agent_reports WHERE id=$1`, id).Scan(&uid, &pj, &status)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var sum map[string]any
	_ = json.Unmarshal(pj, &sum)
	return &Report{ID: id, UserID: uid, Summary: sum, Status: status}, nil
}
