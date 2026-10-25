package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func v178Req(h http.Handler, method, path, body string) (int, map[string]any) {
	var rdr *bytes.Reader
	if body == "" {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// v1.7.8 客服工单全闭环：用户提交 → 客服列表/回复/解决 → 用户看到 → 补充重开 → 关闭。
func TestV178_SupportTicketLifecycle(t *testing.T) {
	d := v12Deps(t)
	userMux := d.SupportMux()
	adminMux := d.AdminMux()
	uid := int64(9917800000000 + (timeNowNano() % 1000000))

	// 1. 用户提交
	createBody := `{"user_id":` + itoa(uid) + `,"contact":"wx_v178","category":"bug","subject":"v178 收不到审批推送","content":"手表点击后无响应"}`
	code, t0 := v178Req(userMux, "POST", "/v1/support/tickets", createBody)
	if code != http.StatusCreated {
		t.Fatalf("create code %d body %v", code, t0)
	}
	tid := int64(t0["id"].(float64))
	ticketNo := t0["ticket_no"].(string)
	if !strings.HasPrefix(ticketNo, "ST-") || t0["status"].(float64) != 1 {
		t.Fatalf("bad ticket_no/status: %v", t0)
	}
	// audit_events append-only：工单 CREATED 审计行保留不删，本测试断言只查 support_tickets/replies。
	t.Cleanup(func() {
		d.DB.Exec(`DELETE FROM support_ticket_replies WHERE ticket_id=$1`, tid)
		d.DB.Exec(`DELETE FROM support_tickets WHERE id=$1`, tid)
	})

	// 2. 客服待处理列表能看到
	code, list := v178Req(adminMux, "GET", "/v1/admin/tickets?status=1", "")
	if code != 200 || !strings.Contains(fmt.Sprint(list["tickets"]), ticketNo) {
		t.Fatalf("admin list missing %d: %d %v", tid, code, list)
	}
	// 用户按自己 user_id 查询也能看到
	code, mine := v178Req(userMux, "GET", "/v1/support/tickets?user_id="+itoa(uid), "")
	if code != 200 || !strings.Contains(fmt.Sprint(mine["tickets"]), ticketNo) {
		t.Fatalf("user list missing: %d %v", code, mine)
	}

	// 3. 客服回复并标记已解决
	code, t1 := v178Req(adminMux, "POST", "/v1/admin/tickets/"+itoa(tid)+"/reply",
		`{"author":"kefu_lin","assignee":"kefu_lin","content":"已重启推送通道，请重试","status":3}`)
	if code != 200 || t1["status"].(float64) != 3 {
		t.Fatalf("agent reply not resolved: %d %v", code, t1)
	}
	if t1["resolved_at"] == nil || t1["resolved_at"] == "" {
		t.Fatalf("resolved_at not set")
	}
	if t1["assignee"] != "kefu_lin" {
		t.Fatalf("assignee not set: %v", t1["assignee"])
	}

	// 4. 用户侧详情看到客服回复（author_type=2）
	code, t2 := v178Req(userMux, "GET", "/v1/support/tickets/"+itoa(tid), "")
	if code != 200 || !strings.Contains(fmt.Sprint(t2["replies"]), "已重启推送通道") {
		t.Fatalf("user cannot see agent reply: %d %v", code, t2)
	}

	// 5. 用户补充后自动重开（status 3 → 2，resolved_at 清空）
	code, t3 := v178Req(userMux, "POST", "/v1/support/tickets/"+itoa(tid)+"/reply",
		`{"author":"me","content":"还是不行"}`)
	if code != 200 || t3["status"].(float64) != 2 {
		t.Fatalf("reopen expected status=2: %d %v", code, t3)
	}
	if t3["resolved_at"] != nil && t3["resolved_at"] != "" {
		t.Fatalf("resolved_at should be cleared after reopen: %v", t3["resolved_at"])
	}

	// 6. 客服关闭
	code, t4 := v178Req(adminMux, "POST", "/v1/admin/tickets/"+itoa(tid)+"/status", `{"status":4,"assignee":"kefu_lin"}`)
	if code != 200 || t4["status"].(float64) != 4 {
		t.Fatalf("close failed: %d %v", code, t4)
	}

	// 落库断言：两条回复，author_type 分别 1/2，工单 status=4
	var n1, n2, st int
	if err := d.DB.QueryRow(`SELECT count(*) FROM support_ticket_replies WHERE ticket_id=$1 AND author_type=1`, tid).Scan(&n1); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRow(`SELECT count(*) FROM support_ticket_replies WHERE ticket_id=$1 AND author_type=2`, tid).Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRow(`SELECT status FROM support_tickets WHERE id=$1`, tid).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if n1 != 1 || n2 != 1 || st != 4 {
		t.Fatalf("db assert fail userReplies=%d agentReplies=%d status=%d", n1, n2, st)
	}
}

func timeNowNano() int64 { return time.Now().UnixNano() }
