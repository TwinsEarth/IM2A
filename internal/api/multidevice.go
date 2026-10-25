// v1.5.0 / v1.5.3: 多端同步——设备注册 + 权威审批状态 + LWW 冲突裁决。
//
// ticket_state 表是多端状态的唯一事实源：
//   - 任何一端（手表/手机/PC/离线重放）提交决策都走 applyTicketDecision
//   - 以 updated_at（毫秒时间戳）做 Last-Write-Wins；旧时间戳一律 409 拒绝
//   - 终态（approved/rejected）被旧写入保护，重复提交幂等返回
package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// RegisterDevice POST /v1/device/register：设备心跳注册（真实 upsert）。
func (d *Deps) RegisterDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"deviceId"`
		UserID   string `json:"userId"`
		Type     string `json:"type"` // watch/phone/pc
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.DeviceID == "" || req.UserID == "" {
		writeErr(w, 400, "deviceId/userId required")
		return
	}
	switch req.Type {
	case "watch", "phone", "pc":
	default:
		writeErr(w, 400, "type must be watch/phone/pc")
		return
	}
	if d.DB != nil {
		_, err := d.DB.Exec(`INSERT INTO devices(id, user_id, type, last_seen) VALUES($1,$2,$3,$4)
			ON CONFLICT (id) DO UPDATE SET user_id=$2, type=$3, last_seen=$4`,
			req.DeviceID, req.UserID, req.Type, time.Now())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"deviceId": req.DeviceID, "registered": true})
}

// applyResult 是 LWW 裁决结果。
type applyResult struct {
	status int
	body   map[string]any
}

// applyTicketDecision 是手表/手机/离线共同的权威决策入口。
// clientTS=0 时由服务端授时（实时在线提交）。
func (d *Deps) applyTicketDecision(userID, ticketID, decision, deviceID string, clientTS int64) applyResult {
	if ticketID == "" {
		return applyResult{400, map[string]any{"error": "ticketId required"}}
	}
	if decision != "approved" && decision != "rejected" {
		return applyResult{400, map[string]any{"error": "decision must be approved/rejected"}}
	}
	if d.DB == nil {
		return applyResult{503, map[string]any{"error": "storage unavailable"}}
	}

	now := time.Now().UnixMilli()
	if clientTS <= 0 {
		clientTS = now
	}

	// 读当前权威状态（行锁，防并发覆盖）
	var curDecision, curDevice string
	var curVersion, curTS int64
	err := d.DB.QueryRow(`SELECT decision, version, via_device, updated_at
		FROM ticket_state WHERE ticket_id=$1 FOR UPDATE`, ticketID).
		Scan(&curDecision, &curVersion, &curDevice, &curTS)
	if err != nil {
		// 不存在 → 首次写入
		curDecision, curVersion, curTS = "pending", 0, 0
	}

	// 幂等：同设备同决策重复提交，直接返回当前状态
	if curDecision == decision && curDevice == deviceID && curTS == clientTS {
		return applyResult{200, map[string]any{
			"ticketId": ticketID, "decision": curDecision, "version": curVersion,
			"via": curDevice, "idempotent": true,
		}}
	}

	// LWW：旧时间戳拒绝（含终态保护）
	if clientTS < curTS {
		return applyResult{409, map[string]any{
			"error":    "stale write, conflict with newer state",
			"ticketId": ticketID, "serverDecision": curDecision,
			"serverTs": curTS, "clientTs": clientTS,
		}}
	}

	// 终态保护：已终态且新决策不同，必须带更新的时间戳（上面已保证），同 ts 拒绝
	if (curDecision == "approved" || curDecision == "rejected") && clientTS == curTS && curDecision != decision {
		return applyResult{409, map[string]any{
			"error": "ticket already in terminal state", "serverDecision": curDecision,
		}}
	}

	newVersion := curVersion + 1
	_, err = d.DB.Exec(`INSERT INTO ticket_state(ticket_id, user_id, decision, version, via_device, updated_at)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (ticket_id) DO UPDATE SET
			decision=$3, version=$4, via_device=$5, updated_at=$6`,
		ticketID, userID, decision, newVersion, deviceID, clientTS)
	if err != nil {
		return applyResult{500, map[string]any{"error": err.Error()}}
	}

	// 写审计（真实不可变轨迹）
	detail, _ := json.Marshal(map[string]any{
		"ticketId": ticketID, "decision": decision, "version": newVersion,
		"via": deviceID, "ts": clientTS,
	})
	_, _ = d.DB.Exec(`INSERT INTO audit_events(agent_id, action, decision, detail) VALUES($1,$2,$3,$4)`,
		"ticket-sync", "ticket_decision", decision, string(detail))

	// SSE 实时广播到该用户的所有在线端
	sseHub.Publish(userID, string(detail))

	return applyResult{200, map[string]any{
		"ticketId": ticketID, "decision": decision, "version": newVersion,
		"via": deviceID, "ts": clientTS, "merged": true,
	}}
}

// SyncState POST /v1/sync/state：多端提交决策，走真实 LWW。
func (d *Deps) SyncState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID    string `json:"userId"`
		TicketID  string `json:"ticketId"`
		Decision  string `json:"decision"`
		Timestamp int64  `json:"ts"`
		DeviceID  string `json:"deviceId"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	res := d.applyTicketDecision(req.UserID, req.TicketID, req.Decision, req.DeviceID, req.Timestamp)
	writeJSON(w, res.status, res.body)
}

// GetState GET /v1/sync/state/:ticketId：多端拉取权威状态（新设备上线对账用）。
func (d *Deps) GetState(w http.ResponseWriter, r *http.Request) {
	ticketID := r.URL.Path[len("/v1/sync/state/"):]
	if ticketID == "" || d.DB == nil {
		writeErr(w, 404, "ticket not found")
		return
	}
	var decision, device string
	var version, ts int64
	err := d.DB.QueryRow(`SELECT decision, version, via_device, updated_at
		FROM ticket_state WHERE ticket_id=$1`, ticketID).Scan(&decision, &version, &device, &ts)
	if err != nil {
		writeJSON(w, 404, map[string]any{"ticketId": ticketID, "found": false})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ticketId": ticketID, "found": true, "decision": decision,
		"version": version, "via": device, "ts": ts,
	})
}
