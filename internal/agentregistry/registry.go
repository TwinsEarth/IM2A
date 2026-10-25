// Package agentregistry 实现 v2.3.3 A2：每用户 PG Agent 注册表。
//
// 每个微信用户（wxid）有一个 master 总 Agent，可注册多个子 Agent
// （calendar/purchase/legal/property/parent/chat）。调用前必须过
// EnsureAllowed：检查 agent 是否注册、是否 active、是否超过 VIP 配额。
package agentregistry

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("agentregistry: agent not registered")
	ErrPaused        = errors.New("agentregistry: agent paused")
	ErrQuotaExceeded = errors.New("agentregistry: quota exceeded")
)

// Agent 一条注册记录。
type Agent struct {
	UserWxid    string
	AgentID     string
	Role        string
	DisplayName string
	TrustLevel  int
	QuotaMax    int
	QuotaUsed   int
	Status      string
}

// Store PG 实现。
type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

// Ensure 幂等注册 agent；已存在则更新 displayName/trustLevel（不重置配额）。
func (s *Store) Ensure(ctx context.Context, userWxid, agentID, role, displayName string, trustLevel, quotaMax int) (*Agent, error) {
	const q = `
INSERT INTO agent_registry (user_wxid, agent_id, role, display_name, trust_level, quota_max)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (user_wxid, agent_id) DO UPDATE
  SET display_name = EXCLUDED.display_name,
      trust_level  = EXCLUDED.trust_level,
      quota_max    = EXCLUDED.quota_max,
      role         = EXCLUDED.role,
      updated_at   = now()
RETURNING user_wxid, agent_id, role, display_name, trust_level, quota_max, quota_used, status;
`
	a := &Agent{}
	err := s.db.QueryRowContext(ctx, q, userWxid, agentID, role, displayName, trustLevel, quotaMax).
		Scan(&a.UserWxid, &a.AgentID, &a.Role, &a.DisplayName, &a.TrustLevel, &a.QuotaMax, &a.QuotaUsed, &a.Status)
	return a, err
}

// ListByUser 返回某用户全部注册 Agent。
func (s *Store) ListByUser(ctx context.Context, userWxid string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_wxid, agent_id, role, display_name, trust_level, quota_max, quota_used, status
		   FROM agent_registry WHERE user_wxid=$1 ORDER BY role DESC, agent_id`, userWxid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.UserWxid, &a.AgentID, &a.Role, &a.DisplayName, &a.TrustLevel, &a.QuotaMax, &a.QuotaUsed, &a.Status); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// EnsureAllowed 调用前闸门：注册存在 + active + 配额未超；通过则原子 +1。
func (s *Store) EnsureAllowed(ctx context.Context, userWxid, agentID string) (*Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a := &Agent{}
	err = tx.QueryRowContext(ctx,
		`SELECT user_wxid, agent_id, role, display_name, trust_level, quota_max, quota_used, status
		   FROM agent_registry
		  WHERE user_wxid=$1 AND agent_id=$2 FOR UPDATE`, userWxid, agentID).
		Scan(&a.UserWxid, &a.AgentID, &a.Role, &a.DisplayName, &a.TrustLevel, &a.QuotaMax, &a.QuotaUsed, &a.Status)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if a.Status != "active" {
		return nil, ErrPaused
	}
	if a.QuotaUsed >= a.QuotaMax {
		return nil, ErrQuotaExceeded
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_registry SET quota_used=quota_used+1, updated_at=now()
		  WHERE user_wxid=$1 AND agent_id=$2`, userWxid, agentID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	a.QuotaUsed++
	return a, nil
}

// ResetQuota 周期清零（可由定时任务调用）。
func (s *Store) ResetQuota(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE agent_registry SET quota_used=0, updated_at=now()`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// Pause / Resume 供管理员停用某个 Agent。
func (s *Store) SetStatus(ctx context.Context, userWxid, agentID, status string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_registry SET status=$3, updated_at=now() WHERE user_wxid=$1 AND agent_id=$2`,
		userWxid, agentID, status)
	return err
}
