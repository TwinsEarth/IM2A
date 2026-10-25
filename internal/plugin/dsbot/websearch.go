// SPDX-License-Identifier: MIT

// 本文件把移植的 web-search provider 接到 IM2A 已有的模型环境变量上，
// 作为 `dsbot.web-search` 能力对编排层的便捷入口（原生优先、无密钥兜底）。
package dsbotplugin

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

// WebSearch 用 IM2A 的 LLM_* 环境变量执行一次 `auto` 搜索：
// 先尝试模型路由的原生搜索（LLM_BASE_URL / LLM_API_KEY / LLM_MODEL），
// 失败再按上游策略抓取公开结果页兜底（需要出网），两条都失败返回
// *SearchUnavailableError（明确要求不要编造 URL）。
//
// 编排层也可以绕过本方法，直接用 NewSearcher + 自己会话的 Route 构造 Searcher。
func (p *Plugin) WebSearch(ctx context.Context, query string) (SearchResult, error) {
	route := Route{
		Provider: "im2a-llm",
		Model:    strings.TrimSpace(os.Getenv("LLM_MODEL")),
		API:      "openai-completions",
		BaseURL:  strings.TrimSpace(os.Getenv("LLM_BASE_URL")),
		APIKey:   os.Getenv("LLM_API_KEY"),
	}
	searcher := NewSearcher(SearcherOptions{
		Route: route,
		Doer:  &http.Client{Timeout: 20 * time.Second},
	})
	return searcher.Search(ctx, query)
}
