// SPDX-License-Identifier: MIT
//
// Package selftest 提供网关启动自检：必须走与生产完全相同的装配路径
// （bootstrap.Assemble + routes.Register），禁止自己 new 一个 mux 直接挂 handler。
//
// 背景（Wave A 审计）：旧实现 -selftest 用 api.NewMsgMux 单独装配，导致
// "自测绿、生产红"——真实网关 /v1/msg/seq 404 而自检一直 PASS。本包把自检
// 固定到生产装配路径上，挂载类回归会在 -selftest 里直接暴露。
//
// B2-a（契约 v2）：追加 CONN_MODE 三模式门禁——embedded 行为不变、standalone
// /v1/ws 必须 421+hint 且网关其余端点可用、dual 行为同 embedded、非法模式必须
// 在装配期失败并给出合法值列表。
package selftest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/kernel/routes"
)

// selftestAdminToken 是自检进程内固定 admin token（只影响本进程，不污染调用方环境）。
const selftestAdminToken = "selftest-admin-token"

// testServer 是一次 Assemble+Register 的进程内网关实例（与生产同一条装配路径）。
type testServer struct {
	ts     *httptest.Server
	kernel *bootstrap.Kernel
	cfg    bootstrap.Config
}

func (s *testServer) close() {
	if s.ts != nil {
		s.ts.Close()
	}
	if s.kernel != nil {
		_ = s.kernel.Close(context.Background())
	}
}

// startServer 按当前环境变量装配一个内核并起 httptest server。
func startServer() (*testServer, error) {
	cfg := bootstrap.Load(0)
	kernel, err := bootstrap.Assemble(cfg)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	routes.Register(mux, kernel)
	return &testServer{ts: httptest.NewServer(mux), kernel: kernel, cfg: cfg}, nil
}

// mustStart 同 startServer，失败即 Fatal（自检场景没有"可恢复"语义）。
func mustStart() *testServer {
	s, err := startServer()
	if err != nil {
		log.Fatalf("[FAIL] selftest assemble: %v", err)
	}
	return s
}

// do 发一条自检请求，返回 status 与 body（最多 1KB）。
func (s *testServer) do(method, path string, body []byte, admin bool) (int, string) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, s.ts.URL+path, reader)
	if err != nil {
		log.Fatalf("[FAIL] build request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if admin {
		req.Header.Set("Authorization", "Bearer "+s.cfg.AdminProbeToken)
	}
	resp, err := s.ts.Client().Do(req)
	if err != nil {
		log.Fatalf("[FAIL] %s %s: gateway not serving: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return resp.StatusCode, string(raw)
}

// expectOK 断言 200。
func (s *testServer) expectOK(name, method, path string, body []byte, admin bool) {
	status, raw := s.do(method, path, body, admin)
	if status != http.StatusOK {
		log.Fatalf("[FAIL] %s %s: status=%d want 200 body=%q", name, path, status, truncate(raw, 200))
	}
	log.Printf("  [PASS] %s %s → %d", name, path, status)
}

// expectStatus 断言精确状态码，可选断言 body 包含全部子串。
func (s *testServer) expectStatus(name, method, path string, body []byte, admin bool, want int, wantSubstrs ...string) {
	status, raw := s.do(method, path, body, admin)
	if status != want {
		log.Fatalf("[FAIL] %s %s: status=%d want %d body=%q", name, path, status, want, truncate(raw, 200))
	}
	for _, sub := range wantSubstrs {
		if !strings.Contains(raw, sub) {
			log.Fatalf("[FAIL] %s %s: body missing %q; body=%q", name, path, sub, truncate(raw, 200))
		}
	}
	log.Printf("  [PASS] %s %s → %d%s", name, path, status, substrsNote(wantSubstrs))
}

// expectDegraded 断言 4xx/5xx（降级路径：服务活着但依赖不可用）。
func (s *testServer) expectDegraded(name, method, path string, body []byte, admin bool) int {
	status, raw := s.do(method, path, body, admin)
	if status < 400 {
		log.Fatalf("[FAIL] %s %s: status=%d want degraded (>=400) body=%q", name, path, status, truncate(raw, 200))
	}
	log.Printf("  [PASS] %s %s → %d (degraded path, gateway still serves)", name, path, status)
	return status
}

// substrsNote 把断言子串缩成一行日志后缀。
func substrsNote(subs []string) string {
	if len(subs) == 0 {
		return ""
	}
	return " (body contains " + strings.Join(subs, " & ") + ")"
}

// expectConnMirrorDisabled 断言 B2-c 事件镜像未装配（embedded 恒不装配；
// standalone/dual 在 PG_DSN 为空时降级不装配）。网关必须仍可用。
func (s *testServer) expectConnMirrorDisabled(name string) {
	if s.kernel == nil {
		log.Fatalf("[FAIL] %s: kernel is nil", name)
	}
	if s.kernel.ConnBridge != nil || s.kernel.ConnSink != nil || s.kernel.ConnTap != nil {
		log.Fatalf("[FAIL] %s: conn mirror unexpectedly assembled (mode=%s)", name, s.cfg.ConnMode)
	}
	if s.kernel.Deps != nil && s.kernel.Deps.Stream != nil && s.kernel.Deps.Stream.HasMirror() {
		log.Fatalf("[FAIL] %s: stream.Mirror unexpectedly set (mode=%s)", name, s.cfg.ConnMode)
	}
	log.Printf("  [PASS] %s: conn mirror disabled (expected, mode=%s)", name, s.cfg.ConnMode)
}

// Run 执行网关自检。任何一条断言失败都会打印实际 status + body 前 200 字符并退出。
//
// 日志约定：所有行带 `[selftest] ` 前缀，生产验收时 `journalctl | grep CONN_MODE`
// 只会命中真实服务进程，不会把自检 WARN 误当成线上状态。
func Run() {
	prevPrefix := log.Prefix()
	log.SetPrefix("[selftest] ")
	defer log.SetPrefix(prevPrefix)

	log.Println("[gateway] SELF-TEST: 统一 API 网关路由分发（装配路径与生产一致）")

	// 自检是纯进程内诊断：只验证「路由与装配」，不依赖外部插件——
	// 关闭 OpenClaw/DSBOT 探测可把自检从 ~10s 压到 ~2s，且不污染宿主环境
	// （os.Setenv 只作用于本进程；-selftest 跑完即退出，defer 恢复）。
	prevOpenclaw, hadOpenclaw := os.LookupEnv("OPENCLAW_ENABLED")
	prevDSBot, hadDSBot := os.LookupEnv("DSBOT_ENABLED")
	prevAdmin, hadAdmin := os.LookupEnv("IM2A_ADMIN_TOKEN")
	prevGateway, hadGateway := os.LookupEnv("GATEWAY_TOKEN")
	prevPG, hadPG := os.LookupEnv("PG_DSN")
	prevUDS, hadUDS := os.LookupEnv("CONN_UDS_PATH")
	_ = os.Setenv("OPENCLAW_ENABLED", "off")
	_ = os.Setenv("DSBOT_ENABLED", "off")
	_ = os.Setenv("IM2A_ADMIN_TOKEN", selftestAdminToken)
	_ = os.Setenv("GATEWAY_TOKEN", "")
	// 自检必须自包含：清空 PG_DSN（避免 -selftest 在生产机上打开真库、往 im_delivery_log 写数据），
	// 并把 CONN_UDS_PATH 指到不存在的临时路径（避免碰生产 /run/im2a/conn.sock）。
	_ = os.Setenv("PG_DSN", "")
	_ = os.Setenv("CONN_UDS_PATH", filepath.Join(os.TempDir(), "im2a-selftest-conn.sock"))
	defer restoreEnv("OPENCLAW_ENABLED", prevOpenclaw, hadOpenclaw)
	defer restoreEnv("DSBOT_ENABLED", prevDSBot, hadDSBot)
	defer restoreEnv("IM2A_ADMIN_TOKEN", prevAdmin, hadAdmin)
	defer restoreEnv("GATEWAY_TOKEN", prevGateway, hadGateway)
	defer restoreEnv("PG_DSN", prevPG, hadPG)
	defer restoreEnv("CONN_UDS_PATH", prevUDS, hadUDS)

	// ---- 1) embedded（缺省，Wave A 行为必须逐字保持）----
	_ = os.Unsetenv("CONN_MODE")
	_ = os.Unsetenv("CONN_HINT")
	embedded := mustStart()
	embedded.expectOK("healthz", http.MethodGet, "/healthz", nil, true)
	// B2-c：embedded 不装配事件镜像（stream.Mirror 恒 nil，Wave A 行为不变）。
	embedded.expectConnMirrorDisabled("embedded")
	// C1：NATIVE_ENABLED 默认 off → /v1/native/* 路由不存在（404），不影响既有端点。
	embedded.expectStatus("native routes off (default)", http.MethodGet, "/v1/native/presence/self", nil, false, http.StatusNotFound)
	// C5b：关闭态必须是**可解释拒绝码**（不是 500，也不是裸 404 文本），回滚演练依赖它。
	embedded.expectStatus("native disabled code", http.MethodGet, "/v1/native/presence/self", nil, false, http.StatusNotFound, `"NATIVE_DISABLED"`)
	// 生产故障回归点：旧实现真实网关 404，自检单独 new mux 却绿。
	embedded.expectOK("msg seq", http.MethodPost, "/v1/msg/seq", []byte(`{"userId":1}`), false)
	embedded.expectOK("trust grant", http.MethodPost, "/v1/trust/grant", []byte(`{"userId":1,"targetId":2,"level":3}`), false)
	// embedded 的 /v1/ws 必须还是旧语义（无 topic → 400 missing topic，不建真实 WS）。
	embedded.expectStatus("ws embedded missing topic", http.MethodGet, "/v1/ws", nil, false, http.StatusBadRequest, `"missing topic"`)
	// D8-B 降级路径：人类 IM 走腾讯云 PaaS，缺少真实凭据时必须优雅降级而不是崩溃。
	embedded.expectDegraded("im webhook (no PaaS creds)", http.MethodPost, "/v1/im/webhook", []byte(`{}`), false)
	embedded.expectOK("healthz after degraded IM", http.MethodGet, "/healthz", nil, true)
	embedded.close()

	// ---- 2) standalone（B2-a）：不接 conn-gateway 内核仍可用，/v1/ws 421+hint ----
	_ = os.Setenv("CONN_MODE", bootstrap.ConnModeStandalone)
	_ = os.Setenv("CONN_HINT", ":19999") // 固定 hint 便于断言
	standalone := mustStart()
	standalone.expectOK("standalone healthz", http.MethodGet, "/healthz", nil, true)
	standalone.expectOK("standalone plugins", http.MethodGet, "/v1/plugins", nil, false)
	standalone.expectStatus("standalone ws 421+hint", http.MethodGet, "/v1/ws", nil, false,
		http.StatusMisdirectedRequest,
		`"error":"ws moved to conn-gateway"`,
		`"hint":"connect :19999"`)
	// B2-c：PG_DSN 为空 → 投递链路降级不装配（端点仍可用），自检必须证明网关没挂。
	standalone.expectConnMirrorDisabled("standalone (PG_DSN empty)")
	standalone.close()

	// ---- 3) dual（B2-a 占位）：端点语义同 embedded ----
	_ = os.Setenv("CONN_MODE", bootstrap.ConnModeDual)
	_ = os.Unsetenv("CONN_HINT")
	dual := mustStart()
	dual.expectStatus("ws dual same as embedded", http.MethodGet, "/v1/ws", nil, false, http.StatusBadRequest, `"missing topic"`)
	dual.expectConnMirrorDisabled("dual (PG_DSN empty)")
	dual.close()

	// ---- 4) 非法模式：装配期必须失败，且错误含合法值列表 ----
	_ = os.Setenv("CONN_MODE", "bogus")
	if _, err := startServer(); err == nil {
		log.Fatalf("[FAIL] CONN_MODE=bogus: Assemble succeeded, want %v", bootstrap.ErrConnModeMisconfigured)
	} else {
		if !errors.Is(err, bootstrap.ErrConnModeMisconfigured) {
			log.Fatalf("[FAIL] CONN_MODE=bogus: err=%v, want errors.Is(ErrConnModeMisconfigured)", err)
		}
		for _, mode := range bootstrap.ValidConnModes() {
			if !strings.Contains(err.Error(), mode) {
				log.Fatalf("[FAIL] CONN_MODE=bogus: error missing valid mode %q; err=%v", mode, err)
			}
		}
		log.Printf("  [PASS] CONN_MODE=bogus → startup rejected: %v", err)
	}
	_ = os.Unsetenv("CONN_MODE")
	_ = os.Unsetenv("CONN_HINT")

	log.Println("[gateway] SELF-TEST PASS: 网关路由分发正常（含 IM 未配置降级 + CONN_MODE 三模式）")
}

// truncate 截断过长的响应体，失败信息最多打印 200 字符。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s…(%d bytes truncated)", s[:n], len(s)-n)
}

// restoreEnv 恢复环境变量（had=false 表示原本不存在 → Unset）。
func restoreEnv(key, value string, had bool) {
	if had {
		_ = os.Setenv(key, value)
		return
	}
	_ = os.Unsetenv(key)
}
