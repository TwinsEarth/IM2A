// SPDX-License-Identifier: MIT
//
// approval 域：审批 / 协商。
package routes

func registerApproval(r *Registry, c *routeCtx) {
	r.Add("", "/v1/approvals", c.deps.ApprovalMux("approval-engine"), c.wrap)
	r.Add("", "/v1/approvals/", c.deps.ApprovalMux("approval-engine"), c.wrap)
	r.Add("", "/v1/negotiations", c.deps.NegotiationMux("negotiation-svc"), c.wrap)
	r.Add("", "/v1/negotiations/", c.deps.NegotiationMux("negotiation-svc"), c.wrap)
}
