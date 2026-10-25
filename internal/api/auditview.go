package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// v1.7.9 审计可视化：事件流时间线 + 门控决策回放（按 trace_id 串联一次完整决策链）。
type auditEvent struct {
	ID         int64           `json:"id"`
	OccurredAt string          `json:"occurred_at"`
	TraceID    string          `json:"trace_id"`
	MessageID  string          `json:"message_id"`
	AgentID    string          `json:"agent_id"`
	UserID     int64           `json:"user_id"`
	Action     string          `json:"action"`
	Decision   string          `json:"decision"`
	Detail     json.RawMessage `json:"detail"`
}

const auditCols = `id, COALESCE(occurred_at::text,''), COALESCE(trace_id,''), COALESCE(message_id,''), COALESCE(agent_id,''), COALESCE(user_id,0), action, COALESCE(decision,''), COALESCE(detail,'{}'::jsonb)`

func (d *Deps) auditViewRegister(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/audit/stream", d.adminAuditStream)
	mux.HandleFunc("GET /v1/admin/audit/trace/{trace}", d.adminAuditTrace)
	mux.HandleFunc("GET /v1/admin/audit/aggregate", d.adminAuditAggregate)
}

func scanAudit(s interface{ Scan(dest ...any) error }) (*auditEvent, error) {
	var e auditEvent
	if err := s.Scan(&e.ID, &e.OccurredAt, &e.TraceID, &e.MessageID, &e.AgentID, &e.UserID, &e.Action, &e.Decision, &e.Detail); err != nil {
		return nil, err
	}
	return &e, nil
}

// GET /v1/admin/audit/stream?limit=&action=&decision=&trace_id=&agent=
func (d *Deps) adminAuditStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), 100)
	if limit < 1 || limit > 500 {
		limit = 100
	}
	where := " WHERE 1=1"
	args := []any{}
	for k, col := range map[string]string{"action": "action", "decision": "decision", "trace_id": "trace_id", "agent": "agent_id"} {
		if v := q.Get(k); v != "" {
			args = append(args, v)
			where += " AND " + col + "=$" + strconv.Itoa(len(args))
		}
	}
	args = append(args, limit)
	rows, err := d.DB.QueryContext(r.Context(), `SELECT `+auditCols+` FROM audit_events`+where+` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []auditEvent{}
	for rows.Next() {
		if e, err := scanAudit(rows); err == nil {
			out = append(out, *e)
		}
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "total": len(out), "events": out})
}

// GET /v1/admin/audit/trace/{trace} —— 门控决策回放，按时间正序还原整条链。
func (d *Deps) adminAuditTrace(w http.ResponseWriter, r *http.Request) {
	trace := r.PathValue("trace")
	if trace == "" {
		writeErr(w, 400, "trace required")
		return
	}
	rows, err := d.DB.QueryContext(r.Context(), `SELECT `+auditCols+` FROM audit_events WHERE trace_id=$1 ORDER BY occurred_at ASC, id ASC`, trace)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []auditEvent{}
	for rows.Next() {
		if e, err := scanAudit(rows); err == nil {
			out = append(out, *e)
		}
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "trace_id": trace, "steps": len(out), "events": out})
}

// GET /v1/admin/audit/aggregate —— action/decision 分布。
func (d *Deps) adminAuditAggregate(w http.ResponseWriter, r *http.Request) {
	type kv struct {
		K string `json:"k"`
		V int64  `json:"v"`
	}
	rows, err := d.DB.QueryContext(r.Context(), `SELECT action, COALESCE(decision,''), count(*) FROM audit_events GROUP BY 1,2 ORDER BY 3 DESC LIMIT 100`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type adv struct {
		Action   string `json:"action"`
		Decision string `json:"decision"`
		Count    int64  `json:"count"`
	}
	advOut := []adv{}
	for rows.Next() {
		var a adv
		if err := rows.Scan(&a.Action, &a.Decision, &a.Count); err == nil {
			advOut = append(advOut, a)
		}
	}
	drows, err := d.DB.QueryContext(r.Context(), `SELECT COALESCE(decision,''), count(*) FROM audit_events GROUP BY 1 ORDER BY 2 DESC`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer drows.Close()
	decOut := []kv{}
	for drows.Next() {
		var x kv
		if err := drows.Scan(&x.K, &x.V); err == nil {
			decOut = append(decOut, x)
		}
	}
	var total int64
	d.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM audit_events`).Scan(&total)
	writeJSON(w, 200, map[string]any{"status": "ok", "total": total, "action_decisions": advOut, "decisions": decOut})
}
