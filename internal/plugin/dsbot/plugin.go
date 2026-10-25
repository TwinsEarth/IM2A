// SPDX-License-Identifier: MIT

// Package dsbotplugin 把 DeepSeek-Bot（Fei2411/DeepSeek-Bot，Apache-2.0）里"可移植的精华"
// 以 Go 重新实现为 IM2A 子插件：web-search provider/解析、长消息切分、roster/权限只读规格，
// 以及可选的 DS Bot HTTP 桥接（仅在 DSBOT_ENDPOINT 配置且探测通过时声明）。
//
// 判断依据（见契约 §0）：上游是 DSH(Cordis) 插件，host/client/app 深度耦合 DSH 注入点，
// Go 无法直接复用；可移植的是上游自己声明"独立于 Bot 团队代码"的 ds-bot/web-search.js、
// 长消息策略，以及 roster/permissions 的数据模型与语义（只读规格，不执行）。
package dsbotplugin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"human-agent-im/internal/plugin"
)

const (
	// PluginID 是插件在 Host / HTTP 端点中的唯一标识。
	PluginID = "dsbot"
	// PluginVersion 是插件侧 Go 实现版本；上游 DeepSeek-Bot 版本见 UpstreamVersion()。
	PluginVersion = "1.0.0"

	// UpstreamDSBotVersion 是移植来源的上游版本（ds-bot@0.2.1 / v0.2_preview），
	// 与上游 package.json 的版本元数据一致（编译期常量，不伪造运行期探测）。
	UpstreamDSBotVersion = "0.2.1"

	// 能力 ID（与 DSBOT-PLUGIN-CONTRACT.md §2 对齐）。
	CapWebSearch   = "dsbot.web-search"
	CapLongMessage = "dsbot.long-message"
	CapRosterSpec  = "dsbot.roster-spec"
	CapBridge      = "dsbot.bridge"

	// KindUtil 是 IM2A 插件框架 Kind 常量之外、由 DSBOT 契约 §2 指定的能力类型。
	// 框架 Kind 定义（channel|runtime|skill|audit|ui）不排除新值；此处按契约写 "util"。
	KindUtil = "util"

	// 配置项（契约 §3）。
	EnvEnabled  = "DSBOT_ENABLED"
	EnvEndpoint = "DSBOT_ENDPOINT"
	EnvTimeout  = "DSBOT_TIMEOUT_MS"
	// DefaultBridgeTimeout 是桥接探测/调用的默认超时（契约 §3：3000ms）。
	DefaultBridgeTimeout = 3 * time.Second
)

// Deps 是插件依赖注入（契约 §1）。DSBOT_ENABLED 的默认值由接线层折算成 Enabled 传入。
type Deps struct {
	Logger  *log.Logger
	Enabled bool
}

// Plugin 是 DeepSeek-Bot 子插件本体。
//
// 并发约定：endpoint / timeout 在 New 之后不可变；其余状态由 mu 保护。
type Plugin struct {
	deps    Deps
	logger  *log.Logger
	endpoint string
	timeout  time.Duration

	mu              sync.RWMutex
	state           string
	detail          string
	started         bool
	stopped         bool
	bridgeAvailable bool
}

// New 构造插件。移植能力（web-search / long-message / roster-spec）是纯 Go 实现，
// 不依赖外部进程；只有桥接能力需要 DSBOT_ENDPOINT 且探测通过。
func New(deps Deps) *Plugin {
	logger := deps.Logger
	if logger == nil {
		logger = log.Default()
	}
	p := &Plugin{
		deps:     deps,
		logger:   logger,
		endpoint: strings.TrimSpace(os.Getenv(EnvEndpoint)),
		timeout:  resolveBridgeTimeout(),
	}
	switch {
	case !deps.Enabled:
		p.state, p.detail = plugin.StateDisabled, "DSBOT_ENABLED=0"
	default:
		p.state, p.detail = plugin.StateDegraded, "dsbot plugin not started"
	}
	return p
}

// resolveBridgeTimeout 解析 DSBOT_TIMEOUT_MS（默认 3000ms，限制在 [100ms, 60s]）。
func resolveBridgeTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(EnvTimeout))
	if raw == "" {
		return DefaultBridgeTimeout
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return DefaultBridgeTimeout
	}
	d := time.Duration(ms) * time.Millisecond
	if d < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if d > time.Minute {
		return time.Minute
	}
	return d
}

// ID 实现 plugin.Plugin。
func (p *Plugin) ID() string { return PluginID }

// Version 返回插件侧 Go 实现版本，实现 plugin.Plugin。
func (p *Plugin) Version() string { return PluginVersion }

// UpstreamVersion 返回移植来源的上游版本（契约 §1：编译期常量，非运行期探测）。
func (p *Plugin) UpstreamVersion() string { return UpstreamDSBotVersion }

// Capabilities 返回当前**真实可用**的能力集合；不可用的能力不声明。
//
//   - dsbot.web-search / dsbot.long-message / dsbot.roster-spec：纯 Go 实现，随插件启用即可用；
//   - dsbot.bridge：仅当 DSBOT_ENDPOINT 配置且 Start 时健康探测通过才声明（契约 §2）。
func (p *Plugin) Capabilities() []plugin.Capability {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.deps.Enabled || p.stopped {
		return nil
	}
	caps := []plugin.Capability{
		{ID: CapWebSearch, Kind: plugin.KindSkill, Version: PluginVersion, Modes: []string{"auto"}},
		{ID: CapLongMessage, Kind: KindUtil, Version: PluginVersion, Modes: []string{"split"}},
		{ID: CapRosterSpec, Kind: plugin.KindRuntime, Version: PluginVersion, Modes: []string{"read"}},
	}
	if p.bridgeAvailable {
		caps = append(caps, plugin.Capability{ID: CapBridge, Kind: plugin.KindChannel, Version: PluginVersion, Modes: []string{"send"}})
	}
	return caps
}

// Start 语义（契约 §4）：
//   - Enabled=false → disabled，返回 nil；
//   - 移植能力纯 Go、无需外部探测 → 就绪；
//   - DSBOT_ENDPOINT 未配置 → ok（bridge 不声明），detail 写明 "bridge not configured"；
//   - DSBOT_ENDPOINT 配置且探测通过 → ok 并声明 bridge；
//   - DSBOT_ENDPOINT 配置但不可达 → degraded（仅在配置了 bridge 时），返回 nil，不影响 IM2A 其余功能。
func (p *Plugin) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return errors.New("dsbotplugin: cannot Start after Stop")
	}
	if p.started {
		return nil
	}
	if !p.deps.Enabled {
		p.state, p.detail = plugin.StateDisabled, "DSBOT_ENABLED=0"
		p.logger.Printf("[plugin/dsbot] disabled (DSBOT_ENABLED=0)")
		return nil
	}

	// 移植能力已就绪：web-search / long-message / roster-spec 不需要外部依赖。
	const migrated = "migrated capabilities ready (web-search, long-message, roster-spec)"
	if p.endpoint == "" {
		p.started = true
		p.bridgeAvailable = false
		p.state = plugin.StateOK
		p.detail = migrated + "; bridge not configured (DSBOT_ENDPOINT unset)"
		p.logger.Printf("[plugin/dsbot] ok: %s", p.detail)
		return nil
	}

	probeStart := time.Now()
	err := p.probeBridge(ctx) // Start 持锁调用；探测不修改状态，只返回连通性
	took := formatDuration(time.Since(probeStart))
	if err != nil {
		p.started = true
		p.bridgeAvailable = false
		p.state = plugin.StateDegraded
		p.detail = fmt.Sprintf("%s; bridge degraded: %v (probe took %s); DSBOT_ENDPOINT=%s",
			migrated, err, took, p.endpoint)
		p.logger.Printf("[plugin/dsbot] degraded: %s", p.detail)
		return nil
	}
	p.started = true
	p.bridgeAvailable = true
	p.state = plugin.StateOK
	p.detail = fmt.Sprintf("%s; bridge reachable at %s (probe took %s)", migrated, p.endpoint, took)
	p.logger.Printf("[plugin/dsbot] ok: %s", p.detail)
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
	p.bridgeAvailable = false
	p.state = plugin.StateFailed
	p.detail = "plugin stopped"
	p.logger.Printf("[plugin/dsbot] stopped")
	return nil
}

// Health 返回当前健康状态，实现 plugin.Plugin。
func (p *Plugin) Health(ctx context.Context) plugin.Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return plugin.Health{State: p.state, Detail: p.detail, CheckedAt: time.Now()}
}

// BridgeEndpoint 返回配置的桥接基址（未配置时为空），供运维/端点展示。
func (p *Plugin) BridgeEndpoint() string { return p.endpoint }

// formatDuration 统一耗时展示（四舍五入到 10ms）。
func formatDuration(d time.Duration) string {
	return d.Round(10 * time.Millisecond).String()
}
