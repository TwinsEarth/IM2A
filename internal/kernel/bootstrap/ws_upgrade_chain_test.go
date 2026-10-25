// SPDX-License-Identifier: MIT
//
// 完整生产链 WebSocket 升级判定性测试（task-20 收尾）：
//
//	httptest server → metrics.Middleware → http.ServeMux → routes.wrap
//	(Authenticate → IPLimit → Audit → CORS → Headers → MaxBody) → /v1/ws → stream.Hub
//
// 这条链与 services/gateway/main.go 的 `ListenAndServe(addr, metrics.Middleware(mux))`
// 完全一致。修复前它会失败于最后断言：
//   - metrics.statusWriter 无 Hijack  → "response does not implement http.Hijacker"
//   - security.statusWriter 无 Hijack → 同上（Layer 2）
//   - security.statusWriter 无 Flush  → SSE 流式失败（由 security 包单测覆盖）
//
// 该测试必须真实 gorilla 升级 + 实际收发消息，不接受"类型断言通过"作为证据。
package bootstrap_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/kernel/routes"
	"human-agent-im/internal/metrics"
)

func TestProductionWSUpgradeChain(t *testing.T) {
	b2cEnv(t, map[string]string{
		"OPENCLAW_ENABLED": "off",
		"DSBOT_ENABLED":    "off",
		"PG_DSN":           "", // 内存 store 即可：本缺陷与 DB 无关（Audit 无条件包装）
		"GATEWAY_TOKEN":    "",
		"IM2A_ADMIN_TOKEN": "ws-chain-admin",
		"IM2A_JWT_SECRET":  "ws-chain-jwt",
	})
	_ = os.Unsetenv("CONN_MODE")     // embedded（Wave A 默认）
	_ = os.Unsetenv("CONN_HINT")     // 默认
	_ = os.Unsetenv("CONN_UDS_PATH") // 不装配 mirror（embedded 恒不装配）
	k, err := bootstrap.Assemble(bootstrap.Load(0))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer func() { _ = k.Close(context.Background()) }()

	mux := http.NewServeMux()
	routes.Register(mux, k)
	// 与 main.go 完全一致的顶层包装顺序。
	srv := httptest.NewServer(metrics.Middleware(mux))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws?topic=prod-chain"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("真实 gorilla 升级失败（metrics→routes.wrap→/v1/ws 链）: %v", err)
	}
	defer c.Close()

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, connected, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("读 connected 帧: %v", err)
	}
	if !strings.Contains(string(connected), `"type":"connected"`) {
		t.Fatalf("首帧不是 connected: %s", connected)
	}

	// Hub 广播 → 客户端真实收到（证明升级后的连接可用，不只是握手成功）。
	k.Deps.Stream.Broadcast("prod-chain", map[string]any{"type": "prod_chain_ok", "n": 1})
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("读广播帧: %v", err)
	}
	if !strings.Contains(string(msg), "prod_chain_ok") {
		t.Fatalf("广播未送达: %s", msg)
	}
	t.Logf("PROD_CHAIN_WS_UPGRADE_OK: connected + broadcast received over metrics→wrap→routes→/v1/ws")
}
