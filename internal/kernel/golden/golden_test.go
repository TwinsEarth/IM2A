// SPDX-License-Identifier: MIT
//
// 黑盒 golden 测试：构建并启动真实 gateway 子进程，遍历固定路由表，把响应归一化后
// 与 testdata/golden.json 对比。用于证明「路由注册表化 / bootstrap 抽取」重构前后
// 对外可观察行为不变。
//
// 用法：
//
//	go test ./internal/kernel/golden -run TestGoldenRoutes -count=1 -v              # 对比基线
//	go test ./internal/kernel/golden -run TestGoldenRoutes -count=1 -v -args -update # 重录基线
package golden

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var goldenUpdate = flag.Bool("update", false, "rewrite testdata/golden.json with current observations")

const (
	goldenAdminToken = "golden-token"
	goldenJWTSecret  = "golden-secret"
	goldenFileName   = "golden.json"
	// goldenStandaloneFileName 是 B2-a standalone 模式的独立基线（与 embedded 文件分离）。
	goldenStandaloneFileName = "golden-standalone.json"
)

// goldenCase 是一条黑盒用例：最小请求 + 是否携带 admin token。
type goldenCase struct {
	method string
	path   string
	body   string // 非空时作为 JSON body 发送
	admin  bool   // true 时携带 Authorization: Bearer golden-token
	stream bool   // true 时只读响应头（SSE 类长连接，不读 body）
}

// Observation 是一条路由的归一化观察值（只含稳定信息）。
type Observation struct {
	Status      int               `json:"status"`
	ContentType string            `json:"contentType,omitempty"`
	JSONShape   []string          `json:"jsonShape,omitempty"`
	Stable      map[string]string `json:"stable,omitempty"`
	TextHead    string            `json:"textHead,omitempty"`
	BodyHash    string            `json:"bodyHash,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// goldenFile 是 testdata/golden.json 的结构。
type goldenFile struct {
	Generator string                 `json:"generator"`
	Note      string                 `json:"note,omitempty"`
	Routes    map[string]Observation `json:"routes"`
}

// stableValueKeys 是要保留「值」的稳定字段（其余字段只记录类型/路径）。
// 这些值在同样输入下必须逐字不变，可用于发现归属信息/版本/状态的意外变化。
var stableValueKeys = map[string]bool{
	"product": true, "version": true, "state": true, "status": true,
	"service": true, "license": true, "pluginId": true, "copyright": true,
	"noticePath": true, "upstream": true, "upstreamName": true, "kind": true,
	"commit": true,
}

// routeTable 是黑盒路由清单：从 services/gateway/main.go 的 mux.Handle/HandleFunc 实测抄写，
// 并补充每个前缀域的代表性内部路径（真正走 handler 而不是外层的 404/405）。
func routeTable() []goldenCase {
	return []goldenCase{
		// ---- core：根/健康/指标/认证/WS/事件 ----
		{method: "GET", path: "/"},
		{method: "GET", path: "/healthz", admin: true},
		{method: "GET", path: "/metrics", admin: true},
		{method: "GET", path: "/v1/ops/outbox", admin: true},
		{method: "GET", path: "/v1/ops/orchestration", admin: true},
		{method: "GET", path: "/v1/ws"},
		{method: "GET", path: "/v1/ws/stats"},
		{method: "GET", path: "/v1/events"},
		{method: "POST", path: "/v1/auth/login", body: "{}"},
		{method: "GET", path: "/v1/auth/me"},

		// ---- relation / directory / experience ----
		{method: "GET", path: "/v1/trust/"},
		{method: "POST", path: "/v1/trust/grant", body: "{}"},
		{method: "GET", path: "/v1/agents/"},
		{method: "GET", path: "/v1/agents/register"},
		{method: "GET", path: "/v1/directory/"},
		{method: "GET", path: "/v1/exp/"},

		// ---- approval / negotiation ----
		{method: "GET", path: "/v1/approvals"},
		{method: "GET", path: "/v1/approvals/"},
		{method: "GET", path: "/v1/approvals/some-id"},
		{method: "GET", path: "/v1/negotiations"},
		{method: "GET", path: "/v1/negotiations/"},

		// ---- payment / compliance / ledger ----
		{method: "GET", path: "/v1/paid/"},
		{method: "GET", path: "/v1/paidflow/"},
		{method: "GET", path: "/v1/compliance/"},
		{method: "GET", path: "/v1/ledger/"},

		// ---- memory / distill ----
		{method: "POST", path: "/v1/memory/encrypted", body: "{}"},
		{method: "GET", path: "/v1/memory/encrypted"},
		{method: "GET", path: "/v1/memory/encrypted/export"},
		{method: "POST", path: "/v1/memory/encrypted/import", body: "{}"},
		{method: "GET", path: "/v1/memory/"},
		{method: "GET", path: "/v1/distill/"},
		{method: "POST", path: "/v1/distill/feedback", body: "{}"},

		// ---- model / sse / misc LLM ----
		{method: "GET", path: "/v1/model/"},
		{method: "GET", path: "/v1/model/stats"},
		{method: "GET", path: "/v1/sse/", stream: true},
		{method: "POST", path: "/v1/sse/publish", body: "{}"},
		{method: "GET", path: "/v1/chat/stream"}, // PG_DSN 空 → DB 相关路由未注册，记录 404
		{method: "GET", path: "/v1/capability/"},
		{method: "GET", path: "/v1/route/"},
		{method: "GET", path: "/v1/intent/"},
		{method: "GET", path: "/v1/guard/"},
		{method: "GET", path: "/v1/moderation/"},
		{method: "GET", path: "/v1/push/"},
		{method: "GET", path: "/v1/support/"},

		// ---- im ----
		{method: "GET", path: "/v1/im/"},
		{method: "POST", path: "/v1/im/webhook", body: "{}"},

		// ---- msg 域（v0.5.2 msg-svc；Wave A 期间由 msg.go 根因修复，三条从生产 404 → 200）----
		{method: "POST", path: "/v1/msg/seq", body: `{"userId":1}`},
		{method: "POST", path: "/v1/msg/seq/batch", body: `{"userId":1,"n":2}`},
		{method: "GET", path: "/v1/msg/stats"},

		// ---- plugin / attribution（无认证，匿名可读）----
		{method: "GET", path: "/v1/plugins"},
		{method: "GET", path: "/v1/plugins/openclaw"},
		{method: "GET", path: "/v1/plugins/not-registered"},
		{method: "GET", path: "/v1/about/attribution"},
		{method: "GET", path: "/v1/about/attribution?format=text"},

		// ---- openclaw / wechat ----
		{method: "GET", path: "/v1/openclaw/"},
		{method: "GET", path: "/v1/openclaw/health"},
		{method: "GET", path: "/v1/wechatbot/"},
		{method: "GET", path: "/v1/wechat/"},
		{method: "POST", path: "/v1/wechat/identify", body: "{}"},
		{method: "GET", path: "/v1/quota/"},
		{method: "GET", path: "/v1/tempgroup/"},
		{method: "GET", path: "/v1/botroute/"},
		{method: "GET", path: "/v1/report/"},
		{method: "GET", path: "/v2/wechat/mcp"},       // PG_DSN 空 → 未注册
		{method: "GET", path: "/v1/wechat/webhook"},   // PG_DSN 空 → 未注册
		{method: "GET", path: "/v1/wechat/auto-nego"}, // PG_DSN 空 → 未注册

		// ---- a2a / orchestrator / mcp ----
		{method: "GET", path: "/v1/a2a/"},
		{method: "GET", path: "/v1/gate/"},
		{method: "GET", path: "/v1/stranger/"},
		{method: "GET", path: "/v1/orchestrator/"},
		{method: "GET", path: "/v1/audit"},
		{method: "GET", path: "/mcp"},
		{method: "POST", path: "/mcp", body: "{}"},
		{method: "GET", path: "/mcp/skills"},

		// ---- ops / admin / privacy ----
		{method: "POST", path: "/v1/watch/approve", body: "{}"},
		{method: "POST", path: "/v1/watch/reject", body: "{}"},
		{method: "POST", path: "/v1/watch/escalate", body: "{}"},
		{method: "POST", path: "/v1/device/register", body: "{}"},
		{method: "POST", path: "/v1/sync/state", body: "{}"},
		{method: "GET", path: "/v1/sync/state/u1"},
		{method: "POST", path: "/v1/offline/submit", body: "{}"},
		{method: "GET", path: "/v1/offline/pending"},
		{method: "POST", path: "/v1/offline/sync", body: "{}"},
		{method: "GET", path: "/v1/agent/logs"},
		{method: "GET", path: "/v1/gray/eval?feature=f&entity=e"},
		{method: "POST", path: "/v1/edge/pipeline", body: "{}"},
		{method: "GET", path: "/v1/edge/egress/report"},
		{method: "GET", path: "/v1/admin/metrics", admin: true},
		{method: "GET", path: "/v1/admin/audit", admin: true},
		{method: "GET", path: "/v1/admin/health", admin: true},
		{method: "GET", path: "/v1/admin/security", admin: true},
		{method: "GET", path: "/v1/egress/tickets", admin: true},
		{method: "POST", path: "/v1/privacy/export", admin: true, body: "{}"},
		{method: "POST", path: "/v1/privacy/delete", admin: true, body: "{}"},
		{method: "GET", path: "/v1/privacy/requests", admin: true},
		{method: "GET", path: "/v1/admin/", admin: true},
		{method: "GET", path: "/v1/admin/dashboard", admin: true},

		// ---- admin 页面（内嵌 HTML，adminAuth 保护）----
		{method: "GET", path: "/admin/api-playground"},                           // 无 token → 401 登录页
		{method: "GET", path: "/admin/api-playground?token=" + goldenAdminToken}, // token → 200 页面
		{method: "GET", path: "/admin/broadcast"},
		{method: "GET", path: "/admin/broadcast?token=" + goldenAdminToken},
		{method: "GET", path: "/admin/"},
		{method: "GET", path: "/admin/?token=" + goldenAdminToken},
		{method: "GET", path: "/admin/wechat-bind"}, // 公开绑定页（无 adminAuth）
	}
}

func TestGoldenRoutes(t *testing.T) {
	if os.Getenv("GOLDEN_SKIP") == "1" {
		t.Skip("GOLDEN_SKIP=1")
	}
	root := repoRoot(t)
	bin := buildGateway(t, root)
	baseURL, stop := startGateway(t, bin)
	defer stop()

	client := &http.Client{Timeout: 6 * time.Second}
	got := goldenFile{
		Generator: "internal/kernel/golden",
		Note:      "black-box gateway routes snapshot; normalized (no timestamps/IDs/random values)",
		Routes:    make(map[string]Observation, len(routeTable())),
	}
	for _, c := range routeTable() {
		key := c.method + " " + c.path
		got.Routes[key] = observe(t, client, baseURL, c)
	}

	if *goldenUpdate {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		raw, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("marshal golden: %v", err)
		}
		raw = append(raw, '\n')
		path := filepath.Join("testdata", goldenFileName)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("golden baseline updated: %d routes -> %s", len(got.Routes), path)
		return
	}

	raw, err := os.ReadFile(filepath.Join("testdata", goldenFileName))
	if err != nil {
		t.Fatalf("read golden baseline (record it first with -args -update): %v", err)
	}
	var want goldenFile
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse golden baseline: %v", err)
	}
	diffs := compareGolden(want.Routes, got.Routes)
	if len(diffs) > 0 {
		for _, d := range diffs {
			t.Errorf("golden diff:\n%s", d)
		}
		t.Fatalf("%d golden diff(s) across %d routes", len(diffs), len(got.Routes))
	}
	t.Logf("golden: %d routes, 0 diff", len(got.Routes))
}

// standaloneCases 是 B2-a 独立基线（CONN_MODE=standalone）：只覆盖"模式相关"的最小集合，
// 与 embedded 的 104-case 基线文件完全分离（契约 v2 §2 第 6 条）。
//   - /v1/ws 必须是 421 + hint（而不是 404/500，也不是 WS 升级）；
//   - /healthz、/v1/plugins 证明"conn-gateway 没起，内核仍可用"。
func standaloneCases() []goldenCase {
	return []goldenCase{
		{method: "GET", path: "/v1/ws"},
		{method: "GET", path: "/healthz", admin: true},
		{method: "GET", path: "/v1/plugins"},
	}
}

// TestGoldenStandaloneRoute 用第二个 gateway 子进程（CONN_MODE=standalone）记录/比对独立基线，
// 不改动 embedded 的 104-case 基线。
func TestGoldenStandaloneRoute(t *testing.T) {
	if os.Getenv("GOLDEN_SKIP") == "1" {
		t.Skip("GOLDEN_SKIP=1")
	}
	root := repoRoot(t)
	bin := buildGateway(t, root)
	baseURL, stop := startGatewayWithEnv(t, bin, gatewayEnvWith(map[string]string{
		"CONN_MODE": "standalone",
		"CONN_HINT": ":18081",
	}))
	defer stop()

	client := &http.Client{Timeout: 6 * time.Second}
	got := goldenFile{
		Generator: "internal/kernel/golden",
		Note:      "B2-a CONN_MODE=standalone routes (separate baseline; embedded 104-case file untouched)",
		Routes:    make(map[string]Observation, len(standaloneCases())),
	}
	for _, c := range standaloneCases() {
		got.Routes[c.method+" "+c.path] = observe(t, client, baseURL, c)
	}

	// 强断言（不只依赖快照）：421 + 契约要求的 error/hint 文案。
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/ws", nil)
	if err != nil {
		t.Fatalf("build /v1/ws request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/ws: %v", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("standalone GET /v1/ws: status=%d, want 421; body=%q", resp.StatusCode, raw)
	}
	for _, sub := range []string{`"error":"ws moved to conn-gateway"`, `"hint":"connect :18081"`} {
		if !strings.Contains(string(raw), sub) {
			t.Fatalf("standalone GET /v1/ws: body missing %q; body=%q", sub, raw)
		}
	}

	path := filepath.Join("testdata", goldenStandaloneFileName)
	if *goldenUpdate {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		buf, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("marshal standalone golden: %v", err)
		}
		buf = append(buf, '\n')
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("golden standalone baseline updated: %d routes -> %s", len(got.Routes), path)
		return
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read standalone golden baseline (record it first with -args -update): %v", err)
	}
	var want goldenFile
	if err := json.Unmarshal(saved, &want); err != nil {
		t.Fatalf("parse standalone golden baseline: %v", err)
	}
	if diffs := compareGolden(want.Routes, got.Routes); len(diffs) > 0 {
		for _, d := range diffs {
			t.Errorf("standalone golden diff:\n%s", d)
		}
		t.Fatalf("%d standalone golden diff(s) across %d routes", len(diffs), len(got.Routes))
	}
	t.Logf("golden standalone: %d routes, 0 diff", len(got.Routes))
}

// observe 发送一条最小请求并把响应归一化。
func observe(t *testing.T, client *http.Client, base string, c goldenCase) Observation {
	t.Helper()
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	req, err := http.NewRequest(c.method, base+c.path, body)
	if err != nil {
		t.Fatalf("build request %s %s: %v", c.method, c.path, err)
	}
	if c.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.admin {
		req.Header.Set("Authorization", "Bearer "+goldenAdminToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Observation{Error: sanitizeErrText(err.Error())}
	}
	defer resp.Body.Close()
	obs := Observation{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type")}
	if c.stream {
		return obs // 长连接/SSE：只验响应头，不读 body
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		obs.Error = sanitizeErrText(err.Error())
		return obs
	}
	norm := normalizeResponse(resp.StatusCode, obs.ContentType, raw)
	norm.ContentType = obs.ContentType
	return norm
}

// normalizeResponse 把响应归一化成稳定观察值。
func normalizeResponse(status int, contentType string, raw []byte) Observation {
	obs := Observation{Status: status, ContentType: contentType}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return obs
	}
	lowerCT := strings.ToLower(contentType)
	if strings.Contains(lowerCT, "json") || trimmed[0] == '{' || trimmed[0] == '[' {
		var v any
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&v); err == nil {
			shape := map[string]string{}
			stable := map[string]string{}
			walkJSON(v, "", shape, stable, 0)
			obs.JSONShape = sortedShape(shape)
			if len(stable) > 0 {
				obs.Stable = stable
			}
			return obs
		}
	}
	if strings.Contains(lowerCT, "text/html") {
		sum := sha256.Sum256(raw)
		obs.BodyHash = "sha256:" + hex.EncodeToString(sum[:8])
		return obs
	}
	obs.TextHead = textHead(string(raw))
	return obs
}

// walkJSON 记录 JSON 的键路径 + 类型（数组只展开首个元素，路径用 [0] 表示）。
func walkJSON(v any, path string, shape, stable map[string]string, depth int) {
	if depth > 10 || len(shape) > 400 {
		return
	}
	name := path
	if name == "" {
		name = "$"
	}
	switch x := v.(type) {
	case map[string]any:
		shape[name] = "object"
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			walkJSON(x[k], childPath, shape, stable, depth+1)
			if s, ok := x[k].(string); ok && stableValueKeys[k] {
				stable[childPath] = s
			}
		}
	case []any:
		// 数组只展开首个元素；先按元素的 JSON 序列化排序，消除服务端
		// map 迭代序 / 注册序导致的「首元素不稳定」假 diff（如 /mcp/skills）。
		if len(x) == 0 {
			shape[name] = "array(empty)"
			return
		}
		shape[name] = "array"
		type sortableElem struct {
			key string
			val any
		}
		elems := make([]sortableElem, 0, len(x))
		for _, e := range x {
			raw, err := json.Marshal(e)
			if err != nil {
				raw = []byte(fmt.Sprintf("%v", e))
			}
			elems = append(elems, sortableElem{key: string(raw), val: e})
		}
		sort.Slice(elems, func(i, j int) bool { return elems[i].key < elems[j].key })
		walkJSON(elems[0].val, name+"[0]", shape, stable, depth+1)
	case string:
		shape[name] = "string"
	case json.Number:
		shape[name] = "number"
	case bool:
		shape[name] = "bool"
	case nil:
		shape[name] = "null"
	default:
		shape[name] = fmt.Sprintf("%T", x)
	}
}

var (
	timestampRe = regexp.MustCompile(`\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})?`)
	probeDurRe  = regexp.MustCompile(`probe took [0-9.]+(ns|µs|ms|s)`)
	addrRe      = regexp.MustCompile(`(127\.0\.0\.1|\[::1\]|localhost):\d+`)
)

// textHead 取 text 响应前 40 字符（去时间戳/耗时/端口等易变内容）。
func textHead(s string) string {
	s = timestampRe.ReplaceAllString(s, "<ts>")
	s = probeDurRe.ReplaceAllString(s, "probe took <dur>")
	s = addrRe.ReplaceAllString(s, "$1:<port>")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "<empty>"
	}
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

// sanitizeErrText 去掉错误文本里的端口等易变内容。
func sanitizeErrText(s string) string {
	return addrRe.ReplaceAllString(s, "$1:<port>")
}

// sortedShape 把 shape map 变成 "path:type" 的排序切片。
func sortedShape(shape map[string]string) []string {
	out := make([]string, 0, len(shape))
	for k, v := range shape {
		out = append(out, k+":"+v)
	}
	sort.Strings(out)
	return out
}

// compareGolden 逐路由比较，返回人类可读的差异列表。
func compareGolden(want, got map[string]Observation) []string {
	keys := make([]string, 0, len(want)+len(got))
	seen := map[string]bool{}
	for k := range want {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range got {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var diffs []string
	for _, k := range keys {
		w, wok := want[k]
		g, gok := got[k]
		switch {
		case !wok:
			diffs = append(diffs, fmt.Sprintf("%s\n  want: <absent from baseline>\n  got:  %s", k, oneLine(g)))
		case !gok:
			diffs = append(diffs, fmt.Sprintf("%s\n  want: %s\n  got:  <missing>", k, oneLine(w)))
		case !reflect.DeepEqual(w, g):
			diffs = append(diffs, fmt.Sprintf("%s\n  want: %s\n  got:  %s", k, oneLine(w), oneLine(g)))
		}
	}
	return diffs
}

func oneLine(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(raw)
}

// repoRoot 从当前包目录向上找到含 go.mod 的仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// buildGateway 构建真实 gateway 二进制（GOLDEN_GATEWAY_BIN 可跳过构建）。
func buildGateway(t *testing.T, root string) string {
	t.Helper()
	if bin := os.Getenv("GOLDEN_GATEWAY_BIN"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "gateway")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./services/gateway")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./services/gateway: %v\n%s", err, out)
	}
	return bin
}

// startGateway 用「随机高位端口 + 重试」启动 gateway（默认 embedded 环境），等 /healthz 200。
func startGateway(t *testing.T, bin string) (baseURL string, stop func()) {
	return startGatewayWithEnv(t, bin, gatewayEnv())
}

// startGatewayWithEnv 用指定环境启动 gateway，等 /healthz 200。
func startGatewayWithEnv(t *testing.T, bin string, env []string) (baseURL string, stop func()) {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		port := freePort(t)
		base := "http://127.0.0.1:" + strconv.Itoa(port)
		cmd := exec.Command(bin, "-port", strconv.Itoa(port))
		cmd.Env = env
		var logBuf bytes.Buffer
		cmd.Stdout = &logBuf
		cmd.Stderr = &logBuf
		if err := cmd.Start(); err != nil {
			lastErr = fmt.Errorf("start attempt %d: %w", attempt, err)
			continue
		}
		if err := waitHealthy(base, 20*time.Second); err != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			lastErr = fmt.Errorf("attempt %d (%s): %w\n--- gateway log ---\n%s", attempt, base, err, logBuf.String())
			continue
		}
		stop = func() {
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func() {
				_, _ = cmd.Process.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
			}
		}
		return base, stop
	}
	t.Fatalf("gateway failed to become healthy: %v", lastErr)
	return "", func() {}
}

// freePort 让内核分配一个空闲端口后立即释放（配合重试，避免 :0 无法从启动日志读取的问题）。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitHealthy 轮询 /healthz（带 admin token）直到 200。
func waitHealthy(base string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, base+"/healthz", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+goldenAdminToken)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("gateway not healthy within %s", timeout)
}

// gatewayEnv 是 golden 启动环境：内存存储、关闭 openclaw 探测、开启 dsbot、固定 token。
// B2-a：显式固定 CONN_MODE=embedded，避免宿主环境变量改变被测行为（行为不变 → 0 diff）。
func gatewayEnv() []string { return gatewayEnvWith(nil) }

// gatewayEnvWith 在默认 golden 环境上叠加 extra（同名覆盖，先删后加，保证唯一键）。
func gatewayEnvWith(extra map[string]string) []string {
	overrides := map[string]string{
		"PG_DSN":                "",
		"PG_DRIVER":             "",
		"GATEWAY_TOKEN":         "",
		"IM2A_ADMIN_TOKEN":      goldenAdminToken,
		"IM2A_JWT_SECRET":       goldenJWTSecret,
		"OPENCLAW_ENABLED":      "off",
		"DSBOT_ENABLED":         "1",
		"WEBHOOK_ASYNC":         "off",
		"L4_ENABLED":            "off",
		"CORS_ALLOWED":          "",
		"RATE_LIMIT_RPS":        "",
		"RATE_LIMIT_BURST":      "",
		"TIM_BASE_URL":          "",
		"LLM_BASE_URL":          "",
		"LLM_API_KEY":           "",
		"LLM_MODEL":             "",
		"DSBOT_ENDPOINT":        "",
		"BROADCAST_ACCOUNT":     "",
		"OPENCLAW_ACCOUNTS_DIR": "",
		"CONN_MODE":             "embedded",
		"CONN_HINT":             ":18081",
	}
	for k, v := range extra {
		overrides[k] = v
	}
	out := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if _, replaced := overrides[key]; !replaced {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+overrides[k])
	}
	return out
}

// ensure utf8 import is used even if textHead changes shape.
var _ = utf8.RuneCountInString
