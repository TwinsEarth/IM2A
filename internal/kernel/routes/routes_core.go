// SPDX-License-Identifier: MIT
//
// core 域：根页 / 健康 / 指标 / WS / 事件流 / JWT 认证 / msg 序号。
//
// 通道分组（Wave D 插件化时可整组搬迁）：
//   - channel: native（自研长连接）—— /v1/ws、/v1/ws/stats、/v1/events、/v1/msg/*（本地/Agent 消息）
//   - channel: 无关 —— /、/healthz、/metrics、/v1/auth/*
package routes

import (
	"encoding/json"
	"net/http"
	"time"

	"human-agent-im/internal/api"
	"human-agent-im/internal/auth"
	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/metrics"
	"human-agent-im/internal/ratelimit"
	"human-agent-im/internal/security"
	"human-agent-im/internal/version"
)

func registerCore(r *Registry, c *routeCtx) {
	// v1.9.9：/metrics 与 /healthz 均要求 admin token 鉴权（IM2A_ADMIN_TOKEN）
	r.Add("", "/metrics", auth.RequireToken(metrics.Handler(), c.adminProbeToken)) // Prometheus 抓取端点
	r.Add("", "/healthz", auth.RequireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.HealthHandler("gateway")(w, r)
	}), c.adminProbeToken))

	// 根路径：欢迎页（不经过认证/限流），方便用户直接打开 IP 验证
	r.HandleFunc("", "/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{
			"service": "IM2A Gateway",
			"status":  "ok",
			"version": version.Version,
			"commit":  version.GitCommit,
			"docs":    "/healthz, /v1/events, /v1/trust/, /v1/approvals/, /v1/negotiations/, /v1/paid/, /v1/compliance/, /v1/msg/, /v1/memory/, /v1/distill/, /v1/model/",
			"auth":    "Bearer token required",
		})
	})

	// v1.1.3：通用 WS 端点 /v1/ws?topic=xxx[&access_token=...]（v1.1 审查：纳入认证+限流，修复匿名越权订阅）
	// B2-a（契约 v2 §1）：按 CONN_MODE 分派——
	//   standalone → 421 + hint（长连接已迁独立 conn-gateway，内核不再承载）；
	//   embedded(缺省)/dual → 原逻辑逐字不动（dual 的镜像投递属 B2-b，本阶段不改行为）。
	r.Add("", "/v1/ws", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.connMode == bootstrap.ConnModeStandalone {
			writeJSON(w, http.StatusMisdirectedRequest, map[string]any{
				"error": "ws moved to conn-gateway",
				"hint":  "connect " + c.connHint,
			})
			return
		}
		topic := r.URL.Query().Get("topic")
		if topic == "" {
			writeJSON(w, 400, map[string]any{"error": "missing topic"})
			return
		}
		c.deps.Stream.HandleWS(topic, w, r)
	}), c.wrap)
	// v1.1.4：WS 连接统计（需认证）
	r.Add("", "/v1/ws/stats", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, c.deps.Stream.Stats())
	}), c.wrap)
	// 事件流查询（经过认证）— 用 deps.Bus（与 memory/distill/model 同源）
	r.Add("", "/v1/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events := c.deps.Bus.List()
		json.NewEncoder(w).Encode(map[string]any{"events": events, "count": len(events)})
	}), c.wrap)

	// v1.9.0：JWT 登录/me 路由
	const accessTTL = 72 * time.Hour
	// login 公开（IP 限流防爆破），按 identifier 落库并签发 JWT
	r.Add("POST", "/v1/auth/login",
		security.MaxBody(2<<20,
			security.Headers(
				security.CORS(c.corsAllowed,
					ratelimit.IPLimit(http.HandlerFunc(c.deps.AuthLogin(c.jwtSecret, accessTTL)), 20, 100)))))
	// me 只认 JWT（RequireIdentity 把 sub 注入 user_id）
	r.Add("GET", "/v1/auth/me",
		security.Headers(
			security.CORS(c.corsAllowed,
				ratelimit.IPLimit(
					auth.RequireIdentity(http.HandlerFunc(c.deps.AuthMe), c.jwtSecret), 60, 200))))

	// msg 序号服务
	r.Add("", "/v1/msg/", api.NewMsgMuxWithStore("msg-svc", c.deps.Store.Message), c.wrap)
}
