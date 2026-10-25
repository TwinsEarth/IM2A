package agentquota

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrQuotaExceeded = errors.New("agentquota: quota exceeded for plan")

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) Occupy(ctx context.Context, userID int64, role string) error {
	col := map[string]string{"main": "main_used", "manager": "manager_used", "sub": "sub_used"}[role]
	lim := map[string]string{"main": "main_limit", "manager": "manager_limit", "sub": "sub_limit"}[role]
	if col == "" {
		return fmt.Errorf("agentquota: bad role %q", role)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_quota_usage WHERE user_id=$1`, userID).Scan(&exists); err != nil {
		return fmt.Errorf("select exists: %w", err)
	}
	if exists == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_quota_usage(user_id,plan_key) VALUES($1,'free')`, userID); err != nil {
			return fmt.Errorf("insert quota: %w", err)
		}
	}

	var used, limit int
	q := fmt.Sprintf(`SELECT q.%[1]s, p.%[2]s FROM agent_quota_usage q JOIN agent_plans p ON p.plan_key=q.plan_key WHERE q.user_id=$1 FOR UPDATE`, col, lim)
	if err := tx.QueryRowContext(ctx, q, userID).Scan(&used, &limit); err != nil {
		return fmt.Errorf("join select [%s]: %w", q, err)
	}
	if used >= limit {
		return ErrQuotaExceeded
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE agent_quota_usage SET %[1]s=%[1]s+1 WHERE user_id=$1`, col), userID); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return tx.Commit()
}

func (s *Service) Upgrade(ctx context.Context, userID int64, plan string) error {
	var ok bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_plans WHERE plan_key=$1)`, plan).Scan(&ok); err != nil {
		return fmt.Errorf("plan exists: %w", err)
	}
	if !ok {
		return fmt.Errorf("agentquota: unknown plan %q", plan)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_quota_usage(user_id,plan_key) VALUES($1,$2)
		 ON CONFLICT(user_id) DO UPDATE SET plan_key=EXCLUDED.plan_key`, userID, plan)
	return err
}
