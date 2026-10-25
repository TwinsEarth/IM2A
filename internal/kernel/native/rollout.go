// SPDX-License-Identifier: MIT
//
// C5b：NATIVE_ROLLOUT 灰度白名单（user / device / agent，逗号分隔）。
//
// 语义（与 C2/C3 的 fail-closed 风格一致，**不做静默 404**）：
//   - NATIVE_ENABLED=off            → 路由不装配，/v1/native/* 返回 404 code=NATIVE_DISABLED；
//   - NATIVE_ENABLED=on，名单为空    → 全量放行（当前行为，默认）；
//   - NATIVE_ENABLED=on，名单非空    → 仅名单内 principal/device 放行，
//     其余返回 **421 NATIVE_NOT_ROLLED_OUT**（可解释拒绝码）。
//
// 名单语法：
//
//	user:<users.id>   ／ 裸数字 <id>   → 人类/agent 连接的 JWT.sub（权威 userID）
//	agent:<agent_id>                   → 该 agent 的 principal id（查 n_principal kind='agent'）
//	device:<deviceID>                  → 仅对连接建立（ConnOpen）生效；HTTP 路由没有设备上下文
//	*                                  → 全量放行（灰度回退用）
package native

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
)

// Rollout 是解析后的灰度名单。
type Rollout struct {
	all     bool
	users   map[int64]bool
	agents  map[string]bool
	devices map[string]bool
}

// ParseRollout 解析 NATIVE_ROLLOUT（逗号分隔）。空/全空项 → nil（不启用灰度，全量放行）。
func ParseRollout(spec string) *Rollout {
	entries := strings.Split(spec, ",")
	r := &Rollout{users: map[int64]bool{}, agents: map[string]bool{}, devices: map[string]bool{}}
	any := false
	for _, raw := range entries {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		any = true
		switch {
		case e == "*":
			r.all = true
		case strings.HasPrefix(e, "user:"):
			if id, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(e, "user:")), 10, 64); err == nil && id > 0 {
				r.users[id] = true
			}
		case strings.HasPrefix(e, "agent:"):
			if id := strings.TrimSpace(strings.TrimPrefix(e, "agent:")); id != "" {
				r.agents[id] = true
			}
		case strings.HasPrefix(e, "device:"):
			if id := strings.TrimSpace(strings.TrimPrefix(e, "device:")); id != "" {
				r.devices[id] = true
			}
		default:
			if id, err := strconv.ParseInt(e, 10, 64); err == nil && id > 0 {
				r.users[id] = true
			}
		}
	}
	if !any {
		return nil
	}
	return r
}

// Configured 报告是否配置了灰度名单（nil/空 = 全量放行）。
func (r *Rollout) Configured() bool { return r != nil }

// AllowsUser 只按 user 名单判断（HTTP 路由用；不含 agent/device 解析、不查库）。
func (r *Rollout) AllowsUser(userID int64) bool {
	if r == nil || r.all {
		return true
	}
	return r.users[userID]
}

// Allows 综合判断：user → device → agent（agent 需要 DB 查 principal 的 agent_id）。
// 返回 (allowed, reason)：reason 仅用于日志/指标（"user_allowed"/"agent_allowed"/"not_in_rollout"）。
func (r *Rollout) Allows(ctx context.Context, db *sql.DB, userID int64, deviceID string) (bool, string) {
	if r == nil || r.all {
		return true, "all"
	}
	if userID > 0 && r.users[userID] {
		return true, "user_allowed"
	}
	if deviceID != "" && r.devices[deviceID] {
		return true, "device_allowed"
	}
	if len(r.agents) > 0 && db != nil && userID > 0 {
		if agentID, ok := agentIDOfPrincipal(ctx, db, userID); ok && r.agents[agentID] {
			return true, "agent_allowed"
		}
	}
	return false, "not_in_rollout"
}

// agentIDOfPrincipal 查 principal(kind='agent') 的 agent_id（带短 TTL 缓存，避免每条 HTTP 都查库）。
var (
	agentIDCacheMu sync.RWMutex
	agentIDCache   = map[int64]string{}
)

func agentIDOfPrincipal(ctx context.Context, db *sql.DB, principalID int64) (string, bool) {
	agentIDCacheMu.RLock()
	if id, ok := agentIDCache[principalID]; ok {
		agentIDCacheMu.RUnlock()
		return id, id != ""
	}
	agentIDCacheMu.RUnlock()
	var id sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT agent_id FROM n_principal WHERE id=$1 AND kind='agent'`, principalID).Scan(&id)
	agentIDCacheMu.Lock()
	if err == nil && id.Valid {
		agentIDCache[principalID] = id.String
	} else {
		agentIDCache[principalID] = ""
	}
	agentIDCacheMu.Unlock()
	return id.String, err == nil && id.Valid
}

// ResetAgentIDCache 测试用：清空 principal→agent_id 缓存。
func ResetAgentIDCache() {
	agentIDCacheMu.Lock()
	agentIDCache = map[int64]string{}
	agentIDCacheMu.Unlock()
}
