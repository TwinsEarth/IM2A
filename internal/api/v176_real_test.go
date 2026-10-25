package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// v1.7.6：运营后台仪表盘必须从真实 PG 聚合——插入真实行后统计增量必须可见。
func TestV176_AdminDashboardStats(t *testing.T) {
	d := v12Deps(t)
	h := d.AdminMux()
	suf := fmt.Sprintf("%d", time.Now().UnixNano())
	mk := "v176_msg_" + suf
	mid := "11111111-0000-0000-0000-000000000176"
	trace := "v176_" + suf

	// audit_events append-only：审计行保留不删，model_calls 用 before/after 相对增量断言（见下）。
	cleanup := func() {
		d.DB.Exec(`DELETE FROM im_messages WHERE msg_key=$1`, mk)
		d.DB.Exec(`DELETE FROM paid_message_ledger WHERE message_id=$1`, mid)
	}
	cleanup()
	t.Cleanup(cleanup)

	fetch := func() map[string]any {
		req := httptest.NewRequest("GET", "/v1/admin/stats", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("stats code=%d body=%s", rec.Code, rec.Body.String())
		}
		var out struct {
			Stats map[string]any `json:"stats"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		return out.Stats
	}
	asInt := func(m map[string]any, k string) float64 {
		v, ok := m[k].(float64)
		if !ok {
			t.Fatalf("missing %s in %v", k, m)
		}
		return v
	}

	before := fetch()
	bMsg := asInt(before, "im_messages")
	bPaid := asInt(before, "paid_charged")
	bModel := asInt(before, "model_calls")
	bRev := asInt(before, "paid_total_cents")

	// 真实插入：一条 IM 消息、一条已计费付费（66 分）、一条模型调用审计
	if _, err := d.DB.Exec(`INSERT INTO im_messages(msg_key,from_user,to_user,dir,text) VALUES($1,'v176_f','v176_t','recv','仪表盘测试')`, mk); err != nil {
		t.Fatalf("insert im: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO paid_message_ledger(message_id,sender_id,receiver_id,billing_mode,amount_cents,status) VALUES($1,0,0,1,66,1)`, mid); err != nil {
		t.Fatalf("insert paid: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO audit_events(occurred_at,trace_id,action,decision,detail) VALUES(now(),$1,'model_call','ok','{"src":"v176"}')`, trace); err != nil {
		t.Fatalf("insert audit: %v", err)
	}

	after := fetch()
	if got := asInt(after, "im_messages"); got != bMsg+1 {
		t.Fatalf("im_messages delta want 1, got %v->%v", bMsg, got)
	}
	if got := asInt(after, "paid_charged"); got != bPaid+1 {
		t.Fatalf("paid_charged delta want 1, got %v->%v", bPaid, got)
	}
	if got := asInt(after, "paid_total_cents"); got != bRev+66 {
		t.Fatalf("paid revenue delta want 66, got %v->%v", bRev, got)
	}
	if got := asInt(after, "model_calls"); got != bModel+1 {
		t.Fatalf("model_calls delta want 1, got %v->%v", bModel, got)
	}
}
