// SPDX-License-Identifier: MIT
//
// wechat 域：微信 Bot 体系全部端点；DB 相关（MCP/webhook/chat stream/auto-nego）按
// 重构前条件（deps.DB != nil）注册。
//
// 通道分组（Wave D 插件化时整组搬迁到 channel.tencent-im / 微信通道插件）：
//   - /v1/wechatbot/、/v1/wechat/、/v1/quota/、/v1/tempgroup/、/v1/botroute/、/v1/report/
//   - DB 条件：/v2/wechat/mcp、/v1/wechat/webhook、/v1/chat/stream、/v1/wechat/auto-nego
package routes

import (
	"human-agent-im/internal/api"
)

func registerWechat(r *Registry, c *routeCtx) {
	r.Add("", "/v1/wechatbot/", c.deps.WechatV2Mux(), c.wrap) // v2.0.5 微信 Bot 协议入口
	r.Add("", "/v1/wechat/", c.deps.WechatV2Mux(), c.wrap)    // v2.0.0/v2.0.1 Bot组/识别
	r.Add("", "/v1/quota/", c.deps.WechatV2Mux(), c.wrap)     // v2.0.1 Agent 额度
	r.Add("", "/v1/tempgroup/", c.deps.WechatV2Mux(), c.wrap) // v2.0.8 临时协商群
	r.Add("", "/v1/botroute/", c.deps.WechatV2Mux(), c.wrap)  // v2.0.9 Bot 间路由
	r.Add("", "/v1/report/", c.deps.WechatV2Mux(), c.wrap)    // v2.1.0 工作汇报

	if c.deps.DB != nil {
		r.Add("", "/v2/wechat/mcp", c.kernel.MCPHandler)
		r.Add("", "/v1/wechat/webhook", c.kernel.WechatWebhookHandler)
		r.HandleFunc("", "/v1/chat/stream", api.ChatStreamHandler)
		r.Add("", "/v1/wechat/auto-nego", c.kernel.WechatAutoNegoHandler)
	}
}
