// SPDX-License-Identifier: MIT
//
// plugin 域：插件清单 / 归属公示（匿名只读）与 OpenClaw 扫码绑定前缀。
//
// 通道分组：/v1/openclaw/* 是微信个人号通道的登录/账号桥接（Wave D 时归
// channel.tencent-im 的微信通道插件）；/v1/plugins、/v1/about/attribution 属平台核心。
package routes

import "net/http"

func registerPlugin(r *Registry, c *routeCtx) {
	// 插件 / 归属公示端点（契约 §4）：匿名只读，不套认证（MIT 合规公示 + 用户自查）。
	// 只读、不含 env / token 等敏感信息；插件宿主未接线时端点回 503。
	r.Add("GET", "/v1/plugins", http.HandlerFunc(c.deps.PluginList))
	r.Add("GET", "/v1/plugins/{id}", http.HandlerFunc(c.deps.PluginGet))
	r.Add("GET", "/v1/about/attribution", http.HandlerFunc(c.deps.Attribution))

	// v2.1.2 OpenClaw 扫码绑定（OpenClaw 账号目录 watcher 的回调入口也在该前缀下）
	r.Add("", "/v1/openclaw/", c.deps.WechatV2Mux(), c.wrap)
	// A9 账号健康探测（watcher 未启动时如实返回 degraded）
	r.Add("GET", "/v1/openclaw/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.kernel.OpenClawHealth == nil {
			writeJSON(w, 200, map[string]any{"state": "degraded", "detail": "openclaw accounts dir unresolved"})
			return
		}
		writeJSON(w, 200, c.kernel.OpenClawHealth.Status())
	}), c.wrap)
}
