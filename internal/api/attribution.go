// SPDX-License-Identifier: MIT
//
// 第三方归属聚合端点（契约来源：PLUGIN-CONTRACT.md §4）：把已注册插件的
// Attribution() 汇总为产品级公示，JSON 供机器自检，?format=text 供人读。
package api

import (
	"fmt"
	"net/http"
	"strings"

	"human-agent-im/internal/plugin"
	"human-agent-im/internal/version"
)

// attributionProduct 产品名（契约 §4：JSON 顶层 product 固定为 IM2A）。
const attributionProduct = "IM2A"

// thirdPartyAttribution 是 /v1/about/attribution 的 thirdParty 元素，字段严格按契约 §4。
type thirdPartyAttribution struct {
	PluginID   string   `json:"pluginId"`
	Upstream   string   `json:"upstream"`
	License    string   `json:"license"`
	Copyright  string   `json:"copyright"`
	NoticePath string   `json:"noticePath"`
	Usage      []string `json:"usage"`

	// upstreamName 仅用于人读版，不进入 JSON（JSON 字段以上面标签为准）。
	upstreamName string
}

// attributionResponse 是 /v1/about/attribution 的 JSON 响应体（契约 §4）。
type attributionResponse struct {
	Product    string                  `json:"product"`
	Version    string                  `json:"version"`
	ThirdParty []thirdPartyAttribution `json:"thirdParty"`
}

// Attribution 处理 GET /v1/about/attribution[?format=text]。
// 匿名只读（MIT 合规公示）；归属数据全部来自插件 Attribution()，本层不硬编码归属字符串。
func (d *Deps) Attribution(w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Plugins == nil {
		writeErr(w, http.StatusServiceUnavailable, "plugin host not initialized")
		return
	}
	resp := buildAttribution(d.Plugins)
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "text") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(renderAttributionText(resp)))
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// attributionsProvider 是插件可选扩展（契约 §4 多上游，向后兼容）：
// 一个内嵌插件可能连带引入第二个上游（例如 OpenClaw 的微信通道插件 openclaw-weixin），
// 归属公示必须覆盖全部上游。约定：第 0 条恒等于 Attribution()。
type attributionsProvider interface {
	Attributions() []plugin.Attribution
}

// attributionRegistry 是归属聚合需要的只读宿主视图（*plugin.Host 满足）。
type attributionRegistry interface {
	Snapshots() []plugin.Snapshot
	Get(id string) (plugin.Plugin, bool)
}

// buildAttribution 从插件宿主聚合第三方归属（主上游取自快照，多上游由插件扩展展开）。
func buildAttribution(host attributionRegistry) attributionResponse {
	resp := attributionResponse{
		Product:    attributionProduct,
		Version:    version.Version,
		ThirdParty: make([]thirdPartyAttribution, 0),
	}
	for _, s := range host.Snapshots() {
		attrs := []plugin.Attribution{s.Attribution}
		if p, ok := host.Get(s.ID); ok {
			if ap, ok := p.(attributionsProvider); ok {
				if expanded := ap.Attributions(); len(expanded) > 0 {
					attrs = expanded
				}
			}
		}
		for i, a := range attrs {
			if a.Upstream == "" {
				continue // 没有上游的条目不属于第三方归属
			}
			usage := a.Usage
			if usage == nil {
				usage = []string{}
			}
			resp.ThirdParty = append(resp.ThirdParty, thirdPartyAttribution{
				PluginID:     attributionPluginID(s.ID, a, i),
				Upstream:     a.Upstream,
				License:      a.License,
				Copyright:    a.Copyright,
				NoticePath:   a.NoticePath,
				Usage:        usage,
				upstreamName: a.UpstreamName,
			})
		}
	}
	return resp
}

// attributionPluginID 给出 thirdParty 条目的稳定标识：第 0 条用插件 ID（如 openclaw），
// 其余条目用上游名（如 openclaw-weixin）；不在 API 层硬编码任何上游标识。
func attributionPluginID(snapID string, a plugin.Attribution, index int) string {
	if index == 0 {
		return snapID
	}
	if a.UpstreamName != "" {
		return a.UpstreamName
	}
	if u := strings.TrimRight(a.Upstream, "/"); u != "" {
		if i := strings.LastIndex(u, "/"); i >= 0 && i+1 < len(u) {
			return u[i+1:]
		}
		return u
	}
	return snapID
}

// renderAttributionText 生成人读版归属声明（与 JSON 同源，避免两处数据漂移）。
func renderAttributionText(resp attributionResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s — Third-Party Attribution / 第三方归属公示\n", resp.Product, resp.Version)
	b.WriteString("This build bundles the third-party projects listed below; full notice: ATTRIBUTION.md\n\n")
	if len(resp.ThirdParty) == 0 {
		b.WriteString("(no third-party components recorded)\n")
		return b.String()
	}
	for i, tp := range resp.ThirdParty {
		name := tp.upstreamName
		if name == "" {
			name = tp.PluginID
		}
		fmt.Fprintf(&b, "[%d] %s (sub-plugin %q, license %s)\n", i+1, name, tp.PluginID, tp.License)
		fmt.Fprintf(&b, "    upstream:    %s\n", tp.Upstream)
		fmt.Fprintf(&b, "    license:     %s\n", tp.License)
		fmt.Fprintf(&b, "    copyright:   %s\n", tp.Copyright)
		fmt.Fprintf(&b, "    notice file: %s\n", tp.NoticePath)
		b.WriteString("    used for:\n")
		for _, u := range tp.Usage {
			fmt.Fprintf(&b, "      - %s\n", u)
		}
		b.WriteString("\n")
	}
	return b.String()
}
