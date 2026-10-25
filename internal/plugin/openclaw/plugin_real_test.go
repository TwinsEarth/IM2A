// SPDX-License-Identifier: MIT

package openclawplugin

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/plugin"
)

// TestRealOpenClawProbe 是可选的**真实环境**探测（默认 skip，不拖慢 CI）：
// 在装有 openclaw CLI 的机器上设置 OPENCLAW_REAL_TEST=1 运行，用于核对本插件
// 探测逻辑与真实 CLI 输出契约是否一致（假 bin 单测之外的最后一道自证）。
//
//	OPENCLAW_REAL_TEST=1 go test ./internal/plugin/openclaw -run TestRealOpenClawProbe -v
func TestRealOpenClawProbe(t *testing.T) {
	if os.Getenv("OPENCLAW_REAL_TEST") != "1" {
		t.Skip("set OPENCLAW_REAL_TEST=1 to probe the real openclaw CLI")
	}
	openclaw.ResetResolveCache()
	t.Cleanup(openclaw.ResetResolveCache)

	p := New(Deps{Logger: log.Default(), Enabled: true})
	t.Logf("bin=%q accountsDir=%q channel=%q gatewayMode=%q",
		p.Bin(), p.AccountsDir(), openclaw.ResolveChannel(), openclaw.ResolveGatewayMode())
	if p.Bin() == "" {
		t.Fatal("real openclaw binary not resolved; install openclaw or set OPENCLAW_BIN")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start must never fail hard (degraded only), got %v", err)
	}
	health := p.Health(ctx)
	t.Logf("health: state=%s detail=%q", health.State, health.Detail)
	// 真实环境下只允许 ok / degraded（enabled 且 bin 在，不应 failed/disabled）。
	if health.State != plugin.StateOK && health.State != plugin.StateDegraded {
		t.Fatalf("unexpected health state in real environment: %+v", health)
	}
	t.Logf("capabilities: %+v", p.Capabilities())

	ver, err := p.UpstreamVersion(ctx)
	if err != nil {
		t.Fatalf("UpstreamVersion: %v", err)
	}
	t.Logf("upstreamVersion=%q", ver)
	if ver == "" {
		t.Fatal("UpstreamVersion returned empty string")
	}

	gwJSON, err := p.GatewayStatus(ctx)
	if err != nil {
		t.Fatalf("GatewayStatus: %v", err)
	}
	t.Logf("gatewayStatus=%s", gwJSON)

	// gateway 未运行是正常态；这里只要求探测不改变已探测到的健康状态。
	after := p.Health(ctx)
	if after.State != health.State {
		t.Fatalf("GatewayStatus must not mutate plugin health: before=%q after=%q", health.State, after.State)
	}
}
