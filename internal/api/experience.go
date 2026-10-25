package api

import (
	"encoding/json"
	"fmt"
	"human-agent-im/internal/experience"
	"net/http"
)

func (d *Deps) ExperienceMux() http.Handler {
	svc := experience.New(d.DB)
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	// 人类审批决策 → 案例层
	mux.HandleFunc("POST /v1/exp/cases", func(w http.ResponseWriter, r *http.Request) {
		var c experience.Case
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			write(w, 400, map[string]any{"error": "bad json"})
			return
		}
		id, err := svc.RecordCase(r.Context(), &c)
		if err != nil {
			write(w, 400, map[string]any{"error": err.Error()})
			return
		}
		write(w, 201, map[string]any{"id": id, "layer": "case"})
	})
	mux.HandleFunc("GET /v1/exp/cases", func(w http.ResponseWriter, r *http.Request) {
		list, err := svc.ListCases(r.Context(), r.URL.Query().Get("user"))
		if err != nil {
			write(w, 500, map[string]any{"error": err.Error()})
			return
		}
		write(w, 200, map[string]any{"cases": list, "count": len(list)})
	})
	// 蒸馏提案
	mux.HandleFunc("POST /v1/exp/patterns/propose", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Text    string  `json:"text"`
			CaseIDs []int64 `json:"caseIds"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		id, err := svc.ProposePattern(r.Context(), b.Text, b.CaseIDs)
		if err != nil {
			write(w, 400, map[string]any{"error": err.Error()})
			return
		}
		write(w, 201, map[string]any{"id": id, "status": "proposed", "approved": false})
	})
	mux.HandleFunc("POST /v1/exp/patterns/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var id int64
		// id 取自路径（approve 无需请求体）
		pathID := r.PathValue("id")
		if _, err := fmt.Sscanf(pathID, "%d", &id); err != nil {
			write(w, 400, map[string]any{"error": "bad id"})
			return
		}
		if err := svc.ApprovePattern(r.Context(), id); err != nil {
			write(w, 400, map[string]any{"error": err.Error()})
			return
		}
		write(w, 200, map[string]any{"id": id, "approvedByHuman": true})
	})
	mux.HandleFunc("GET /v1/exp/patterns", func(w http.ResponseWriter, r *http.Request) {
		list, err := svc.EffectivePatterns(r.Context())
		if err != nil {
			write(w, 500, map[string]any{"error": err.Error()})
			return
		}
		write(w, 200, map[string]any{"patterns": list, "count": len(list)})
	})
	// Propose-Commit-Audit
	mux.HandleFunc("POST /v1/exp/audit/propose", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			UserID   string `json:"userId"`
			Proposal string `json:"proposal"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		id, err := svc.Propose(r.Context(), b.UserID, b.Proposal)
		if err != nil {
			write(w, 400, map[string]any{"error": err.Error()})
			return
		}
		write(w, 201, map[string]any{"id": id, "phase": "propose"})
	})
	mux.HandleFunc("POST /v1/exp/audit/{id}/finalize", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			CommitVerdict string `json:"commitVerdict"`
			AuditVerdict  string `json:"auditVerdict"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		var id int64
		if n, _ := fmt.Sscanf(r.PathValue("id"), "%d", &id); n != 1 {
			write(w, 400, map[string]any{"error": "bad id"})
			return
		}
		if err := svc.FinalAudit(r.Context(), id, b.CommitVerdict, b.AuditVerdict); err != nil {
			write(w, 400, map[string]any{"error": err.Error()})
			return
		}
		rec, _ := svc.GetAudit(r.Context(), id)
		write(w, 200, rec)
	})
	return mux
}
