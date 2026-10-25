package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	//
	//

	"human-agent-im/internal/push"
)

// PushMux v1.7.4：离线推送管理 HTTP API（凭据录入 / 任务查询 / 手动重试）。
func (d *Deps) PushMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/push/credentials", d.listPushCredential)
	mux.HandleFunc("POST /v1/push/credentials", d.upsertPushCredential)
	mux.HandleFunc("GET /v1/push/tasks", d.listPushTasks)
	mux.HandleFunc("POST /v1/push/retry", d.retryPush)
	return mux
}

type pushCredentialReq struct {
	UserID      string `json:"user_id"`
	Platform    string `json:"platform"`
	CertRef     string `json:"cert_ref"`
	DeviceToken string `json:"device_token"`
}

// upsertPushCredential 真实落 im_push_credentials。
func (d *Deps) upsertPushCredential(w http.ResponseWriter, r *http.Request) {
	var req pushCredentialReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad body"})
		return
	}
	if req.UserID == "" || req.Platform == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "user_id and platform required"})
		return
	}
	_, err := d.DB.Exec(`
INSERT INTO im_push_credentials(user_id, platform, cert_ref, device_token, updated_at)
VALUES($1,$2,$3,$4, now())
ON CONFLICT (user_id, platform) DO UPDATE SET cert_ref=$3, device_token=$4, updated_at=now()`,
		req.UserID, req.Platform, req.CertRef, req.DeviceToken)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user_id": req.UserID, "platform": req.Platform})
}

func (d *Deps) listPushCredential(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("user_id")
	if uid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "user_id required"})
		return
	}
	rows, err := d.DB.Query(`SELECT platform, cert_ref, device_token FROM im_push_credentials WHERE user_id=$1`, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var p, c, t string
		_ = rows.Scan(&p, &c, &t)
		out = append(out, map[string]any{"platform": p, "cert_ref": c, "device_token": t})
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": uid, "credentials": out})
}

func (d *Deps) listPushTasks(w http.ResponseWriter, r *http.Request) {
	targetUID := r.URL.Query().Get("user_id")
	// v1.9.1：带 JWT 身份时，目标 user_id 必须等于本人 id，否则横向越权 403
	if ctxUID, ok := UserIDFromContext(r); ok {
		wantUID := strconv.FormatInt(ctxUID, 10)
		if targetUID != "" && targetUID != wantUID {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": ErrCallerMismatch.Error()})
			return
		}
		targetUID = wantUID
	}
	if targetUID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "user_id required"})
		return
	}
	rows, err := d.DB.Query(`SELECT msg_key, status, attempts, COALESCE(last_error,''), title, body
		FROM im_push_tasks WHERE user_id=$1 ORDER BY id DESC LIMIT 100`, targetUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var mk, st, le, title, body string
		var att int
		_ = rows.Scan(&mk, &st, &att, &le, &title, &body)
		out = append(out, map[string]any{"msg_key": mk, "status": st, "attempts": att, "last_error": le, "title": title, "body": body})
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": targetUID, "tasks": out})
}

// retryPush 手动对 pending 任务真实下发（走 push.Client；未配置返回 503 而不是假成功）。
func (d *Deps) retryPush(w http.ResponseWriter, r *http.Request) {
	uidStr := r.URL.Query().Get("user_id")
	if uidStr == "" {
		var req struct {
			UserID string `json:"user_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		uidStr = req.UserID
	}
	// v1.9.1：带 JWT 身份时，目标 user_id 必须等于本人
	if ctxUID, ok := UserIDFromContext(r); ok {
		wantUID := strconv.FormatInt(ctxUID, 10)
		if uidStr != "" && uidStr != wantUID {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": ErrCallerMismatch.Error()})
			return
		}
		uidStr = wantUID
	}
	if uidStr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "user_id required"})
		return
	}
	if d.PushClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "push client not configured (set TIM_BASE_URL)"})
		return
	}
	ctx := context.Background()
	// 仅重试该用户 pending，绝不越权推送其他用户（按 user_id 过滤 + 行级抢占锁）。
	sent, attempted, err := push.DispatchUserPending(ctx, d.DB, d.PushClient, uidStr)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": sent, "attempted": attempted})
}
