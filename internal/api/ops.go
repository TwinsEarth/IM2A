// v1.7: 运维——metrics / audit 查询 / 全依赖健康检查。
package api

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strconv"
	"time"
)

// v1.7.0: Prometheus 风格 JSON 指标。
// GET /v1/admin/metrics
func (d *Deps) AdminMetrics(w http.ResponseWriter, r *http.Request) {
	var ms struct{ Num, Alloc, Sys, Uptime, GC uint64 }
	_ = ms
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	metrics := map[string]any{
		"go_goroutines":      runtime.NumGoroutine(),
		"go_mem_alloc_bytes": m.Alloc,
		"go_mem_sys_bytes":   m.Sys,
		"go_gc_count":        m.NumGC,
		"go_num_cpu":         runtime.NumCPU(),
		"time":               time.Now().Unix(),
	}

	// 从 PG 查真实业务计数
	if d.DB != nil {
		tables := map[string]string{
			"im2a_messages_total":     "SELECT COUNT(*) FROM messages",
			"im2a_audit_events_total": "SELECT COUNT(*) FROM audit_events",
			"im2a_ledger_total":       "SELECT COUNT(*) FROM paid_message_ledger", // v2.3.27：与扣费/统计统一到权威账本
			"im2a_devices_total":      "SELECT COUNT(*) FROM devices",
		}
		for name, q := range tables {
			var n int64
			if err := d.DB.QueryRow(q).Scan(&n); err == nil {
				metrics[name] = n
			}
		}
	}
	writeJSON(w, 200, metrics)
}

// v1.7.1: 审计事件查询（分页 + 按 agent/action 过滤）。
// GET /v1/admin/audit?limit=20&agent=butler&action=xxx
func (d *Deps) AdminAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 20
	if l := q.Get("limit"); l != "" {
		if n := atoiSafe(l); n > 0 && n <= 200 {
			limit = n
		}
	}
	agent := q.Get("agent")
	action := q.Get("action")

	if d.DB == nil {
		writeJSON(w, 200, map[string]any{"total": 0, "items": []any{}})
		return
	}

	query := `SELECT agent_id, action, detail, occurred_at, COALESCE(decision, $1) FROM audit_events WHERE 1=1`
	args := []any{""}
	idx := 2
	if agent != "" {
		query += " AND agent_id=$" + strconv.Itoa(idx)
		args = append(args, agent)
		idx++
	}
	if action != "" {
		query += " AND action=$" + strconv.Itoa(idx)
		args = append(args, action)
		idx++
	}
	query += " ORDER BY occurred_at DESC LIMIT $" + strconv.Itoa(idx)
	args = append(args, limit)

	rows, err := d.DB.Query(query, args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type AuditItem struct {
		Agent    string          `json:"agent"`
		Action   string          `json:"action"`
		Detail   json.RawMessage `json:"detail"`
		Time     string          `json:"time"`
		Decision string          `json:"decision,omitempty"`
	}
	items := []AuditItem{}
	for rows.Next() {
		var it AuditItem
		_ = rows.Scan(&it.Agent, &it.Action, &it.Detail, &it.Time, &it.Decision)
		items = append(items, it)
	}
	writeJSON(w, 200, map[string]any{"total": len(items), "items": items})
}

// v1.7.2: 全依赖健康检查（PG + 内存组件）。
// GET /v1/admin/health
func (d *Deps) AdminHealth(w http.ResponseWriter, r *http.Request) {
	deps := map[string]any{}
	status := "ok"

	if d.DB != nil {
		if err := d.DB.Ping(); err != nil {
			deps["postgres"] = map[string]any{"status": "down", "error": err.Error()}
			status = "degraded"
		} else {
			deps["postgres"] = map[string]any{"status": "up"}
		}
	} else {
		deps["postgres"] = map[string]any{"status": "not_configured"}
		status = "degraded"
	}

	deps["memory"] = map[string]any{"status": "up"}
	deps["stream"] = map[string]any{"status": "up"}
	deps["model"] = map[string]any{"status": map[bool]string{true: "configured", false: "not_configured"}[d.Model != nil]}

	code := 200
	if status != "ok" {
		code = 503
	}
	writeJSON(w, code, map[string]any{"status": status, "dependencies": deps, "time": time.Now().Unix()})
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
