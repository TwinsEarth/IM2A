// Package skillruntime 实现 MCP 2026-07-28 无状态核心的 Skill 运行时与权限门控代理。
//
// 协议要点（§3.2）：
//   - 无状态：移除协议级初始化握手与会话粘性，每个请求自包含协议版本与能力，可路由到任意实例；
//   - 每个子 Agent 作为 MCP Host，Skill 作为 MCP Server 暴露 tools/list 与 tools/call；
//   - Skill 声明所需权限与数据范围，运行时在 PreToolUse 钩子执行 Trust Zone Capability 检查；
//   - 门控是代码层强制（trustzone.Check），不依赖模型"自觉遵守"。
//
// 本包仅用标准库实现 JSON-RPC 2.0 over HTTP（Streamable HTTP），可直接运行，无需 gRPC 依赖。
package skillruntime

import (
	"context"

	"human-agent-im/internal/trustzone"
)

// MCPProtocolVersion 支持的 MCP 协议版本（2026-07-28 无状态核心）。
const MCPProtocolVersion = "2026-07-28"

// JSONRPC 版本标识。
const JSONRPCVersion = "2.0"

// FinancialImpact 财务影响等级（Skill SDK 清单字段）。
type FinancialImpact string

const (
	// FinancialNone 无资金动作。
	FinancialNone FinancialImpact = "none"
	// FinancialReadOnly 只读，不产生费用。
	FinancialReadOnly FinancialImpact = "read_only"
	// FinancialSpendLimit 可能产生费用，受预算/金额阈值约束。
	FinancialSpendLimit FinancialImpact = "spend_limit"
)

// ApprovalPolicy 审批策略。
type ApprovalPolicy string

const (
	// ApprovalAuto 门控 ALLOW 时自动执行。
	ApprovalAuto ApprovalPolicy = "auto"
	// ApprovalApprove 始终需单次审批。
	ApprovalApprove ApprovalPolicy = "approve"
	// ApprovalDual 需双重审批（大额/高危）。
	ApprovalDual ApprovalPolicy = "dual"
)

// Tool 一个可调用工具的描述（MCP Tool）。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// 门控元数据（非标准 MCP 扩展，平台侧强制字段）
	Scope          trustzone.Scope `json:"-"`
	ReadOnly       bool            `json:"-"`
	Destructive    bool            `json:"-"`
	AmountFromArgs string          `json:"-"` // 从 arguments 取金额字段（如 "amountCents"），无则空
}

// Manifest Skill 声明式清单（Skill SDK + §3.2）。
type Manifest struct {
	SkillID              string          `json:"skill_id"`
	Version              string          `json:"version"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	RequiredCapabilities []string        `json:"required_capabilities"` // 所需数据域
	FinancialImpact      FinancialImpact `json:"financial_impact"`
	TrustZoneMin         trustzone.Level `json:"trust_zone_min"`
	ApprovalPolicy       ApprovalPolicy  `json:"approval_policy"`
}

// Skill 一个可注册的 Skill（对应一个 MCP Server）。
type Skill interface {
	Manifest() Manifest
	// Tools 返回该 Skill 暴露的工具（tools/list）。
	Tools() []Tool
	// Call 执行一个工具（tools/call），返回文本结果与结构化结果。
	Call(ctx context.Context, toolName string, args map[string]any) (text string, result map[string]any, err error)
}
