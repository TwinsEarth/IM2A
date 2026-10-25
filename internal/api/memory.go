// Context 记忆 HTTP 端点（v0.6）：总 Agent 管理子 Agent 记忆，含跨 Agent 授权。
package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"human-agent-im/internal/memory"
)

// PutFactRequest 写入/更新语义记忆（偏好/底线）。
type PutFactRequest struct {
	UserID     uint64  `json:"userId"`
	AgentID    string  `json:"agentId"`
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
}

// AppendEpisodeRequest 追加情景记忆（办事轨迹）。
type AppendEpisodeRequest struct {
	UserID  uint64         `json:"userId"`
	AgentID string         `json:"agentId"`
	Episode memory.Episode `json:"episode"`
}

// GrantMemoryRequest 总 Agent 授权访客访问目标 Agent 记忆。
type GrantMemoryRequest struct {
	UserID       uint64 `json:"userId"`
	OwnerAgent   string `json:"ownerAgent"`
	VisitorAgent string `json:"visitorAgent"`
	Access       string `json:"access"` // read / write / none
}

// MemoryMux 挂载记忆相关路由。
func (d *Deps) MemoryMux(serviceName string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthHandler(serviceName))

	scopeOf := func(uid uint64, agent string) memory.Scope {
		return memory.Scope{UserID: uid, AgentID: agent}
	}

	// 写入语义记忆（偏好/事实）
	mux.HandleFunc("POST /v1/memory/fact", func(w http.ResponseWriter, r *http.Request) {
		var req PutFactRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 || req.AgentID == "" || req.Key == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId/key required")
			return
		}
		src := req.Source
		if src == "" {
			src = "manual"
		}
		f := &memory.Fact{
			Key: req.Key, Value: req.Value,
			Confidence: req.Confidence, Source: src,
		}
		sc := scopeOf(req.UserID, req.AgentID)
		if err := d.Memory.UpsertFact(r.Context(), sc, f); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fact": f})
	})

	// 查询语义记忆（本 Agent 自己的；?q= 关键词）
	mux.HandleFunc("GET /v1/memory/search", func(w http.ResponseWriter, r *http.Request) {
		kw := r.URL.Query().Get("q")
		if kw == "" {
			writeJSON(w, 400, map[string]any{"error": "missing q"})
			return
		}
		hits, err := d.Memory.SearchFacts(r.Context(), memory.Scope{UserID: 1, AgentID: "butler"}, kw)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"results": hits, "count": len(hits)})
	})
	mux.HandleFunc("GET /v1/memory/facts", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		agent := r.URL.Query().Get("agentId")
		if uid == 0 || agent == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId required")
			return
		}
		sc := scopeOf(uid, agent)
		q := r.URL.Query().Get("q")
		var facts []*memory.Fact
		var err error
		if q != "" {
			facts, err = d.Memory.SearchFacts(r.Context(), sc, q)
		} else {
			facts, err = d.Memory.ListFacts(r.Context(), sc)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"facts": facts, "count": len(facts)})
	})

	// 追加情景记忆
	mux.HandleFunc("POST /v1/memory/episode", func(w http.ResponseWriter, r *http.Request) {
		var req AppendEpisodeRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 || req.AgentID == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId required")
			return
		}
		sc := scopeOf(req.UserID, req.AgentID)
		if err := d.Memory.AppendEpisode(r.Context(), sc, &req.Episode); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "episode": req.Episode})
	})

	// 写入工作记忆（KV，可带 TTL）
	mux.HandleFunc("POST /v1/memory/working", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID  uint64 `json:"userId"`
			AgentID string `json:"agentId"`
			Key     string `json:"key"`
			Value   any    `json:"value"`
			TTL     int64  `json:"ttlSeconds"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 || req.AgentID == "" || req.Key == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId/key required")
			return
		}
		sc := scopeOf(req.UserID, req.AgentID)
		var err error
		if req.TTL > 0 {
			err = d.Memory.PutWorkingWithTTL(r.Context(), sc, req.Key, req.Value, req.TTL)
		} else {
			err = d.Memory.PutWorking(r.Context(), sc, req.Key, req.Value)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 读取工作记忆
	mux.HandleFunc("GET /v1/memory/working", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		agent := r.URL.Query().Get("agentId")
		key := r.URL.Query().Get("key")
		if uid == 0 || agent == "" || key == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId/key required")
			return
		}
		sc := scopeOf(uid, agent)
		v, err := d.Memory.GetWorking(r.Context(), sc, key)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": v})
	})

	// 删除工作记忆
	mux.HandleFunc("DELETE /v1/memory/working", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		agent := r.URL.Query().Get("agentId")
		key := r.URL.Query().Get("key")
		if uid == 0 || agent == "" || key == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId/key required")
			return
		}
		sc := scopeOf(uid, agent)
		d.Memory.DeleteWorking(r.Context(), sc, key)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 查询情景记忆（?kind= 可选）
	mux.HandleFunc("GET /v1/memory/episodes", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		agent := r.URL.Query().Get("agentId")
		if uid == 0 || agent == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId required")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		eps, err := d.Memory.ListEpisodes(r.Context(), scopeOf(uid, agent),
			memory.EpisodeQuery{Kind: r.URL.Query().Get("kind"), Tag: r.URL.Query().Get("tag"), Limit: limit})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"episodes": eps, "count": len(eps)})
	})

	// 注册总 Agent（管家）
	mux.HandleFunc("POST /v1/memory/master", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID   uint64 `json:"userId"`
			MasterID string `json:"masterId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 || req.MasterID == "" {
			writeErr(w, http.StatusBadRequest, "userId/masterId required")
			return
		}
		d.Memory.RegisterMaster(req.UserID, req.MasterID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 总 Agent 给子 Agent 之间授权（跨 Agent 记忆访问）
	mux.HandleFunc("POST /v1/memory/grant", func(w http.ResponseWriter, r *http.Request) {
		var req GrantMemoryRequest
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 || req.OwnerAgent == "" || req.VisitorAgent == "" {
			writeErr(w, http.StatusBadRequest, "userId/ownerAgent/visitorAgent required")
			return
		}
		owner := scopeOf(req.UserID, req.OwnerAgent)
		switch req.Access {
		case "read":
			d.Memory.Grant(owner, req.VisitorAgent, memory.AccessRead)
		case "write":
			d.Memory.Grant(owner, req.VisitorAgent, memory.AccessWrite)
		case "none", "revoke":
			d.Memory.Revoke(owner, req.VisitorAgent)
		default:
			writeErr(w, http.StatusBadRequest, "access must be read/write/none")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 跨 Agent 读取记忆（总 Agent 或被授权访客）
	mux.HandleFunc("GET /v1/memory/cross/facts", func(w http.ResponseWriter, r *http.Request) {
		callerUID, _ := strconv.ParseUint(r.URL.Query().Get("callerUserId"), 10, 64)
		callerAgent := r.URL.Query().Get("callerAgent")
		targetUID, _ := strconv.ParseUint(r.URL.Query().Get("targetUserId"), 10, 64)
		targetAgent := r.URL.Query().Get("targetAgent")
		if callerUID == 0 || callerAgent == "" || targetUID == 0 || targetAgent == "" {
			writeErr(w, http.StatusBadRequest, "caller/target userId+agentId required")
			return
		}
		caller := scopeOf(callerUID, callerAgent)
		target := scopeOf(targetUID, targetAgent)
		facts, err := d.Memory.ReadFactsOf(r.Context(), caller, target)
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"facts": facts, "count": len(facts)})
	})

	// v0.6.2：导出该用户全部记忆（仅 master）
	mux.HandleFunc("GET /v1/memory/export", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		agent := r.URL.Query().Get("agentId")
		if uid == 0 || agent == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId required")
			return
		}
		exp, err := d.Memory.ExportUser(r.Context(), scopeOf(uid, agent))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, exp)
	})

	// v0.6.2：导入记忆包（仅 master）
	mux.HandleFunc("POST /v1/memory/import", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			CallerUID uint64                   `json:"callerUserId"`
			Caller    string                   `json:"callerAgent"`
			Export    *memory.UserMemoryExport `json:"export"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.CallerUID == 0 || req.Caller == "" || req.Export == nil {
			writeErr(w, http.StatusBadRequest, "callerUserId/callerAgent/export required")
			return
		}
		if err := d.Memory.ImportUser(r.Context(), scopeOf(req.CallerUID, req.Caller), req.Export); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// v0.6.2：记忆统计（仅 master）
	mux.HandleFunc("GET /v1/memory/stats", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		agent := r.URL.Query().Get("agentId")
		if uid == 0 || agent == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId required")
			return
		}
		st, err := d.Memory.StatsUser(r.Context(), scopeOf(uid, agent))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	})

	// v0.6.3：事实置信度衰减（记忆遗忘，Agent 自己调）
	mux.HandleFunc("POST /v1/memory/decay", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID  uint64  `json:"userId"`
			AgentID string  `json:"agentId"`
			Factor  float64 `json:"factor"` // 默认 0.9
			Min     float64 `json:"min"`    // 默认 0.5
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 || req.AgentID == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId required")
			return
		}
		if req.Factor == 0 {
			req.Factor = 0.9
		}
		if req.Min == 0 {
			req.Min = 0.5
		}
		decayed, removed, err := d.Memory.DecayConfidence(r.Context(), scopeOf(req.UserID, req.AgentID), req.Factor, req.Min)
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"decayed": decayed, "removed": removed})
	})

	// v0.6.3：master 跨子 Agent 全局搜索
	mux.HandleFunc("GET /v1/memory/search/all", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.ParseUint(r.URL.Query().Get("userId"), 10, 64)
		agent := r.URL.Query().Get("agentId")
		if uid == 0 || agent == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId required")
			return
		}
		hits, err := d.Memory.SearchAllFacts(r.Context(), scopeOf(uid, agent), r.URL.Query().Get("q"))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"facts": hits, "count": len(hits)})
	})

	// v0.6.4：强化/弱化事实（审批反馈调节置信度，蒸馏前置）
	mux.HandleFunc("POST /v1/memory/fact/reinforce", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID  uint64  `json:"userId"`
			AgentID string  `json:"agentId"`
			Key     string  `json:"key"`
			Delta   float64 `json:"delta"` // 正=强化，负=弱化
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 || req.AgentID == "" || req.Key == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId/key required")
			return
		}
		f, err := d.Memory.ReinforceFact(r.Context(), scopeOf(req.UserID, req.AgentID), req.Key, req.Delta)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"fact": f})
	})

	// v0.6.4：清理过期工作记忆
	mux.HandleFunc("POST /v1/memory/working/gc", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID  uint64 `json:"userId"`
			AgentID string `json:"agentId"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.UserID == 0 || req.AgentID == "" {
			writeErr(w, http.StatusBadRequest, "userId/agentId required")
			return
		}
		n, err := d.Memory.GCWorking(r.Context(), scopeOf(req.UserID, req.AgentID))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cleaned": n})
	})

	// v0.6.5：对某 scope 事实做快照（master）
	mux.HandleFunc("POST /v1/memory/snapshot", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			CallerUID   uint64 `json:"callerUserId"`
			CallerAgent string `json:"callerAgent"`
			TargetUID   uint64 `json:"targetUserId"`
			TargetAgent string `json:"targetAgent"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		snap, err := d.Memory.TakeSnapshot(r.Context(),
			scopeOf(req.CallerUID, req.CallerAgent),
			scopeOf(req.TargetUID, req.TargetAgent))
		if err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, snap)
	})

	// v0.6.5：用快照回滚（master）
	mux.HandleFunc("POST /v1/memory/restore", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			CallerUID   uint64           `json:"callerUserId"`
			CallerAgent string           `json:"callerAgent"`
			Snapshot    *memory.Snapshot `json:"snapshot"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Snapshot == nil {
			writeErr(w, http.StatusBadRequest, "snapshot required")
			return
		}
		if err := d.Memory.RestoreSnapshot(r.Context(), scopeOf(req.CallerUID, req.CallerAgent), req.Snapshot); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	return mux
}
