// JSON-RPC 2.0 与 MCP 消息类型（无状态，§3.2）。
package skillruntime

import "encoding/json"

// MCP 方法名。
const (
	MethodInitialize = "initialize"
	MethodToolsList  = "tools/list"
	MethodToolsCall  = "tools/call"
)

// JSON-RPC 2.0 错误码（标准 + 平台扩展）。
const (
	ErrCodeParse          = -32700
	ErrCodeInvalidReq     = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
	// 平台扩展：权限门控相关（-32000 ~ -32099 为服务端自定义保留段）
	ErrCodeGateDenied    = -32001 // 门控拒绝（越权）
	ErrCodeGateApproval  = -32002 // 需要用户审批（auth-required）
	ErrCodeSkillNotFound = -32003
)

// RPCRequest JSON-RPC 请求（无状态：每个请求自包含，不依赖 Mcp-Session-Id）。
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCError JSON-RPC 错误对象。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// RPCResponse JSON-RPC 响应。
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// InitializeResult initialize 结果（无状态核心：仅声明协议版本与能力，不建立会话）。
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      map[string]any `json:"serverInfo"`
}

// ToolsListResult tools/list 结果。
type ToolsListResult struct {
	Tools []ToolListEntry `json:"tools"`
}

// ToolListEntry 对外暴露的工具描述（含门控标注）。
type ToolListEntry struct {
	Tool
	SkillID         string `json:"skillId"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
}

// CallParams tools/call 参数（含平台侧门控上下文）。
type CallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	// Gate 由调用方（编排层/客户端）注入的调用上下文，门控不采信模型自报。
	Gate GateContext `json:"_gate"`
}

// GateContext 一次工具调用的门控上下文（编排层在调用前注入，非模型可控）。
type GateContext struct {
	AgentID     string `json:"agentId"`
	TrustLevel  int    `json:"trustLevel"`
	Scope       string `json:"scope"` // 可空，缺省取工具声明的 Scope
	AmountCents int64  `json:"amountCents"`
}

// CallResult tools/call 结果（含门控状态）。
type CallResult struct {
	Status     string         `json:"status"` // OK / REQUIRES_APPROVAL / DENIED
	Content    []ContentBlock `json:"content"`
	Gate       *GateDecision  `json:"gate,omitempty"`
	Structured map[string]any `json:"structured,omitempty"`
}

// ContentBlock MCP 内容块。
type ContentBlock struct {
	Type string `json:"type"` // text
	Text string `json:"text"`
}

// GateDecision 门控决策（供审计/编排层消费）。
type GateDecision struct {
	Decision string `json:"decision"` // ALLOW / REQUIRE_APPROVAL / DENY
	Reason   string `json:"reason"`
	SkillID  string `json:"skillId"`
	Tool     string `json:"tool"`
}
