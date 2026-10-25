package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCounterAccumulates(t *testing.T) {
	IncCounter("http_requests_total", `path="/x",code="201"`)
	IncCounter("http_requests_total", `path="/x",code="201"`)
	IncCounter("http_requests_total", `path="/y",code="400"`)
	out := Render()
	if !strings.Contains(out, `http_requests_total{path="/x",code="201"} 2`) {
		t.Error("计数器累加错误:\n" + out)
	}
	if !strings.Contains(out, `http_requests_total{path="/y",code="400"} 1`) {
		t.Error("标签维度错误")
	}
	if !strings.Contains(out, "# TYPE http_requests_total counter") {
		t.Error("缺少 Prometheus TYPE 头")
	}
}

func TestMiddlewareRealCount(t *testing.T) {
	// 清空样本
	summaries = map[string][]float64{}
	counters = map[string]map[string]int64{}
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/v1/msg/send", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	out := Render()
	if !strings.Contains(out, `code="201"` ) && !strings.Contains(out, "code=201") {
		t.Error("中间件未记录 201 计数:\n" + out)
	}
	if !strings.Contains(out, "http_request_duration_ms_count 5") {
		t.Error("延迟样本未达 5:\n" + out)
	}
}

func TestEndpointServesText(t *testing.T) {
	IncCounter("up", `x="1"`)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type 错误: %s", ct)
	}
	if !strings.Contains(rec.Body.String(), "up") {
		t.Error("响应体无指标")
	}
}
