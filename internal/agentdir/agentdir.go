package agentdir

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Agent 通讯录条目：每个子 Agent 是一等公民联系人。
type Agent struct {
	AgentID            string    `json:"agentId"`
	OwnerUser          string    `json:"ownerUser"`
	DisplayName        string    `json:"displayName"`
	Alias              string    `json:"alias"`
	AttentionLevel     int       `json:"attentionLevel"` // 0低 1中 2高 3紧急
	Tags               []string  `json:"tags"`
	MonthlyBudgetCents int64     `json:"monthlyBudgetCents"`
	TrustLevel         int       `json:"trustLevel"` // 继承自背后的人 L0-L4
	CreatedAt          time.Time `json:"createdAt,omitempty"`
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

// Upsert 注册或更新 Agent；trustLevel 由调用方从关系档位派生传入（不允许 Agent 自打分）。
func (s *Service) Upsert(ctx context.Context, a *Agent) error {
	if a.AgentID == "" || a.OwnerUser == "" || a.DisplayName == "" {
		return errors.New("agentdir: agentId/ownerUser/displayName required")
	}
	if a.Tags == nil {
		a.Tags = []string{}
	}
	tj, _ := json.Marshal(a.Tags)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_directory(agent_id,owner_user,display_name,alias,attention_level,tags,monthly_budget_cents,trust_level)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT(agent_id) DO UPDATE SET
		   display_name=EXCLUDED.display_name, alias=EXCLUDED.alias,
		   attention_level=EXCLUDED.attention_level, tags=EXCLUDED.tags,
		   monthly_budget_cents=EXCLUDED.monthly_budget_cents,
		   trust_level=EXCLUDED.trust_level, updated_at=now()`,
		a.AgentID, a.OwnerUser, a.DisplayName, a.Alias, a.AttentionLevel, tj, a.MonthlyBudgetCents, a.TrustLevel)
	return err
}

func (s *Service) Get(ctx context.Context, agentID string) (*Agent, error) {
	a := &Agent{}
	var tj []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT agent_id,owner_user,display_name,alias,attention_level,tags,monthly_budget_cents,trust_level,created_at
		 FROM agent_directory WHERE agent_id=$1`, agentID).
		Scan(&a.AgentID, &a.OwnerUser, &a.DisplayName, &a.Alias, &a.AttentionLevel, &tj, &a.MonthlyBudgetCents, &a.TrustLevel, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(tj, &a.Tags)
	return a, nil
}

func (s *Service) ListByOwner(ctx context.Context, owner string) ([]*Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id,owner_user,display_name,alias,attention_level,tags,monthly_budget_cents,trust_level,created_at
		 FROM agent_directory WHERE owner_user=$1 ORDER BY attention_level DESC, created_at`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Agent, 0)
	for rows.Next() {
		a := &Agent{}
		var tj []byte
		if err := rows.Scan(&a.AgentID, &a.OwnerUser, &a.DisplayName, &a.Alias, &a.AttentionLevel, &tj, &a.MonthlyBudgetCents, &a.TrustLevel, &a.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(tj, &a.Tags)
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAttention 调整关注级别（巡视态一键置顶/静音）。
func (s *Service) SetAttention(ctx context.Context, agentID string, level int) error {
	if level < 0 || level > 3 {
		return errors.New("attention_level must be 0..3")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agent_directory SET attention_level=$2, updated_at=now() WHERE agent_id=$1`, agentID, level)
	return err
}
