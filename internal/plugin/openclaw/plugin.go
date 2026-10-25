// SPDX-License-Identifier: MIT

// Package openclawplugin 把 IM2A 既有的 OpenClaw 集成能力封装为可挂载插件：
// 声明真实能力、上游归属、健康状态与生命周期边界。插件不重写 OpenClaw，
// 只复用 internal/openclaw（Sender/Launcher/Pool）与 openclaw CLI 的调用契约。
//
// 能力探测口径（fail-closed，宁缺勿虚报）：
//   - L1 二进制：openclaw --version（实测格式 "OpenClaw <ver> (<commit>)"）；
//   - L2 通道：openclaw channels capabilities --channel <channel>（实测输出形如
//     "WeChat default / Support: ... / Actions: ... / Status: not configured, enabled"）。
//     该命令拒绝未知 channel，因此「能返回 Status: 行」本身就证明通道已安装；
//     exit!=0、超时、或输出无 Status: 行一律判不可用（绝不乐观假定）。
//     注意上游对微信渠道的展示名在不同安装状态下为 Weixin 或 WeChat，只作详情展示，不参与判定。
package openclawplugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/plugin"
	"human-agent-im/internal/store"
)

const (
	// PluginID 是插件在 Host / HTTP 端点中的唯一标识。
	PluginID = "openclaw"
	// PluginVersion 是插件侧实现版本（与上游 openclaw 版本无关，上游版本由 UpstreamVersion(ctx) 探测）。
	PluginVersion = "1.0.0"

	// WeixinUpstreamVersion 是本仓库声明并集成验证的微信通道插件版本
	// （openclaw-weixin，github.com/NewFuture/openclaw-weixin）。
	// 该版本由 /v1/about/attribution 与启动横幅公示；实际安装版本可用
	// `openclaw plugins list --json` 核对。
	WeixinUpstreamVersion = "3.3.1"

	// 能力 ID（与集成契约 §2 对齐）。
	CapWechatPersonalChannel = "openclaw.wechat-personal-channel"
	CapAccountRegistry       = "openclaw.account-registry"
	CapMCPRouteTool          = "openclaw.mcp-route-tool"
	CapAgentGateway          = "openclaw.agent-gateway"
)

// Deps 是插件依赖注入。Store/DB 按契约保留（可 nil），本迭代暂不使用，不造假使用。
type Deps struct {
	Logger  *log.Logger
	Store   *store.Store
	DB      *sql.DB
	Enabled bool
}

// Plugin 是 OpenClaw 子插件本体。
//
// 并发约定：bin / channel / accountsDir / gatewayMode / sender 在 New 之后不可变；
// state / detail / version / channelInstalled 等由 mu 保护。
type Plugin struct {
	deps   Deps
	logger *log.Logger

	bin         string
	channel     string
	accountsDir string
	gatewayMode string
	sender      *openclaw.Sender

	mu               sync.RWMutex
	state            string
	detail           string
	version          string // 已探测到的上游 CLI 版本（如 "2026.10.7 (157d8c2)"）
	binOK            bool   // L1 版本探测是否成功（决定 account-registry / agent-gateway 是否可声明）
	channelLabel     string
	channelStatus    string
	channelInstalled bool
	started          bool
	stopped          bool
}

// New 构造插件。Start 之前 Health 会如实返回 disabled / degraded 而非乐观 ok。
func New(deps Deps) *Plugin {
	logger := deps.Logger
	if logger == nil {
		logger = log.Default()
	}
	p := &Plugin{
		deps:        deps,
		logger:      logger,
		bin:         openclaw.ResolveBin(),
		channel:     openclaw.ResolveChannel(),
		accountsDir: openclaw.ResolveAccountsDir(),
		gatewayMode: openclaw.ResolveGatewayMode(),
		// Sender 单例：替代散落的 openclaw.NewSender()（契约 §6）。
		sender: openclaw.NewSender(),
	}
	switch {
	case !deps.Enabled:
		p.state, p.detail = plugin.StateDisabled, "OPENCLAW_ENABLED=false"
	case p.bin == "":
		p.state, p.detail = plugin.StateDegraded, "openclaw binary not found (set OPENCLAW_BIN or install openclaw)"
	default:
		p.state, p.detail = plugin.StateDegraded, "openclaw plugin not started"
	}
	return p
}

// ID 实现 plugin.Plugin。
func (p *Plugin) ID() string { return PluginID }

// Version 返回插件侧实现版本，实现 plugin.Plugin。
// 上游 openclaw 的版本用 UpstreamVersion(ctx) 探测。
func (p *Plugin) Version() string { return PluginVersion }

// Capabilities 返回当前**真实可用**的能力集合；不可用的能力不声明。
//
//   - wechat-personal-channel：仅在通道探测确认已安装后声明。Modes 只声明
//     qr-login（Launcher/Pool 会真实执行 `openclaw channels login`）；text-send
//     需要账号已登录，启动时无法可靠判定，故不写进 Modes，只在 Health.Detail 说明。
//   - account-registry：上游 CLI 可用（L1 探测成功）即可；账号目录为空/不存在属"可用但无账号"。
//   - mcp-route-tool：IM2A 自身 MCP 端点提供的 im2a_route（internal/api/mcp.go），
//     与 openclaw 二进制无关。
//   - agent-gateway：上游 CLI 可用即可（`openclaw gateway status --json`）。
func (p *Plugin) Capabilities() []plugin.Capability {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.deps.Enabled || p.stopped {
		return nil
	}
	caps := make([]plugin.Capability, 0, 4)
	if p.channelInstalled {
		caps = append(caps, plugin.Capability{
			ID: CapWechatPersonalChannel, Kind: plugin.KindChannel, Version: PluginVersion,
			Modes: []string{"qr-login"},
		})
	}
	if p.binOK {
		caps = append(caps, plugin.Capability{
			ID: CapAccountRegistry, Kind: plugin.KindChannel, Version: PluginVersion,
			Modes: []string{"watch", "resolve"},
		})
	}
	caps = append(caps, plugin.Capability{
		ID: CapMCPRouteTool, Kind: plugin.KindRuntime, Version: PluginVersion,
		Modes: []string{"im2a_route"},
	})
	if p.binOK {
		caps = append(caps, plugin.Capability{
			ID: CapAgentGateway, Kind: plugin.KindRuntime, Version: PluginVersion,
			Modes: []string{"status", "ensure"},
		})
	}
	return caps
}

// Attribution 返回主上游（OpenClaw）归属，实现 plugin.Plugin。
func (p *Plugin) Attribution() plugin.Attribution { return Attributions()[0] }

// Attributions 返回本集成涉及的全部上游归属（主上游 OpenClaw + 微信通道插件 openclaw-weixin）。
//
// 契约 §1 的 Attribution 是单条结构；/v1/about/attribution 需要公示多条时，
// api 层用本方法展开（第 0 条恒等于 Attribution()）。本方法为契约的向后兼容扩展。
func (p *Plugin) Attributions() []plugin.Attribution { return Attributions() }

// AttributionVersions 与 Attributions() 一一对应：返回各上游的版本标识，
// 空字符串表示"运行期探测"。
//
//	[0] OpenClaw：由 UpstreamVersion(ctx) 探测（openclaw --version），故此处为空串；
//	[1] openclaw-weixin：本仓库声明并集成验证的上游插件版本（WeixinUpstreamVersion）。
//
// 启动横幅 / /v1/about/attribution 用它公示传递依赖版本；本方法为契约的向后兼容扩展
// （调用方可用类型断言 `interface{ AttributionVersions() []string }` 可选接入）。
func (p *Plugin) AttributionVersions() []string {
	return []string{"", WeixinUpstreamVersion}
}

// Attributions 返回静态归属清单：索引 0 为 OpenClaw 本体，索引 1 为微信通道插件。
func Attributions() []plugin.Attribution {
	return []plugin.Attribution{
		{
			Upstream:     "https://github.com/openclaw/openclaw",
			UpstreamName: "OpenClaw",
			License:      "MIT",
			Copyright:    "Copyright (c) 2026 OpenClaw Foundation",
			NoticePath:   "THIRD_PARTY_LICENSES/OpenClaw-MIT.txt",
			Derived:      true, // 沿用其 CLI/账号文件/MCP 字段契约与字段命名
			Usage: []string{
				"openclaw CLI 出站契约：openclaw message send --channel <channel> --account <id> --target <wxid> -m <text> --json（internal/openclaw/sender.go）",
				"openclaw 扫码登录契约：openclaw channels login --channel <channel>（internal/openclaw/launcher.go、pool.go）",
				"openclaw 账号文件契约：<accountsDir>/<accountId>.json 含 userId/token 字段（internal/openclaw/watcher.go、internal/relay/accounts.go）",
				"openclaw 版本探测契约：openclaw --version 的 \"OpenClaw <ver> (<commit>)\" 输出格式（本插件 UpstreamVersion）",
				"openclaw 通道探测契约：openclaw channels capabilities --channel <channel> 的 Status: 行（本插件 Start 的 L2 探测）",
				"openclaw gateway 状态契约：openclaw gateway status --json 的 cli.version/service.loaded/service.runtime.status/rpc.ok 字段（本插件 GatewayStatus）",
				"openclaw gateway 启动契约：openclaw gateway start（本插件 EnsureGateway，仅 OPENCLAW_GATEWAY_MODE=managed）",
				"MCP 工具名 im2a_route 与参数 from_wxid/text/idem_key 的字段命名（internal/api/mcp.go）",
				"插件 Capability/Attribution/Health/Snapshot 的字段命名沿用本集成交付契约 v1",
			},
		},
		{
			Upstream:     "https://github.com/NewFuture/openclaw-weixin",
			UpstreamName: "openclaw-weixin",
			License:      "MIT",
			Copyright:    "Copyright (c) 2026 Tencent; Copyright (c) 2026 Contributors to NewFuture/openclaw-weixin",
			NoticePath:   "THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt",
			Derived:      false, // 不复制其代码，仅通过 OpenClaw 渠道契约依赖其能力
			Usage: []string{
				"微信个人号渠道能力（channels login 扫码登录、通道注册）由 OpenClaw 侧安装的 openclaw-weixin 插件提供",
				"渠道标识 openclaw-weixin 及其 CLI 展示名（不同版本为 Weixin / WeChat）沿用该插件",
				"账号文件结构与扫码会话语义沿用该插件；本仓库只读取账号目录，不复用其代码",
				"本仓库集成验证的上游插件版本为 " + WeixinUpstreamVersion + "（见 AttributionVersions）",
			},
		},
	}
}

// Start 执行两级探测：L1 二进制/版本，L2 通道可用性；不启动 watcher/health
// （按 Lead D1 裁决，本迭代 watcher/health 生命周期仍留在 services/gateway/main.go）。
//
// 语义（与契约 §2 及 Lead 修正一致）：
//   - Enabled=false → disabled，返回 nil；
//   - bin 解析失败 / 版本探测失败 → degraded，返回 nil；
//   - 通道未安装或探测失败 → degraded（fail-closed），返回 nil；
//   - 通道 installed → ok（account not configured 属正常待扫码状态，不降级）。
func (p *Plugin) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return errors.New("openclawplugin: cannot Start after Stop")
	}
	if p.started {
		return nil
	}
	if !p.deps.Enabled {
		p.state, p.detail = plugin.StateDisabled, "OPENCLAW_ENABLED=false"
		p.logger.Printf("[plugin/openclaw] disabled (OPENCLAW_ENABLED=false)")
		return nil
	}
	if p.bin == "" {
		p.state = plugin.StateDegraded
		p.detail = "openclaw binary not found (set OPENCLAW_BIN or install openclaw)"
		p.logger.Printf("[plugin/openclaw] degraded: %s", p.detail)
		return nil
	}

	// L1/L2 **并行**探测。
	// 背景（2026-10-25 生产实测）：openclaw 是 Node CLI，单次冷启动 6–15s；串行探测
	// 实测 24.68s 且 L2 仍超时（memory.high 节流下更慢）。并行后启动耗时 ≈ max(L1, L2)。
	probeStart := time.Now()
	type versionResult struct {
		ver string
		err error
	}
	type channelResult struct {
		probe channelProbe
		err   error
	}
	versionCh := make(chan versionResult, 1)
	channelCh := make(chan channelResult, 1)
	go func() {
		v, e := probeCLIVersion(ctx, p.bin)
		versionCh <- versionResult{ver: v, err: e}
	}()
	go func() {
		pr, e := probeChannelAvailability(ctx, p.bin, p.channel)
		channelCh <- channelResult{probe: pr, err: e}
	}()
	vr := <-versionCh
	cr := <-channelCh
	ver, err := vr.ver, vr.err
	probe, probeErr := cr.probe, cr.err
	probeTook := formatProbeDuration(time.Since(probeStart))
	if err != nil {
		p.state = plugin.StateDegraded
		p.detail = fmt.Sprintf("openclaw binary not runnable: %s (%v; probe took %s)",
			p.bin, err, probeTook)
		p.logger.Printf("[plugin/openclaw] degraded: %s", p.detail)
		return nil
	}
	p.version = ver
	p.binOK = true

	p.channelLabel = probe.Label
	p.channelStatus = probe.Status
	p.channelInstalled = probe.Available
	switch {
	case probe.Available:
		p.started = true
		account := "status: " + labelOr(probe.Status, "unknown")
		if strings.Contains(strings.ToLower(probe.Status), "not configured") {
			account = "account not configured (QR login required)"
		}
		p.state = plugin.StateOK
		p.detail = fmt.Sprintf("openclaw %s; channel %s ready (%s); %s; probe took %s",
			ver, p.channel, labelOr(probe.Label, p.channel), account, probeTook)
		p.logger.Printf("[plugin/openclaw] ok: %s", p.detail)
	case probeErr != nil:
		p.started = true
		p.state = plugin.StateDegraded
		p.detail = fmt.Sprintf("channel %s probe failed (fail-closed): %v; probe took %s; output: %s",
			p.channel, probeErr, probeTook, labelOr(probe.Excerpt, "<empty>"))
		p.logger.Printf("[plugin/openclaw] degraded: %s", p.detail)
	default:
		p.started = true
		p.state = plugin.StateDegraded
		p.detail = fmt.Sprintf("channel %s not installed (run: openclaw plugins install %s); probe took %s; output: %s",
			p.channel, pluginHint(p.channel), probeTook, labelOr(probe.Excerpt, "<empty>"))
		p.logger.Printf("[plugin/openclaw] degraded: %s", p.detail)
	}
	return nil
}

// Stop 停止插件（无子进程需要回收）：此后 Health 如实返回 failed。
func (p *Plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return nil
	}
	p.stopped = true
	p.started = false
	p.binOK = false
	p.channelInstalled = false
	p.state = plugin.StateFailed
	p.detail = "plugin stopped"
	p.logger.Printf("[plugin/openclaw] stopped")
	return nil
}

// Health 返回当前健康状态，实现 plugin.Plugin。
func (p *Plugin) Health(ctx context.Context) plugin.Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return plugin.Health{State: p.state, Detail: p.detail, CheckedAt: time.Now()}
}

// Sender 返回出站发送器单例（替代散落的 openclaw.NewSender()）。
func (p *Plugin) Sender() *openclaw.Sender { return p.sender }

// AccountsDir 返回解析后的 openclaw 账号目录（可能尚不存在 = 可用但无账号）。
func (p *Plugin) AccountsDir() string { return p.accountsDir }

// Bin 返回解析后的 openclaw 可执行文件路径（未解析到时为 ""）。
func (p *Plugin) Bin() string { return p.bin }

// UpstreamVersion 执行 `<bin> --version` 并解析上游版本与 commit。
// 实测格式 "OpenClaw 2026.10.7 (157d8c2)" → "2026.10.7 (157d8c2)"；
// 解析失败返回原始首行字符串（按 Lead 口径不算错误），仅命令执行失败才返回 error。
// 契约 §2 的 `Version(ctx) (string, error)` 即本方法（因 plugin.Plugin 接口已占用 Version()，
// 经 Lead 确认改名为 UpstreamVersion）。
func (p *Plugin) UpstreamVersion(ctx context.Context) (string, error) {
	return probeCLIVersion(ctx, p.bin)
}

// GatewayStatusInfo 是 GatewayStatus 的结构化结果。
// 契约 §2 的 GatewayStatus 返回 string，故序列化为 JSON 字符串返回；
// 调用方可 json.Unmarshal 回本类型。
type GatewayStatusInfo struct {
	OK            bool   `json:"ok"`                      // gateway RPC 是否可用；未运行=false 属正常态
	Version       string `json:"version,omitempty"`       // CLI 版本（gateway status --json 的 cli.version）
	Loaded        bool   `json:"loaded"`                  // 服务是否已加载（service.loaded）
	ServiceStatus string `json:"serviceStatus,omitempty"` // 服务运行时状态（service.runtime.status）
	Detail        string `json:"detail,omitempty"`        // 人读说明 / 解析失败时的原始摘要
}

// GatewayStatus 执行 `<bin> gateway status --json`，返回 GatewayStatusInfo 的 JSON 字符串。
// gateway 未运行不算插件 degraded；只有 CLI 完全跑不动（命令执行失败且无可用输出）才返回 error。
// 契约来源：OpenClaw `cli/gateway status --json`。
func (p *Plugin) GatewayStatus(ctx context.Context) (string, error) {
	info, err := p.gatewayStatusInfo(ctx)
	if err != nil {
		return "", err
	}
	raw, merr := json.Marshal(info)
	if merr != nil {
		return "", fmt.Errorf("openclawplugin: encode gateway status: %w", merr)
	}
	return string(raw), nil
}

// EnsureGateway 仅当 OPENCLAW_GATEWAY_MODE=managed 时生效：确保 gateway 可达，
// 不可达则执行 `openclaw gateway start`。external（默认）模式下直接返回 nil，不做任何事。
// 契约来源：OpenClaw `cli/gateway start` / `cli/gateway status --json`。
func (p *Plugin) EnsureGateway(ctx context.Context) error {
	if p.gatewayMode != openclaw.GatewayModeManaged {
		return nil
	}
	if p.bin == "" {
		return errors.New("openclawplugin: ensure gateway: openclaw binary not found")
	}
	info, err := p.gatewayStatusInfo(ctx)
	if err != nil {
		return fmt.Errorf("openclawplugin: ensure gateway: status probe: %w", err)
	}
	if info.OK {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, gatewayStatusTimeout)
	defer cancel()
	out, err := runOpenCLI(cctx, p.bin, "gateway", "start")
	if err != nil {
		return fmt.Errorf("openclawplugin: ensure gateway (mode=managed): gateway start failed: %v; output: %s",
			err, labelOr(excerpt(string(out), maxExcerptRunes), "<empty>"))
	}
	p.logger.Printf("[plugin/openclaw] gateway start issued (mode=managed)")
	return nil
}

// gatewayStatusInfo 是 GatewayStatus / EnsureGateway 共用的探测实现。
func (p *Plugin) gatewayStatusInfo(ctx context.Context) (GatewayStatusInfo, error) {
	if p.bin == "" {
		return GatewayStatusInfo{}, errors.New("openclawplugin: gateway status: openclaw binary not found")
	}
	cctx, cancel := context.WithTimeout(ctx, gatewayStatusTimeout)
	defer cancel()
	out, err := runOpenCLI(cctx, p.bin, "gateway", "status", "--json")
	if info, ok := parseGatewayStatus(out); ok {
		if err != nil {
			info.Detail = strings.TrimSpace(info.Detail + "; CLI exit: " + err.Error())
		}
		return info, nil
	}
	if err != nil {
		return GatewayStatusInfo{}, fmt.Errorf("openclawplugin: gateway status: %w", err)
	}
	// CLI 成功但输出无法解析：优雅降级为结构化告警（ok=false），不报错。
	return GatewayStatusInfo{
		OK:     false,
		Detail: "unparsed `openclaw gateway status --json` output: " + excerpt(string(out), maxExcerptRunes),
	}, nil
}
