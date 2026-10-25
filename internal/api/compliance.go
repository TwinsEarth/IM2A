// compliance-gate HTTP 端点：统一出口合规检查（机审前置）。
package api

import (
	"net/http"

	"human-agent-im/internal/compliance"
)

// ComplianceCheckRequest 合规检查请求。
type ComplianceCheckRequest struct {
	MessageID string `json:"messageId"`
	Content   string `json:"content"`
}

// ComplianceMux 挂载 compliance-gate 路由。
func (d *Deps) ComplianceMux(serviceName string) http.Handler {
	g := compliance.NewGuard(d.Store.Audit)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	mux.HandleFunc("POST /v1/compliance/check", func(w http.ResponseWriter, r *http.Request) {
		var req ComplianceCheckRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		dec := g.Check(req.MessageID, req.Content)
		code := http.StatusOK
		if !dec.Allowed {
			code = http.StatusUnprocessableEntity
		}
		writeJSON(w, code, map[string]any{"allowed": dec.Allowed, "reason": dec.Reason})
	})

	return mux
}
