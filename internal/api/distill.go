package api

import (
	"net/http"
	"strconv"

	"human-agent-im/internal/distill"
)

// DistillMux v0.7 蒸馏闭环 HTTP 端点。
//
// 路线：
//
//	GET  /v1/distill/stats                 全局统计（master 查看）
//	GET  /v1/distill/feedback?agentId=...  某 Agent 的反馈明细
//	GET  /v1/distill/require?agentId=...&key=...  查询是否强制审批
//	POST /v1/distill/reset                 master 手动解除收紧（回滚）
func (d *Deps) DistillMux(service string) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/distill/stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, d.Distill.GetStats())
	})

	mux.HandleFunc("GET /v1/distill/feedback", func(w http.ResponseWriter, r *http.Request) {
		agent := r.URL.Query().Get("agentId")
		if agent == "" {
			writeErr(w, http.StatusBadRequest, "agentId required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agentId":  agent,
			"feedback": d.Distill.Feedback(agent),
		})
	})

	mux.HandleFunc("GET /v1/distill/require", func(w http.ResponseWriter, r *http.Request) {
		agent := r.URL.Query().Get("agentId")
		key := r.URL.Query().Get("key")
		if agent == "" || key == "" {
			writeErr(w, http.StatusBadRequest, "agentId/key required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agentId":         agent,
			"key":             key,
			"requireApproval": d.Distill.RequireApproval(agent, key),
		})
	})

	mux.HandleFunc("POST /v1/distill/reset", func(w http.ResponseWriter, r *http.Request) {
		// 简化：master 调用，agentId+key 在 body
		var req struct {
			AgentID string `json:"agentId"`
			Key     string `json:"key"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := d.Distill.Reset(req.AgentID, req.Key); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// v0.7.3：查询/修改蒸馏规则参数（master）
	mux.HandleFunc("GET /v1/distill/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, d.Distill.GetConfig())
	})
	mux.HandleFunc("POST /v1/distill/config", func(w http.ResponseWriter, r *http.Request) {
		var cfg distill.Config
		if err := decode(r, &cfg); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		d.Distill.SetConfig(cfg)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": d.Distill.GetConfig()})
	})

	// v0.7.3：master 手动注入反馈（冷启动期/修正）
	mux.HandleFunc("POST /v1/distill/feedback", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID  uint64  `json:"userId"`
			AgentID string  `json:"agentId"`
			Key     string  `json:"key"`
			Delta   float64 `json:"delta"` // 正=强化，负=弱化
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.AgentID == "" || req.Key == "" {
			writeErr(w, http.StatusBadRequest, "agentId/key required")
			return
		}
		d.Distill.Record(req.UserID, req.AgentID, req.Key, req.Delta)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":              true,
			"requireApproval": d.Distill.RequireApproval(req.AgentID, req.Key),
			"reason":          d.Distill.TightenReason(req.AgentID, req.Key),
		})
	})

	// v0.7.4：按用户列出/导出/导入蒸馏反馈（数据主权）
	mux.HandleFunc("GET /v1/distill/list", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		if uid == 0 {
			writeErr(w, http.StatusBadRequest, "userId required")
			return
		}
		writeJSON(w, http.StatusOK, d.Distill.FeedbackByUser(uid))
	})
	mux.HandleFunc("GET /v1/distill/export", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		if uid == 0 {
			writeErr(w, http.StatusBadRequest, "userId required")
			return
		}
		writeJSON(w, http.StatusOK, d.Distill.ExportUser(uid))
	})
	mux.HandleFunc("POST /v1/distill/import", func(w http.ResponseWriter, r *http.Request) {
		var list []*distill.DecisionFeedback
		if err := decode(r, &list); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		d.Distill.ImportUser(list)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "imported": len(list)})
	})

	// v0.7.5：蒸馏汇总报表（master 视角）
	mux.HandleFunc("GET /v1/distill/report", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		if uid == 0 {
			writeErr(w, http.StatusBadRequest, "userId required")
			return
		}
		writeJSON(w, http.StatusOK, d.Distill.ReportUser(uid))
	})

	// v0.7.6：按用户批量重置收紧（master 清理）
	mux.HandleFunc("POST /v1/distill/reset/user", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		if uid == 0 {
			writeErr(w, http.StatusBadRequest, "userId required")
			return
		}
		n := d.Distill.ResetUser(uid)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reset": n})
	})

	// 健康检查
	mux.HandleFunc("GET /healthz", HealthHandler(service))
	return mux
}
