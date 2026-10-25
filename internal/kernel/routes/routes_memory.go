// SPDX-License-Identifier: MIT
//
// memory 域：Context 三层记忆（含端侧加密导入导出）/ 蒸馏闭环。
package routes

import "net/http"

func registerMemory(r *Registry, c *routeCtx) {
	// Context 记忆（v0.6）：三层记忆 + 子 Agent 隔离（加密上下文 v1.5）
	r.Add("POST", "/v1/memory/encrypted", http.HandlerFunc(c.deps.PutEncryptedContext), c.wrap)
	r.Add("GET", "/v1/memory/encrypted", http.HandlerFunc(c.deps.GetEncryptedContext), c.wrap)
	r.Add("GET", "/v1/memory/encrypted/export", http.HandlerFunc(c.deps.ExportEncryptedContext), c.wrap)
	r.Add("POST", "/v1/memory/encrypted/import", http.HandlerFunc(c.deps.ImportEncryptedContext), c.wrap)
	r.Add("", "/v1/memory/", c.deps.MemoryMux("memory-svc"), c.wrap)

	// 蒸馏闭环（v0.7）：审批反馈→阈值自适应
	r.Add("", "/v1/distill/", c.deps.DistillMux("distill-svc"), c.wrap)
	// v0.7 蒸馏反馈（方法不限定，与重构前 mux.Handle 一致）
	r.Add("", "/v1/distill/feedback", c.deps.LLMDistillMux(), c.wrap)
}
