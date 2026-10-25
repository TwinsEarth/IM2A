// relation-svc HTTP endpoints.
package api

import (
	"encoding/json"
	"net/http"

	"human-agent-im/internal/trustzone"
)

type GateCheckRequest struct {
	AgentID         string          `json:"agentId"`
	SkillID         string          `json:"skillId"`
	Scope           trustzone.Scope `json:"scope"`
	TrustLevel      int             `json:"trustLevel"`
	AmountCents     int64           `json:"amountCents"`
	TargetUID       uint64          `json:"targetUid"`
	UserIDForMemory uint64          `json:"userId,omitempty"`
}

func (d *Deps) RelationMux(serviceName string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	mux.HandleFunc("POST /v1/trust/relation", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OwnerID      string   `json:"ownerId"`
			ContactID    string   `json:"contactId"`
			TrustLevel   int      `json:"trustLevel"`
			Capabilities []string `json:"capabilities"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.OwnerID == "" || req.ContactID == "" {
			writeErr(w, 400, "ownerId/contactId required")
			return
		}
		if d.DB != nil {
			d.DB.Exec(`INSERT INTO trust_relations(owner_id, contact_id, trust_level, capabilities)
				VALUES($1,$2,$3,$4)
				ON CONFLICT(owner_id, contact_id) DO UPDATE SET trust_level=$3, capabilities=$4, updated_at=NOW()`,
				req.OwnerID, req.ContactID, req.TrustLevel, req.Capabilities)
			// v1.4.2: 写审计事件
			payload, _ := json.Marshal(map[string]any{
				"ownerId": req.OwnerID, "contactId": req.ContactID,
				"trustLevel": req.TrustLevel, "capabilities": req.Capabilities,
			})
			d.DB.Exec(`INSERT INTO audit_events(agent_id, action, detail)
				VALUES($1,$2,$3)`, req.OwnerID, "trust_changed", payload)
			// WS 广播
			if d.Stream != nil {
				d.Stream.Broadcast("trust:"+req.OwnerID, map[string]any{
					"type": "trust_changed", "contactId": req.ContactID, "trustLevel": req.TrustLevel,
				})
			}
		}
		writeJSON(w, 200, map[string]any{
			"ownerId": req.OwnerID, "contactId": req.ContactID,
			"trustLevel": req.TrustLevel, "capabilities": req.Capabilities,
		})
	})

	mux.HandleFunc("GET /v1/trust/relation", func(w http.ResponseWriter, r *http.Request) {
		ownerID := r.URL.Query().Get("ownerId")
		list := []map[string]any{}
		if d.DB != nil {
			rows, _ := d.DB.Query(`SELECT contact_id, trust_level, capabilities FROM trust_relations WHERE owner_id=$1`, ownerID)
			if rows != nil {
				defer rows.Close()
				for rows.Next() {
					var cid string
					var lvl int
					var caps []string
					rows.Scan(&cid, &lvl, &caps)
					list = append(list, map[string]any{"contactId": cid, "trustLevel": lvl, "capabilities": caps})
				}
			}
		}
		writeJSON(w, 200, map[string]any{"ownerId": ownerID, "relations": list, "count": len(list)})
	})

	// v1.4.1: 删除/撤销信任关系
	mux.HandleFunc("DELETE /v1/trust/relation", func(w http.ResponseWriter, r *http.Request) {
		ownerID := r.URL.Query().Get("ownerId")
		contactID := r.URL.Query().Get("contactId")
		if d.DB != nil {
			d.DB.Exec(`DELETE FROM trust_relations WHERE owner_id=$1 AND contact_id=$2`, ownerID, contactID)
		}
		writeJSON(w, 200, map[string]any{"deleted": true, "ownerId": ownerID, "contactId": contactID})
	})

	// v1.4.1: 更新信任档位
	mux.HandleFunc("PATCH /v1/trust/relation", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OwnerID    string `json:"ownerId"`
			ContactID  string `json:"contactId"`
			TrustLevel int    `json:"trustLevel"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if d.DB != nil {
			d.DB.Exec(`UPDATE trust_relations SET trust_level=$3, updated_at=NOW() WHERE owner_id=$1 AND contact_id=$2`,
				req.OwnerID, req.ContactID, req.TrustLevel)
		}
		writeJSON(w, 200, map[string]any{"updated": true, "trustLevel": req.TrustLevel})
	})

	// v1.4.1: 查单条关系
	mux.HandleFunc("GET /v1/trust/relation/one", func(w http.ResponseWriter, r *http.Request) {
		ownerID := r.URL.Query().Get("ownerId")
		contactID := r.URL.Query().Get("contactId")
		var lvl int
		var caps []string
		err := d.DB.QueryRow(`SELECT trust_level, capabilities FROM trust_relations WHERE owner_id=$1 AND contact_id=$2`,
			ownerID, contactID).Scan(&lvl, &caps)
		if err != nil {
			writeJSON(w, 404, map[string]any{"found": false})
			return
		}
		writeJSON(w, 200, map[string]any{"found": true, "ownerId": ownerID, "contactId": contactID, "trustLevel": lvl, "capabilities": caps})
	})
	mux.HandleFunc("POST /v1/trust/grant", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"granted": true})
	})
	mux.HandleFunc("GET /v1/trust/get", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"level": 2})
	})
	// 真实代码层门控：skill 白名单 + 金额阈值
	mux.HandleFunc("POST /v1/trust/check", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AgentID     string `json:"agentId"`
			SkillID     string `json:"skillId"`
			Scope       string `json:"scope"`
			TrustLevel  int    `json:"trustLevel"`
			AmountCents int64  `json:"amountCents"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		cap, ok := d.agentCaps[req.AgentID]
		if !ok {
			writeErr(w, 404, "agent not registered")
			return
		}
		// skill 白名单
		skillOK := false
		for _, sk := range cap.Skills {
			if sk == req.SkillID {
				skillOK = true
				break
			}
		}
		if !skillOK {
			writeJSON(w, 403, map[string]any{"decision": "DENY", "reason": "skill not allowed"})
			return
		}
		// 信任档位
		if req.TrustLevel > cap.TrustMin {
			writeJSON(w, 403, map[string]any{"decision": "DENY", "reason": "trust level insufficient"})
			return
		}
		// 金额审批阈值
		if cap.ApprovalAbove > 0 && req.AmountCents > cap.ApprovalAbove {
			writeJSON(w, 202, map[string]any{"decision": "REQUIRE_APPROVAL", "amountCents": req.AmountCents,
				"threshold": cap.ApprovalAbove})
			return
		}
		writeJSON(w, 200, map[string]any{"decision": "ALLOW", "agentId": req.AgentID, "skillId": req.SkillID})
	})
	// v1.4.1: Agent 注册能力（信任档位 + skill 白名单 + 金额审批阈值）
	mux.HandleFunc("POST /v1/agents/capabilities", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AgentID                    string   `json:"agentId"`
			TrustLevelMin              int      `json:"trustLevelMin"`
			AllowedSkills              []string `json:"allowedSkills"`
			RequiresApprovalAboveCents int64    `json:"requiresApprovalAboveCents"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.AgentID == "" {
			writeErr(w, 400, "agentId required")
			return
		}
		// 内存注册
		if d.agentCaps == nil {
			d.agentCaps = map[string]agentCap{}
		}
		d.agentCaps[req.AgentID] = agentCap{
			TrustMin:      req.TrustLevelMin,
			Skills:        req.AllowedSkills,
			ApprovalAbove: req.RequiresApprovalAboveCents,
		}
		writeJSON(w, 200, map[string]any{"agentId": req.AgentID, "registered": true})
	})
	return mux
}
