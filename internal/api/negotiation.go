// negotiation-svc HTTP 端点：Agent 协商 8 态 Task、轮次/token 熔断。
package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"human-agent-im/internal/negotiation"
	"human-agent-im/internal/store"
)

// CreateNegotiationRequest 创建协商会话。
type CreateNegotiationRequest struct {
	ID          string `json:"id"`
	FromAgentID string `json:"fromAgentId"`
	ToAgentID   string `json:"toAgentId"`
	MaxRounds   int    `json:"maxRounds,omitempty"`
	TokenBudget uint64 `json:"tokenBudget,omitempty"`
}

// TurnRequest 一轮协商。
type TurnRequest struct {
	FromAgentID string         `json:"fromAgentId"`
	ToAgentID   string         `json:"toAgentId"`
	Intent      string         `json:"intent"` // PROPOSE/COUNTER/ACCEPT/REJECT/REQUEST_INFO/REQUEST_AUTH
	Payload     map[string]any `json:"payload"`
	Artifacts   []string       `json:"artifacts,omitempty"`
}

// BindApprovalRequest 绑定审批单（AUTH_REQUIRED 后）。
type BindApprovalRequest struct {
	TicketID string `json:"ticketId"`
}

// NegotiationMux 挂载 negotiation-svc 路由。
func (d *Deps) NegotiationMux(serviceName string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	mux.HandleFunc("POST /v1/negotiations", func(w http.ResponseWriter, r *http.Request) {
		var req CreateNegotiationRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.ID == "" || req.FromAgentID == "" || req.ToAgentID == "" {
			writeErr(w, http.StatusBadRequest, "id/fromAgentId/toAgentId required")
			return
		}
		maxRounds := req.MaxRounds
		if maxRounds <= 0 {
			maxRounds = negotiation.DefaultMaxRounds
		}
		s := negotiation.NewSession(req.ID, maxRounds, req.TokenBudget)
		s.Deadline = time.Now().Add(30 * time.Minute)
		if err := d.Store.Negotiation.SaveSession(r.Context(), s); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, sessionJSON(s))
		// v1.1.1：注册 WebSocket 消息处理器
		d.Stream.RegisterHandler(req.ID, func(nid string, in map[string]any) (any, error) {
			action, _ := in["action"].(string)
			if action != "turn" {
				return map[string]any{"type": "ack", "action": action}, nil
			}
			turnRaw, ok := in["turn"].(map[string]any)
			if !ok {
				return nil, errors.New("turn required")
			}
			fromAgent, _ := turnRaw["fromAgentId"].(string)
			toAgent, _ := turnRaw["toAgentId"].(string)
			intent, _ := turnRaw["intent"].(string)
			payload, _ := turnRaw["payload"].(map[string]any)
			t, err := d.Store.Negotiation.LoadSession(r.Context(), nid)
			if err != nil {
				return nil, err
			}
			state, err := t.Apply(negotiation.Turn{
				SessionID: nid, FromAgentID: fromAgent, ToAgentID: toAgent,
				Intent: negotiation.Intent(intent), Payload: payload,
				DeadlineUnix: time.Now().Add(30 * time.Minute).Unix(),
			})
			if err != nil {
				_ = d.Store.Negotiation.SaveSession(r.Context(), t)
				return map[string]any{"type": "turn_error", "error": err.Error(), "state": string(state)}, nil
			}
			_ = d.Store.Negotiation.SaveSession(r.Context(), t)
			return map[string]any{"type": "turn_applied", "session": sessionJSON(t)}, nil
		})
	})

	mux.HandleFunc("POST /v1/negotiations/{id}/turns", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s, err := d.Store.Negotiation.LoadSession(r.Context(), id)
		if err != nil {
			negotiationErr(w, err)
			return
		}
		var req TurnRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		state, err := s.Apply(negotiation.Turn{
			SessionID: id, FromAgentID: req.FromAgentID, ToAgentID: req.ToAgentID,
			Intent: negotiation.Intent(req.Intent), Payload: req.Payload, ArtifactRefs: req.Artifacts,
			DeadlineUnix: time.Now().Add(30 * time.Minute).Unix(),
		})
		if err != nil {
			// 熔断（轮次/token 预算）：上抛人工，返回 422 但保留会话状态
			_ = d.Store.Negotiation.SaveSession(r.Context(), s)
			writeErr(w, http.StatusUnprocessableEntity, err.Error()+", state="+string(state))
			return
		}
		if err := d.Store.Negotiation.SaveSession(r.Context(), s); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := sessionJSON(s)
		writeJSON(w, http.StatusOK, resp)
		// v1.1.1：WS 广播状态变更
		d.Stream.Broadcast(id, map[string]any{"type": "state_changed", "session": resp})
	})

	mux.HandleFunc("POST /v1/negotiations/{id}/bind-approval", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s, err := d.Store.Negotiation.LoadSession(r.Context(), id)
		if err != nil {
			negotiationErr(w, err)
			return
		}
		var req BindApprovalRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.BindApprovalTicket(req.TicketID)
		if err := d.Store.Negotiation.SaveSession(r.Context(), s); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp := sessionJSON(s)
		writeJSON(w, http.StatusOK, resp)
		// v1.1.1：WS 广播状态变更
		d.Stream.Broadcast(id, map[string]any{"type": "state_changed", "session": resp})
	})

	mux.HandleFunc("POST /v1/intent/precheck", func(w http.ResponseWriter, r *http.Request) { d.intentPrecheck(w, r) })
	mux.HandleFunc("POST /v1/negotiations/{id}/llm-auto", func(w http.ResponseWriter, r *http.Request) { d.llmAutoRun(w, r) })

	// v1.3.0：LLM 自动协商
	mux.HandleFunc("POST /v1/negotiations/{id}/llm-turn", func(w http.ResponseWriter, r *http.Request) {
		d.llmTurn(w, r)
	})

	mux.HandleFunc("GET /v1/negotiations/list", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		list, err := d.Store.Negotiation.ListSessions(r.Context(), limit)
		if err != nil {
			negotiationErr(w, err)
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, s := range list {
			out = append(out, sessionJSON(s))
		}
		writeJSON(w, http.StatusOK, map[string]any{"negotiations": out, "count": len(out)})
	})
	mux.HandleFunc("GET /v1/negotiations/{id}/ws", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		d.Stream.HandleWS(id, w, r)
	})
	mux.HandleFunc("GET /v1/negotiations/{id}", func(w http.ResponseWriter, r *http.Request) {
		s, err := d.Store.Negotiation.LoadSession(r.Context(), r.PathValue("id"))
		if err != nil {
			negotiationErr(w, err)
			return
		}
		resp := sessionJSON(s)
		writeJSON(w, http.StatusOK, resp)
	})

	return mux
}

func negotiationErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

func sessionJSON(s *negotiation.Session) map[string]any {
	turns := make([]map[string]any, 0, len(s.Turns))
	for _, t := range s.Turns {
		turns = append(turns, map[string]any{
			"from": t.FromAgentID, "to": t.ToAgentID,
			"intent": string(t.Intent), "payload": t.Payload, "artifacts": t.ArtifactRefs,
		})
	}
	return map[string]any{
		"id": s.ID, "state": string(s.State), "round": s.Round, "maxRounds": s.MaxRounds,
		"tokenUsed": s.TokenUsed, "tokenBudget": s.TokenBudget,
		"approvalTicketId": s.ApprovalTicketID, "turns": turns,
	}
}
