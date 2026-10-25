// SPDX-License-Identifier: MIT
//
// payment 域：付费 / 付费流水 / 合规 / 台账。
package routes

func registerPayment(r *Registry, c *routeCtx) {
	r.Add("", "/v1/paid/", c.deps.PaymentMux("payment-svc"), c.wrap)
	r.Add("", "/v1/paidflow/", c.deps.PaidFlowMux("paid-flow-svc"), c.wrap)
	r.Add("", "/v1/compliance/", c.deps.ComplianceMux("compliance-gate"), c.wrap)
	r.Add("", "/v1/ledger/", c.deps.PaidMux(), c.wrap)
}
