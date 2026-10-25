// SPDX-License-Identifier: MIT

package openclawplugin

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/plugin"
)

// fakeBinScript 是可配置的假 openclaw 可执行文件：通过 OC_TEST_* 环境变量模拟
// 未安装通道 / installed-not-configured / 命令失败 等真实输出。
const fakeBinScript = `#!/bin/sh
if [ -n "$OC_TEST_LOG" ]; then printf '%s\n' "$*" >> "$OC_TEST_LOG"; fi
case "$1" in
  --version)
    if [ -n "$OC_TEST_VERSION_ERR" ]; then printf '%s\n' "$OC_TEST_VERSION_ERR" >&2; fi
    if [ -n "$OC_TEST_VERSION" ]; then printf '%s\n' "$OC_TEST_VERSION"; else printf '%s\n' "OpenClaw 2026.10.7 (157d8c2)"; fi
    if [ -n "$OC_TEST_VERSION_EXIT" ]; then exit "$OC_TEST_VERSION_EXIT"; fi
    ;;
  channels)
    if [ -n "$OC_TEST_CHANNEL_SLEEP" ]; then sleep "$OC_TEST_CHANNEL_SLEEP"; fi
    if [ -n "$OC_TEST_CHANNEL_OUT" ]; then printf '%s\n' "$OC_TEST_CHANNEL_OUT"; fi
    if [ -n "$OC_TEST_CHANNEL_ERR" ]; then printf '%s\n' "$OC_TEST_CHANNEL_ERR" >&2; fi
    if [ -n "$OC_TEST_CHANNEL_EXIT" ]; then exit "$OC_TEST_CHANNEL_EXIT"; fi
    ;;
  gateway)
    if [ "$2" = "status" ]; then
      if [ -n "$OC_TEST_GATEWAY_JSON" ]; then printf '%s\n' "$OC_TEST_GATEWAY_JSON"; fi
      if [ -n "$OC_TEST_GATEWAY_EXIT" ]; then exit "$OC_TEST_GATEWAY_EXIT"; fi
    else
      if [ -n "$OC_TEST_GATEWAY_START_MARKER" ]; then printf 'started\n' >> "$OC_TEST_GATEWAY_START_MARKER"; fi
      if [ -n "$OC_TEST_GATEWAY_START_EXIT" ]; then exit "$OC_TEST_GATEWAY_START_EXIT"; fi
    fi
    ;;
esac
exit 0
`

// 真实观测到的两种状态（Lead 在服务器上抓的原文）。
const (
	fixtureInstalledNotConfigured = "WeChat default\nSupport: chatTypes=direct media blockStreaming\nActions: send, broadcast\nStatus: not configured, enabled"
	fixtureInstalledConfigured   = "WeChat default\nSupport: chatTypes=direct media blockStreaming\nActions: send, broadcast\nStatus: configured, enabled"
)

type pluginFixture struct {
	bin         string
	accountsDir string
	enabled     bool
	gatewayMode string
	channel     string
}

func newFixturePlugin(t *testing.T, fx pluginFixture) *Plugin {
	t.Helper()
	if fx.bin == "" {
		fx.bin = newFakeBin(t)
	}
	if fx.accountsDir == "" {
		fx.accountsDir = filepath.Join(t.TempDir(), "accounts")
	}
	t.Setenv(openclaw.EnvBin, fx.bin)
	t.Setenv(openclaw.EnvHome, t.TempDir())
	t.Setenv(openclaw.EnvAccountsDir, fx.accountsDir)
	t.Setenv(openclaw.EnvChannel, fx.channel)
	t.Setenv(openclaw.EnvGatewayMode, fx.gatewayMode)
	openclaw.ResetResolveCache()
	t.Cleanup(openclaw.ResetResolveCache)
	return New(Deps{Logger: log.New(io.Discard, "", 0), Enabled: fx.enabled})
}

func newFakeBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "openclaw")
	if err := os.WriteFile(bin, []byte(fakeBinScript), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	return bin
}

func wantState(t *testing.T, p *Plugin, want string) plugin.Health {
	t.Helper()
	h := p.Health(context.Background())
	if h.State != want {
		t.Fatalf("Health.State = %q, want %q (detail=%q)", h.State, want, h.Detail)
	}
	return h
}

func capByID(caps []plugin.Capability, id string) (plugin.Capability, bool) {
	for _, c := range caps {
		if c.ID == id {
			return c, true
		}
	}
	return plugin.Capability{}, false
}

func capIDs(caps []plugin.Capability) []string {
	ids := make([]string, len(caps))
	for i, c := range caps {
		ids[i] = c.ID
	}
	return ids
}

func TestDisabledPlugin(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: false})
	if got := p.Health(context.Background()).State; got != plugin.StateDisabled {
		t.Fatalf("initial state = %q, want disabled", got)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start disabled plugin must return nil, got %v", err)
	}
	h := wantState(t, p, plugin.StateDisabled)
	if !strings.Contains(h.Detail, "OPENCLAW_ENABLED=false") {
		t.Fatalf("disabled detail = %q", h.Detail)
	}
	if caps := p.Capabilities(); len(caps) != 0 {
		t.Fatalf("disabled plugin capabilities = %v, want none", capIDs(caps))
	}
}

func TestStartBinMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there", "openclaw")
	p := newFixturePlugin(t, pluginFixture{bin: missing, enabled: true})
	if p.Bin() != missing {
		t.Fatalf("Bin() = %q, want resolved env path %q", p.Bin(), missing)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start with missing bin must return nil (degraded), got %v", err)
	}
	h := wantState(t, p, plugin.StateDegraded)
	if !strings.Contains(h.Detail, "not runnable") {
		t.Fatalf("detail = %q, want binary not runnable", h.Detail)
	}
	// 二进制不可运行：只能声明与本地二进制无关的 mcp-route-tool，不得虚报 wechat/registry/gateway。
	caps := p.Capabilities()
	if got, want := capIDs(caps), []string{CapMCPRouteTool}; !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
}

func TestStartVersionProbeFailure(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_VERSION_EXIT", "2")
	t.Setenv("OC_TEST_VERSION_ERR", "cannot execute node")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil, got %v", err)
	}
	h := wantState(t, p, plugin.StateDegraded)
	if !strings.Contains(h.Detail, "not runnable") {
		t.Fatalf("detail = %q", h.Detail)
	}
}

// 【关键用例】bin 在、微信通道未安装 → degraded，且不声明 wechat 能力。
func TestStartChannelNotInstalled(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_ERR", "Channel openclaw-weixin not installed")
	t.Setenv("OC_TEST_CHANNEL_EXIT", "1")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil (degraded), got %v", err)
	}
	h := wantState(t, p, plugin.StateDegraded)
	if !strings.Contains(h.Detail, "not installed") {
		t.Fatalf("detail = %q, want not installed", h.Detail)
	}
	if !strings.Contains(h.Detail, "plugins install openclaw-weixin") {
		t.Fatalf("detail = %q, want install hint", h.Detail)
	}
	caps := p.Capabilities()
	if _, ok := capByID(caps, CapWechatPersonalChannel); ok {
		t.Fatalf("capabilities must NOT declare wechat channel when not installed: %v", capIDs(caps))
	}
	// 二进制可用：registry / mcp / gateway 仍可声明。
	for _, id := range []string{CapAccountRegistry, CapMCPRouteTool, CapAgentGateway} {
		if _, ok := capByID(caps, id); !ok {
			t.Fatalf("capability %s missing: %v", id, capIDs(caps))
		}
	}
}

// 【关键用例】installed + not configured → ok（未登录不是通道不可用），声明 wechat 且 Modes 仅 qr-login。
func TestStartChannelInstalledNotConfigured(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_OUT", fixtureInstalledNotConfigured)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil, got %v", err)
	}
	h := wantState(t, p, plugin.StateOK)
	if !strings.Contains(h.Detail, "channel openclaw-weixin ready") {
		t.Fatalf("detail = %q, want channel ready", h.Detail)
	}
	if !strings.Contains(h.Detail, "account not configured (QR login required)") {
		t.Fatalf("detail = %q, want QR login hint", h.Detail)
	}
	if !strings.Contains(h.Detail, "probe took") {
		t.Fatalf("detail = %q, want probe duration for ops", h.Detail)
	}
	caps := p.Capabilities()
	wechat, ok := capByID(caps, CapWechatPersonalChannel)
	if !ok {
		t.Fatalf("wechat capability missing when channel installed: %v", capIDs(caps))
	}
	if !reflect.DeepEqual(wechat.Modes, []string{"qr-login"}) {
		t.Fatalf("wechat modes = %v, want [qr-login] (text-send 需登录态，不虚报)", wechat.Modes)
	}
	if len(caps) != 4 {
		t.Fatalf("capabilities = %v, want 4", capIDs(caps))
	}
}

// 展示名 Weixin（旧版本）同样必须识别；判据只看 Status: 行，不依赖展示名。
func TestStartChannelInstalledOldLabel(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_OUT", "Weixin default\nSupport: chatTypes=direct\nActions: send\nStatus: not configured, enabled")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil, got %v", err)
	}
	h := wantState(t, p, plugin.StateOK)
	if !strings.Contains(h.Detail, "(Weixin)") {
		t.Fatalf("detail = %q, want Weixin label", h.Detail)
	}
}

func TestStartChannelInstalledConfigured(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_OUT", fixtureInstalledConfigured)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil, got %v", err)
	}
	h := wantState(t, p, plugin.StateOK)
	if !strings.Contains(h.Detail, "status: configured, enabled") {
		t.Fatalf("detail = %q, want raw status", h.Detail)
	}
	if _, ok := capByID(p.Capabilities(), CapWechatPersonalChannel); !ok {
		t.Fatalf("wechat capability missing: %v", capIDs(p.Capabilities()))
	}
}

func TestStartChannelProbeFailure(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_ERR", "network unreachable")
	t.Setenv("OC_TEST_CHANNEL_EXIT", "1")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil, got %v", err)
	}
	h := wantState(t, p, plugin.StateDegraded)
	if !strings.Contains(h.Detail, "probe failed (fail-closed)") {
		t.Fatalf("detail = %q, want fail-closed probe failure", h.Detail)
	}
	if _, ok := capByID(p.Capabilities(), CapWechatPersonalChannel); ok {
		t.Fatal("wechat capability must not be declared on probe failure")
	}
}

func TestStartChannelProbeTimeoutFailClosed(t *testing.T) {
	old := channelProbeTimeout
	channelProbeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { channelProbeTimeout = old })

	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_SLEEP", "1")
	start := time.Now()
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil, got %v", err)
	}
	h := wantState(t, p, plugin.StateDegraded)
	if !strings.Contains(h.Detail, "probe failed (fail-closed)") {
		t.Fatalf("detail = %q, want timeout fail-closed", h.Detail)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout probe took too long: %v", elapsed)
	}
}

func TestUpstreamVersionParsing(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	got, err := p.UpstreamVersion(context.Background())
	if err != nil {
		t.Fatalf("UpstreamVersion: %v", err)
	}
	if got != "2026.10.7 (157d8c2)" {
		t.Fatalf("UpstreamVersion() = %q, want parsed version + commit", got)
	}
	if p.Version() != PluginVersion {
		t.Fatalf("Version() = %q, want plugin version %q", p.Version(), PluginVersion)
	}
}

func TestUpstreamVersionUnparsableReturnsRaw(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_VERSION", "development build without version")
	got, err := p.UpstreamVersion(context.Background())
	if err != nil {
		t.Fatalf("unparsable version must not be an error, got %v", err)
	}
	if got != "development build without version" {
		t.Fatalf("UpstreamVersion() = %q, want raw output", got)
	}
}

func TestStartIdempotent(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_OUT", fixtureInstalledNotConfigured)
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("OC_TEST_LOG", logPath)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	if n := strings.Count(string(body), "--version"); n != 1 {
		t.Fatalf("--version invoked %d times, want 1 (Start must be idempotent):\n%s", n, body)
	}
}

func TestSenderSingleton(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	if p.Sender() == nil {
		t.Fatal("Sender() is nil")
	}
	if p.Sender() != p.Sender() {
		t.Fatal("Sender() must return the same instance")
	}
}

func TestAccountsDirAndBin(t *testing.T) {
	accounts := filepath.Join(t.TempDir(), "custom-accounts")
	p := newFixturePlugin(t, pluginFixture{enabled: true, accountsDir: accounts})
	if got := p.AccountsDir(); got != accounts {
		t.Fatalf("AccountsDir() = %q, want %q", got, accounts)
	}
	if p.Bin() == "" {
		t.Fatal("Bin() should be resolved for fake bin")
	}
}

func TestStopMarksFailed(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_OUT", fixtureInstalledNotConfigured)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h := wantState(t, p, plugin.StateFailed)
	if !strings.Contains(h.Detail, "stopped") {
		t.Fatalf("detail = %q", h.Detail)
	}
	if caps := p.Capabilities(); len(caps) != 0 {
		t.Fatalf("capabilities after Stop = %v, want none", capIDs(caps))
	}
	if err := p.Start(context.Background()); err == nil {
		t.Fatal("Start after Stop should return error")
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop must be idempotent, got %v", err)
	}
}

func TestAttributions(t *testing.T) {
	all := Attributions()
	if len(all) != 2 {
		t.Fatalf("Attributions() = %d entries, want 2 (openclaw + openclaw-weixin)", len(all))
	}
	main, wx := all[0], all[1]
	if main.Upstream != "https://github.com/openclaw/openclaw" || main.License != "MIT" {
		t.Fatalf("main attribution = %+v", main)
	}
	if main.Copyright != "Copyright (c) 2026 OpenClaw Foundation" {
		t.Fatalf("main copyright = %q", main.Copyright)
	}
	if main.NoticePath != "THIRD_PARTY_LICENSES/OpenClaw-MIT.txt" {
		t.Fatalf("main noticePath = %q", main.NoticePath)
	}
	if !main.Derived {
		t.Fatal("main attribution must be Derived=true (沿用上游契约/字段命名)")
	}
	if len(main.Usage) < 5 {
		t.Fatalf("main usage too short: %v", main.Usage)
	}
	if wx.Upstream != "https://github.com/NewFuture/openclaw-weixin" || wx.License != "MIT" {
		t.Fatalf("weixin attribution = %+v", wx)
	}
	if !strings.Contains(wx.Copyright, "Copyright (c) 2026 Tencent") ||
		!strings.Contains(wx.Copyright, "Contributors to NewFuture/openclaw-weixin") {
		t.Fatalf("weixin copyright = %q", wx.Copyright)
	}
	if wx.NoticePath != "THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt" {
		t.Fatalf("weixin noticePath = %q (大小写敏感，必须与仓库文件一致)", wx.NoticePath)
	}
	if wx.Derived {
		t.Fatal("weixin attribution must be Derived=false (仅契约依赖，不含派生代码)")
	}

	p := newFixturePlugin(t, pluginFixture{enabled: true})
	if !reflect.DeepEqual(p.Attribution(), main) {
		t.Fatalf("Attribution() must equal Attributions()[0]")
	}
	if !reflect.DeepEqual(p.Attributions(), all) {
		t.Fatalf("method Attributions() diverges from package-level Attributions()")
	}
	versions := p.AttributionVersions()
	if len(versions) != len(all) {
		t.Fatalf("AttributionVersions() = %v, want same length as Attributions() (%d)", versions, len(all))
	}
	if versions[0] != "" {
		t.Fatalf("versions[0] = %q, want empty (OpenClaw 版本运行期由 UpstreamVersion 探测)", versions[0])
	}
	if versions[1] != WeixinUpstreamVersion || WeixinUpstreamVersion != "3.3.1" {
		t.Fatalf("versions[1] = %q, want openclaw-weixin %q", versions[1], WeixinUpstreamVersion)
	}
}

func TestCapabilityKindsAndModes(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_OUT", fixtureInstalledNotConfigured)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	caps := p.Capabilities()
	wantKind := map[string]string{
		CapWechatPersonalChannel: plugin.KindChannel,
		CapAccountRegistry:       plugin.KindChannel,
		CapMCPRouteTool:          plugin.KindRuntime,
		CapAgentGateway:          plugin.KindRuntime,
	}
	for id, kind := range wantKind {
		c, ok := capByID(caps, id)
		if !ok {
			t.Fatalf("capability %s missing: %v", id, capIDs(caps))
		}
		if c.Kind != kind {
			t.Fatalf("%s kind = %q, want %q", id, c.Kind, kind)
		}
		if c.Version == "" {
			t.Fatalf("%s version empty", id)
		}
	}
	mcp, _ := capByID(caps, CapMCPRouteTool)
	if !reflect.DeepEqual(mcp.Modes, []string{"im2a_route"}) {
		t.Fatalf("mcp modes = %v", mcp.Modes)
	}
	gw, _ := capByID(caps, CapAgentGateway)
	if !reflect.DeepEqual(gw.Modes, []string{"status", "ensure"}) {
		t.Fatalf("gateway modes = %v", gw.Modes)
	}
}

const gatewayNotRunningJSON = `{
  "cli": {"version": "2026.10.7", "entrypoint": "/usr/local/node24/bin/openclaw"},
  "service": {"loaded": false, "runtime": {"status": "stopped"}},
  "gateway": {"port": 18789},
  "port": {"status": "free"},
  "rpc": {"ok": false, "error": "connect ECONNREFUSED 127.0.0.1:18789", "url": "ws://127.0.0.1:18789"}
}`

const gatewayReachableJSON = `{
  "cli": {"version": "2026.10.7"},
  "service": {"loaded": true, "runtime": {"status": "running"}},
  "port": {"status": "listening"},
  "rpc": {"ok": true}
}`

func decodeGatewayStatus(t *testing.T, raw string) GatewayStatusInfo {
	t.Helper()
	var info GatewayStatusInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("GatewayStatus returned non-JSON %q: %v", raw, err)
	}
	return info
}

// 场景 E：gateway status --json 返回 rpc.ok=false 属正常态（gateway 未启动 ≠ 插件 degraded）。
func TestGatewayStatusNotRunningIsNotDegraded(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_CHANNEL_OUT", fixtureInstalledNotConfigured)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	wantState(t, p, plugin.StateOK) // 通道 ready → ok

	t.Setenv("OC_TEST_GATEWAY_JSON", gatewayNotRunningJSON)
	raw, err := p.GatewayStatus(context.Background())
	if err != nil {
		t.Fatalf("GatewayStatus: %v", err)
	}
	info := decodeGatewayStatus(t, raw)
	if info.OK {
		t.Fatal("gateway not running must report ok=false")
	}
	if info.Version != "2026.10.7" {
		t.Fatalf("version = %q", info.Version)
	}
	if info.Loaded || info.ServiceStatus != "stopped" {
		t.Fatalf("service info = %+v", info)
	}
	if !strings.Contains(info.Detail, "gateway not running") || !strings.Contains(info.Detail, "gateway start") {
		t.Fatalf("detail = %q", info.Detail)
	}
	// gateway 未启动是正常态：探测后插件健康状态不得变化（仍为 ok）。
	wantState(t, p, plugin.StateOK)
}

func TestGatewayStatusReachable(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_GATEWAY_JSON", gatewayReachableJSON)
	raw, err := p.GatewayStatus(context.Background())
	if err != nil {
		t.Fatalf("GatewayStatus: %v", err)
	}
	info := decodeGatewayStatus(t, raw)
	if !info.OK || !info.Loaded || info.ServiceStatus != "running" {
		t.Fatalf("info = %+v, want reachable/loaded/running", info)
	}
	if info.Detail != "gateway reachable" {
		t.Fatalf("detail = %q", info.Detail)
	}
}

func TestGatewayStatusUnparsedDegradesGracefully(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_GATEWAY_JSON", "totally not json")
	raw, err := p.GatewayStatus(context.Background())
	if err != nil {
		t.Fatalf("unparsable output must degrade gracefully, got error: %v", err)
	}
	info := decodeGatewayStatus(t, raw)
	if info.OK {
		t.Fatal("unparsed output must report ok=false")
	}
	if !strings.Contains(info.Detail, "unparsed") {
		t.Fatalf("detail = %q", info.Detail)
	}
}

func TestGatewayStatusCommandFailure(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	t.Setenv("OC_TEST_GATEWAY_EXIT", "2")
	if _, err := p.GatewayStatus(context.Background()); err == nil {
		t.Fatal("command failure without usable output must return error")
	}
}

func TestEnsureGatewayExternalModeDoesNothing(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("OC_TEST_LOG", logPath)
	if err := p.EnsureGateway(context.Background()); err != nil {
		t.Fatalf("EnsureGateway external mode: %v", err)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("external mode must not invoke openclaw CLI at all")
	}
}

func TestEnsureGatewayManagedStartsWhenDown(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true, gatewayMode: "managed"})
	t.Setenv("OC_TEST_GATEWAY_JSON", gatewayNotRunningJSON)
	marker := filepath.Join(t.TempDir(), "start.marker")
	t.Setenv("OC_TEST_GATEWAY_START_MARKER", marker)
	if err := p.EnsureGateway(context.Background()); err != nil {
		t.Fatalf("EnsureGateway managed: %v", err)
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("gateway start was not invoked: %v", err)
	}
	if !strings.Contains(string(body), "started") {
		t.Fatalf("marker = %q", body)
	}
}

func TestEnsureGatewayManagedSkipsWhenReachable(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true, gatewayMode: "managed"})
	t.Setenv("OC_TEST_GATEWAY_JSON", gatewayReachableJSON)
	marker := filepath.Join(t.TempDir(), "start.marker")
	t.Setenv("OC_TEST_GATEWAY_START_MARKER", marker)
	if err := p.EnsureGateway(context.Background()); err != nil {
		t.Fatalf("EnsureGateway managed: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("gateway start must not be invoked when gateway is already reachable")
	}
}

func TestEnsureGatewayManagedStartFailure(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true, gatewayMode: "managed"})
	t.Setenv("OC_TEST_GATEWAY_JSON", gatewayNotRunningJSON)
	t.Setenv("OC_TEST_GATEWAY_START_EXIT", "3")
	if err := p.EnsureGateway(context.Background()); err == nil {
		t.Fatal("gateway start failure must be reported")
	}
}

func TestHealthBeforeStartIsNotOptimistic(t *testing.T) {
	p := newFixturePlugin(t, pluginFixture{enabled: true})
	h := wantState(t, p, plugin.StateDegraded)
	if !strings.Contains(h.Detail, "not started") {
		t.Fatalf("detail = %q, want honest not-started", h.Detail)
	}
}
