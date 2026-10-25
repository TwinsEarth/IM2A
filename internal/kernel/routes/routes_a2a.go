// SPDX-License-Identifier: MIT
//
// a2a 域：A2A 协议入口 + 编排器（orchestrator）与 MCP 特殊路由。
package routes

import "human-agent-im/internal/api"

func registerA2A(r *Registry, c *routeCtx) {
	r.Add("", "/v1/a2a/", c.deps.A2AMux(), c.wrap)

	// 编排器（特殊路由）
	orch := api.NewOrchestrator(*c.deps)
	r.Add("", "/v1/agents/register", orch.Mux("agent-orchestrator"), c.wrap)
	r.Add("", "/v1/gate/", orch.Mux("agent-orchestrator"), c.wrap)
	r.Add("", "/v1/stranger/", orch.Mux("agent-orchestrator"), c.wrap)
	r.Add("", "/v1/orchestrator/", orch.Mux("agent-orchestrator"), c.wrap)
	r.Add("", "/v1/audit", orch.Mux("agent-orchestrator"), c.wrap)
	r.Add("", "/mcp", orch.Mux("agent-orchestrator"), c.wrap)
	r.Add("", "/mcp/", orch.Mux("agent-orchestrator"), c.wrap)
	r.Add("", "/mcp/skills", orch.Mux("agent-orchestrator"), c.wrap)
}
