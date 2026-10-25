package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestV170Metrics(t *testing.T) {
	d := &Deps{}
	req := httptest.NewRequest("GET", "/v1/admin/metrics", nil)
	w := httptest.NewRecorder()
	d.AdminMetrics(w, req)
	if w.Code != 200 {
		t.Errorf("metrics 应 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "go_goroutines") {
		t.Errorf("应含 goroutine 指标, got %s", w.Body.String())
	}
}

func TestV171AuditNoDB(t *testing.T) {
	d := &Deps{} // DB=nil，返回空
	req := httptest.NewRequest("GET", "/v1/admin/audit?limit=20", nil)
	w := httptest.NewRecorder()
	d.AdminAudit(w, req)
	if w.Code != 200 {
		t.Errorf("audit 应 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"total":0`) {
		t.Errorf("无 DB 应 total=0, got %s", w.Body.String())
	}
}

func TestV172Health(t *testing.T) {
	d := &Deps{} // DB=nil → degraded 503
	req := httptest.NewRequest("GET", "/v1/admin/health", nil)
	w := httptest.NewRecorder()
	d.AdminHealth(w, req)
	if w.Code != 503 {
		t.Errorf("无 DB 应 503 degraded, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "degraded") {
		t.Errorf("应 degraded, got %s", w.Body.String())
	}
}

func TestV170AtoiSafe(t *testing.T) {
	if atoiSafe("25") != 25 {
		t.Errorf("25 应解析为 25")
	}
	if atoiSafe("abc") != 0 {
		t.Errorf("abc 应解析为 0")
	}
}
