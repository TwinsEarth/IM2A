// SPDX-License-Identifier: MIT
//
// 本文件是 OpenClaw 安装与运行配置的单一解析入口，解析顺序统一为
// 显式环境变量 → PATH 查找 → 已知安装位置 → 空，
// 用于替换历史上散落在 sender / launcher / pool / watcher / health 里的硬编码路径。

package openclaw

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// 环境变量名（全部可覆盖，缺省值见各 Resolve* 函数注释）。
const (
	EnvBin          = "OPENCLAW_BIN"
	EnvHome         = "OPENCLAW_HOME"
	EnvChannel      = "OPENCLAW_CHANNEL"
	EnvAccountsDir  = "OPENCLAW_ACCOUNTS_DIR"
	EnvGatewayMode  = "OPENCLAW_GATEWAY_MODE"
	// DefaultChannel 是历史默认渠道标识：微信个人号通道。
	//
	// 前置依赖：OpenClaw 2026.10.7 的微信通道不是内置能力，而由外部社区插件
	// openclaw-weixin（MIT，github.com/NewFuture/openclaw-weixin）另行提供，
	// 需先执行 `openclaw plugins install openclaw-weixin`；未安装该插件时
	// CLI 的 channels 列表里不存在该渠道，`--channel openclaw-weixin` 会失败。
	// 安装该插件后本默认值即指向真实渠道；其他渠道通过 OPENCLAW_CHANNEL 覆盖。
	DefaultChannel = "openclaw-weixin"

	// GatewayModeManaged 表示由 IM2A 负责确保 gateway 已启动（见 EnsureGateway）。
	GatewayModeManaged = "managed"
	// GatewayModeExternal 表示 gateway 生命周期由外部管理，IM2A 只读状态。
	GatewayModeExternal = "external"
)

// legacyOpenClawBinDir 是历史版本的 Node 安装目录：旧部署把 openclaw 装在这里。
// 仅作迁移期的最后 fallback（目录与文件名分开存放，避免出现完整硬编码可执行路径）。
const legacyOpenClawBinDir = "/opt/node-v22.23.4-linux-x64/bin"

// legacyBinCandidates 已知安装位置列表（按顺序探测）。
// 只在 OPENCLAW_BIN 与 PATH 都未命中时使用；测试可临时替换为夹具路径。
var legacyBinCandidates = []string{
	filepath.Join(legacyOpenClawBinDir, "openclaw"),
}

// resolvedConfig 是一次性解析出的配置快照（sync.Once 缓存）。
type resolvedConfig struct {
	bin         string
	home        string
	channel     string
	accountsDir string
	gatewayMode string
	binDir      string
}

var (
	resolveMu   sync.RWMutex
	resolveOnce sync.Once
	resolved    resolvedConfig
)

// config 返回解析并缓存后的配置快照；ResetResolveCache 后可重新解析。
func config() resolvedConfig {
	resolveMu.RLock()
	defer resolveMu.RUnlock()
	resolveOnce.Do(func() {
		resolved = resolveAll()
	})
	return resolved
}

// ResolveBin 返回 openclaw 可执行文件路径。
// 顺序：OPENCLAW_BIN（显式优先，不校验存在性，交给版本探测暴露问题）
// → PATH 查找 openclaw → 已知安装位置（旧 Node 目录）→ ""。
func ResolveBin() string { return config().bin }

// ResolveHome 返回 openclaw 的 HOME（配置文件与账号目录的根）。
// 顺序：OPENCLAW_HOME → os.UserHomeDir() → ""。
func ResolveHome() string { return config().home }

// ResolveChannel 返回 openclaw 渠道标识。顺序：OPENCLAW_CHANNEL → "openclaw-weixin"。
// 注意：openclaw-weixin 渠道依赖外部插件，安装前置依赖见 DefaultChannel 注释。
func ResolveChannel() string { return config().channel }

// ResolveAccountsDir 返回 openclaw 账号目录。
// 顺序：OPENCLAW_ACCOUNTS_DIR → <Home>/.openclaw/<Channel>/accounts → ""
// （Home 无法解析时返回 ""，调用方应降级而不是拼出一个相对路径）。
func ResolveAccountsDir() string { return config().accountsDir }

// ResolveGatewayMode 返回 gateway 管理模式（managed | external，默认 external）。
// 仅 EnsureGateway 使用：只有 managed 时 IM2A 才会尝试拉起 gateway。
func ResolveGatewayMode() string { return config().gatewayMode }

// ResolveBinDir 返回 openclaw 可执行文件所在目录（用于给子进程 PATH 前置 Node 目录）；
// bin 未解析时返回 ""。
func ResolveBinDir() string { return config().binDir }

// PathWithBinDir 返回把 openclaw bin 目录前置后的 PATH。
// Node 方式安装的 openclaw 需要同目录的 node 才能启动。
func PathWithBinDir() string {
	path := os.Getenv("PATH")
	dir := ResolveBinDir()
	switch {
	case dir == "":
		return path
	case path == "":
		return dir
	default:
		return dir + string(os.PathListSeparator) + path
	}
}

// ResetResolveCache 清空解析缓存，仅供测试在修改环境变量/夹具后重新解析。
func ResetResolveCache() {
	resolveMu.Lock()
	defer resolveMu.Unlock()
	resolveOnce = sync.Once{}
	resolved = resolvedConfig{}
}

// resolveAll 执行一次完整解析（调用方不得持有 resolveMu）。
func resolveAll() resolvedConfig {
	bin := resolveBin()
	home := resolveHome()
	channel := resolveChannel()
	cfg := resolvedConfig{
		bin:         bin,
		home:        home,
		channel:     channel,
		accountsDir: resolveAccountsDir(home, channel),
		gatewayMode: resolveGatewayMode(),
	}
	if bin != "" {
		cfg.binDir = filepath.Dir(bin)
	}
	return cfg
}

// resolveBin 实现 ResolveBin 的解析顺序（不含缓存）。
func resolveBin() string {
	if b := strings.TrimSpace(os.Getenv(EnvBin)); b != "" {
		return b
	}
	if p, err := exec.LookPath("openclaw"); err == nil && p != "" {
		return p
	}
	for _, candidate := range legacyBinCandidates {
		if candidate == "" {
			continue
		}
		if p, err := exec.LookPath(candidate); err == nil && p != "" {
			return p
		}
	}
	return ""
}

// resolveHome 实现 ResolveHome 的解析顺序（不含缓存）。
func resolveHome() string {
	if h := strings.TrimSpace(os.Getenv(EnvHome)); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return strings.TrimSpace(h)
	}
	return ""
}

// resolveChannel 实现 ResolveChannel 的解析顺序（不含缓存）。
func resolveChannel() string {
	if ch := strings.TrimSpace(os.Getenv(EnvChannel)); ch != "" {
		return ch
	}
	return DefaultChannel
}

// resolveAccountsDir 实现 ResolveAccountsDir 的解析顺序（不含缓存）。
func resolveAccountsDir(home, channel string) string {
	if dir := strings.TrimSpace(os.Getenv(EnvAccountsDir)); dir != "" {
		return dir
	}
	if home == "" || channel == "" {
		return ""
	}
	return filepath.Join(home, ".openclaw", channel, "accounts")
}

// resolveGatewayMode 归一化 gateway 管理模式（不含缓存）。
func resolveGatewayMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvGatewayMode))) {
	case GatewayModeManaged:
		return GatewayModeManaged
	case "", GatewayModeExternal:
		return GatewayModeExternal
	default:
		// 未知取值按 external 处理：不擅自接管 gateway 生命周期。
		return GatewayModeExternal
	}
}
