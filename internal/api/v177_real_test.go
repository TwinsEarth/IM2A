package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func v177Get(h http.Handler, path string) map[string]any {
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return map[string]any{"_code": rec.Code, "_body": rec.Body.String()}
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// v1.7.7：时间序列今日增量、付费流水、Agent 明细均来自真实 PG。
func TestV177_DashboardDetails(t *testing.T) {
	d := v12Deps(t)
	h := d.AdminMux()
	suf := fmt.Sprintf("%d", time.Now().UnixNano())
	mk := "v177_msg_" + suf
	mid := "22222222-0000-0000-0000-000000000177"
	trace := "v177_" + suf
	agentID := "v177_agent_" + suf
	var ownerID int64
	phone := "v177phone_" + suf

	// audit_events append-only：审计行保留不删，model_calls 用 before/after 相对增量断言。
	cleanup := func() {
		d.DB.Exec(`DELETE FROM im_messages WHERE msg_key=$1`, mk)
		d.DB.Exec(`DELETE FROM paid_message_ledger WHERE message_id=$1`, mid)
		d.DB.Exec(`DELETE FROM agent_capabilities WHERE agent_id=$1`, agentID)
		d.DB.Exec(`DELETE FROM users WHERE id=$1`, ownerID)
	}
	cleanup()
	t.Cleanup(cleanup)

	today := func() string {
		var ds string
		_ = d.DB.QueryRow(`SELECT to_char(now(),'YYYY-MM-DD')`).Scan(&ds)
		return ds
	}
	todayModel := func() int {
		m := v177Get(h, "/v1/admin/timeseries?days=7")
		td := today()
		for _, row := range m["series"].([]any) {
			r := row.(map[string]any)
			if r["date"].(string) == td {
				return int(r["model_calls"].(float64))
			}
		}
		return -1
	}

	before := todayModel()
	if _, err := d.DB.Exec(`INSERT INTO im_messages(msg_key,from_user,to_user,dir,text) VALUES($1,'v177_f','v177_t','recv','趋势')`, mk); err != nil {
		t.Fatalf("im: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO paid_message_ledger(message_id,sender_id,receiver_id,billing_mode,amount_cents,status,charged_at) VALUES($1,0,0,1,88,1,now())`, mid); err != nil {
		t.Fatalf("paid: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO audit_events(occurred_at,trace_id,action,decision,detail) VALUES(now(),$1,'model_call','ok','{"src":"v177"}')`, trace); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if err := d.DB.QueryRow(`INSERT INTO users(phone_encrypted,status) VALUES($1,1) RETURNING id`, phone).Scan(&ownerID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO agent_capabilities(agent_id,owner_id,kind,trust_level_min,budget_cents_per_day,enabled) VALUES($1,$2,'scheduler',4,1000,true)`, agentID, ownerID); err != nil {
		t.Fatalf("agent: %v", err)
	}

	// 时间序列：今日 model_calls 增量
	if got := todayModel(); got != before+1 {
		t.Fatalf("today model_calls want %d got %d", before+1, got)
	}
	// 付费流水出现新行
	pay := v177Get(h, "/v1/admin/payments?limit=50")
	if !strings.Contains(fmt.Sprint(pay["payments"]), mid) {
		t.Fatalf("new payment %s not in list", mid)
	}
	// Agent 明细出现新行
	ag := v177Get(h, "/v1/admin/agents")
	if !strings.Contains(fmt.Sprint(ag["agents"]), agentID) {
		t.Fatalf("new agent %s not in list", agentID)
	}
}
