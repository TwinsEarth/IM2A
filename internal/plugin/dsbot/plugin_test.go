// SPDX-License-Identifier: MIT

package dsbotplugin

import (
	"context"
	"errors"
	"io"
	"log"
	"reflect"
	"strings"
	"testing"

	"human-agent-im/internal/plugin"
)

func newTestPlugin(t *testing.T, enabled bool) *Plugin {
	t.Helper()
	return New(Deps{Logger: log.New(io.Discard, "", 0), Enabled: enabled})
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

func TestPluginIdentity(t *testing.T) {
	p := newTestPlugin(t, true)
	if p.ID() != PluginID || PluginID != "dsbot" {
		t.Fatalf("ID() = %q, want dsbot", p.ID())
	}
	if p.Version() != PluginVersion || PluginVersion != "1.0.0" {
		t.Fatalf("Version() = %q, want plugin version", p.Version())
	}
	if p.UpstreamVersion() != UpstreamDSBotVersion || UpstreamDSBotVersion != "0.2.1" {
		t.Fatalf("UpstreamVersion() = %q, want 0.2.1", p.UpstreamVersion())
	}
}

func TestDisabledPlugin(t *testing.T) {
	p := newTestPlugin(t, false)
	if got := p.Health(context.Background()).State; got != plugin.StateDisabled {
		t.Fatalf("initial state = %q, want disabled", got)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start disabled plugin must return nil, got %v", err)
	}
	if got := p.Health(context.Background()).State; got != plugin.StateDisabled {
		t.Fatalf("state = %q, want disabled", got)
	}
	if caps := p.Capabilities(); len(caps) != 0 {
		t.Fatalf("disabled plugin capabilities = %v, want none", capIDs(caps))
	}
}

func TestEnabledNoEndpointIsOK(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	p := newTestPlugin(t, true)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := p.Health(context.Background())
	if h.State != plugin.StateOK {
		t.Fatalf("state = %q, want ok (移植能力纯 Go 可用)", h.State)
	}
	if !strings.Contains(h.Detail, "bridge not configured (DSBOT_ENDPOINT unset)") {
		t.Fatalf("detail = %q, want bridge-not-configured", h.Detail)
	}
	caps := p.Capabilities()
	if _, ok := capByID(caps, CapBridge); ok {
		t.Fatalf("bridge capability must NOT be declared without endpoint: %v", capIDs(caps))
	}
	// 三项移植能力必须齐全。
	wantIDs := []string{CapWebSearch, CapLongMessage, CapRosterSpec}
	for _, id := range wantIDs {
		if _, ok := capByID(caps, id); !ok {
			t.Fatalf("capability %s missing: %v", id, capIDs(caps))
		}
	}
	if len(caps) != 3 {
		t.Fatalf("capabilities = %v, want exactly 3 migrated capabilities", capIDs(caps))
	}
}

func TestCapabilityKindsAndModes(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	p := newTestPlugin(t, true)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	caps := p.Capabilities()
	wechatSearch, _ := capByID(caps, CapWebSearch)
	if wechatSearch.Kind != plugin.KindSkill || !reflect.DeepEqual(wechatSearch.Modes, []string{"auto"}) {
		t.Fatalf("web-search capability = %+v", wechatSearch)
	}
	longMsg, _ := capByID(caps, CapLongMessage)
	if longMsg.Kind != "util" || !reflect.DeepEqual(longMsg.Modes, []string{"split"}) {
		t.Fatalf("long-message capability = %+v (契约 §2 Kind=util, Modes=[split])", longMsg)
	}
	roster, _ := capByID(caps, CapRosterSpec)
	if roster.Kind != plugin.KindRuntime || !reflect.DeepEqual(roster.Modes, []string{"read"}) {
		t.Fatalf("roster-spec capability = %+v", roster)
	}
}

func TestHealthBeforeStartIsNotOptimistic(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	p := newTestPlugin(t, true)
	h := p.Health(context.Background())
	if h.State != plugin.StateDegraded || !strings.Contains(h.Detail, "not started") {
		t.Fatalf("pre-start health = %+v, want degraded not-started", h)
	}
}

func TestStartIdempotentAndStop(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	p := newTestPlugin(t, true)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h := p.Health(context.Background())
	if h.State != plugin.StateFailed || !strings.Contains(h.Detail, "stopped") {
		t.Fatalf("post-stop health = %+v, want failed", h)
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

func TestAttribution(t *testing.T) {
	attr := Attribution()
	if attr.Upstream != "https://github.com/Fei2411/DeepSeek-Bot" {
		t.Fatalf("upstream = %q", attr.Upstream)
	}
	if attr.UpstreamName != "DeepSeek-Bot" || attr.License != "Apache-2.0" {
		t.Fatalf("attribution = %+v", attr)
	}
	if attr.Copyright != "Copyright 2026 Fei2411 and the DS Bot contributors" {
		t.Fatalf("copyright = %q (必须逐字取上游 NOTICE)", attr.Copyright)
	}
	if attr.NoticePath != "THIRD_PARTY_LICENSES/DeepSeek-Bot-NOTICE.txt" {
		t.Fatalf("noticePath = %q", attr.NoticePath)
	}
	if !attr.Derived {
		t.Fatal("Derived must be true (移植并改写为 Go = derivative work)")
	}
	if len(attr.Usage) < 4 {
		t.Fatalf("usage must enumerate each ported file, got %v", attr.Usage)
	}
	joined := strings.Join(attr.Usage, "\n")
	for _, want := range []string{"web-search.js", "long-messages.md", "roster.js", "0.2.1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("usage missing %q:\\n%s", want, joined)
		}
	}
	p := newTestPlugin(t, true)
	if !reflect.DeepEqual(p.Attribution(), attr) {
		t.Fatal("(*Plugin).Attribution() must equal package Attribution()")
	}
}

func TestBridgeTimeoutConfig(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want string // formatDuration 结果
	}{
		{"", "3s"},
		{"1500", "1.5s"},
		{"0", "3s"},
		{"abc", "3s"},
		{"10", "100ms"}, // 下限保护
		{"999999", "1m0s"},
	} {
		t.Setenv(EnvTimeout, tc.env)
		t.Setenv(EnvEndpoint, "")
		p := newTestPlugin(t, true)
		if got := formatDuration(p.timeout); got != tc.want {
			t.Fatalf("DSBOT_TIMEOUT_MS=%q → %s, want %s", tc.env, got, tc.want)
		}
	}
}

func TestBridgeSendWithoutEndpoint(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	p := newTestPlugin(t, true)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.BridgeSend(context.Background(), "hi"); !errors.Is(err, ErrBridgeUnavailable) {
		t.Fatalf("BridgeSend = %v, want ErrBridgeUnavailable", err)
	}
}
