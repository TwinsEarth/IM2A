package api

// 合规回归（task-27，2026-10-25 用户硬指令）：
//   源码/默认值里不得出现任何用户生产机地址；端侧出端白名单必须显式配置，
//   默认空 = fail-closed（拒绝），不能因为"忘了配置"而全部放行。
//
// 本文件刻意不在测试里写任何生产 IP 字面量：默认值断言为「空列表」比「不含某个
// IP」更强（空列表不可能包含任何地址），环境变量/端点派生用占位域名验证。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ① 默认值不含任何 host（即不可能含生产 IP）。
func TestEdgeEgressAllowHostsDefaultEmpty(t *testing.T) {
	t.Setenv("IM2A_EGRESS_ALLOW", "")
	hosts := egressAllowHosts()
	if len(hosts) != 0 {
		t.Fatalf("默认白名单必须为空（源码不得内置任何环境/生产地址），got %v", hosts)
	}
}

// ② env 覆盖生效：解析逗号分隔、去空白/空项，端点按白名单派生。
func TestEdgeEgressAllowHostsEnvOverride(t *testing.T) {
	t.Setenv("IM2A_EGRESS_ALLOW", " api.example.com , localhost ,, ")
	hosts := egressAllowHosts()
	if len(hosts) != 2 || hosts[0] != "api.example.com" || hosts[1] != "localhost" {
		t.Fatalf("env 解析错误，got %v", hosts)
	}
	if got := egressEndpoint(hosts); got != "https://api.example.com/v1/intent" {
		t.Fatalf("端点应由白名单第一个 host 派生，got %q", got)
	}

	// 显式端点优先于派生值。
	t.Setenv("IM2A_EDGE_EGRESS_ENDPOINT", "https://gateway.example/v1/intent")
	if got := egressEndpoint(hosts); got != "https://gateway.example/v1/intent" {
		t.Fatalf("IM2A_EDGE_EGRESS_ENDPOINT 覆盖未生效，got %q", got)
	}
}

// ②b 格式校验：空格/空项/大小写/scheme/路径/端口的行为可预期（合法保留、非法丢弃、去重）。
func TestEdgeEgressAllowHostsValidation(t *testing.T) {
	t.Setenv("IM2A_EGRESS_ALLOW", " API.EXAMPLE.COM ,, localhost , HTTPS://bad.example , bad.example/path , bad.example:443 , api.example.com ")
	hosts := egressAllowHosts()
	want := []string{"api.example.com", "localhost"}
	if len(hosts) != len(want) {
		t.Fatalf("合法项解析错误：got %v, want %v（非法项必须被丢弃）", hosts, want)
	}
	for i := range want {
		if hosts[i] != want[i] {
			t.Fatalf("合法项解析错误：got %v, want %v", hosts, want)
		}
	}
}

// ②c 全是非法项 → 等价于未配置 → fail-closed（绝不把垃圾配置静默当成放行集合）。
func TestEdgeEgressAllowHostsAllInvalidFailsClosed(t *testing.T) {
	t.Setenv("IM2A_EGRESS_ALLOW", "https://only.example, only.example/path, only.example:443")
	if hosts := egressAllowHosts(); len(hosts) != 0 {
		t.Fatalf("全部非法项必须被丢弃（白名单为空），got %v", hosts)
	}
	t.Setenv("IM2A_EDGE_EGRESS_ENDPOINT", "")
	p := newEdgePipeline()
	r := p.Process("帮我打开空调")
	ok, reason, _ := p.Egress(r)
	if ok {
		t.Fatalf("全部非法项时出站必须拒绝（fail-closed），reason=%q", reason)
	}
}

// ③ 空配置时行为可预期：fail-closed 拒绝，且原因是空白名单而不是别的。
func TestEdgePipelineEmptyConfigFailsClosed(t *testing.T) {
	t.Setenv("IM2A_EGRESS_ALLOW", "")
	t.Setenv("IM2A_EDGE_EGRESS_ENDPOINT", "")
	p := newEdgePipeline()
	r := p.Process("帮我打开空调")
	if !r.ShouldUpload {
		t.Fatalf("测试前置：正常命令应通过敏感过滤，got %+v", r)
	}
	ok, reason, _ := p.Egress(r)
	if ok {
		t.Fatalf("白名单为空必须拒绝（fail-closed），reason=%q", reason)
	}
	if !strings.Contains(reason, "fail-closed") {
		t.Fatalf("拒绝原因应包含 fail-closed，got %q", reason)
	}
}

// ⑤ handler 级回归（不依赖 PG）：显式配置白名单 → /v1/edge/pipeline 允许出端；
// 未配置 → 同一 handler fail-closed 拒绝。锁住"handler 使用包级运行期管道"的接线，
// 也防止 v14 真库测试以外再出现"默认放行"的假设。
func TestEdgePipelineHandlerExplicitConfig(t *testing.T) {
	prev := edgePipeline
	t.Cleanup(func() { edgePipeline = prev })

	call := func() map[string]any {
		t.Helper()
		d := &Deps{}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/edge/pipeline", strings.NewReader(`{"text":"帮我打开空调"}`))
		d.EdgePipeline(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
		return out
	}

	t.Setenv("IM2A_EGRESS_ALLOW", "localhost")
	t.Setenv("IM2A_EDGE_EGRESS_ENDPOINT", "")
	edgePipeline = newEdgePipeline()
	if out := call(); out["egress_allowed"] != true {
		t.Fatalf("显式配置白名单后 handler 应允许出端，got %v reason=%v", out["egress_allowed"], out["reason"])
	}

	t.Setenv("IM2A_EGRESS_ALLOW", "")
	t.Setenv("IM2A_EDGE_EGRESS_ENDPOINT", "")
	edgePipeline = newEdgePipeline()
	if out := call(); out["egress_allowed"] != false {
		t.Fatalf("未配置白名单时 handler 必须 fail-closed 拒绝，got %v reason=%v", out["egress_allowed"], out["reason"])
	}
}

// ④ 显式配置后放行：证明 env 配置链路真的生效（不是只解析不启用）。
func TestEdgePipelineEnvConfigAllowsConfiguredHost(t *testing.T) {
	t.Setenv("IM2A_EGRESS_ALLOW", "api.example.com")
	t.Setenv("IM2A_EDGE_EGRESS_ENDPOINT", "")
	p := newEdgePipeline()
	r := p.Process("帮我打开空调")
	ok, reason, _ := p.Egress(r)
	if !ok {
		t.Fatalf("显式配置白名单后应放行，reason=%q", reason)
	}
}
