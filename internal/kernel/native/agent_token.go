// SPDX-License-Identifier: MIT
//
// C4（task-26）agent token 最小闭环：
//   - 签发 `POST /v1/native/agents/{id}/token`（仅 NATIVE_ENABLED，owner JWT 鉴权）；
//   - token 是 C1 同一套 HMAC-SHA256 JWT：`sub = n_principal(kind='agent').id`，
//     `iss = "im2a-agent"`（即 typ=agent 的等价表达；不改 auth.Claims 结构）；
//   - 短 TTL（默认 10min，可 env 覆盖）+ 可吊销（n_agent_token.jti 记录，DELETE 接口）；
//   - C1 的 JWT 校验路径复用：Bridge.OnConnOpen 仍只做 auth.Parse，
//     吊销校验经 Bridge.SetTokenValidator 注入本服务（人类 token 的 jti 不在表里 → 放行）。
//
// 明确不做 RBAC（agent.permissions 插件负责）。
package native

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"human-agent-im/internal/auth"
)

// AgentIssuer agent token 的 iss（= typ=agent 的等价标识）。
const AgentIssuer = "im2a-agent"

// DefaultAgentTokenTTL agent token 默认短 TTL。
const DefaultAgentTokenTTL = 10 * time.Minute

// AgentTokenService 签发/吊销/校验 agent token。
type AgentTokenService struct {
	db         *sql.DB
	secret     string
	ttl        time.Duration
	now        func() time.Time
	principals *PrincipalStore
}

// NewAgentTokenService 构造（secret 为空时签发失败：C1 未配置 JWT 就无从校验）。
func NewAgentTokenService(db *sql.DB, secret string, ttl time.Duration) *AgentTokenService {
	if ttl <= 0 {
		ttl = DefaultAgentTokenTTL
	}
	return &AgentTokenService{db: db, secret: secret, ttl: ttl, now: time.Now, principals: NewPrincipalStore(db)}
}

// IssueResult 是签发结果（对外 JSON）。
type IssueResult struct {
	Token     string    `json:"token"`
	Jti       string    `json:"jti"`
	Typ       string    `json:"typ"`
	AgentID   string    `json:"agent_id"`
	Principal int64     `json:"principal"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Issue 为 ownerUserID 名下的 agentID 签发短 TTL token（agent principal 的 id 作为 sub）。
func (s *AgentTokenService) Issue(ctx context.Context, ownerUserID int64, agentID string) (*IssueResult, error) {
	if s.secret == "" {
		return nil, newError(CodeInvalidArgument, "JWT secret 未配置，无法签发 agent token")
	}
	if ownerUserID <= 0 || agentID == "" {
		return nil, newError(CodeInvalidArgument, "owner/agent id required")
	}
	principalID, err := s.principals.ResolveOrCreateAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	token, err := auth.Issue(s.secret, principalID, s.ttl, AgentIssuer)
	if err != nil {
		return nil, fmt.Errorf("native: issue agent token: %w", err)
	}
	// jti 以 token 内实际 claim 为准（auth.Issue 内部生成），吊销表必须与之一致。
	parsed, err := auth.Parse(s.secret, token)
	if err != nil {
		return nil, fmt.Errorf("native: parse issued agent token: %w", err)
	}
	jti := parsed.Jti
	expires := s.now().Add(s.ttl)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO n_agent_token (jti, agent_id, owner_user_id, principal_id, expires_at)
		 VALUES ($1,$2,$3,$4,$5)`, jti, agentID, ownerUserID, principalID, expires); err != nil {
		return nil, fmt.Errorf("native: record agent token: %w", err)
	}
	return &IssueResult{
		Token: token, Jti: jti, Typ: "agent", AgentID: agentID,
		Principal: principalID, ExpiresAt: expires,
	}, nil
}

// ValidateClaims 供 Bridge 注入：只校验 agent token（iss=im2a-agent）的吊销状态；
// 人类 token（iss 非 agent）直接放行（C1 语义不变）。过期已由 auth.Parse 处理。
func (s *AgentTokenService) ValidateClaims(ctx context.Context, claims *auth.Claims) error {
	if claims == nil || claims.Iss != AgentIssuer || claims.Jti == "" {
		return nil
	}
	var revoked sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT revoked_at FROM n_agent_token WHERE jti=$1`, claims.Jti).Scan(&revoked)
	if err == sql.ErrNoRows {
		return newError(CodeInvalidArgument, "agent token unknown/revoked")
	}
	if err != nil {
		return fmt.Errorf("native: agent token lookup: %w", err)
	}
	if revoked.Valid {
		return newError(CodeInvalidArgument, "agent token revoked")
	}
	return nil
}

// Revoke 吊销 agent token（owner 作用域：只能吊销自己签发的）。
func (s *AgentTokenService) Revoke(ctx context.Context, ownerUserID int64, jti string) error {
	if ownerUserID <= 0 || jti == "" {
		return newError(CodeInvalidArgument, "owner/jti required")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE n_agent_token SET revoked_at=now()
		  WHERE jti=$1 AND owner_user_id=$2 AND revoked_at IS NULL`, jti, ownerUserID)
	if err != nil {
		return fmt.Errorf("native: revoke agent token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return newError(CodeNotFound, "token %q not found", jti)
	}
	return nil
}
