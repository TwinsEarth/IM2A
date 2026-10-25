// Package api 提供各服务的 HTTP/JSON 业务端点。
//
// 仅用 net/http 标准库（Go 1.22+ 方法路由与路径值）。
// v0.2 在 gRPC 代码生成不可用的离线环境下，先以 HTTP/JSON 等价传输落地服务化；
// proto 契约已在 proto/ 定义，具备 gRPC 环境后 make proto 切换，业务语义不变。
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"database/sql"
	"human-agent-im/internal/approval"
	"human-agent-im/internal/broadcast"
	"human-agent-im/internal/distill"
	"human-agent-im/internal/eventbus"
	"human-agent-im/internal/memory"
	"human-agent-im/internal/model"
	"human-agent-im/internal/openclaw"
	"human-agent-im/internal/plugin"
	"human-agent-im/internal/push"
	"human-agent-im/internal/skillruntime"
	"human-agent-im/internal/store"
	"human-agent-im/internal/stream"
	"human-agent-im/internal/version"
)

// Deps 服务共享依赖（按服务注入需要的子集，未用到的字段可空）。
type Deps struct {
	Store      *store.Store
	Skills     *skillruntime.Runtime
	Engine     *approval.Engine
	Memory     *memory.GuardedStore // v0.6：Context 三层记忆（带子 Agent 隔离）
	Bus        *eventbus.Bus        // v0.6.5：事件总线（记忆变更/审批/门控）
	Distill    *distill.Engine      // v0.7：蒸馏闭环（审批反馈→阈值收紧）
	Model      model.Provider       // v0.8：L5 模型层
	Stream     *stream.Hub
	DB         *sql.DB
	PushClient *push.Client       // v1.7.4：离线推送 REST 客户端（可注入 fake）
	Broadcast  *broadcast.Service // v2.2.2：微信全量广播

	// v2.3.26 OpenClaw 子插件（契约 PLUGIN-CONTRACT.md §9-D3）：依赖注入，均为可空。
	Plugins        *plugin.Host            // 插件宿主（nil → 插件端点返回 503）
	OpenClawSender func() *openclaw.Sender // 出站统一走插件单例（nil → 回退 openclaw.NewSender()）

	agentCaps map[string]agentCap
}

// agentCap Agent 注册的能力（门控用）。
type agentCap struct {
	TrustMin      int
	Skills        []string
	ApprovalAbove int64
}

// HealthHandler 健康检查。
func HealthHandler(serviceName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service": serviceName, "status": "ok", "time": time.Now().Unix(),
			"version": version.Version, "commit": version.GitCommit,
		})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// errBody 统一错误响应。
type errBody struct {
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errBody{Error: msg})
}

// decode 解析请求体（空体允许）。
func decode(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if err.Error() == "EOF" {
			return nil
		}
		return err
	}
	return nil
}
