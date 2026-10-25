// Package api: L5 模型层 HTTP 端点（v0.8）。
package api

import (
	"net/http"

	"human-agent-im/internal/model"
)

// ModelMux 挂载模型层路由。
//
//	POST /v1/model/chat   对话（端侧桩/云端兜底）
//	GET  /v1/model/info   当前模型配置
func ModelMux(serviceName string, d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	mux.HandleFunc("POST /v1/model/chat", func(w http.ResponseWriter, r *http.Request) {
		var req model.ChatRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if d.Model == nil {
			writeErr(w, http.StatusServiceUnavailable, "model not configured")
			return
		}
		resp, err := d.Model.Chat(r.Context(), &req)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("GET /v1/model/info", func(w http.ResponseWriter, r *http.Request) {
		name := "unconfigured"
		if d.Model != nil {
			name = d.Model.Name()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"provider": name,
			"edge":     "stub-v1",
			"cloud":    "openai-compatible (configured via LLM_BASE_URL/LLM_API_KEY/LLM_MODEL)",
		})
	})

	// v0.8.3：调用统计（仅 Hybrid 有统计）
	mux.HandleFunc("GET /v1/model/stats", func(w http.ResponseWriter, r *http.Request) {
		h, ok := d.Model.(*model.HybridProvider)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"note": "model is not hybrid, stats unavailable"})
			return
		}
		total, edge, cloud, fallback, promptTok, complTok := h.Stats()
		writeJSON(w, http.StatusOK, map[string]any{
			"totalCalls":       total,
			"edgeCalls":        edge,
			"cloudCalls":       cloud,
			"fallbackCalls":    fallback,
			"promptTokens":     promptTok,
			"completionTokens": complTok,
		})
	})

	// v0.8.5：配额状态
	mux.HandleFunc("GET /v1/model/quota", func(w http.ResponseWriter, r *http.Request) {
		h, ok := d.Model.(*model.HybridProvider)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"note": "model is not hybrid"})
			return
		}
		writeJSON(w, http.StatusOK, h.Quota())
	})

	// v0.8.7：运行时切换云端模型配置（不重启）
	mux.HandleFunc("POST /v1/model/cloud", func(w http.ResponseWriter, r *http.Request) {
		h, ok := d.Model.(*model.HybridProvider)
		if !ok {
			writeErr(w, http.StatusServiceUnavailable, "model is not hybrid")
			return
		}
		var body struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Model   string `json:"model"`
		}
		if err := decode(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cloud := model.NewOpenAICompatible(body.BaseURL, body.APIKey, body.Model)
		h.SetCloud(cloud)
		writeJSON(w, http.StatusOK, map[string]any{"switchedTo": cloud.Name()})
	})

	return mux
}
