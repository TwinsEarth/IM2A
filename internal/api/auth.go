package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"human-agent-im/internal/auth"
)

// upsertUserByIdentifier 按 identifier 找/建用户，返回 user id（真实落库，非内存）。
// 并发撞唯一索引时退化为重查，保证同一 identifier 永远映射同一 id。
func (d *Deps) upsertUserByIdentifier(identifier string) (int64, error) {
	if d.DB == nil {
		return 0, errNoDB
	}
	var id int64
	err := d.DB.QueryRow(`SELECT id FROM users WHERE identifier=$1`, identifier).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	err = d.DB.QueryRow(
		`INSERT INTO users(identifier,status) VALUES($1,1) RETURNING id`, identifier,
	).Scan(&id)
	if err != nil {
		// 并发插入撞唯一索引：退回重查。
		if e2 := d.DB.QueryRow(`SELECT id FROM users WHERE identifier=$1`, identifier).Scan(&id); e2 == nil {
			return id, nil
		}
		return 0, err
	}
	return id, nil
}

var errNoDB = &apiError{msg: "db not configured"}

type apiError struct{ msg string }

func (e *apiError) Error() string { return e.msg }

// LoginRequest POST /v1/auth/login 请求体。
type LoginRequest struct {
	Identifier string `json:"identifier"`
}

// AuthLogin dev 登录端点：按 identifier 建/找用户并签发 JWT（真实落 users 表）。
// v1.9.0 为身份地基，暂用 identifier 直登；后续版本接真实短信验证码。
func (d *Deps) AuthLogin(secret string, ttl time.Duration) http.HandlerFunc {
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req LoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request body"})
			return
		}
		if req.Identifier == "" || len(req.Identifier) > 128 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "identifier required (<=128)"})
			return
		}
		uid, err := d.upsertUserByIdentifier(req.Identifier)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "login store unavailable"})
			return
		}
		tok, err := auth.Issue(secret, uid, ttl, "im2a")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to sign token"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": tok,
			"token_type":   "Bearer",
			"user_id":      uid,
			"expires_in":   int64(ttl / time.Second),
		})
	}
}

// AuthMe GET /v1/auth/me：返回 RequireIdentity 注入的当前用户主体。
func (d *Deps) AuthMe(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth.UserIDFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "no identity in context"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": uid})
}


// ErrCallerMismatch 调用者身份与目标 user_id 不一致（横向越权）。
var ErrCallerMismatch = errors.New("forbidden: caller identity does not match target")

// ResolveCallerUID 返回调用者被授权操作的 user_id（v1.9.1）。
//   - 请求携带 JWT 身份：必须以 JWT sub 为准；queryUID 非 0 且与 sub 不符 → ErrCallerMismatch；
//   - 静态共享 token（无 JWT 身份）：回退 queryUID（向后兼容，逐步淘汰）。
func ResolveCallerUID(r *http.Request, queryUID int64) (int64, error) {
	if ctxUID, ok := UserIDFromContext(r); ok {
		if queryUID != 0 && queryUID != ctxUID {
			return 0, ErrCallerMismatch
		}
		return ctxUID, nil
	}
	return queryUID, nil
}

// UserIDFromContext 透传 auth 包的取身份方法（本文件已 import auth）。
func UserIDFromContext(r *http.Request) (int64, bool) {
	return auth.UserIDFromContext(r)
}
