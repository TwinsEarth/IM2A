package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v1.5.0: 付费账本——无 DB 时走内存路径，验证参数校验和幂等响应结构。
func TestV150ChargeValidation(t *testing.T) {
	d := &Deps{} // DB=nil

	// 缺 messageId → 400
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"senderId":"1001","receiverId":"1002","amountCents":10}`))
	w := httptest.NewRecorder()
	d.paidCharge(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("缺 messageId 应 400, got %d", w.Code)
	}

	// amount <= 0 → 400
	req2 := httptest.NewRequest("POST", "/", strings.NewReader(`{"messageId":"m1","senderId":"1001","receiverId":"1002","amountCents":0}`))
	w2 := httptest.NewRecorder()
	d.paidCharge(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("amount=0 应 400, got %d", w2.Code)
	}

	// 正常请求（无 DB 时直接返回 charged）
	req3 := httptest.NewRequest("POST", "/", strings.NewReader(`{"messageId":"m2","senderId":"1001","receiverId":"1002","amountCents":100}`))
	w3 := httptest.NewRecorder()
	d.paidCharge(w3, req3)
	if !strings.Contains(w3.Body.String(), `"status":"charged"`) {
		t.Errorf("正常请求应 charged, got %s", w3.Body.String())
	}
	// v2.3.27：senderId/receiverId 现在是 paid_message_ledger 的 BIGINT 列，
	// 非数字入参必须显式 400（不能像老实现那样当 TEXT 塞进退役表）。
	req4 := httptest.NewRequest("POST", "/", strings.NewReader(`{"messageId":"m3","senderId":"stranger1","receiverId":"1002","amountCents":100}`))
	w4 := httptest.NewRecorder()
	d.paidCharge(w4, req4)
	if w4.Code != http.StatusBadRequest {
		t.Errorf("非数字 senderId 应 400, got %d body=%s", w4.Code, w4.Body.String())
	}
}