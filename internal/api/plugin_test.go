// SPDX-License-Identifier: MIT
//
// 插件 / 归属端点单测（契约来源：PLUGIN-CONTRACT.md §4）：httptest 覆盖
// GET /v1/plugins、GET /v1/plugins/{id}（含未注册 404）、GET /v1/about/attribution
// （JSON + ?format=text 人读版），以及出站统一用的插件单例发送器取用（nil-safe 回退）。
package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/plugin"
	"human-agent-im/internal/version"
)

const (
	testPluginID      = "openclaw"
	testPluginVersion = "2026.10.7"
	testCopyright     = "Copyright (c) 2026 OpenClaw Foundation"
	testUpstream      = "https://github.com/openclaw/openclaw"
	testNoticePath    = "THIRD_PARTY_LICENSES/OpenClaw-MIT.txt"

	// 第二个上游：OpenClaw 的微信通道插件（契约 §9-D5）。
	testWeixinName      = "openclaw-weixin"
	testWeixinUpstream  = "https://github.com/NewFuture/openclaw-weixin"
	testWeixinCopyright = "Copyright (c) 2026 Tencent; Copyright (c) 2026 Contributors to NewFuture/openclaw-weixin"
	testWeixinNotice    = "THIRD_PARTY_LICENSES/OpenClaw-Weixin-MIT.txt"
)

// fakePlugin 是契约 §1 Plugin 接口的测试替身：不启动子进程、不触网。
// attrs 非空时模拟插件的多上游扩展（Attributions()），否则回退单条 Attribution()。
type fakePlugin struct {
	id     string
	ver    string
	state  string
	detail string
	caps   []plugin.Capability
	attr   plugin.Attribution
	attrs  []plugin.Attribution
}

func (f *fakePlugin) ID() string                        { return f.id }
func (f *fakePlugin) Version() string                   { return f.ver }
func (f *fakePlugin) Capabilities() []plugin.Capability { return f.caps }
func (f *fakePlugin) Attribution() plugin.Attribution   { return f.attr }
func (f *fakePlugin) Start(context.Context) error       { return nil }
func (f *fakePlugin) Stop(context.Context) error        { return nil }

// Attributions 模拟 openclawplugin 的向后兼容多上游扩展（契约 §9-D5）。
func (f *fakePlugin) Attributions() []plugin.Attribution {
	if len(f.attrs) > 0 {
		return f.attrs
	}
	return []plugin.Attribution{f.attr}
}

func (f *fakePlugin) Health(context.Context) plugin.Health {
	state := f.state
	if state == "" {
		state = "ok"
	}
	return plugin.Health{State: state, Detail: f.detail, CheckedAt: time.Now()}
}

// newFakeOpenClawPlugin 构造与契约 §2 对齐的 openclaw 插件替身（四项能力 + 两条 MIT 归属）。
func newFakeOpenClawPlugin() *fakePlugin {
	main := plugin.Attribution{
		Upstream:     testUpstream,
		UpstreamName: "OpenClaw",
		License:      "MIT",
		Copyright:    testCopyright,
		NoticePath:   testNoticePath,
		Derived:      true,
		Usage:        []string{"wechat personal channel via openclaw CLI", "account registry file contract"},
	}
	weixin := plugin.Attribution{
		Upstream:     testWeixinUpstream,
		UpstreamName: testWeixinName,
		License:      "MIT",
		Copyright:    testWeixinCopyright,
		NoticePath:   testWeixinNotice,
		Derived:      false,
		Usage:        []string{"wechat personal channel supplied by the openclaw-weixin plugin"},
	}
	return &fakePlugin{
		id:    testPluginID,
		ver:   testPluginVersion,
		state: "ok",
		caps: []plugin.Capability{
			{ID: "openclaw.wechat-personal-channel", Kind: "channel", Version: testPluginVersion, Modes: []string{"qr-login", "text-send"}},
			{ID: "openclaw.account-registry", Kind: "channel", Version: testPluginVersion, Modes: []string{"watch", "resolve"}},
			{ID: "openclaw.mcp-route-tool", Kind: "runtime", Version: testPluginVersion, Modes: []string{"im2a_route"}},
			{ID: "openclaw.agent-gateway", Kind: "runtime", Version: testPluginVersion, Modes: []string{"status", "ensure"}},
		},
		attr:  main,
		attrs: []plugin.Attribution{main, weixin},
	}
}

// setupPluginRoutes 注册替身插件、构造带宿主注入的 Deps，并返回挂好三个端点的测试服务器。
func setupPluginRoutes(t *testing.T) *httptest.Server {
	return setupPluginRoutesWith(t, newFakeOpenClawPlugin())
}

// setupPluginRoutesWith 用指定插件集合构造测试服务器。
func setupPluginRoutesWith(t *testing.T, plugins ...plugin.Plugin) *httptest.Server {
	t.Helper()
	host := plugin.NewHost(log.New(io.Discard, "", 0))
	for _, p := range plugins {
		if err := host.Register(p); err != nil {
			t.Fatalf("register fake plugin: %v", err)
		}
	}
	if err := host.Start(context.Background()); err != nil {
		t.Fatalf("start fake plugin: %v", err)
	}
	t.Cleanup(func() { _ = host.Stop(context.Background()) })

	d := &Deps{Plugins: host}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/plugins", http.HandlerFunc(d.PluginList))
	mux.Handle("GET /v1/plugins/{id}", http.HandlerFunc(d.PluginGet))
	mux.Handle("GET /v1/about/attribution", http.HandlerFunc(d.Attribution))

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// getHTTP 发 GET 请求并返回状态码、响应体与响应头。
func getHTTP(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body), resp.Header
}

// TestPluginListEndpoint 覆盖 GET /v1/plugins：openclaw 已注册且带 attribution。
func TestPluginListEndpoint(t *testing.T) {
	ts := setupPluginRoutes(t)
	code, body, hdr := getHTTP(t, ts.URL+"/v1/plugins")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	if ct := hdr.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got struct {
		Plugins []plugin.Snapshot `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode /v1/plugins: %v; body=%s", err, body)
	}
	if len(got.Plugins) != 1 {
		t.Fatalf("len(plugins) = %d, want 1; body=%s", len(got.Plugins), body)
	}
	p := got.Plugins[0]
	if p.ID != testPluginID {
		t.Fatalf("plugin id = %q, want %q", p.ID, testPluginID)
	}
	if p.Version != testPluginVersion {
		t.Fatalf("plugin version = %q, want %q", p.Version, testPluginVersion)
	}
	if p.Attribution.License != "MIT" {
		t.Fatalf("attribution.license = %q, want MIT", p.Attribution.License)
	}
	if p.Attribution.Copyright != testCopyright {
		t.Fatalf("attribution.copyright = %q, want %q", p.Attribution.Copyright, testCopyright)
	}
	if p.Attribution.NoticePath != testNoticePath {
		t.Fatalf("attribution.noticePath = %q, want %q", p.Attribution.NoticePath, testNoticePath)
	}
	if len(p.Capabilities) != 4 {
		t.Fatalf("len(capabilities) = %d, want 4", len(p.Capabilities))
	}
	// 契约 §4：快照必须带 attribution 字段名，供机器自检。
	if !strings.Contains(body, `"attribution"`) {
		t.Fatalf("response missing attribution key: %s", body)
	}
}

// TestPluginGetEndpoint 覆盖 GET /v1/plugins/{id}：命中 200、未注册 404。
func TestPluginGetEndpoint(t *testing.T) {
	ts := setupPluginRoutes(t)

	code, body, _ := getHTTP(t, ts.URL+"/v1/plugins/"+testPluginID)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	var got plugin.Snapshot
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode snapshot: %v; body=%s", err, body)
	}
	if got.ID != testPluginID {
		t.Fatalf("snapshot id = %q, want %q", got.ID, testPluginID)
	}
	if got.Attribution.Upstream != testUpstream {
		t.Fatalf("attribution.upstream = %q, want %q", got.Attribution.Upstream, testUpstream)
	}

	// 未注册插件 → 404，且响应体不泄漏内部错误细节（只给稳定错误文本）。
	code, body, _ = getHTTP(t, ts.URL+"/v1/plugins/not-registered")
	if code != http.StatusNotFound {
		t.Fatalf("unknown plugin status = %d, want 404; body=%s", code, body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &errBody); err != nil {
		t.Fatalf("decode 404 body: %v; body=%s", err, body)
	}
	if errBody.Error == "" {
		t.Fatalf("404 body missing error field: %s", body)
	}
	// 不泄漏内部细节：404 只回稳定错误文本，不回路径 / 栈 / 环境信息。
	for _, leak := range []string{"panic", "goroutine", "/root", "OPENCLAW_BIN"} {
		if strings.Contains(body, leak) {
			t.Fatalf("404 body leaks internals (%q): %s", leak, body)
		}
	}
}

// TestAttributionJSON 覆盖 GET /v1/about/attribution 的契约 §4 JSON 结构（含 §9-D5 第二条上游）。
func TestAttributionJSON(t *testing.T) {
	ts := setupPluginRoutes(t)
	code, body, hdr := getHTTP(t, ts.URL+"/v1/about/attribution")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	if ct := hdr.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got attributionResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode attribution: %v; body=%s", err, body)
	}
	if got.Product != attributionProduct {
		t.Fatalf("product = %q, want %q", got.Product, attributionProduct)
	}
	if got.Version != version.Version {
		t.Fatalf("version = %q, want %q", got.Version, version.Version)
	}
	if len(got.ThirdParty) != 2 {
		t.Fatalf("len(thirdParty) = %d, want 2 (openclaw + openclaw-weixin); body=%s", len(got.ThirdParty), body)
	}

	// [0] OpenClaw 本体：pluginId = 插件 ID。
	tp := got.ThirdParty[0]
	if tp.PluginID != testPluginID {
		t.Fatalf("thirdParty[0].pluginId = %q, want %q", tp.PluginID, testPluginID)
	}
	if tp.License != "MIT" {
		t.Fatalf("thirdParty[0].license = %q, want MIT", tp.License)
	}
	if tp.Copyright != testCopyright {
		t.Fatalf("thirdParty[0].copyright = %q, want %q", tp.Copyright, testCopyright)
	}
	if tp.NoticePath != testNoticePath {
		t.Fatalf("thirdParty[0].noticePath = %q, want %q", tp.NoticePath, testNoticePath)
	}
	if tp.Upstream != testUpstream {
		t.Fatalf("thirdParty[0].upstream = %q, want %q", tp.Upstream, testUpstream)
	}
	if len(tp.Usage) == 0 {
		t.Fatalf("thirdParty[0].usage is empty")
	}

	// [1] openclaw-weixin（外部通道插件）：pluginId 用上游名区分。
	tw := got.ThirdParty[1]
	if tw.PluginID != testWeixinName {
		t.Fatalf("thirdParty[1].pluginId = %q, want %q", tw.PluginID, testWeixinName)
	}
	if tw.Upstream != testWeixinUpstream {
		t.Fatalf("thirdParty[1].upstream = %q, want %q", tw.Upstream, testWeixinUpstream)
	}
	if tw.Copyright != testWeixinCopyright {
		t.Fatalf("thirdParty[1].copyright = %q, want %q", tw.Copyright, testWeixinCopyright)
	}
	if tw.License != "MIT" {
		t.Fatalf("thirdParty[1].license = %q, want MIT", tw.License)
	}
	if tw.NoticePath != testWeixinNotice {
		t.Fatalf("thirdParty[1].noticePath = %q, want %q", tw.NoticePath, testWeixinNotice)
	}

	for _, key := range []string{`"product"`, `"version"`, `"thirdParty"`, `"pluginId"`, `"noticePath"`} {
		if !strings.Contains(body, key) {
			t.Fatalf("response missing contract key %s: %s", key, body)
		}
	}
}

// TestAttributionText 覆盖 ?format=text：人读版含两条上游的 MIT 版权行与上游 URL。
func TestAttributionText(t *testing.T) {
	ts := setupPluginRoutes(t)
	code, body, hdr := getHTTP(t, ts.URL+"/v1/about/attribution?format=text")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	if ct := hdr.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Fatalf("format=text returned JSON: %s", body)
	}
	for _, want := range []string{
		"IM2A", "MIT", "used for:", testPluginID, testUpstream, testCopyright, testNoticePath,
		testWeixinName, testWeixinUpstream, testWeixinCopyright, testWeixinNotice,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("text attribution missing %q:\n%s", want, body)
		}
	}
}

// plainPlugin 只实现契约 §1 接口、不提供多上游扩展：验证 api 层向后兼容回退为单条归属。
type plainPlugin struct {
	id   string
	attr plugin.Attribution
}

func (p *plainPlugin) ID() string                        { return p.id }
func (p *plainPlugin) Version() string                   { return "0.0.1" }
func (p *plainPlugin) Capabilities() []plugin.Capability { return nil }
func (p *plainPlugin) Attribution() plugin.Attribution   { return p.attr }
func (p *plainPlugin) Start(context.Context) error       { return nil }
func (p *plainPlugin) Stop(context.Context) error        { return nil }
func (p *plainPlugin) Health(context.Context) plugin.Health {
	return plugin.Health{State: "ok", CheckedAt: time.Now()}
}

// TestAttributionSingleUpstreamPlugin 验证没有 Attributions() 扩展的插件仍按单条归属输出。
func TestAttributionSingleUpstreamPlugin(t *testing.T) {
	ts := setupPluginRoutesWith(t, &plainPlugin{
		id: "solo",
		attr: plugin.Attribution{
			Upstream: "https://example.com/solo", UpstreamName: "Solo", License: "MIT",
			Copyright: "Copyright (c) 2026 Solo", NoticePath: "THIRD_PARTY_LICENSES/Solo-MIT.txt",
			Usage: []string{"solo usage"},
		},
	})
	code, body, _ := getHTTP(t, ts.URL+"/v1/about/attribution")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	var got attributionResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode attribution: %v; body=%s", err, body)
	}
	if len(got.ThirdParty) != 1 {
		t.Fatalf("len(thirdParty) = %d, want 1; body=%s", len(got.ThirdParty), body)
	}
	if got.ThirdParty[0].PluginID != "solo" {
		t.Fatalf("thirdParty[0].pluginId = %q, want solo", got.ThirdParty[0].PluginID)
	}
	if got.ThirdParty[0].Upstream != "https://example.com/solo" {
		t.Fatalf("thirdParty[0].upstream = %q", got.ThirdParty[0].Upstream)
	}
}

// TestResolveOpenClawSender 验证出站统一取插件单例，且未接线时 nil-safe 回退不 panic。
func TestResolveOpenClawSender(t *testing.T) {
	singleton := openclaw.NewSender()
	provider := func() *openclaw.Sender { return singleton }
	if got := resolveOpenClawSender(provider); got != singleton {
		t.Fatalf("resolveOpenClawSender(provider) != plugin singleton")
	}
	if got := resolveOpenClawSender(func() *openclaw.Sender { return nil }); got == nil {
		t.Fatalf("resolveOpenClawSender(nil-returning provider) = nil, want fallback instance")
	}
	if got := resolveOpenClawSender(nil); got == nil {
		t.Fatalf("resolveOpenClawSender(nil) = nil, want fallback instance")
	}
}

// TestPluginEndpointsWithoutHost 验证宿主未接线时返回 503（不 panic、不伪造数据）。
func TestPluginEndpointsWithoutHost(t *testing.T) {
	d := &Deps{}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{
		{"plugins", d.PluginList, "/v1/plugins"},
		{"plugin get", d.PluginGet, "/v1/plugins/openclaw"},
		{"attribution", d.Attribution, "/v1/about/attribution"},
	} {
		rec := httptest.NewRecorder()
		tc.handler(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status = %d, want 503", tc.name, rec.Code)
		}
	}
}
