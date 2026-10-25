package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// 真实 PG 测试：端侧 pipeline 端点 + 出端审计真实落库。PG_DSN 空则跳过。
func newEdgeDeps(t *testing.T) *Deps {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("pg not reachable: %v", err)
	}
	return &Deps{DB: db}
}

func TestV14EdgePipeline_Allow(t *testing.T) {
	d := newEdgeDeps(t)
	// task-27（v2.3.28）：端侧出站默认改为 fail-closed（空白名单一律拒绝）。
	// 本用例验证 allow 路径，必须**显式注入白名单**并重建包级管道（handler 使用 edgePipeline）；
	// 与生产部署要求一致：未配置 IM2A_EGRESS_ALLOW 的部署不允许出站。
	// 本包测试无 t.Parallel，测试内重建 + Cleanup 恢复，避免顺序污染。
	t.Setenv("IM2A_EGRESS_ALLOW", "localhost")
	t.Setenv("IM2A_EDGE_EGRESS_ENDPOINT", "")
	prev := edgePipeline
	edgePipeline = newEdgePipeline()
	t.Cleanup(func() { edgePipeline = prev })

	// audit_events append-only：edge_egress 审计行不删除；报告断言用相对增量（>=2/>=1）。
	body := `{"text":"帮我打开空调"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/edge/pipeline", strings.NewReader(body))
	d.EdgePipeline(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["category"] != "command" {
		t.Errorf("category 应 command, got %v", out["category"])
	}
	if out["egress_allowed"] != true {
		t.Errorf("正常命令应允许出端, got %v reason=%v", out["egress_allowed"], out["reason"])
	}
}

// task-27（v2.3.28）：默认 fail-closed 的 **API 层**断言。
// 与 TestV14EdgePipeline_Allow 同层（真实 handler + PG 审计落库），避免"PG-gated 与单测
// 路径不一致"：不配置任何白名单时，正常文本也必须 refused(egress_allowed=false)，
// 且 reason 含 fail-closed，而不是被静默放行。
func TestV14EdgePipeline_DefaultDeny(t *testing.T) {
	d := newEdgeDeps(t)
	// 显式清空白名单并重建包级管道：不依赖宿主环境是否配置过 IM2A_EGRESS_ALLOW。
	t.Setenv("IM2A_EGRESS_ALLOW", "")
	t.Setenv("IM2A_EDGE_EGRESS_ENDPOINT", "")
	prev := edgePipeline
	edgePipeline = newEdgePipeline()
	t.Cleanup(func() { edgePipeline = prev })

	body := `{"text":"帮我打开空调"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/edge/pipeline", strings.NewReader(body))
	d.EdgePipeline(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["should_upload"] != true {
		t.Fatalf("测试前置：正常命令应通过敏感过滤（should_upload=true），got %v", out)
	}
	if out["egress_allowed"] != false {
		t.Fatalf("未配置白名单必须默认拒绝（fail-closed），got egress_allowed=%v reason=%v", out["egress_allowed"], out["reason"])
	}
	reason, _ := out["reason"].(string)
	if !strings.Contains(reason, "fail-closed") {
		t.Fatalf("默认拒绝原因应含 fail-closed，got %q", reason)
	}
}

func TestV14EdgePipeline_BlockPhone(t *testing.T) {
	d := newEdgeDeps(t)
	// audit_events append-only：edge_egress 审计行不删除；报告断言用相对增量（>=2/>=1）。
	// 纯手机号，不含"手机号"字面
	body := `{"text":"联系我 13812345678 即可"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/edge/pipeline", strings.NewReader(body))
	d.EdgePipeline(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["blocked"] != true {
		t.Errorf("含手机号应 blocked, out=%v", out)
	}
	if out["should_upload"] != false {
		t.Errorf("敏感文本不应 should_upload")
	}
	if out["egress_allowed"] != false {
		t.Errorf("敏感文本出端裁决应 block")
	}
	// 脱敏文本里不能出现原始号码
	if s, _ := out["redacted_text"].(string); strings.Contains(s, "13812345678") {
		t.Errorf("redacted 不应含原始号码: %s", s)
	}
}

func TestV14EgressReport_Persisted(t *testing.T) {
	d := newEdgeDeps(t)
	// audit_events append-only：edge_egress 审计行不删除；报告断言用相对增量（>=2/>=1）。
	// 制造一次 allow、一次 block
	for _, txt := range []string{"帮我关灯", "我的身份证110101199003071234"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/edge/pipeline", strings.NewReader(`{"text":"`+txt+`"}`))
		d.EdgePipeline(rec, req)
		if rec.Code != 200 {
			t.Fatalf("pipeline %s -> %d", txt, rec.Code)
		}
	}
	// 审计报告（真实查 PG）
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/edge/egress/report", nil)
	d.EgressReport(rec, req)
	if rec.Code != 200 {
		t.Fatalf("report status=%d body=%s", rec.Code, rec.Body.String())
	}
	var rep struct {
		Total      int              `json:"total"`
		Blocked    int              `json:"blocked"`
		Violations []map[string]any `json:"violations"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &rep)
	if rep.Total < 2 {
		t.Errorf("audit_events 中 edge_egress 总行数应 >=2, got %d", rep.Total)
	}
	if rep.Blocked < 1 {
		t.Errorf("拦截数应 >=1, got %d", rep.Blocked)
	}
	// 直接查库：身份证那次 decision 必须是 block
	var cnt int
	if err := d.DB.QueryRow(
		`SELECT COUNT(*) FROM audit_events
		  WHERE action='edge_egress' AND decision='block'
		    AND detail::text LIKE '%idcard%'`,
	).Scan(&cnt); err != nil {
		t.Fatalf("query audit_events: %v", err)
	}
	if cnt < 1 {
		t.Error("audit_events 中应至少有一条 idcard 拦截记录")
	}
}
