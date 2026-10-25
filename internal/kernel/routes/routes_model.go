// SPDX-License-Identifier: MIT
//
// model 域：L5 模型层 / SSE / 能力与路由 / 护栏/审核。
//
// 通道分组：/v1/sse/* 属 channel: native（自研长连接）；其余为模型/护栏能力面。
package routes

import (
	"net/http"

	"human-agent-im/internal/api"
)

func registerModel(r *Registry, c *routeCtx) {
	// L5 模型层（v0.8）：端侧桩 + 云端兼容
	r.Add("", "/v1/model/", api.ModelMux("model-svc", *c.deps), c.wrap)

	r.Add("GET", "/v1/sse/", http.HandlerFunc(c.deps.SSEStream), c.wrap)
	r.Add("POST", "/v1/sse/publish", http.HandlerFunc(c.deps.SSEPublish), c.wrap)

	r.Add("", "/v1/capability/", c.deps.CapabilityMux(), c.wrap)
	r.Add("", "/v1/route/", c.deps.LLMRouterMux(), c.wrap)
	r.Add("", "/v1/intent/", c.deps.LLMIntentMux(), c.wrap)
	r.Add("", "/v1/guard/", c.deps.LLMGuardMux(), c.wrap)
	r.Add("", "/v1/moderation/", c.deps.ModerationMux(), c.wrap)
}
