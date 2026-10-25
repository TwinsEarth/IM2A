package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testDeps(t *testing.T) *Deps {
	t.Helper()
	// 序号单例是进程级的：不重置的话 `-count=N` 第二轮会接着累积（见 msgseq_test_reset_test.go）。
	resetMsgSeq(t)
	d, err := NewDeps("", "")
	if err != nil {
		t.Fatalf("new deps: %v", err)
	}
	return d
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestHealth(t *testing.T) {
	d := testDeps(t)
	code, body := doJSON(t, d.RelationMux("relation-svc"), "GET", "/healthz", nil)
	if code != http.StatusOK {
		t.Fatalf("health code=%d", code)
	}
	if body["status"] != "ok" {
		t.Fatalf("health body=%v", body)
	}
}

// 信任授权 + 门控：授权家庭档，家庭事务放行，工作邮件禁止。
func TestRelationGrantAndGate(t *testing.T) {
	d := testDeps(t)
	h := d.RelationMux("relation-svc")
	// 注册家庭 Agent
	code, _ := doJSON(t, h, "POST", "/v1/agents/capabilities", map[string]any{
		"agentId": "family-agent", "trustLevelMin": 1,
		"allowedSkills":              []string{"home-maintenance", "family-purchase"},
		"requiresApprovalAboveCents": 200000,
	})
	if code != http.StatusOK {
		t.Fatalf("register cap code=%d", code)
	}
	// 门控：家庭档物业报修放行
	code, body := doJSON(t, h, "POST", "/v1/trust/check", map[string]any{
		"agentId": "family-agent", "skillId": "home-maintenance",
		"scope": "family", "trustLevel": 1,
	})
	if code != http.StatusOK || body["decision"] != "ALLOW" {
		t.Fatalf("family gate code=%d body=%v", code, body)
	}
	// 大额采购转审批（202）
	code, body = doJSON(t, h, "POST", "/v1/trust/check", map[string]any{
		"agentId": "family-agent", "skillId": "family-purchase",
		"scope": "purchase", "trustLevel": 1, "amountCents": 500000,
	})
	if code != http.StatusAccepted || body["decision"] != "REQUIRE_APPROVAL" {
		t.Fatalf("amount gate code=%d body=%v", code, body)
	}
}

// 付费：发送即扣费，服务端生成 messageId，重复扣费 409。
func TestPaymentSendChargeFlow(t *testing.T) {
	d := testDeps(t)
	h := d.PaymentMux("payment-svc")
	code, body := doJSON(t, h, "POST", "/v1/paid/reserve", map[string]any{
		"senderId": 100, "receiverId": 200, "billingMode": 1, "amountCents": 100,
	})
	if code != http.StatusOK {
		t.Fatalf("reserve code=%d body=%v", code, body)
	}
	messageID, _ := body["messageId"].(string)
	if messageID == "" {
		t.Fatal("server should generate messageId")
	}
	code, body = doJSON(t, h, "POST", "/v1/paid/charge", map[string]any{
		"messageId": messageID, "approvalCardId": "",
	})
	if code != http.StatusOK || body["status"] != float64(1) {
		t.Fatalf("charge code=%d body=%v", code, body)
	}
	// 重复回调 → 409
	code, _ = doJSON(t, h, "POST", "/v1/paid/charge", map[string]any{"messageId": messageID})
	if code != http.StatusConflict {
		t.Fatalf("dup charge code=%d want 409", code)
	}
}

// 审批：创建 → 待办 → 通过 → 完成。
func TestApprovalFlow(t *testing.T) {
	d := testDeps(t)
	h := d.ApprovalMux("approval-engine")
	code, body := doJSON(t, h, "POST", "/v1/approvals", map[string]any{
		"userId": 1, "sourceAgentId": "a", "type": "PAYMENT",
		"payload": map[string]any{"item": "办公椅"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create code=%d body=%v", code, body)
	}
	ticketID, _ := body["id"].(string)
	code, _ = doJSON(t, h, "POST", "/v1/approvals/"+ticketID+"/approve", map[string]any{"by": "user"})
	if code != http.StatusOK {
		t.Fatalf("approve code=%d", code)
	}
	code, body = doJSON(t, h, "POST", "/v1/approvals/"+ticketID+"/complete", map[string]any{"by": "a"})
	if code != http.StatusOK || body["state"] != "COMPLETED" {
		t.Fatalf("complete code=%d body=%v", code, body)
	}
	// 待办应为空
	code, body = doJSON(t, h, "GET", "/v1/approvals/pending?user=1", nil)
	if code != http.StatusOK {
		t.Fatalf("pending code=%d", code)
	}
	if tickets, _ := body["tickets"].([]any); len(tickets) != 0 {
		t.Fatalf("pending tickets=%d want 0", len(tickets))
	}
}

// 合规：正常放行，违规词 422。
func TestCompliance(t *testing.T) {
	d := testDeps(t)
	h := d.ComplianceMux("compliance-gate")
	code, _ := doJSON(t, h, "POST", "/v1/compliance/check", map[string]any{
		"messageId": "m1", "content": "双方已就周三 15:00 沟通达成一致",
	})
	if code != http.StatusOK {
		t.Fatalf("clean content code=%d want 200", code)
	}
	code, body := doJSON(t, h, "POST", "/v1/compliance/check", map[string]any{
		"messageId": "m2", "content": "恭喜中奖，点击领取",
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("prohibited code=%d want 422 body=%v", code, body)
	}
}

// 陌生人来意：不被接受的类别在预审阶段拒绝，不产生扣费。
func TestStrangerIntentRejected(t *testing.T) {
	d := testDeps(t)
	h := NewOrchestrator(*d).Mux("agent-orchestrator")
	code, body := doJSON(t, h, "POST", "/v1/stranger/intent", map[string]any{
		"receiverId": 42, "strangerAgentId": "spam-agent",
		"intentCategory": "贷款推广", "content": "低息贷款，额度高",
		"billingMode": 1, "amountCents": 100,
	})
	if code != http.StatusOK || body["state"] != "REJECTED" || body["stage"] != "intent_rule" {
		t.Fatalf("intent reject code=%d body=%v", code, body)
	}
}

// 陌生人来意：合规违规直接 422。
func TestStrangerIntentCompliance(t *testing.T) {
	d := testDeps(t)
	h := NewOrchestrator(*d).Mux("agent-orchestrator")
	code, body := doJSON(t, h, "POST", "/v1/stranger/intent", map[string]any{
		"receiverId": 42, "strangerAgentId": "spam-agent",
		"intentCategory": "商务合作", "content": "合作请先汇款到指定账户",
		"billingMode": 1, "amountCents": 100,
	})
	if code != http.StatusUnprocessableEntity || body["stage"] != "compliance" {
		t.Fatalf("compliance code=%d body=%v", code, body)
	}
}

// 陌生人来意：发送即扣费，通过后立即扣款。
func TestStrangerIntentSendChargeApprove(t *testing.T) {
	d := testDeps(t)
	h := NewOrchestrator(*d).Mux("agent-orchestrator")
	code, body := doJSON(t, h, "POST", "/v1/stranger/intent", map[string]any{
		"receiverId": 42, "strangerAgentId": "biz-agent",
		"intentCategory": "商务合作", "content": "希望就产品集成进行一次 20 分钟沟通",
		"billingMode": 1, "amountCents": 100,
	})
	if code != http.StatusCreated || body["state"] != "PENDING_APPROVAL" {
		t.Fatalf("intent code=%d body=%v", code, body)
	}
	ticketID, _ := body["ticketId"].(string)
	messageID, _ := body["messageId"].(string)
	code, body = doJSON(t, h, "POST", "/v1/orchestrator/tickets/"+ticketID+"/decide",
		map[string]any{"decision": "approve", "by": "user"})
	if code != http.StatusOK || body["state"] != "APPROVED" {
		t.Fatalf("approve code=%d body=%v", code, body)
	}
	// 账本应为 CHARGED
	e, err := d.Store.Ledger.Get(messageID)
	if err != nil {
		t.Fatalf("ledger get: %v", err)
	}
	if e.Status != 1 {
		t.Fatalf("ledger status=%d want CHARGED(1)", e.Status)
	}
}

// 陌生人来意：回复才扣费，reject 不扣；reply 后扣款并完成。
func TestStrangerIntentReplyCharge(t *testing.T) {
	d := testDeps(t)
	h := NewOrchestrator(*d).Mux("agent-orchestrator")
	// 一条被拒绝：pending 不扣款
	code, body := doJSON(t, h, "POST", "/v1/stranger/intent", map[string]any{
		"receiverId": 42, "strangerAgentId": "biz-agent",
		"intentCategory": "产品咨询", "content": "咨询报价方案",
		"billingMode": 2, "amountCents": 5000,
	})
	if code != http.StatusCreated {
		t.Fatalf("intent code=%d body=%v", code, body)
	}
	rejectedTicket := body["ticketId"].(string)
	rejectedMsg := body["messageId"].(string)
	code, _ = doJSON(t, h, "POST", "/v1/orchestrator/tickets/"+rejectedTicket+"/decide",
		map[string]any{"decision": "reject", "by": "user"})
	if code != http.StatusOK {
		t.Fatalf("reject code=%d", code)
	}
	e, _ := d.Store.Ledger.Get(rejectedMsg)
	if e != nil && e.Status != 0 {
		t.Fatalf("rejected entry status=%d want PENDING(0)", e.Status)
	}

	// 一条被回复：reply 时扣款并完成
	code, body = doJSON(t, h, "POST", "/v1/stranger/intent", map[string]any{
		"receiverId": 42, "strangerAgentId": "biz-agent2",
		"intentCategory": "产品咨询", "content": "咨询接入细节",
		"billingMode": 2, "amountCents": 5000,
	})
	if code != http.StatusCreated {
		t.Fatalf("intent2 code=%d body=%v", code, body)
	}
	ticketID := body["ticketId"].(string)
	messageID := body["messageId"].(string)
	code, body = doJSON(t, h, "POST", "/v1/orchestrator/tickets/"+ticketID+"/decide",
		map[string]any{"decision": "reply", "by": "user"})
	if code != http.StatusOK || body["state"] != "COMPLETED" {
		t.Fatalf("reply code=%d body=%v", code, body)
	}
	e, _ = d.Store.Ledger.Get(messageID)
	if e == nil || e.Status != 1 {
		t.Fatalf("replied entry status=%v want CHARGED(1)", e)
	}
}

// MCP 挂载：initialize 与 skills 列表。
func TestMCPMounted(t *testing.T) {
	d := testDeps(t)
	h := NewOrchestrator(*d).Mux("agent-orchestrator")
	code, body := doJSON(t, h, "POST", "/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	if code != http.StatusOK {
		t.Fatalf("mcp initialize code=%d", code)
	}
	res, ok := body["result"].(map[string]any)
	if !ok || res["protocolVersion"] != "2026-07-28" {
		t.Fatalf("mcp init body=%v", body)
	}
	code, _ = doJSON(t, h, "GET", "/mcp/skills", nil)
	if code != http.StatusOK {
		t.Fatalf("mcp skills code=%d", code)
	}
}

// 消息序号：单条与批量。
func TestMsgSeq(t *testing.T) {
	// 断言绝对序号（1）就必须自己重置单例，否则 -count=N 会跨轮累积。
	resetMsgSeq(t)
	h := NewMsgMux("msg-svc")
	code, body := doJSON(t, h, "POST", "/v1/msg/seq", map[string]any{"userId": 7})
	if code != http.StatusOK || body["seq"] != float64(1) {
		t.Fatalf("seq code=%d body=%v", code, body)
	}
	code, body = doJSON(t, h, "POST", "/v1/msg/seq/batch", map[string]any{"userId": 7, "n": 3})
	if code != http.StatusOK {
		t.Fatalf("batch code=%d", code)
	}
	seqs, _ := body["seqs"].([]any)
	if len(seqs) != 3 {
		t.Fatalf("batch seqs=%d want 3", len(seqs))
	}
}
