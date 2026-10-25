// SPDX-License-Identifier: MIT
//
// Register：装配路由注册所需的只读上下文（token/中间件链/adminAuth），再按域注册全部路由。
// 该文件对应重构前 services/gateway/main.go 里「中间件链 + 按前缀分发」的编排部分。
package routes

import (
	"encoding/json"
	"net/http"

	"human-agent-im/internal/api"
	"human-agent-im/internal/auth"
	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/kernel/webui"
	"human-agent-im/internal/ratelimit"
	"human-agent-im/internal/security"
)

// routeCtx 是注册期共享的只读上下文（与重构前 main() 中的内联变量一一对应）。
type routeCtx struct {
	kernel *bootstrap.Kernel
	deps   *api.Deps

	token           string // GATEWAY_TOKEN
	jwtSecret       string // IM2A_JWT_SECRET
	adminProbeToken string // IM2A_ADMIN_TOKEN 原值（/metrics、/healthz、/v1/ops/*、adminAuth 用）
	adminToken      string // IM2A_ADMIN_TOKEN，空则回退 GATEWAY_TOKEN（/v1/admin/* 等用）
	corsAllowed     []string
	limitRPS        int
	limitBurst      int

	// B2-a（契约 v2 §1）：/v1/ws 按模式分派；embedded/dual 走原 stream.Hub 逻辑。
	connMode string
	connHint string

	auditor   *security.Service
	wrap      Middleware
	adminAuth Middleware
}

// newRouteCtx 构造上下文；wrap/adminAuth 的组合顺序与重构前逐行一致。
func newRouteCtx(k *bootstrap.Kernel) *routeCtx {
	cfg := k.Config
	c := &routeCtx{
		kernel:          k,
		deps:            k.Deps,
		token:           cfg.GatewayToken,
		jwtSecret:       cfg.JWTSecret,
		adminProbeToken: cfg.AdminProbeToken,
		adminToken:      cfg.AdminToken,
		corsAllowed:     cfg.CorsAllowed,
		limitRPS:        cfg.LimitRPS,
		limitBurst:      cfg.LimitBurst,
		connMode:        cfg.ConnMode,
		connHint:        cfg.ConnHint,
		auditor:         security.New(k.Deps.DB),
	}
	// 中间件链（外→内）：CORS 白名单 → 安全审计落库 → 限流 → 认证 → 安全响应头 → 业务
	c.wrap = func(h http.Handler) http.Handler {
		h = auth.Authenticate(h, c.token, c.jwtSecret)
		h = ratelimit.IPLimit(h, c.limitRPS, c.limitBurst)
		h = c.auditor.Audit(h)              // 捕获 401/403/429 与正常请求，落 security_events
		h = security.CORS(c.corsAllowed, h) // 预检在认证前；非白名单来源拒绝
		h = security.Headers(h)             // 所有响应（含 401/429）均带安全头
		h = security.MaxBody(2<<20, h)      // 统一 2MiB 请求体上限，缓解内存放大 DoS
		return h
	}
	// 运营后台页面鉴权：cookie admin_token 或 ?token=，与重构前 adminAuth 判定顺序一致。
	c.adminAuth = func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, _ := r.Cookie("admin_token")
			tok := ""
			if cookie != nil {
				tok = cookie.Value
			}
			if tok == "" {
				if q := r.URL.Query().Get("token"); q != "" {
					tok = q
					http.SetCookie(w, &http.Cookie{Name: "admin_token", Value: tok, Path: "/", MaxAge: 86400})
				}
			}
			if tok == "" || tok != c.adminProbeToken {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(401)
				w.Write([]byte(webui.AdminLoginHTML))
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	return c
}

// Register 注册全部网关路由。域划分与文件一一对应；不删除、不合并任何路由。
func Register(mux *http.ServeMux, k *bootstrap.Kernel) {
	c := newRouteCtx(k)
	r := New(mux)
	registerCore(r, c)
	registerOps(r, c)
	registerAdmin(r, c)
	registerRelation(r, c)
	registerApproval(r, c)
	registerPayment(r, c)
	registerMemory(r, c)
	registerModel(r, c)
	registerIM(r, c)
	registerPlugin(r, c)
	registerWechat(r, c)
	registerA2A(r, c)
	// C1：原生通道 presence 路由（默认 NATIVE_ENABLED=0 → 不注册，404；golden 不变）。
	registerNative(r, c)
	// C5b：关闭态显式拒绝（404 NATIVE_DISABLED），保证"回滚后不是 500/裸 404 文本"。
	// 只挂 /v1/native/ 通配，不进入 Registry 计数 → golden 104/3 仍 0 diff。
	if k == nil || !k.Config.NativeEnabled {
		mux.Handle("/v1/native/", http.HandlerFunc(nativeDisabledHandler))
	}
}

// writeJSON 与重构前 main 包的 writeJSON 等价（Content-Type 不带 charset）。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// withAdminToken 把 auth.RequireToken 适配为 Middleware（使用 adminToken，非 adminProbeToken）。
func (c *routeCtx) withAdminToken(h http.Handler) http.Handler {
	return auth.RequireToken(h, c.adminToken)
}
