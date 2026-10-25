// SPDX-License-Identifier: MIT
//
// ops 域：出站/编排可观测、watch 审批、设备/Sync/离线、灰度门控、边缘管道。
package routes

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/gray"
	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/orchestration"
)

func registerOps(r *Registry, c *routeCtx) {
	// A7：出站排队/并发/熔断可观测（admin 鉴权）
	r.Add("", "/v1/ops/outbox", auth.RequireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(openclaw.SnapshotOutbox())
	}), c.adminProbeToken))

	// v2.3.0 L4 编排骨架状态（默认 L4_ENABLED=off，回退 legacy self_chat）
	r.Add("", "/v1/ops/orchestration", auth.RequireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{
			"l4Enabled": orchestration.EnvBool(os.Getenv("L4_ENABLED")),
			"phase":     "D-wired-v2.3.19",
			"package":   "internal/orchestration wired via inqueue.WithL4 (L4_ENABLED=on)",
		})
	}), c.adminProbeToken))

	// v1.7 运维 & v1.2 审批监控 & v1.7.6 设备/同步/离线
	r.Add("POST", "/v1/watch/approve", http.HandlerFunc(c.deps.WatchApprove), c.wrap)
	r.Add("POST", "/v1/watch/reject", http.HandlerFunc(c.deps.WatchReject), c.wrap)
	r.Add("POST", "/v1/watch/escalate", http.HandlerFunc(c.deps.WatchEscalate), c.wrap)
	r.Add("POST", "/v1/device/register", http.HandlerFunc(c.deps.RegisterDevice), c.wrap)
	r.Add("POST", "/v1/sync/state", http.HandlerFunc(c.deps.SyncState), c.wrap)
	r.Add("GET", "/v1/sync/state/", http.HandlerFunc(c.deps.GetState), c.wrap)
	r.Add("POST", "/v1/offline/submit", http.HandlerFunc(c.deps.OfflineSubmit), c.wrap)
	r.Add("GET", "/v1/offline/pending", http.HandlerFunc(c.deps.OfflineList), c.wrap)
	r.Add("POST", "/v1/offline/sync", http.HandlerFunc(c.deps.OfflineSync), c.wrap)
	r.Add("GET", "/v1/agent/logs", http.HandlerFunc(c.deps.AgentLog), c.wrap)

	// v1.9.8 灰度门控评估：GET /v1/gray/eval?feature=f&percent=p&entity=e
	r.Add("GET", "/v1/gray/eval", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		feature := q.Get("feature")
		entity := q.Get("entity")
		pct, _ := strconv.Atoi(q.Get("percent"))
		if feature == "" || entity == "" {
			http.Error(w, "feature/entity required", http.StatusBadRequest)
			return
		}
		hit := gray.Gate{Feature: feature, Percent: pct}.Enabled(entity)
		writeJSON(w, http.StatusOK, map[string]any{"feature": feature, "entity": entity, "percent": pct, "enabled": hit})
	}), c.wrap)

	r.Add("POST", "/v1/edge/pipeline", http.HandlerFunc(c.deps.EdgePipeline), c.wrap)
	r.Add("GET", "/v1/edge/egress/report", http.HandlerFunc(c.deps.EgressReport), c.wrap)
}
