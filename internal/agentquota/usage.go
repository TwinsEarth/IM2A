package agentquota

import (
	"context"
	"fmt"
)

type Usage struct {
	UserID       int64  `json:"userId"`
	Plan         string `json:"plan"`
	MainUsed     int    `json:"mainUsed"`
	MainLimit    int    `json:"mainLimit"`
	ManagerUsed  int    `json:"managerUsed"`
	ManagerLimit int    `json:"managerLimit"`
	SubUsed      int    `json:"subUsed"`
	SubLimit     int    `json:"subLimit"`
}

func (s *Service) Usage(ctx context.Context, userID int64) (*Usage, error) {
	u := &Usage{UserID: userID}
	err := s.db.QueryRowContext(ctx,
		`SELECT q.plan_key, q.main_used, p.main_limit, q.manager_used, p.manager_limit, q.sub_used, p.sub_limit
		 FROM agent_quota_usage q JOIN agent_plans p ON p.plan_key=q.plan_key WHERE q.user_id=$1`,
		userID).Scan(&u.Plan, &u.MainUsed, &u.MainLimit, &u.ManagerUsed, &u.ManagerLimit, &u.SubUsed, &u.SubLimit)
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	return u, nil
}
