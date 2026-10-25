package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCapabilityGate(t *testing.T) {
	d := &Deps{} // DB=nil, 走默认 L4

	// 用例 1：陌生用户（L4）调用 msg.read（需 L3）→ 拒绝
	body := `{"userId":"u1","targetId":"stranger","tool":"msg.read"}`
	req := httptest.NewRequest("POST", "/v1/capability/check", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.gateCheck(w, req)
	if w.Body.String() == "" || !strings.Contains(w.Body.String(), "\"allowed\":false") {
		t.Errorf("陌生用户调 msg.read 应拒绝, got: %s", w.Body.String())
	}

	// 用例 2：未知工具默认 L4 → 拒绝
	body2 := `{"userId":"u1","targetId":"x","tool":"unknown.tool"}`
	req2 := httptest.NewRequest("POST", "/v1/capability/check", strings.NewReader(body2))
	w2 := httptest.NewRecorder()
	d.gateCheck(w2, req2)
	if !strings.Contains(w2.Body.String(), "\"allowed\":false") {
		t.Errorf("未知工具应默认拒绝, got: %s", w2.Body.String())
	}

	// 用例 3：calendar.write 需 L1，默认 L4 → 拒绝
	body3 := `{"userId":"u1","targetId":"x","tool":"calendar.write"}`
	req3 := httptest.NewRequest("POST", "/v1/capability/check", strings.NewReader(body3))
	w3 := httptest.NewRecorder()
	d.gateCheck(w3, req3)
	if !strings.Contains(w3.Body.String(), "\"allowed\":false") {
		t.Errorf("陌生用户写日程应拒绝, got: %s", w3.Body.String())
	}
}
