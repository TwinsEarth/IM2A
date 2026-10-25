// SPDX-License-Identifier: MIT
//
// 启动期辅助逻辑：插件启用开关解析、归属横幅、掉线通知器、自检。
// Wave A 重构时从 services/gateway/main.go 原样搬移（语义不变）。
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/plugin"
	"human-agent-im/internal/version"
)

// dsbotPluginEnabled 解析 DSBOT_ENABLED（缺省启用：移植能力是纯 Go、无外部依赖）。
// 契约来源：DSBOT-PLUGIN-CONTRACT.md §3。
func dsbotPluginEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DSBOT_ENABLED"))) {
	case "off", "false", "0", "no", "disabled":
		return false
	default:
		return true
	}
}

// openclawOutboundCharLimit 单条微信消息的字符上限（Unicode 码点）。
// 默认 2000：远小于上游 DS Bot 的 20000 内联阈值，因为个人号通道比 DSH 更受限；
// 可用 OPENCLAW_OUTBOUND_CHAR_LIMIT 覆盖，<=0 表示不切分（回到历史行为）。
func openclawOutboundCharLimit() int {
	const def = 2000
	raw := strings.TrimSpace(os.Getenv("OPENCLAW_OUTBOUND_CHAR_LIMIT"))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("[openclaw] invalid OPENCLAW_OUTBOUND_CHAR_LIMIT=%q, using %d", raw, def)
		return def
	}
	return n
}

// openclawPluginEnabled 解析 OPENCLAW_ENABLED：on/off/auto（缺省按可执行文件自动探测）。
// 契约来源：PLUGIN-CONTRACT.md §2（Deps.Enabled 来自 OPENCLAW_ENABLED）/ README 配置表。
func openclawPluginEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OPENCLAW_ENABLED"))) {
	case "off", "false", "0", "no", "disabled":
		return false
	case "on", "true", "1", "yes", "enabled":
		return true
	default:
		// 缺省 auto：解析得到 openclaw 可执行文件才启用（契约 §3 解析顺序）。
		return openclaw.ResolveBin() != ""
	}
}

// printPluginBanner 打印子插件归属横幅（契约 PLUGIN-CONTRACT.md §5）。
// 这是"公示引用 OpenClaw"的运行时证据：产品名、插件名与版本、上游真实版本、许可证、
// 版权行、归属端点、上游 URL 必须同时出现在启动日志中，且只在启动时打印一次。
// 多上游（§9-D5，如 openclaw-weixin）逐条附加打印，版本取插件声明（未知则省略）。
func printPluginBanner(host *plugin.Host, fallback plugin.Plugin) {
	snaps := host.Snapshots()
	if len(snaps) == 0 && fallback != nil {
		h := fallback.Health(context.Background())
		snaps = []plugin.Snapshot{{
			ID: fallback.ID(), Version: fallback.Version(), State: h.State, Detail: h.Detail,
			Capabilities: fallback.Capabilities(), Attribution: fallback.Attribution(),
		}}
	}
	if len(snaps) == 0 {
		log.Printf("IM2A gateway %s — no bundled plugin registered (attribution: /v1/about/attribution)", version.Version)
		return
	}
	for _, s := range snaps {
		p := pluginByID(host, s.ID, fallback)
		attrs := pluginAttributions(p, s)
		attrsVers := pluginAttributionVersions(p)
		a := s.Attribution
		if a.Upstream != "" {
			name := a.UpstreamName
			if name == "" {
				name = s.ID
			}
			// 上游真实版本是 MIT 归属里最关键的定位信息（Lead 决策 2026-10-25）：
			// 探测失败退化为 upstream unknown，绝不因版本探测失败而漏打横幅。
			log.Printf("IM2A gateway %s — bundled plugin: %s plugin %s (upstream %s) %s, %s",
				version.Version, name, s.Version, pluginUpstreamVersion(p), a.License, a.Copyright)
			log.Printf("  attribution: /v1/about/attribution | upstream: %s", a.Upstream)
		}
		// 多上游扩展（契约 §9-D5）：启动日志同样公示第二个上游（如微信通道插件 openclaw-weixin）与版本。
		if len(attrs) > 1 {
			for i, extra := range attrs[1:] {
				if extra.Upstream == "" {
					continue
				}
				line := "  bundled upstream: " + extra.UpstreamName
				if ver := attributionVersionAt(attrsVers, i+1); ver != "" {
					line += " " + ver
				}
				line += fmt.Sprintf(" — %s, %s | upstream: %s", extra.License, extra.Copyright, extra.Upstream)
				log.Print(line)
			}
		}
		if s.State != "" && s.State != "ok" {
			log.Printf("%s plugin: %s (%s)", s.ID, s.State, s.Detail)
		}
	}
}

// pluginByID 取插件实例：优先宿主，其次 fallback（快照回退构造时用）。
func pluginByID(host *plugin.Host, id string, fallback plugin.Plugin) plugin.Plugin {
	if p, ok := host.Get(id); ok {
		return p
	}
	if fallback != nil && fallback.ID() == id {
		return fallback
	}
	return nil
}

// pluginAttributions 返回插件的多上游归属清单（契约 §1 单条接口的向后兼容扩展）；
// 插件未提供 Attributions() 时回退为单条 Attribution()。
func pluginAttributions(p plugin.Plugin, s plugin.Snapshot) []plugin.Attribution {
	if p != nil {
		if ap, ok := p.(interface{ Attributions() []plugin.Attribution }); ok {
			if atts := ap.Attributions(); len(atts) > 0 {
				return atts
			}
		}
	}
	if s.Attribution.Upstream == "" {
		return nil
	}
	return []plugin.Attribution{s.Attribution}
}

// pluginAttributionVersions 返回与 Attributions() 一一对应的上游版本（空字符串=未知）。
// 当前由 openclaw 插件提供（[1]=openclaw-weixin 的 npm 版本）；未提供时横幅省略版本号。
func pluginAttributionVersions(p plugin.Plugin) []string {
	if p == nil {
		return nil
	}
	if vp, ok := p.(interface{ AttributionVersions() []string }); ok {
		return vp.AttributionVersions()
	}
	return nil
}

// attributionVersionAt 安全取第 idx 条上游版本（缺失/空白视为未知）。
func attributionVersionAt(versions []string, idx int) string {
	if idx < 0 || idx >= len(versions) {
		return ""
	}
	return strings.TrimSpace(versions[idx])
}

// pluginUpstreamVersion 探测插件主上游真实版本（如 openclaw 2026.10.7）。
// 探测失败/插件不提供时返回 unknown，保证横幅永远打印得出来。
func pluginUpstreamVersion(p plugin.Plugin) string {
	if p == nil {
		return "unknown"
	}
	// 形态一：需要探测的插件（openclaw）——带 ctx 与错误返回。
	// 形态二：版本是编译期常量的插件（dsbot）——上游版本随移植固定，无需探测。
	// 两种形态都支持，避免插件实现方式差异导致横幅漏打版本。
	if uv, ok := p.(interface{ UpstreamVersion() string }); ok {
		if fields := strings.Fields(strings.TrimSpace(uv.UpstreamVersion())); len(fields) > 0 {
			return fields[0]
		}
		return "unknown"
	}
	uv, ok := p.(interface {
		UpstreamVersion(context.Context) (string, error)
	})
	if !ok {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	v, err := uv.UpstreamVersion(ctx)
	if err != nil {
		return "unknown"
	}
	// UpstreamVersion 形如 "2026.10.7 (157d8c2)"：横幅只取语义版本，commit 留在 /v1/plugins detail。
	if fields := strings.Fields(strings.TrimSpace(v)); len(fields) > 0 {
		return fields[0]
	}
	return "unknown"
}

// healthNotifier 实现 openclaw.AccountNotifier：掉线时落日志 + 给该账号绑定用户发"需重扫"。
// dir / sender 由网关注入（§9-D2/D3），notifier 内不再写死账号目录、不再各自新建发送器。
type healthNotifier struct {
	db     interface{}
	dir    string
	sender func() *openclaw.Sender
}

func (n healthNotifier) OnOffline(accountID, wxid string) {
	log.Printf("[A9-health] ALERT account=%s 已掉线（疑似被踢/登录态失效）", accountID)
	// 读账号文件拿到 wxid（账号文件消失前已知；若已删则跳过发送）
	dir := n.dir
	if dir == "" {
		dir = openclaw.ResolveAccountsDir()
	}
	accFile := dir + "/" + accountID + ".json"
	if b, err := os.ReadFile(accFile); err == nil {
		var af struct {
			UserID string `json:"userId"`
		}
		if json.Unmarshal(b, &af) == nil && af.UserID != "" {
			// 出站走插件单例（契约 §6）；未接线时回退默认构造，行为与旧代码一致。
			send := n.sender
			if send == nil {
				send = openclaw.NewSender
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, err := send().Send(ctx, accountID, af.UserID, "你的登录好像掉线了，麻烦重新扫一下二维码绑回来哈。"); err != nil {
					log.Printf("[A9-health] 重扫提示发送失败 account=%s err=%v", accountID, err)
				}
			}()
		}
	}
}

func (n healthNotifier) OnOnline(accountID string) {}
