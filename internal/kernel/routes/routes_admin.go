// SPDX-License-Identifier: MIT
//
// admin 域：运营后台页面与管理员 API（admin token / adminAuth 组合顺序不变）。
package routes

import (
	"encoding/json"
	"net/http"
	"strconv"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/kernel/webui"
	"human-agent-im/internal/masking"
	"human-agent-im/internal/privacy"
)

func registerAdmin(r *Registry, c *routeCtx) {
	// v1.7 运维
	r.Add("GET", "/v1/admin/metrics", http.HandlerFunc(c.deps.AdminMetrics), c.wrap)
	r.Add("GET", "/v1/admin/audit", http.HandlerFunc(c.deps.AdminAudit), c.wrap)
	r.Add("GET", "/v1/admin/health", http.HandlerFunc(c.deps.AdminHealth), c.wrap)

	// v1.7.6：运营后台总入口——仪表盘真实聚合（独立管理员 Token，缺省回退网关 Token）+ 页面
	adminH := auth.RequireToken(http.HandlerFunc(c.deps.AdminMux().ServeHTTP), c.adminToken)
	r.Add("", "/v1/admin/", adminH)
	r.Add("GET", "/v1/admin/security", auth.RequireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.deps.DB == nil {
			http.Error(w, "db not configured", http.StatusServiceUnavailable)
			return
		}
		list, err := c.auditor.List(r.Context(), r.URL.Query().Get("type"), 200)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{"events": list})
	}), c.adminToken))
	r.Add("GET", "/v1/egress/tickets", auth.RequireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.deps.DB == nil {
			http.Error(w, "db not configured", http.StatusServiceUnavailable)
			return
		}
		role := r.URL.Query().Get("role")
		if role == "" {
			role = masking.RoleOther // 缺省最小暴露
		}
		mk := masking.New(role)
		out, n, err := mk.EgressTickets(r.Context(), c.deps.DB, "gateway:"+mk.Role, "egress_tickets")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{"role": mk.Role, "fields_masked": n, "tickets": out})
	}), c.adminToken))

	// v1.8.6 数据主体权利（个保法 §44-47）：导出 / 删除 / 查询，管理员 token 保护。
	privacyH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.deps.DB == nil {
			http.Error(w, "db not configured", http.StatusServiceUnavailable)
			return
		}
		uid, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
		if err != nil || uid <= 0 {
			http.Error(w, "user_id required", http.StatusBadRequest)
			return
		}
		pv := privacy.New(c.deps.DB)
		reason := r.URL.Query().Get("reason")
		switch {
		case r.URL.Path == "/v1/privacy/export" && r.Method == http.MethodPost:
			data, id, err := pv.Export(r.Context(), uid, reason)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"request_id": id, "data": data})
		case r.URL.Path == "/v1/privacy/delete" && r.Method == http.MethodPost:
			report, id, err := pv.Erase(r.Context(), uid, reason)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"request_id": id, "report": report})
		case r.URL.Path == "/v1/privacy/requests" && r.Method == http.MethodGet:
			reqs, err := pv.List(r.Context(), uid, 100)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"requests": reqs, "count": len(reqs)})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	r.Add("POST", "/v1/privacy/export", privacyH, c.withAdminToken)
	r.Add("POST", "/v1/privacy/delete", privacyH, c.withAdminToken)
	r.Add("GET", "/v1/privacy/requests", privacyH, c.withAdminToken)

	// 运营后台页面（内嵌 HTML；adminAuth 判定逻辑与重构前逐行一致）
	r.Add("", "/admin/api-playground", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(webui.PlaygroundHTML))
	}), c.adminAuth)
	r.Add("", "/admin/broadcast", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(webui.BroadcastHTML))
	}), c.adminAuth)
	// 微信绑定页历史上就是公开页（不受 adminAuth 保护），保持原样。
	r.HandleFunc("", "/admin/wechat-bind", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(webui.WechatBindHTML))
	})
	r.Add("", "/admin/", c.deps.AdminPage(), c.adminAuth)
}
