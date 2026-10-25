// SPDX-License-Identifier: MIT
package native

import (
	"context"
	"database/sql"
	"fmt"
)

// PrincipalStore 是 n_principal 的 PG 实现（C1 建表；C2 起用于会话 actor 映射）。
//
// 说明：C1 的权威投递键是 JWT.sub（n_principal 不参与身份判定）；
// C2 只把 userID → principal.id 的映射补齐给会话/成员表用；
// 真正"三套 id 空间统一"（human/agent/wxid）留到 C3/C4 收敛。
type PrincipalStore struct{ db *sql.DB }

// NewPrincipalStore 构造 principal 存储。
func NewPrincipalStore(db *sql.DB) *PrincipalStore { return &PrincipalStore{db: db} }

// ResolveOrCreateUser 返回 users.id 对应的人类 principal id（不存在则创建）。
// 幂等：并发调用由 uq_n_principal_user 唯一索引 + ON CONFLICT 保序。
func (s *PrincipalStore) ResolveOrCreateUser(ctx context.Context, userID int64) (int64, error) {
	return resolveOrCreateUser(ctx, s.db, userID)
}

// ResolveOrCreateAgent 返回 agent_id 对应的 agent principal id（不存在则创建）。
// C4 agent token 的 sub 用该 id（agent 的投递键）；owner 关系仍由 agent_registry 记录。
func (s *PrincipalStore) ResolveOrCreateAgent(ctx context.Context, agentID string) (int64, error) {
	if agentID == "" {
		return 0, newError(CodeInvalidArgument, "agent_id required")
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO n_principal (kind, agent_id)
		 VALUES ('agent', $1)
		 ON CONFLICT (kind, agent_id) WHERE agent_id IS NOT NULL
		 DO UPDATE SET updated_at = now()
		 RETURNING id`, agentID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("native: resolve agent principal %q: %w", agentID, err)
	}
	return id, nil
}

type execQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// resolveOrCreateUser 可在事务（*sql.Tx）或连接池上执行。
func resolveOrCreateUser(ctx context.Context, q execQuerier, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, newError(CodeInvalidArgument, "user_id 必须为正整数")
	}
	var id int64
	err := q.QueryRowContext(ctx,
		`INSERT INTO n_principal (kind, user_id)
		 VALUES ('human', $1)
		 ON CONFLICT (kind, user_id) WHERE user_id IS NOT NULL
		 DO UPDATE SET updated_at = now()
		 RETURNING id`, userID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("native: resolve principal user=%d: %w", userID, err)
	}
	return id, nil
}

// UserIDOf 把 principal id 映射回 users.id（仅 human）；不存在/非 human 返回 (0,false)。
func (s *PrincipalStore) UserIDOf(ctx context.Context, principalID int64) (int64, bool, error) {
	if principalID <= 0 {
		return 0, false, nil
	}
	var uid sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id FROM n_principal WHERE id = $1 AND kind = 'human'`, principalID).Scan(&uid)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("native: principal user id: %w", err)
	}
	if !uid.Valid {
		return 0, false, nil
	}
	return uid.Int64, true, nil
}
