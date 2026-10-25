package api

import (
	"net/http"
	"strconv"
	"strings"
)

// broadcastAdminRegister v2.2.2 全量广播管理端点（随 AdminMux 受 admin token 保护）。
func (d *Deps) broadcastAdminRegister(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/admin/broadcast", d.adminBroadcastCreate)
	mux.HandleFunc("GET /v1/admin/broadcast", d.adminBroadcastQuery)
}

// POST /v1/admin/broadcast  {"text": "...", "idempotencyKey": "..."} → 创建并异步群发。
// 幂等键也可由请求头 X-Idempotency-Key 传入（浏览器丢响应后重试只命中同一条任务）。
func (d *Deps) adminBroadcastCreate(w http.ResponseWriter, r *http.Request) {
	if d.Broadcast == nil {
		writeErr(w, http.StatusServiceUnavailable, "broadcast service not configured")
		return
	}
	var req struct {
		Text           string `json:"text"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	idemKey := strings.TrimSpace(req.IdempotencyKey)
	if hk := strings.TrimSpace(r.Header.Get("X-Idempotency-Key")); idemKey == "" {
		idemKey = hk
	}
	id, n, err := d.Broadcast.Create(r.Context(), req.Text, "admin", idemKey)
	if err != nil {
		msg := err.Error()
		code := http.StatusInternalServerError
		if strings.Contains(msg, "empty text") || strings.Contains(msg, "no target") || strings.Contains(msg, "too long") {
			code = http.StatusBadRequest
		}
		writeErr(w, code, msg)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "id": id, "total": n,
		"poll": "/v1/admin/broadcast?id=" + id,
	})
}

// GET /v1/admin/broadcast
//
//	?preview=1          预览目标（不发送）
//	?id=<broadcastId>   查一条任务 + 投递明细
//	(无参)              最近任务列表
func (d *Deps) adminBroadcastQuery(w http.ResponseWriter, r *http.Request) {
	if d.Broadcast == nil {
		writeErr(w, http.StatusServiceUnavailable, "broadcast service not configured")
		return
	}
	q := r.URL.Query()
	if q.Get("preview") == "1" {
		targets, acc, swxid, err := d.Broadcast.Targets(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"count": len(targets), "sender_account": acc, "sender_wxid": swxid, "targets": targets,
		})
		return
	}
	if id := q.Get("id"); id != "" {
		bc, err := d.Broadcast.Get(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"broadcast": bc})
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := d.Broadcast.List(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"broadcasts": list})
}
