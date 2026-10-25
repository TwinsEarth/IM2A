// v1.6.0-v1.6.4 审查重做：真实断言测试。
// 需 PG_DSN（真实 PG 落库/越权断言）；未设置时 skip。
package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 唯一数字 userId，避免与库内既有数据冲突。
func uniqueUID() uint64 {
	return uint64(time.Now().UnixNano()%50000000) + 700000000
}

// TestV160SSEPublish 真实 SSE 通道：建立订阅 → publish → 必须收到 event: approval。
func TestV160SSEPublish(t *testing.T) {
	d := &Deps{}
	srv := httptest.NewServer(http.HandlerFunc(d.SSEStream))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?userId=alice-sse-test")
	if err != nil {
		t.Fatalf("订阅 SSE 失败: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	// 先读 connected 事件
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "connected") {
		t.Fatalf("未收到 connected 事件: %q %v", line, err)
	}

	// 通过 handler 真实推送
	pw := httptest.NewRecorder()
	preq := httptest.NewRequest("POST", "/", strings.NewReader(`{"userId":"alice-sse-test","event":"{\"ticket\":\"tk-x\",\"title\":\"采购\"}"}`))
	d.SSEPublish(pw, preq)
	if pw.Code != 200 {
		t.Fatalf("publish 应 200, got %d", pw.Code)
	}

	// 订阅端必须在 2 秒内收到 event: approval
	deadline := time.After(2 * time.Second)
	got := false
	for !got {
		select {
		case <-deadline:
			t.Fatalf("订阅端未收到实时审批事件")
		default:
		}
		l, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("读 SSE 失败: %v", err)
		}
		if strings.Contains(l, "event: approval") {
			got = true
		}
	}
}

// TestV161AgentLogNoLeak 真实越权断言：字符串 userId 不得返回全表。
func TestV161AgentLogNoLeak(t *testing.T) {
	d := newDBDeps(t)
	uid := uniqueUID()
	agent := "leakprobe" + strconv64(time.Now().UnixNano())
	// audit_events append-only：agent 泄漏探针审计行保留，agent_id 带 leakprobe 唯一标识。

	// 插入一条属于 uid 的真实日志
	if _, err := d.DB.Exec(`INSERT INTO audit_events(user_id, agent_id, action, decision, detail)
		VALUES($1,$2,'leak_test','y','{}'::jsonb)`, uid, agent); err != nil {
		t.Fatalf("插入审计失败: %v", err)
	}

	// 数字 userId：只应返回自己的 1 条
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/agent/logs?userId="+strconv64(int64(uid)), nil)
	d.AgentLog(w, r)
	var numResp struct {
		Count   int `json:"count"`
		Entries []struct {
			Agent string `json:"agent"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &numResp); err != nil {
		t.Fatalf("数字查询解析失败: %v", err)
	}
	if numResp.Count != 1 || (len(numResp.Entries) > 0 && numResp.Entries[0].Agent != agent) {
		t.Fatalf("数字 userId 应只返回自己的 1 条, got count=%d", numResp.Count)
	}

	// 字符串 userId（不存在的 agent）：修复前返回全表 50 条，修复后必须为 0
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/v1/agent/logs?userId=nonexistent_agent_"+agent, nil)
	d.AgentLog(w2, r2)
	var strResp struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &strResp); err != nil {
		t.Fatalf("字符串查询解析失败: %v", err)
	}
	if strResp.Count != 0 {
		t.Fatalf("字符串 userId 不应返回全表日志（越权）, got count=%d", strResp.Count)
	}
}

// TestV164PhoneApproveSync 手机端审批必须真实落权威表 ticket_state，与手表多端一致。
func TestV164PhoneApproveSync(t *testing.T) {
	d := newDBDeps(t)
	srv := httptest.NewServer(d.ApprovalMux("test"))
	defer srv.Close()

	uid := uniqueUID()
	// 1) 真实创建审批单
	cr := `{"userId":` + strconv64(int64(uid)) + `,"sourceAgentId":"shopping","type":"NEGOTIATION","payload":{}}`
	cresp, err := http.Post(srv.URL+"/v1/approvals", "application/json", strings.NewReader(cr))
	if err != nil {
		t.Fatalf("创建审批单失败: %v", err)
	}
	if cresp.StatusCode != 201 {
		t.Fatalf("创建应 201, got %d", cresp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	json.NewDecoder(cresp.Body).Decode(&created)
	cresp.Body.Close()
	if created.ID == "" {
		t.Fatalf("未拿到审批单 id")
	}
	t.Cleanup(func() {
		cleanTicket(t, d.DB, created.ID)
		// audit_events append-only：审批审计行保留，靠 ticketId 唯一标识隔离。
	})

	// 2) 手机端 approve
	aresp, err := http.Post(srv.URL+"/v1/approvals/"+created.ID+"/approve", "application/json",
		strings.NewReader(`{"by":"owner","note":"ok"}`))
	if err != nil {
		t.Fatalf("approve 失败: %v", err)
	}
	if aresp.StatusCode != 200 {
		t.Fatalf("手机 approve 应 200, got %d", aresp.StatusCode)
	}
	aresp.Body.Close()

	// 3) 真实落库断言：ticket_state 必须存在 via=phone 的 approved
	var decision, via string
	err = d.DB.QueryRow(`SELECT decision, via_device FROM ticket_state WHERE ticket_id=$1`, created.ID).
		Scan(&decision, &via)
	if err != nil {
		t.Fatalf("手机审批未同步到权威表 ticket_state: %v", err)
	}
	if decision != "approved" || via != "phone" {
		t.Fatalf("权威表状态错 decision=%s via=%s", decision, via)
	}
}

func strconv64(n int64) string {
	return strings.TrimSpace(strings.ReplaceAll(
		strings.ReplaceAll(
			jsonNumber(n), " ", ""), "\n", ""))
}
func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
