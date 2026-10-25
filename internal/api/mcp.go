package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"human-agent-im/internal/relay"
)

// NewMCPHandler 构造 v2.2.0 MCP Streamable-HTTP 端点（/mcp）。
//
// 每个 openclaw bot 账号注册一个独立 MCP server，URL 带 account：
//   http://127.0.0.1:8080/mcp?account=<openclawAccountId>
// openclaw agent 收到微信消息后被强制调用工具 im2a_route，由 relay 中枢分流，
// 工具返回的文本即 agent 原样回发给微信的内容。
//
// 仅实现 MCP 最小必需子集：initialize / tools/list / tools/call，
// 响应统一为 application/json（非 SSE），无状态。
func NewMCPHandler(relaySvc *relay.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			// GET 用于 SSE 建流；本实现无状态，返回 405 即可。
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		accountID := r.URL.Query().Get("account")
		ctx := r.Context()

		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeMCPError(w, nil, http.StatusBadRequest, -32700, "parse error")
			return
		}

		// 支持单请求与 JSON-RPC batch。
		trimmed := strings.TrimSpace(string(raw))
		if strings.HasPrefix(trimmed, "[") {
			var batch []rpcRequest
			if err := json.Unmarshal(raw, &batch); err != nil {
				writeMCPError(w, nil, http.StatusBadRequest, -32700, "parse error")
				return
			}
			results := make([]rpcResponse, 0, len(batch))
			for _, req := range batch {
				if resp := handleRPC(ctx, req, accountID, relaySvc); resp != nil {
					results = append(results, *resp)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(results)
			return
		}

		var req rpcRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			writeMCPError(w, nil, http.StatusBadRequest, -32700, "parse error")
			return
		}
		resp := handleRPC(ctx, req, accountID, relaySvc)
		if resp == nil {
			// 通知（无 id）：返回 202 空体。
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *rpcErr       `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func handleRPC(ctx context.Context, req rpcRequest, accountID string, relaySvc *relay.Service) *rpcResponse {
	// 无 id 视为通知（initialized），无需响应。
	isNotification := len(req.ID) == 0 || strings.TrimSpace(string(req.ID)) == "null"

	switch req.Method {
	case "initialize":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "im2a-relay", "version": "2.2.0"},
		}}
	case "notifications/initialized":
		if isNotification {
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "ping":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"tools": []map[string]any{im2aRouteTool},
		}}
	case "tools/call":
		var p struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, -32602, "invalid params")
		}
		if p.Name != "im2a_route" {
			return errResp(req.ID, -32601, "unknown tool: "+p.Name)
		}
		fromWxid, _ := p.Arguments["from_wxid"].(string)
		text, _ := p.Arguments["text"].(string)
		idemKey, _ := p.Arguments["idem_key"].(string)
		if fromWxid == "" || text == "" {
			return errResp(req.ID, -32602, "from_wxid and text are required")
		}
		res, err := relaySvc.Relay(ctx, accountID, fromWxid, text, idemKey)
		if err != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"content": []map[string]any{{"type": "text", "text": "IM2A 处理失败：" + err.Error()}},
				"isError": true,
			}}
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": res.Reply}},
			"isError": false,
			"structuredContent": map[string]any{
				"route":    res.Route,
				"routedTo": res.RoutedTo,
				"botWxid":  res.BotWxid,
			},
		}}
	default:
		if isNotification {
			return nil
		}
		return errResp(req.ID, -32601, "method not found: "+req.Method)
	}
}

var im2aRouteTool = map[string]any{
	"name":        "im2a_route",
	"description": "IM2A 微信消息中枢。每当收到微信用户的消息（私聊或群聊提及），必须调用本工具，把发送者 wxid 和消息正文传入；工具返回的文本就是你要原样回发给该用户的回复，不要自行改写、扩写或二次创作。",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"from_wxid": map[string]any{
				"type":        "string",
				"description": "消息发送者的微信 wxid（openid），从入站消息的 sender 元数据取。",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "消息正文原文。",
			},
			"idem_key": map[string]any{
				"type":        "string",
				"description": "幂等键，取消息 ID；没有可留空。",
			},
		},
		"required": []string{"from_wxid", "text"},
	},
}

func errResp(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: msg}}
}

func writeMCPError(w http.ResponseWriter, id json.RawMessage, httpStatus, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: msg}})
}
