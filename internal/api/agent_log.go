// v1.6.1: 巡视态——Agent 工作日志 API。
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// AgentLog GET /v1/agent/logs?userId=1
func (d *Deps) AgentLog(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		writeErr(w, 400, "userId required")
		return
	}
	if d.DB == nil {
		writeJSON(w, 200, map[string]any{"userId": userID, "entries": []any{}})
		return
	}
	uid, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		// 非数字 userId 时按 agent_id 查（兼容字符串 ID）
		uid = 0
	}
	// v1.9.1：数字 user_id 维度必须服从调用者 JWT 身份
	if uid > 0 {
		caller, cerr := ResolveCallerUID(r, uid)
		if cerr != nil {
			writeErr(w, 403, cerr.Error())
			return
		}
		uid = caller
	}
	var rows interface {
		Close() error
		Next() bool
		Scan(...interface{}) error
	}
	var qerr error
	if uid > 0 {
		rows, qerr = d.DB.Query(`SELECT agent_id, action, detail, occurred_at
			FROM audit_events WHERE user_id=$1 ORDER BY occurred_at DESC LIMIT 50`, uid)
	} else {
		// v1.6 审查修复：字符串 userId 不得返回全表（越权），按 agent_id 精确匹配
		rows, qerr = d.DB.Query(`SELECT agent_id, action, detail, occurred_at
			FROM audit_events WHERE agent_id=$1 ORDER BY occurred_at DESC LIMIT 50`, userID)
	}
	if qerr != nil {
		writeErr(w, 500, qerr.Error())
		return
	}
	defer rows.Close()
	type LogEntry struct {
		Agent  string          `json:"agent"`
		Action string          `json:"action"`
		Detail json.RawMessage `json:"detail"`
		Time   string          `json:"time"`
	}
	entries := []LogEntry{}
	for rows.Next() {
		var e LogEntry
		var t string
		_ = rows.Scan(&e.Agent, &e.Action, &e.Detail, &t)
		e.Time = t
		entries = append(entries, e)
	}
	writeJSON(w, 200, map[string]any{"userId": userID, "count": len(entries), "entries": entries})
}
