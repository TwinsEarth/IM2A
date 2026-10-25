// v1.5.1: 手表一键审批——通过/驳回真实落库（LWW）+ 30 分钟未操作紧急升级。
package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type WatchReq struct {
	UserID   string `json:"userId"`
	TicketID string `json:"ticketId"`
}

// WatchApprove POST /v1/watch/approve
func (d *Deps) WatchApprove(w http.ResponseWriter, r *http.Request) {
	var req WatchReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.TicketID == "" {
		writeErr(w, 400, "ticketId required")
		return
	}
	uid := req.UserID
	if uid == "" {
		uid = "watch-user"
	}
	res := d.applyTicketDecision(uid, req.TicketID, "approved", "watch", 0)
	if res.status == 200 {
		res.body["via"] = "watch"
	}
	writeJSON(w, res.status, res.body)
}

// WatchReject POST /v1/watch/reject
func (d *Deps) WatchReject(w http.ResponseWriter, r *http.Request) {
	var req WatchReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.TicketID == "" {
		writeErr(w, 400, "ticketId required")
		return
	}
	uid := req.UserID
	if uid == "" {
		uid = "watch-user"
	}
	res := d.applyTicketDecision(uid, req.TicketID, "rejected", "watch", 0)
	if res.status == 200 {
		res.body["via"] = "watch"
	}
	writeJSON(w, res.status, res.body)
}

// EscalateReq 紧急升级请求。deadlineMs 为截止时间戳（毫秒）。
type EscalateReq struct {
	UserID     string `json:"userId"`
	TicketID   string `json:"ticketId"`
	DeadlineMs int64  `json:"deadlineMs"`
}

// WatchEscalate POST /v1/watch/escalate
// 真实超时升级：仅当 ticket 仍 pending 且已过 deadline 才升级；写审计、广播。
// 升级通道（短信/电话）由外部通道适配器消费 SSE escalate 事件，此处产出权威事件。
func (d *Deps) WatchEscalate(w http.ResponseWriter, r *http.Request) {
	var req EscalateReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.TicketID == "" || req.DeadlineMs == 0 {
		writeErr(w, 400, "ticketId/deadlineMs required")
		return
	}
	if d.DB == nil {
		writeJSON(w, 503, map[string]any{"error": "storage unavailable"})
		return
	}

	now := time.Now().UnixMilli()
	if now < req.DeadlineMs {
		remaining := (req.DeadlineMs - now) / 1000
		writeJSON(w, 200, map[string]any{
			"ticketId": req.TicketID, "escalated": false,
			"remainingSec": remaining, "reason": "deadline not reached",
		})
		return
	}

	// 已过 deadline：查当前状态，只有 pending 才升级
	var decision string
	err := d.DB.QueryRow(`SELECT decision FROM ticket_state WHERE ticket_id=$1`, req.TicketID).Scan(&decision)
	if err != nil {
		decision = "pending" // 从未决策
	}
	if decision == "approved" || decision == "rejected" {
		writeJSON(w, 200, map[string]any{
			"ticketId": req.TicketID, "escalated": false,
			"reason": "already decided: " + decision,
		})
		return
	}

	detail, _ := json.Marshal(map[string]any{
		"ticketId": req.TicketID, "escalation": "sms_call",
		"deadlineMs": req.DeadlineMs, "overdueMs": now - req.DeadlineMs,
	})
	_, _ = d.DB.Exec(`INSERT INTO audit_events(agent_id, action, detail) VALUES($1,$2,$3)`,
		"watch", "approval_escalated", string(detail))
	uid := req.UserID
	if uid == "" {
		uid = "watch-user"
	}
	sseHub.Publish(uid, string(detail))

	writeJSON(w, 200, map[string]any{
		"ticketId": req.TicketID, "escalated": true,
		"channel": "sms_call", "overdueMs": now - req.DeadlineMs,
	})
}
