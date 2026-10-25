// SPDX-License-Identifier: MIT

package dsbotplugin

import "human-agent-im/internal/plugin"

// Attribution 返回本插件的主上游归属（DeepSeek-Bot 唯一上游），实现 plugin.Plugin。
func (p *Plugin) Attribution() plugin.Attribution { return Attribution() }

// Attribution 返回 DeepSeek-Bot 的上游归属（Apache-2.0）。
//
// Apache-2.0 义务（比 MIT 重）：保留 LICENSE 与 NOTICE 原文、显著声明修改。
// 本插件是 derivative work（移植并改写为 Go），故 Derived=true，Usage 逐条写明
// "移植了哪个文件、改成了什么、删了什么"。
func Attribution() plugin.Attribution {
	return plugin.Attribution{
		Upstream:     "https://github.com/Fei2411/DeepSeek-Bot",
		UpstreamName: "DeepSeek-Bot",
		License:      "Apache-2.0",
		// 逐字取自上游 NOTICE 第 2 行：Copyright 2026 Fei2411 and the DS Bot contributors
		Copyright:  "Copyright 2026 Fei2411 and the DS Bot contributors",
		NoticePath: "THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt",
		Derived:    true,
		Usage: []string{
			"移植 ds-bot/web-search.js（Apache-2.0）的 `auto` provider 抽象与结果解析：显式 SearchAPI/Route 候选规划、Anthropic Messages / OpenAI Responses / Gemini 三种协议响应读取、通用 JSON 源提取、Bing 跳转链接解码与 Bing/DuckDuckGo 结果页解析 → Go 实现 internal/plugin/dsbot/search.go。差异：去掉 DSH 注入（ctx.web / ctx.llm / credentials / launchEnvironment）、provider 目录与凭据 seam，改为显式 Route 配置 + 可注入 HTTP doer，便于 IM2A 编排层接入与离线测试。",
			"移植 ds-bot/src/host/text.js 的 headOf 边界处理（不切断 UTF-16 surrogate）与 docs/plans/2026-10-16-long-messages.md、ds-bot/src/host/delivery.js、constants.js 的长消息策略与阈值（MESSAGE_INLINE_MAX=20000、LONG_HEAD=1000、TEXT_KEPT=2000）→ Go 函数 Split()（internal/plugin/dsbot/chunk.go）。差异：上游把超长文本原样存成 DSH 附件、消息只保留开头 1,000 字并附说明；Go 版面向没有附件能力的微信通道，改为按字符边界把文本切成多条消息，并保留上游阈值常量作为调用方可选上限。",
			"移植 ds-bot/src/host/roster.js、permissions.js、prompt.js、constants.js 的 Bot 名册 / 角色 / 权限 / 分组语义为只读规格 RosterSpec()（internal/plugin/dsbot/roster.go）：Main Bot 最高权限与可多名、每 Bot 三种会话、Bot 字段与角色标签、DSH 权限预设继承与分组覆盖、群聊模式与上限、团队工具清单。差异：只输出 JSON 数据，不执行任何 Bot、不触碰 DSH 注入点与执行代码。",
			"移植上游 package.json 版本元数据 0.2.1（v0.2_preview）为 UpstreamVersion() 编译期常量。",
			"未移植（上游与 DSH 深度耦合的糟粕，契约 §0）：ds-bot/index.js、src/host/* 的执行代码、src/client/ 与生成物 client.js、cordis.patch.yml、app/（Electron 壳）、dev/ 测试台。本插件不运行 DSH，也不伪造其运行期状态。",
		},
	}
}
