package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"human-agent-im/internal/negotiation"
)

// v1.3.2 协商时间线：真实 SaveSession 落 PG 后，GET /v1/negotiations/list 必须包含该会话，
// 且状态/轮次字段真实（不允许固定 JSON）。
func TestV132NegotiationTimeline(t *testing.T) {
	d := newDBDeps(t)
	id := fmt.Sprintf("neg-v132-%d", uniqueUID())
	t.Cleanup(func() { cleanNegotiation(t, d.DB, id) })
	s := &negotiation.Session{
		ID: id, State: negotiation.StateCompleted, MaxRounds: 6, Round: 2,
		TokenUsed: 123, TokenBudget: 100000,
		Turns: []negotiation.Turn{
			{SessionID: id, FromAgentID: "schedule-a", ToAgentID: "schedule-b",
				Intent: negotiation.IntentPropose, Payload: map[string]any{"time": "周四15:00"}},
		},
		CreatedAt: time.Now(),
	}
	if err := d.Store.Negotiation.SaveSession(context.Background(), s); err != nil {
		t.Fatalf("SaveSession 落库失败: %v", err)
	}

	handler := d.NegotiationMux("test-neg")
	req := httptest.NewRequest("GET", "/v1/negotiations/list?limit=100", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list 状态码=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Negotiations []map[string]any `json:"negotiations"`
		Count        int              `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range body.Negotiations {
		if n["id"] == id {
			found = true
			if n["state"] != string(negotiation.StateCompleted) {
				t.Fatalf("state 不真实: %v", n["state"])
			}
			turns, _ := n["turns"].([]any)
			if len(turns) != 1 {
				t.Fatalf("turns 重建数量错误: %d", len(turns))
			}
		}
	}
	if !found {
		t.Fatalf("真实落库的协商 %s 未出现在时间线: %s", id, rec.Body.String())
	}
}

// v1.3.3 会话态：POST /v1/msg/send 真实落库（分配 seq），GET /v1/msg/list 必须能取回，
// 且内容/发送方为刚写入的值。禁止只返回固定消息。
func TestV133MessageSendList(t *testing.T) {
	d := newDBDeps(t)
	uid := uniqueUID()
	other := uniqueUID() + 1
	t.Cleanup(func() { cleanMessagesByUsers(t, d.DB, uid, other) })
	content := fmt.Sprintf("v133 真实消息-%d", uid)

	handler := NewMsgMuxWithStore("test-msg", d.Store.Message)

	// 发送
	sendBody := fmt.Sprintf(`{"senderId":%d,"receiverId":%d,"content":%q}`, uid, other, content)
	req := httptest.NewRequest("POST", "/v1/msg/send", strings.NewReader(sendBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send 状态码=%d body=%s", rec.Code, rec.Body.String())
	}
	var sent struct {
		MessageID string `json:"messageId"`
		Seq       int64  `json:"seq"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.MessageID == "" {
		t.Fatal("send 未返回 messageId（未真实落库）")
	}

	// 列表：发送方视角
	req2 := httptest.NewRequest("GET", fmt.Sprintf("/v1/msg/list?userId=%d&limit=100", uid), nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list 状态码=%d", rec2.Code)
	}
	var listed struct {
		Messages []struct {
			MessageID string `json:"messageId"`
			Content   string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range listed.Messages {
		if m.MessageID == sent.MessageID && m.Content == content {
			found = true
		}
	}
	if !found {
		t.Fatalf("刚发送的消息 %s 未在列表中取回: %s", sent.MessageID, rec2.Body.String())
	}
}
