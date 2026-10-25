// SPDX-License-Identifier: MIT
//
// relation 域：信任矩阵 / Agent 目录 / 体验。
package routes

func registerRelation(r *Registry, c *routeCtx) {
	// 各服务路由（按前缀分发，经过中间件）
	r.Add("", "/v1/trust/", c.deps.RelationMux("relation-svc"), c.wrap)
	r.Add("", "/v1/agents/", c.deps.RelationMux("relation-svc"), c.wrap)
	r.Add("", "/v1/directory/", c.deps.DirectoryMux(), c.wrap)
	r.Add("", "/v1/exp/", c.deps.ExperienceMux(), c.wrap)
}
