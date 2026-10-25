// HTTP 绑定：MCP Streamable HTTP（无状态，POST JSON-RPC）。
// 每个请求自包含，可路由到任意实例（Serverless 友好，§3.2）。
package skillruntime

import (
	"encoding/json"
	"io"
	"net/http"
)

// Handler 返回 Skill Runtime 的 HTTP handler（POST 为 JSON-RPC，GET 为健康/发现）。
func (rt *Runtime) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)) // 1 MiB 上限
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcError(nil, ErrCodeParse, err.Error()))
			return
		}
		resp := rt.Dispatch(r.Context(), body)
		code := http.StatusOK
		if resp.Error != nil {
			switch resp.Error.Code {
			case ErrCodeGateDenied, ErrCodeSkillNotFound:
				code = http.StatusForbidden
			case ErrCodeGateApproval:
				code = http.StatusAccepted // 202：已接收但需人工审批
			case ErrCodeMethodNotFound:
				code = http.StatusNotFound
			case ErrCodeInvalidParams, ErrCodeParse:
				code = http.StatusBadRequest
			default:
				code = http.StatusInternalServerError
			}
		}
		writeJSON(w, code, resp)
	})
	// 轻量发现：返回已注册 Skill 清单（非 MCP 标准，便于巡检）。
	mux.HandleFunc("/mcp/skills", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"protocolVersion": MCPProtocolVersion,
			"skills":          rt.ListTools(),
		})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}
