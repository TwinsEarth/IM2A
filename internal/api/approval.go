// approval-engine HTTP 端点：审批单生命周期、事件溯源、超时升级。
package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"human-agent-im/internal/approval"
	"human-agent-im/internal/audit"
)

// CreateApprovalRequest 创建审批单。
type CreateApprovalRequest struct {
	UserID        uint64         `json:"userId"`
	SourceAgentID string         `json:"sourceAgentId"`
	Type          string         `json:"type"` // NEGOTIATION/PAYMENT/SKILL_CALL/EXTERNAL_ACTION
	Payload       map[string]any `json:"payload"`
	DeadlineUnix  int64          `json:"deadlineUnix"` // 0 用默认 30 分钟
}

// DecideRequest 审批决策。
type DecideRequest struct {
	By    string         `json:"by"`
	Note  string         `json:"note"`
	Patch map[string]any `json:"patch,omitempty"` // modify 时使用
}

// ApprovalMux 挂载 approval-engine 路由。
func (d *Deps) ApprovalMux(serviceName string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	mux.HandleFunc("POST /v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		var req CreateApprovalRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 || req.SourceAgentID == "" {
			writeErr(w, http.StatusBadRequest, "userId and sourceAgentId required")
			return
		}
		tt := approval.TicketType(req.Type)
		if tt == "" {
			tt = approval.TicketNegotiation
		}
		deadline := time.Now().Add(30 * time.Minute)
		if req.DeadlineUnix > 0 {
			deadline = time.Unix(req.DeadlineUnix, 0)
		}
		t, err := d.Engine.CreateTicket(req.UserID, req.SourceAgentID, tt, req.Payload, deadline)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = d.Store.Audit.Write(audit.Entry{UserID: req.UserID, AgentID: req.SourceAgentID,
			Action: audit.ActionEscalate, Decision: "CREATED", Detail: map[string]any{"ticketId": t.ID}})
		writeJSON(w, http.StatusCreated, ticketJSON(t))
		// v1.1.2：WS 推送审批卡片
		d.Stream.Broadcast("approval:"+strconv.FormatUint(t.UserID, 10), map[string]any{
			"type": "approval_card", "ticket": ticketJSON(t),
		})
	})

	mux.HandleFunc("POST /v1/approvals/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var req DecideRequest
		_ = decode(r, &req)
		t, err := d.Engine.Approve(r.PathValue("id"), defaultBy(req.By), req.Note)
		if err != nil {
			approvalErr(w, err)
			return
		}
		_ = d.Store.Audit.Write(audit.Entry{UserID: t.UserID, AgentID: t.SourceAgentID,
			MessageID: t.ID, Action: audit.ActionApprove, Decision: "APPROVED"})
		// v1.6 审查修复：手机端审批同步落权威表 ticket_state + SSE，与手表/离线多端一致
		if d.DB != nil {
			d.applyTicketDecision(strconv.FormatUint(t.UserID, 10), t.ID, "approved", "phone", 0)
		}
		resp := ticketJSON(t)
		writeJSON(w, http.StatusOK, resp)
		d.Stream.Broadcast("approval:"+strconv.FormatUint(t.UserID, 10), map[string]any{
			"type": "approval_decision", "ticket": resp,
		})
		// v1.2.0：审批通过 → 恢复绑定的协商会话
		if t.Payload != nil {
			if nid, ok := t.Payload["negotiationId"].(string); ok && nid != "" {
				if sess, err := d.Store.Negotiation.LoadSession(r.Context(), nid); err == nil {
					// 协商从 AUTH_REQUIRED 恢复到 WORKING
					sess.State = "WORKING"
					_ = d.Store.Negotiation.SaveSession(r.Context(), sess)
					d.Stream.Broadcast(nid, map[string]any{
						"type": "negotiation_resumed", "reason": "approved", "session": sessionJSON(sess),
					})
				}
			}
		}
	})

	mux.HandleFunc("POST /v1/approvals/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		var req DecideRequest
		_ = decode(r, &req)
		t, err := d.Engine.Reject(r.PathValue("id"), defaultBy(req.By), req.Note)
		if err != nil {
			approvalErr(w, err)
			return
		}
		_ = d.Store.Audit.Write(audit.Entry{UserID: t.UserID, AgentID: t.SourceAgentID,
			MessageID: t.ID, Action: audit.ActionReject, Decision: "REJECTED"})
		// v1.6 审查修复：手机端驳回同步落权威表 ticket_state
		if d.DB != nil {
			d.applyTicketDecision(strconv.FormatUint(t.UserID, 10), t.ID, "rejected", "phone", 0)
		}
		writeJSON(w, http.StatusOK, ticketJSON(t))
	})

	mux.HandleFunc("POST /v1/approvals/{id}/modify", func(w http.ResponseWriter, r *http.Request) {
		var req DecideRequest
		_ = decode(r, &req)
		t, err := d.Engine.Modify(r.PathValue("id"), req.Patch, defaultBy(req.By), req.Note)
		if err != nil {
			approvalErr(w, err)
			return
		}
		resp := ticketJSON(t)
		writeJSON(w, http.StatusOK, resp)
		d.Stream.Broadcast("approval:"+strconv.FormatUint(t.UserID, 10), map[string]any{
			"type": "approval_decision", "ticket": resp,
		})
		// v1.2.2：修改 → 协商恢复 + patch 同步
		if t.Payload != nil {
			if nid, ok := t.Payload["negotiationId"].(string); ok && nid != "" {
				if sess, e := d.Store.Negotiation.LoadSession(r.Context(), nid); e == nil {
					sess.State = "WORKING"
					_ = d.Store.Negotiation.SaveSession(r.Context(), sess)
					d.Stream.Broadcast(nid, map[string]any{
						"type": "negotiation_resumed", "reason": "modified", "patch": req.Patch,
					})
				}
			}
		}
	})

	mux.HandleFunc("POST /v1/approvals/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req DecideRequest
		_ = decode(r, &req)
		if _, err := d.Engine.BeginExecuting(id, defaultBy(req.By)); err != nil {
			approvalErr(w, err)
			return
		}
		t, err := d.Engine.Complete(id, defaultBy(req.By))
		if err != nil {
			approvalErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ticketJSON(t))
	})

	mux.HandleFunc("GET /v1/approvals/pending", func(w http.ResponseWriter, r *http.Request) {
		user := atou(r.URL.Query().Get("user"))
		list, err := d.Engine.ListPending(user)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, t := range list {
			out = append(out, ticketJSON(t))
		}
		writeJSON(w, http.StatusOK, map[string]any{"tickets": out})
	})

	mux.HandleFunc("GET /v1/approvals/history", func(w http.ResponseWriter, r *http.Request) {
		user := r.URL.Query().Get("userId")
		if user == "" {
			writeErr(w, 400, "userId required")
			return
		}
		// v1.2.4：返回所有审批单（不限 PENDING）
		list, _ := d.Engine.ListPending(0)
		out := make([]map[string]any, 0)
		for _, t := range list {
			out = append(out, ticketJSON(t))
		}
		writeJSON(w, 200, map[string]any{"tickets": out, "total": len(out)})
	})

	mux.HandleFunc("GET /v1/approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		t, err := d.Engine.Get(r.PathValue("id"))
		if err != nil {
			approvalErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ticketJSON(t))
	})

	// 超时扫描：由定时器/运维周期调用，30 分钟→手表，2 小时→短信。
	mux.HandleFunc("POST /v1/approvals/timeouts", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		escalated, err := d.Engine.CheckTimeouts(now)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]map[string]any, 0, len(escalated))
		for _, t := range escalated {
			out = append(out, map[string]any{"ticketId": t.ID, "escalation": t.Escalation,
				"note": t.EscalationNote})
		}
		writeJSON(w, http.StatusOK, map[string]any{"escalated": out, "count": len(out)})
	})

	return mux
}

func approvalErr(w http.ResponseWriter, err error) {
	if errors.Is(err, approval.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, approval.ErrInvalidTransition) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeErr(w, http.StatusBadRequest, err.Error())
}

func defaultBy(by string) string {
	if by == "" {
		return "user"
	}
	return by
}

func ticketJSON(t *approval.Ticket) map[string]any {
	events := make([]map[string]any, 0, len(t.Events))
	for _, e := range t.Events {
		events = append(events, map[string]any{
			"type": string(e.Type), "by": e.By, "note": e.Note, "atUnix": e.At.Unix(),
		})
	}
	return map[string]any{
		"id": t.ID, "userId": t.UserID, "sourceAgentId": t.SourceAgentID,
		"type": string(t.Type), "state": string(t.State), "payload": t.Payload,
		"escalation": t.Escalation, "escalationNote": t.EscalationNote,
		"createdAtUnix": t.CreatedAt.Unix(), "deadlineUnix": deadlineUnix(t.Deadline),
		"events": events,
	}
}

func deadlineUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
