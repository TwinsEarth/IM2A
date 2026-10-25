package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestV161AgentLogValidation(t *testing.T) {
	d := &Deps{}
	// 无 userId → 400
	req := httptest.NewRequest("GET", "/v1/agent/logs", nil)
	w := httptest.NewRecorder()
	d.AgentLog(w, req)
	if w.Code != 400 {
		t.Errorf("无 userId 应 400, got %d", w.Code)
	}
}

func TestV162Watch(t *testing.T) {
	d := &Deps{} // DB=nil
	// 无存储：真实审批不可落库 → 503（不再返回假成功）
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"ticketId":"t1"}`))
	w := httptest.NewRecorder()
	d.WatchApprove(w, req)
	if w.Code != 503 {
		t.Errorf("无 DB 审批应 503, got %d %s", w.Code, w.Body.String())
	}
	req2 := httptest.NewRequest("POST", "/", strings.NewReader(`{"ticketId":"t2"}`))
	w2 := httptest.NewRecorder()
	d.WatchReject(w2, req2)
	if w2.Code != 503 {
		t.Errorf("无 DB 驳回应 503, got %d", w2.Code)
	}
	// 缺 ticketId → 400
	req3 := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	w3 := httptest.NewRecorder()
	d.WatchApprove(w3, req3)
	if w3.Code != 400 {
		t.Errorf("缺 ticketId 应 400, got %d", w3.Code)
	}
}
