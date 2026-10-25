package api

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// v1.7.9 审计可视化 + WORM 不可变审计。
// audit_events 是 append-only（触发器只禁 UPDATE/DELETE，INSERT 正常）；先断言不可变，
// 再用唯一 trace_id 构造决策链，审计行保留不删（合规 append-only），查询按 trace 精确隔离。
func TestV179_AuditTraceReplay(t *testing.T) {
	d := v12Deps(t)
	h := d.AdminMux()

	// 0. WORM 不可变断言：对真实行的 UPDATE/DELETE 必须被拒（语句被拒，无副作用）。
	var minID int64
	if err := d.DB.QueryRow(`SELECT id FROM audit_events ORDER BY id LIMIT 1`).Scan(&minID); err != nil {
		t.Skipf("no audit rows: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE audit_events SET action=action WHERE id=$1`, minID); err == nil {
		t.Fatal("audit_events must reject UPDATE (append-only)")
	}
	if _, err := d.DB.Exec(`DELETE FROM audit_events WHERE id=$1`, minID); err == nil {
		t.Fatal("audit_events must reject DELETE (append-only)")
	}

	// 1. 决策链直接 INSERT（触发器不禁 INSERT）；行 append-only 保留，用唯一 trace_id 隔离。

	ns := time.Now().UnixNano()
	trace := "v179trace-" + strconv.FormatInt(ns, 10)
	other := "v179other-" + strconv.FormatInt(ns, 10)

	// 决策链三步：Agent 预审 → 出端白名单 → 审批通过
	steps := []struct{ action, decision string }{
		{"intent_precheck", "ok"},
		{"edge_egress", "allow"},
		{"ticket_decision", "approved"},
	}
	for i, s := range steps {
		detail := fmt.Sprintf(`{"seq":%d,"gate":"%s"}`, i+1, s.action)
		if _, err := d.DB.Exec(`INSERT INTO audit_events(occurred_at,trace_id,agent_id,user_id,action,decision,detail)
			VALUES(now(),$1,'butler',0,$2,$3,$4::jsonb)`, trace, s.action, s.decision, detail); err != nil {
			t.Fatalf("insert step %d: %v", i, err)
		}
	}
	if _, err := d.DB.Exec(`INSERT INTO audit_events(occurred_at,trace_id,agent_id,user_id,action,decision,detail)
		VALUES(now(),$1,'butler',0,'edge_egress','block','{"seq":1}'::jsonb)`, other); err != nil {
		t.Fatal(err)
	}

	// 2. 时间线按 trace 过滤：仅 3 条
	code, stream := v178Req(h, "GET", "/v1/admin/audit/stream?trace_id="+trace, "")
	if code != 200 || int(stream["total"].(float64)) != 3 {
		t.Fatalf("stream trace filter code=%d total=%v", code, stream["total"])
	}
	// 按 decision 过滤：approved 至少 1 条
	code, appr := v178Req(h, "GET", "/v1/admin/audit/stream?decision=approved&limit=500", "")
	if code != 200 || int(appr["total"].(float64)) < 1 {
		t.Fatalf("stream decision filter code=%d total=%v", code, appr["total"])
	}
	// 干扰 trace 隔离
	code, otherStream := v178Req(h, "GET", "/v1/admin/audit/stream?trace_id="+other, "")
	if code != 200 || int(otherStream["total"].(float64)) != 1 {
		t.Fatalf("other trace isolation: %d %v", code, otherStream["total"])
	}
	// 按 action 过滤
	code, act := v178Req(h, "GET", "/v1/admin/audit/stream?action=e_ZZ_no_match&limit=10", "")
	if code != 200 || int(act["total"].(float64)) != 0 {
		t.Fatalf("action filter should return 0: %d %v", code, act["total"])
	}

	// 3. trace 回放：steps=3，顺序为预审→出端→审批
	code, replay := v178Req(h, "GET", "/v1/admin/audit/trace/"+trace, "")
	if code != 200 || int(replay["steps"].(float64)) != 3 {
		t.Fatalf("replay code=%d steps=%v", code, replay["steps"])
	}
	evs := replay["events"].([]any)
	gotOrder := ""
	for i, ev := range evs {
		m := ev.(map[string]any)
		gotOrder += m["action"].(string) + ">"
		if i == 2 && m["decision"].(string) != "approved" {
			t.Fatalf("last decision not approved: %v", m["decision"])
		}
	}
	if want := "intent_precheck>edge_egress>ticket_decision>"; gotOrder != want {
		t.Fatalf("replay order mismatch: %s", gotOrder)
	}
	// 空 trace 回放返回 steps=0
	code, empty := v178Req(h, "GET", "/v1/admin/audit/trace/v179_absent_"+strconv.FormatInt(ns, 10), "")
	if code != 200 || int(empty["steps"].(float64)) != 0 {
		t.Fatalf("absent trace steps should be 0: %d %v", code, empty["steps"])
	}

	// 4. 聚合包含 approved
	code, agg := v178Req(h, "GET", "/v1/admin/audit/aggregate", "")
	if code != 200 {
		t.Fatalf("aggregate code %d", code)
	}
	found := false
	for _, x := range agg["decisions"].([]any) {
		kv := x.(map[string]any)
		if kv["k"] == "approved" && int(kv["v"].(float64)) >= 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("aggregate decisions missing approved: %v", agg["decisions"])
	}
}
