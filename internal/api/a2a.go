package api

import (
	"encoding/json"
	"net/http"

	"human-agent-im/internal/a2a"
)

func (d *Deps) A2AMux() http.Handler {
	svc := a2a.New(d.DB)
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /v1/a2a/send", func(w http.ResponseWriter, r *http.Request) {
		var env a2a.Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			write(w, 400, map[string]any{"error": "bad json: " + err.Error()})
			return
		}
		already, err := svc.Send(r.Context(), &env)
		if err != nil {
			write(w, 400, map[string]any{"error": err.Error()})
			return
		}
		write(w, 201, map[string]any{"msgId": env.MsgID, "already": already, "channel": "agent_negotiation"})
	})
	mux.HandleFunc("GET /v1/a2a/tasks", func(w http.ResponseWriter, r *http.Request) {
		agent := r.URL.Query().Get("agent")
		if agent == "" {
			write(w, 400, map[string]any{"error": "missing agent"})
			return
		}
		list, err := svc.Inbox(r.Context(), agent, 50)
		if err != nil {
			write(w, 500, map[string]any{"error": err.Error()})
			return
		}
		write(w, 200, map[string]any{"agent": agent, "tasks": list, "count": len(list)})
	})
	mux.HandleFunc("POST /v1/a2a/deliver", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MsgID string `json:"msgId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MsgID == "" {
			write(w, 400, map[string]any{"error": "missing msgId"})
			return
		}
		noRows, err := svc.Deliver(r.Context(), body.MsgID)
		if err != nil {
			write(w, 500, map[string]any{"error": err.Error()})
			return
		}
		write(w, 200, map[string]any{"msgId": body.MsgID, "delivered": !noRows, "alreadyDelivered": noRows})
	})
	return mux
}
