// SPDX-License-Identifier: MIT
//
// Package routes 把网关全部 HTTP 路由收敛为「按域注册」的注册表：Registry.Add 直接落到
// net/http.ServeMux（Go 1.22+ 方法路由）；本包只负责注册，不做路由匹配。
//
// 护栏（改动本包前先读）：
//  1. 中间件组合顺序必须与历史逐字一致：wrap 由内到外依次为
//     Authenticate → IPLimit → Audit → CORS → Headers → MaxBody；
//     改这个顺序等于变更对外认证/限流/安全语义，必须走 golden 对比并说明原因。
//  2. 新增或修改任何路由，必须同时在 internal/kernel/golden 增加/更新 case，
//     并跑 `go test ./internal/kernel/golden` 确认 0 diff，否则行为回归无法被发现。
package routes

import "net/http"

// Middleware 是 http.Handler 装饰器（与原 services/gateway/main.go 里 wrap/adminAuth 同型）。
type Middleware func(http.Handler) http.Handler

// Registry 是 http.ServeMux 的薄封装：只负责按 (method, path) 注册，不做路由匹配。
type Registry struct{ mux *http.ServeMux }

// New 构造注册表；mux 为 nil 时使用 http.DefaultServeMux（与 mux.Handle 行为一致）。
func New(mux *http.ServeMux) *Registry {
	if mux == nil {
		mux = http.DefaultServeMux
	}
	return &Registry{mux: mux}
}

// Add 注册一条路由。method 为空表示不限制方法（等价于 mux.Handle("/path", h)）；
// mw 按「由内到外」应用：Add(..., wrap) 等价于 mux.Handle(pattern, wrap(h))。
func (r *Registry) Add(method, path string, h http.Handler, mw ...Middleware) {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	pattern := path
	if method != "" {
		pattern = method + " " + path
	}
	r.mux.Handle(pattern, h)
}

// HandleFunc 是 Add 的 http.HandlerFunc 便利版本。
func (r *Registry) HandleFunc(method, path string, h http.HandlerFunc, mw ...Middleware) {
	r.Add(method, path, h, mw...)
}
