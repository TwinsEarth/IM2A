package orchestration

import (
	"fmt"
	"strings"
)

// v2.3.5 B1：PermissionGate PreToolUse 代码层强制门控。
//
// 设计原则（对齐 L4 v2 §2.3）：
//   - 门控不在 Prompt 里，在 dispatcher 调用 Skill 之前同步拦截。
//   - deny-by-default：未在白名单里的工具一律拒绝。
//   - 三维校验：
//       1) capability：该子 Agent 是否被允许调用这个工具（白名单）。
//       2) trust：高风险工具（pay/send_message_to_others/delete）需要 trust≥3。
//       3) budget：金额超过 threshold 不能自动执行，必须人工审批。
//   - 门控只裁决，不执行；通过的 step 才进 dispatcher。

// Risk 风险等级。
type Risk string

const (
	RiskNone Risk = "none"
	RiskLow  Risk = "low"
	RiskHigh Risk = "high"
)

// toolMeta 内置工具清单（最小集合）。后续 Skill Runtime 接入时从 MCP manifest 读。
type toolMeta struct {
	risk      Risk
	budgetKey string // 从 args 里取金额的字段名，""=不涉及金额
}

var builtinTools = map[string]toolMeta{
	"calendar.add":         {risk: RiskLow, budgetKey: ""},
	"calendar.list":        {risk: RiskNone, budgetKey: ""},
	"purchase.search":      {risk: RiskNone, budgetKey: ""},
	"purchase.checkout":    {risk: RiskHigh, budgetKey: "amount"},
	"property.report":      {risk: RiskLow, budgetKey: ""},
	"legal.draft":          {risk: RiskLow, budgetKey: ""},
	"parenting.remind":     {risk: RiskNone, budgetKey: ""},
	"im.send":              {risk: RiskHigh, budgetKey: ""}, // 发给别人，高风险
	"mail.send":            {risk: RiskHigh, budgetKey: ""},
	"system.delete_record": {risk: RiskHigh, budgetKey: ""},
}

// agentCaps 每个子 Agent 的工具白名单。
var agentCaps = map[SubAgentID]map[string]bool{
	AgentSchedule:    {"calendar.add": true, "calendar.list": true, "parenting.remind": true},
	AgentProcurement: {"purchase.search": true, "purchase.checkout": true},
	AgentProperty:    {"property.report": true},
	AgentLegal:       {"legal.draft": true},
	AgentParenting:   {"parenting.remind": true, "calendar.add": true},
	AgentMaster:      {"im.send": true, "mail.send": true, "system.delete_record": true},
}

// PermissionGate 门控实例。
type PermissionGate struct {
	// BudgetThreshold 单次自动执行的金额上限（元）。超过则需要人工审批。
	BudgetThreshold float64
	// MinTrustForHigh 高风险工具需要的最低信任档位。
	MinTrustForHigh int
}

// DefaultGate 生产默认值。
func DefaultGate() *PermissionGate {
	return &PermissionGate{BudgetThreshold: 500, MinTrustForHigh: 3}
}

// Decision 门控裁决。
type Decision struct {
	Allow         bool
	NeedsApproval bool
	Reason        string
}

// Check 在 PreToolUse 阶段同步裁决。
// agent：发起调用的子 Agent；step：工具调用提案；trustLevel：当前用户对该 agent 的信任档位。
func (g *PermissionGate) Check(agent SubAgentID, step ToolStep, trustLevel int) Decision {
	if g == nil {
		return Decision{Allow: false, Reason: "gate not configured"}
	}
	meta, ok := builtinTools[step.Tool]
	if !ok {
		return Decision{Allow: false, Reason: fmt.Sprintf("unknown tool %q deny-by-default", step.Tool)}
	}
	caps, ok := agentCaps[agent]
	if !ok || !caps[step.Tool] {
		return Decision{Allow: false, Reason: fmt.Sprintf("agent %s not allowed to call %s", agent, step.Tool)}
	}
	if meta.risk == RiskHigh && trustLevel < g.MinTrustForHigh {
		return Decision{Allow: false, Reason: fmt.Sprintf("high-risk %s requires trust>=%d (got %d)", step.Tool, g.MinTrustForHigh, trustLevel)}
	}
	if meta.budgetKey != "" {
		// 涉及金额的工具：金额缺失/无法解析时无法核对预算，必须上抛审批（不得默认放行）；
		// 金额为负或 0 视为非法，拒绝。
		v, present := step.Args[meta.budgetKey]
		if !present {
			return Decision{Allow: true, NeedsApproval: true, Reason: fmt.Sprintf("%s 缺少金额字段 %q，无法核对预算，需人工确认", step.Tool, meta.budgetKey)}
		}
		amt := toFloat(v)
		if amt <= 0 {
			return Decision{Allow: false, Reason: fmt.Sprintf("%s 金额非法（%.2f）", step.Tool, amt)}
		}
		if amt > g.BudgetThreshold {
			return Decision{Allow: true, NeedsApproval: true, Reason: fmt.Sprintf("amount %.2f > threshold %.2f", amt, g.BudgetThreshold)}
		}
	}
	return Decision{Allow: true}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		var f float64
		_, _ = fmt.Sscanf(strings.TrimSpace(x), "%f", &f)
		return f
	}
	return 0
}
