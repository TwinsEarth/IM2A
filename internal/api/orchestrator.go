// agent-orchestrator HTTP 端点：Agent 注册、门控、陌生人来意端到端（预审→占位→审批→执行）。
//
// 核心原则（§1.3，不可妥协）：付费永远发生在 Agent 预审之后。
// 陌生人原始消息先经合规护栏 + 来意规则 + 信任门控，只有放行的消息才进入付费/审批环节。
package api

import (
	"net/http"
	"sync"
	"time"

	"human-agent-im/internal/approval"
	"human-agent-im/internal/audit"
	"human-agent-im/internal/compliance"
	"human-agent-im/internal/id"
	"human-agent-im/internal/ledger"
	"human-agent-im/internal/memory"
	"human-agent-im/internal/negotiation"
	"human-agent-im/internal/trustzone"
)

// Orchestrator 编排器（含 Agent 元数据注册表与合规护栏）。
type Orchestrator struct {
	Deps
	mu    sync.RWMutex
	meta  map[string]AgentMeta
	guard *compliance.Guard
}

// AgentMeta Agent 展示元数据（capability 落 PostgreSQL，元数据进程内）。
type AgentMeta struct {
	AgentID string `json:"agentId"`
	Kind    string `json:"kind"`
	OwnerID uint64 `json:"ownerId"`
	Display string `json:"display"`
	Desc    string `json:"desc"`
}

// RegisterAgentRequest 注册 Agent。
type RegisterAgentRequest struct {
	Meta AgentMeta            `json:"meta"`
	Cap  trustzone.Capability `json:"cap"`
}

// StrangerIntentRequest 陌生人来意（端到端入口）。
type StrangerIntentRequest struct {
	ReceiverID     uint64 `json:"receiverId"`
	StrangerAgent  string `json:"strangerAgentId"`
	IntentCategory string `json:"intentCategory"` // 商务合作/产品咨询/贷款推广/...
	Content        string `json:"content"`
	BillingMode    int    `json:"billingMode"` // 1 发送即扣费 / 2 回复才扣费
	AmountCents    int64  `json:"amountCents"`
}

// DecideIntentRequest 本人对陌生来意卡片决策。
type DecideIntentRequest struct {
	By       string `json:"by"`
	Decision string `json:"decision"` // approve / reject / reply
	Note     string `json:"note"`
}

// NewOrchestrator 构造编排器。
func NewOrchestrator(d Deps) *Orchestrator {
	return &Orchestrator{
		Deps:  d,
		meta:  map[string]AgentMeta{},
		guard: compliance.NewGuard(d.Store.Audit),
	}
}

// 默认接受的来意类别（其余拒绝；生产由用户规则配置）。
var defaultAcceptedIntent = map[string]bool{
	"商务合作": true, "产品咨询": true,
}

// Mux 挂载编排器路由（含 MCP Skill Runtime）。
func (o *Orchestrator) Mux(serviceName string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	// MCP Skill Runtime（无状态 JSON-RPC，§3.2）：POST /mcp、GET /mcp/skills
	mux.Handle("/mcp", o.Skills.Handler())
	mux.Handle("/mcp/", o.Skills.Handler())

	// 注册 Agent（能力落 store，元数据进进程表）
	mux.HandleFunc("POST /v1/agents/register", func(w http.ResponseWriter, r *http.Request) {
		var req RegisterAgentRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Meta.AgentID == "" {
			writeErr(w, http.StatusBadRequest, "agentId required")
			return
		}
		req.Cap.AgentID = req.Meta.AgentID
		if err := o.Store.Capability.UpsertCapability(r.Context(), req.Cap); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		o.mu.Lock()
		o.meta[req.Meta.AgentID] = req.Meta
		o.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agentId": req.Meta.AgentID})
	})

	// 通用门控检查
	mux.HandleFunc("POST /v1/gate/check", func(w http.ResponseWriter, r *http.Request) {
		var req GateCheckRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cap, err := o.Store.Capability.GetCapability(r.Context(), req.AgentID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		dec := cap.Check(trustzone.CallContext{
			SkillID: req.SkillID, Scope: req.Scope, TrustLevel: trustzone.Level(req.TrustLevel),
			AmountCents: req.AmountCents, TargetUID: req.TargetUID,
		})
		// v0.7.1：蒸馏闭环——被收紧的决策类型强制要求审批（代码层强制，非提示词）
		distilled := false
		if o.Distill != nil && o.Distill.RequireApproval(req.AgentID, req.SkillID) {
			distilled = true
			dec.RequiresApproval = true
		}
		code := http.StatusOK
		if !dec.Allowed && !dec.RequiresApproval {
			code = http.StatusForbidden
		} else if dec.RequiresApproval {
			code = http.StatusAccepted
		}
		// v0.6.1：自动读取该 Agent 的语义记忆（偏好/底线），作为决策上下文返回
		resp := map[string]any{"decision": dec.String(), "reason": dec.Reason, "distilled": distilled}
		if distilled && o.Distill != nil {
			resp["distillReason"] = o.Distill.TightenReason(req.AgentID, req.SkillID)
		}
		if o.Memory != nil && req.UserIDForMemory > 0 {
			sc := memory.Scope{UserID: req.UserIDForMemory, AgentID: req.AgentID}
			if facts, ferr := o.Memory.ListFacts(r.Context(), sc); ferr == nil && len(facts) > 0 {
				prefs := map[string]string{}
				for _, f := range facts {
					prefs[f.Key] = f.Value
				}
				resp["memory"] = prefs
			}
		}
		writeJSON(w, code, resp)
	})

	// 陌生人来意端到端：合规 → 来意规则 → 门控 → 占位 → 协商 → 审批卡
	mux.HandleFunc("POST /v1/stranger/intent", func(w http.ResponseWriter, r *http.Request) {
		var req StrangerIntentRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.ReceiverID == 0 || req.StrangerAgent == "" || req.Content == "" {
			writeErr(w, http.StatusBadRequest, "receiverId/strangerAgentId/content required")
			return
		}
		// 1) 合规护栏（原始内容机审）
		if dec := o.guard.Check(id.UUIDv4(), req.Content); !dec.Allowed {
			_ = o.Store.Audit.Write(audit.Entry{AgentID: req.StrangerAgent, UserID: req.ReceiverID,
				Action: audit.ActionGateDeny, Decision: "compliance:" + dec.Reason})
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"state": "REJECTED", "stage": "compliance", "reason": dec.Reason})
			return
		}
		// 2) 来意规则（Agent 预审）
		if !defaultAcceptedIntent[req.IntentCategory] {
			_ = o.Store.Audit.Write(audit.Entry{AgentID: "stranger-filter-agent", UserID: req.ReceiverID,
				Action: audit.ActionGateDeny, Decision: "intent rejected: " + req.IntentCategory})
			writeJSON(w, http.StatusOK, map[string]any{
				"state": "REJECTED", "stage": "intent_rule", "reason": "来意类别不被接受: " + req.IntentCategory})
			return
		}
		// 3) 协商会话（陌生人 Agent ↔ 本方过滤 Agent）
		negID := "neg-" + id.UUIDv4()
		sess := negotiation.NewSession(negID, negotiation.DefaultMaxRounds, 60000)
		sess.Deadline = time.Now().Add(30 * time.Minute)
		_, _ = sess.Apply(negotiation.Turn{
			SessionID: negID, FromAgentID: req.StrangerAgent, ToAgentID: "stranger-filter-agent",
			Intent:  negotiation.IntentPropose,
			Payload: map[string]any{"category": req.IntentCategory, "content": req.Content},
		})
		sess.State = negotiation.StateAuthRequired // 预审通过 → 需本人审批
		if err := o.Store.Negotiation.SaveSession(r.Context(), sess); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 4) 付费占位（预审之后，幂等键 messageId；pending 不实际扣款）
		messageID := id.UUIDv4()
		mode := ledger.ModeReplyCharge
		if req.BillingMode == 1 {
			mode = ledger.ModeSendCharge
		}
		entry, err := o.Store.Ledger.Reserve(messageID, req.ReceiverID, req.ReceiverID, mode, req.AmountCents)
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		// 5) 审批卡片（陌生人付费消息）
		payload := map[string]any{
			"messageId": messageID, "negotiationId": negID,
			"category": req.IntentCategory, "content": req.Content,
			"billingMode": int(mode), "amountCents": req.AmountCents,
			"strangerAgent": req.StrangerAgent,
		}
		// v0.7.6：若该类来意已被蒸馏收紧，在卡片上标注历史（前端展示）
		if o.Distill != nil {
			key := "intent:" + req.IntentCategory
			if o.Distill.RequireApproval("stranger-filter-agent", key) {
				payload["distillNote"] = o.Distill.TightenReason("stranger-filter-agent", key)
			}
		}
		ticket, err := o.Engine.CreateTicket(req.ReceiverID, "stranger-filter-agent", approval.TicketPayment,
			payload, time.Now().Add(30*time.Minute))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		sess.BindApprovalTicket(ticket.ID)
		_ = o.Store.Negotiation.SaveSession(r.Context(), sess)
		_ = o.Store.Audit.Write(audit.Entry{MessageID: messageID, AgentID: "stranger-filter-agent",
			UserID: req.ReceiverID, Action: audit.ActionGateApproval, Decision: "awaiting user",
			Detail: map[string]any{"ticketId": ticket.ID, "negotiationId": negID}})
		writeJSON(w, http.StatusCreated, map[string]any{
			"state": "PENDING_APPROVAL", "stage": "approval_card",
			"ticketId": ticket.ID, "messageId": messageID,
			"negotiationId": negID, "ledgerStatus": int(entry.Status),
		})
	})

	// 本人决策陌生来意卡片：approve（查看）/ reply（回复才扣费）/ reject
	mux.HandleFunc("POST /v1/orchestrator/tickets/{id}/decide", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req DecideIntentRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		t, err := o.Engine.Get(id)
		if err != nil {
			approvalErr(w, err)
			return
		}
		messageID, _ := t.Payload["messageId"].(string)
		intentCategory, _ := t.Payload["category"].(string)
		// v0.6.1：决策后自动追加情景记忆（办事轨迹闭环）
		recordEpisode := func(result, summary string) {
			if o.Memory == nil {
				return
			}
			_ = o.Memory.AppendEpisode(r.Context(),
				memory.Scope{UserID: t.UserID, AgentID: "stranger-filter-agent"},
				&memory.Episode{
					Kind:    "approval",
					Summary: summary,
					Result:  result,
					Tags:    []string{"stranger_intent"},
					Meta:    map[string]any{"ticketId": id, "messageId": messageID, "by": req.By},
				})
		}
		// v0.7.1：审批决策自动写蒸馏反馈（正=通过，负=驳回）
		recordDistill := func(delta float64) {
			if o.Distill == nil || intentCategory == "" {
				return
			}
			o.Distill.Record(t.UserID, "stranger-filter-agent", "intent:"+intentCategory, delta)
		}
		switch req.Decision {
		case "approve":
			// 发送即扣费：审批通过即扣款；回复才扣费：保持 pending 待 reply
			if _, err := o.Engine.Approve(id, defaultBy(req.By), req.Note); err != nil {
				approvalErr(w, err)
				return
			}
			mode := toInt(t.Payload["billingMode"])
			if mode == int(ledger.ModeSendCharge) {
				if _, err := o.Store.Ledger.Charge(messageID, id); err != nil {
					writeErr(w, http.StatusConflict, err.Error())
					return
				}
			}
			recordEpisode("approved", "用户通过陌生来意卡片")
			recordDistill(+0.1)
			writeJSON(w, http.StatusOK, map[string]any{"state": "APPROVED", "ticketId": id, "messageId": messageID})
		case "reply":
			// 回复才扣费：本人回复时扣款（幂等）
			if _, err := o.Store.Ledger.Charge(messageID, id); err != nil {
				writeErr(w, http.StatusConflict, err.Error())
				return
			}
			if _, err := o.Engine.Approve(id, defaultBy(req.By), req.Note); err != nil {
				approvalErr(w, err)
				return
			}
			if _, err := o.Engine.BeginExecuting(id, defaultBy(req.By)); err != nil {
				approvalErr(w, err)
				return
			}
			done, err := o.Engine.Complete(id, defaultBy(req.By))
			if err != nil {
				approvalErr(w, err)
				return
			}
			recordEpisode("replied", "用户回复并扣费")
			recordDistill(+0.1)
			writeJSON(w, http.StatusOK, map[string]any{"state": string(done.State), "ticketId": id, "messageId": messageID})
		case "reject":
			if _, err := o.Engine.Reject(id, defaultBy(req.By), req.Note); err != nil {
				approvalErr(w, err)
				return
			}
			// pending 占位不实际扣款，无需退款；审计记录拒绝
			_ = o.Store.Audit.Write(audit.Entry{MessageID: messageID, UserID: t.UserID,
				Action: audit.ActionReject, Decision: "user rejected stranger intent"})
			recordEpisode("rejected", "用户驳回陌生来意")
			recordDistill(-0.2)
			writeJSON(w, http.StatusOK, map[string]any{"state": "REJECTED", "ticketId": id, "messageId": messageID})
		default:
			writeErr(w, http.StatusBadRequest, "decision must be approve/reply/reject")
		}
	})

	// 审计轨迹回放（内存实现；PG 实现直接查 audit_events）
	mux.HandleFunc("GET /v1/audit", func(w http.ResponseWriter, r *http.Request) {
		mem, ok := o.Store.Audit.(*audit.MemorySink)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"note": "audit sink 非内存实现，请查询 audit_events 表", "events": []any{}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": mem.Entries()})
	})

	return mux
}

// toInt 从 payload 中安全取整：内存存储为 int，经 JSONB/HTTP 往返后为 float64。
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
