// v1.5.2: 离线审批缓存——断网在端侧排队，联网后真实重放（过 LWW）。
package api

import (
	"encoding/json"
	"net/http"
)

// OfflineSubmit POST /v1/offline/submit：联网后批量上报端侧离线审批，进入待重放队列。
func (d *Deps) OfflineSubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID   string `json:"userId"`
		TicketID string `json:"ticketId"`
		Decision string `json:"decision"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.TicketID == "" || req.Decision == "" {
		writeErr(w, 400, "ticketId/decision required")
		return
	}
	if req.Decision != "approved" && req.Decision != "rejected" {
		writeErr(w, 400, "decision must be approved/rejected")
		return
	}
	if d.DB == nil {
		writeJSON(w, 503, map[string]any{"error": "storage unavailable"})
		return
	}
	// 幂等：同一 ticket 已在队列且未同步，不重复插入
	var exist int
	d.DB.QueryRow(`SELECT COUNT(*) FROM offline_approvals
		WHERE user_id=$1 AND ticket_id=$2 AND synced=false`, req.UserID, req.TicketID).Scan(&exist)
	if exist > 0 {
		writeJSON(w, 200, map[string]any{"ticketId": req.TicketID, "queued": true, "idempotent": true})
		return
	}
	_, err := d.DB.Exec(`INSERT INTO offline_approvals(user_id, ticket_id, decision, synced) VALUES($1,$2,$3,false)`,
		req.UserID, req.TicketID, req.Decision)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ticketId": req.TicketID, "decision": req.Decision, "queued": true})
}

// OfflineList GET /v1/offline/pending?userId=alice：真实查询未同步队列。
func (d *Deps) OfflineList(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		writeErr(w, 400, "userId required")
		return
	}
	if d.DB == nil {
		writeJSON(w, 200, map[string]any{"userId": userID, "pending": 0, "items": []any{}})
		return
	}
	var n int
	d.DB.QueryRow(`SELECT COUNT(*) FROM offline_approvals WHERE user_id=$1 AND synced=false`, userID).Scan(&n)

	rows, _ := d.DB.Query(`SELECT ticket_id, decision FROM offline_approvals
		WHERE user_id=$1 AND synced=false ORDER BY id ASC LIMIT 100`, userID)
	items := []map[string]any{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ticket, decision string
			rows.Scan(&ticket, &decision)
			items = append(items, map[string]any{"ticketId": ticket, "decision": decision})
		}
	}
	writeJSON(w, 200, map[string]any{"userId": userID, "pending": n, "items": items})
}

// OfflineSync POST /v1/offline/sync：联网后重放所有未同步审批，逐条过 LWW，标记结果。
func (d *Deps) OfflineSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"userId"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.UserID == "" {
		writeErr(w, 400, "userId required")
		return
	}
	if d.DB == nil {
		writeJSON(w, 503, map[string]any{"error": "storage unavailable"})
		return
	}

	// 按发生顺序（id ASC）取出未同步
	rows, err := d.DB.Query(`SELECT id, ticket_id, decision,
		EXTRACT(EPOCH FROM created_at)*1000 FROM offline_approvals
		WHERE user_id=$1 AND synced=false ORDER BY id ASC FOR UPDATE`, req.UserID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type item struct {
		id       int64
		ticket   string
		decision string
		ts       int64
	}
	var queue []item
	defer rows.Close()
	for rows.Next() {
		var it item
		rows.Scan(&it.id, &it.ticket, &it.decision, &it.ts)
		queue = append(queue, it)
	}
	rows.Close()

	applied, conflict, failed := 0, 0, 0
	for _, it := range queue {
		// 以离线发生时刻（created_at）作为 LWW 时间戳，保证多端裁决符合真实操作顺序
		res := d.applyTicketDecision(req.UserID, it.ticket, it.decision, "offline", it.ts)
		switch {
		case res.status == 200:
			if res.body["idempotent"] == true {
				applied++
			} else {
				applied++
			}
			d.DB.Exec(`UPDATE offline_approvals SET synced=true WHERE id=$1`, it.id)
		case res.status == 409:
			// 冲突：服务端已有更新决策，离线旧操作丢弃，仍标记已处理（避免无限重放）
			conflict++
			d.DB.Exec(`UPDATE offline_approvals SET synced=true WHERE id=$1`, it.id)
		default:
			failed++
		}
	}

	writeJSON(w, 200, map[string]any{
		"userId": req.UserID, "replayed": len(queue),
		"applied": applied, "conflict": conflict, "failed": failed,
	})
}
