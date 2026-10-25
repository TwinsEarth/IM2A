// SPDX-License-Identifier: MIT
//
// 本文件实现插件的能力探测：L1 上游版本、L2 微信通道可用性、gateway 状态。
// 所有解析均 fail-closed：解析不到/超时/格式变化一律按"不可用"处理，绝不乐观放行。

package openclawplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// probeTimeout 是版本探测（openclaw --version）的超时。
	// 注意：openclaw 是 Node CLI，1 vCPU 上冷启动实测 3–15s（cgroup memory.high 下更慢）；
	// 5s/8s 都会在冷启动时误判（生产曾出现 "upstream unknown" 与 L2 连锁超时）。
	probeTimeout = 12 * time.Second
	// gatewayStatusTimeout 是 gateway status / gateway start 的超时。
	gatewayStatusTimeout = 15 * time.Second
	// maxExcerptRunes 是 Health.Detail 里原始输出摘要的最大字符数。
	maxExcerptRunes = 200
)

// channelProbeTimeout 是通道探测（openclaw channels capabilities）的超时；
// 声明为变量以便单测缩短超时验证 fail-closed 行为。
// 5s + 12s = 17s，满足 Lead「启动探测总时长 ≤20s」的约束。
var channelProbeTimeout = 12 * time.Second

// channelProbe 是一次通道可用性探测的结果。
type channelProbe struct {
	Available bool
	Label     string // CLI 展示名（WeChat / Weixin 等，仅用于详情展示）
	Status    string // Status: 行去掉前缀后的内容
	Excerpt   string // 原始输出摘要（≤ maxExcerptRunes）
}

// channelListTimeout 是轻量通道清单探测的超时。
// 该命令是 config-only（不拉起 gateway），但仍是 Node 冷启动：1 vCPU + cgroup 节流下
// 实测单次 8–15s，故给 20s。与 probeTimeout 并行执行，启动耗时 ≈ max(12s, 20s)。
var channelListTimeout = 20 * time.Second

// probeChannelList 用 `channels list --all` 做**轻量**通道可用性探测（快路径）。
//
// 依赖的输出契约（实测 OpenClaw 2026.10.7，config-only，不启 gateway）：
//
//	$ openclaw channels list --all
//	- Weixin: not installed, not configured, disabled      # 未安装设备
//	- WeChat: installed, not configured, disabled          # 已安装但未登录账号
//
// 判据：命中含 weixin/wechat 的 "-" 行 → ok=true；该行含 "not installed" → 不可用；
// 含 "installed" → 可用（"not configured" 只表示账号未登录，不降级）。
func probeChannelList(ctx context.Context, bin, channel string) (channelProbe, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, channelListTimeout)
	defer cancel()
	out, err := runOpenCLI(cctx, bin, "channels", "list", "--all")
	probe := channelProbe{Excerpt: excerpt(string(out), maxExcerptRunes)}
	if err != nil {
		return probe, false, err
	}
	p, ok := parseChannelList(out)
	if !ok {
		return probe, false, nil
	}
	p.Excerpt = probe.Excerpt
	return p, true, nil
}

// parseChannelList 从 `channels list --all` 输出里解析通道行。
func parseChannelList(raw []byte) (channelProbe, bool) {
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "-") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		idx := strings.Index(body, ":")
		if idx < 0 {
			continue
		}
		label := strings.TrimSpace(body[:idx])
		status := strings.TrimSpace(body[idx+1:])
		low := strings.ToLower(label + " " + status)
		if !strings.Contains(low, "weixin") && !strings.Contains(low, "wechat") {
			continue
		}
		lowStatus := strings.ToLower(status)
		available := !strings.Contains(lowStatus, "not installed") &&
			(strings.Contains(lowStatus, "installed") || strings.Contains(lowStatus, "enabled"))
		return channelProbe{Available: available, Label: label, Status: status}, true
	}
	return channelProbe{}, false
}

// probeCLIVersion 执行 `<bin> --version` 并解析上游版本与 commit。
func probeCLIVersion(ctx context.Context, bin string) (string, error) {
	if bin == "" {
		return "", errors.New("openclaw binary not resolved")
	}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := runOpenCLI(cctx, bin, "--version")
	if err != nil {
		return "", err
	}
	parsed := parseVersionOutput(string(out))
	if parsed == "" {
		return "", errors.New("empty output from --version")
	}
	return parsed, nil
}

// probeChannelAvailability 探测渠道是否可用（fail-closed）。
//
// 依赖的输出契约（上游可能变更；变更时 fail-closed 而不是乐观放行）：
//
//	$ openclaw channels capabilities --channel openclaw-weixin
//	WeChat default
//	Support: chatTypes=direct media blockStreaming
//	Actions: send, broadcast
//	Status: not configured, enabled
//
// 判据：exit==0 且输出含 "Status:" 行 → 通道已安装可用（"not configured" 只表示
// 账号尚未登录，不降级）；exit!=0 / 超时 / 无 Status 行 → 不可用。
// 该命令拒绝未知 channel，因此「能返回 Status 行」本身就证明通道存在。
func probeChannelAvailability(ctx context.Context, bin, channel string) (channelProbe, error) {
	if bin == "" {
		return channelProbe{}, errors.New("openclaw binary not resolved")
	}
	// 快路径（生产必须走这条）：channels list --all 是 config-only，不拉起 gateway。
	// 背景：channels capabilities 会经 gateway RPC，在本机 256MB cgroup 下会拉起
	// 200MB+ 的 gateway 孙进程 → 内存节流 + 管道不关导致启动永久阻塞（2026-10-25 事故）。
	if p, ok, lerr := probeChannelList(ctx, bin, channel); lerr == nil && ok {
		return p, nil
	}
	// 回退路径：清单格式变化时用 capabilities 深探测（可能较慢/拉起 gateway，有超时兜底）。
	cctx, cancel := context.WithTimeout(ctx, channelProbeTimeout)
	defer cancel()
	out, err := runOpenCLI(cctx, bin, "channels", "capabilities", "--channel", channel)
	probe := channelProbe{Excerpt: excerpt(string(out), maxExcerptRunes)}
	lowerAll := strings.ToLower(string(out))
	if err != nil {
		lowerErr := strings.ToLower(err.Error())
		// 明确"未安装"是可判定的不可用；其余命令失败按探测失败上报（同样降级）。
		if strings.Contains(lowerAll, "not installed") || strings.Contains(lowerErr, "not installed") {
			return probe, nil
		}
		return probe, fmt.Errorf("channels capabilities --channel %s: %w", channel, err)
	}
	parsed, ok := parseChannelCapabilities(out)
	if !ok {
		return probe, errors.New("`channels capabilities` output has no `Status:` line (fail-closed)")
	}
	probe.Label = parsed.Label
	probe.Status = parsed.Status
	lowStatus := strings.ToLower(parsed.Status)
	// "not configured" 表示账号未登录，不算通道不可用；只有明确异常信号才判不可用。
	probe.Available = !strings.Contains(lowStatus, "not installed") &&
		!strings.Contains(lowStatus, "disabled") &&
		!strings.Contains(lowStatus, "error") &&
		!strings.Contains(lowStatus, "unknown")
	return probe, nil
}

// capabilitiesOutput 是 `channels capabilities` 文本输出的解析结果。
type capabilitiesOutput struct {
	Label  string
	Status string
}

// parseChannelCapabilities 解析 `channels capabilities --channel <ch>` 文本输出。
// 首行形如 "<Label> default"（如 "WeChat default"），另有 "Status: <状态>" 行。
func parseChannelCapabilities(raw []byte) (capabilitiesOutput, bool) {
	var out capabilitiesOutput
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if out.Label == "" {
			out.Label = strings.TrimSpace(strings.TrimSuffix(trimmed, " default"))
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "status:") {
			out.Status = strings.TrimSpace(trimmed[len("status:"):])
		}
	}
	return out, out.Status != ""
}

// pluginHint 返回渠道对应需要安装的 OpenClaw 插件名（用于 degraded 提示）。
func pluginHint(channel string) string {
	if strings.HasPrefix(channel, "openclaw-") {
		return channel
	}
	return "openclaw-" + channel
}

// parseGatewayStatus 解析 `gateway status --json` 输出为结构化结果。
func parseGatewayStatus(raw []byte) (GatewayStatusInfo, bool) {
	body := trimToJSONObject(raw)
	if body == nil {
		return GatewayStatusInfo{}, false
	}
	var st struct {
		CLI struct {
			Version string `json:"version"`
		} `json:"cli"`
		Service struct {
			Loaded  bool `json:"loaded"`
			Runtime struct {
				Status string `json:"status"`
			} `json:"runtime"`
		} `json:"service"`
		RPC struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"rpc"`
		Port struct {
			Status string `json:"status"`
		} `json:"port"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return GatewayStatusInfo{}, false
	}
	info := GatewayStatusInfo{
		OK:            st.RPC.OK,
		Version:       st.CLI.Version,
		Loaded:        st.Service.Loaded,
		ServiceStatus: st.Service.Runtime.Status,
	}
	if info.OK {
		info.Detail = "gateway reachable"
		return info, true
	}
	detail := "gateway not running"
	if st.RPC.Error != "" {
		detail += " (rpc: " + excerpt(st.RPC.Error, 120) + ")"
	}
	if st.Port.Status != "" {
		detail += "; port: " + st.Port.Status
	}
	detail += "; start with `openclaw gateway start` (or set OPENCLAW_GATEWAY_MODE=managed)"
	info.Detail = detail
	return info, true
}

var (
	versionRe = regexp.MustCompile(`\b(\d+\.\d+\.\d+)\b`)
	commitRe  = regexp.MustCompile(`\(([0-9a-zA-Z][0-9a-zA-Z._-]{2,39})\)`)
)

// parseVersionOutput 从 --version 输出中挑出含版本号的行并解析。
func parseVersionOutput(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if versionRe.MatchString(line) {
			return parseVersionLine(line)
		}
	}
	return parseVersionLine(firstNonEmptyLine(raw))
}

// parseVersionLine 从 "OpenClaw 2026.10.7 (157d8c2)" 提取版本与 commit；
// 解析失败返回原始字符串（按 Lead 口径：解析失败不算错误）。
func parseVersionLine(line string) string {
	line = strings.TrimSpace(line)
	v := versionRe.FindString(line)
	c := commitRe.FindStringSubmatch(line)
	switch {
	case v != "" && len(c) == 2:
		return v + " (" + c[1] + ")"
	case v != "":
		return v
	default:
		return line
	}
}
