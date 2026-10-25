package api

import (
	"encoding/json"
	"human-agent-im/internal/agentdir"
	"net/http"
)

func (d *Deps) DirectoryMux() http.Handler {
	svc := agentdir.New(d.DB)
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /v1/directory/agents", func(w http.ResponseWriter, r *http.Request) {
		var a agentdir.Agent
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			write(w, 400, map[string]any{"error": "bad json: " + err.Error()})
			return
		}
		if a.TrustLevel == 0 {
			a.TrustLevel = 4
		} // 默认继承陌生档
		if err := svc.Upsert(r.Context(), &a); err != nil {
			write(w, 400, map[string]any{"error": err.Error()})
			return
		}
		write(w, 200, map[string]any{"agentId": a.AgentID, "ok": true, "trustLevel": a.TrustLevel})
	})
	mux.HandleFunc("GET /v1/directory/agents", func(w http.ResponseWriter, r *http.Request) {
		owner := r.URL.Query().Get("owner")
		if owner == "" {
			write(w, 400, map[string]any{"error": "missing owner"})
			return
		}
		list, err := svc.ListByOwner(r.Context(), owner)
		if err != nil {
			write(w, 500, map[string]any{"error": err.Error()})
			return
		}
		write(w, 200, map[string]any{"owner": owner, "agents": list, "count": len(list)})
	})
	mux.HandleFunc("GET /v1/directory/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		a, err := svc.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			write(w, 404, map[string]any{"error": err.Error()})
			return
		}
		write(w, 200, a)
	})
	mux.HandleFunc("PATCH /v1/directory/agents/{id}/attention", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Level int `json:"level"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			write(w, 400, map[string]any{"error": "bad json"})
			return
		}
		if err := svc.SetAttention(r.Context(), r.PathValue("id"), body.Level); err != nil {
			write(w, 400, map[string]any{"error": err.Error()})
			return
		}
		write(w, 200, map[string]any{"agentId": r.PathValue("id"), "attentionLevel": body.Level})
	})
	return mux
}
