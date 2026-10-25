// SPDX-License-Identifier: MIT
//
// im 域：IM 网关（含 webhook 独立验签分支）/ 离线推送 / 客服支持。
//
// 通道分组（D8-B：人类 IM 走腾讯云 PaaS）：
//   - channel: tencent-im —— /v1/im/*（人类消息投递，凭据缺失时降级 4xx/503，不影响网关启动）
//   - /v1/push/ 为离线推送管理面，/v1/support/ 为客服面（与具体通道弱耦合）
package routes

import (
	"net/http"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/ratelimit"
)

func registerIM(r *Registry, c *routeCtx) {
	// v1.2.2：腾讯云服务器到服务器回调不携带用户 Bearer；webhook 走独立 HMAC 验签（?key 或签名头）。
	// 其余 /v1/im/ 接口仍强制用户 Token；整体保留限流。
	imH := c.deps.IMMux()
	r.Add("", "/v1/im/", ratelimit.IPLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/im/webhook" {
			imH.ServeHTTP(w, r)
			return
		}
		auth.RequireToken(imH, c.token).ServeHTTP(w, r)
	}), 100, 200))

	// v1.7.4：离线推送管理 API
	r.Add("", "/v1/push/", c.deps.PushMux(), c.wrap)
	r.Add("", "/v1/support/", c.deps.SupportMux(), c.wrap)
}
