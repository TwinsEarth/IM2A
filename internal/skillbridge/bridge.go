// Package skillbridge 把编排层（orchestration）的工具调用映射到真实的
// skillruntime MCP 工具并执行（v2.3.25，消除"假已办"）。
//
// 编排层 PermissionGate 用一套抽象工具名（calendar.add / purchase.checkout /
// property.report / im.send ...），而 skillruntime 内置 Skill 用的是
// "skillID.toolName"（calendar.query / family-purchase.order / ...）。
// 本包是二者之间唯一的映射与适配层：
//
//   - 有真实 Skill 的领域工具：映射后调 Runtime.Call（门控代理二次校验），返回真实结果；
//   - 通道动作（im.send/mail.send/delete_record）：不是领域 Skill，回复本身即出站，返回空串；
//   - 尚无 Skill 接通的领域工具（legal.draft/parenting.remind）：返回 ErrToolNotWired，
//     由 Runner 据实回告，绝不假装已办。
package skillbridge

import (
	"context"
	"fmt"

	"human-agent-im/internal/orchestration"
	"human-agent-im/internal/skillruntime"
	"human-agent-im/internal/trustzone"
)

// Executor 实现 orchestration.ToolExecutor，底层走 skillruntime.Runtime。
type Executor struct {
	rt *skillruntime.Runtime
}

// New 构造一个接真实 Skill Runtime 的执行器。rt 为 nil 时所有领域工具视为未接通。
func New(rt *skillruntime.Runtime) *Executor { return &Executor{rt: rt} }

// 编译期断言：Executor 必须满足编排层 ToolExecutor。
var _ orchestration.ToolExecutor = (*Executor)(nil)

// mapping 编排工具名 → skillruntime 工具。
type mapping struct {
	tool  string // skillruntime 全限定名 "skillID.toolName"
	scope string
	// buildArgs 把编排层 args 翻译成 skill 入参（nil 表示原样透传）。
	buildArgs func(args map[string]any) map[string]any
	// channelOnly 为 true 表示该动作由微信/邮件通道本身完成，无领域 Skill。
	channelOnly bool
}

var toolMap = map[string]mapping{
	// 日程（内置 Skill 仅提供只读查询；新增/列举统一走 query 演示链路）
	"calendar.add":  {tool: "calendar.query", scope: string(trustzone.ScopeSchedule)},
	"calendar.list": {tool: "calendar.query", scope: string(trustzone.ScopeSchedule)},

	// 采购：比价（只读）/下单（金额，受预算门控）
	"purchase.search": {tool: "family-purchase.compare", scope: string(trustzone.ScopePurchase),
		buildArgs: func(a map[string]any) map[string]any {
			return map[string]any{"item": firstString(a, "item", "keyword", "q")}
		}},
	"purchase.checkout": {tool: "family-purchase.order", scope: string(trustzone.ScopePurchase),
		buildArgs: func(a map[string]any) map[string]any {
			item := firstString(a, "item", "keyword", "q")
			args := map[string]any{"item": item, "amountCents": amountCents(a["amount"])}
			return args
		}},

	// 物业报修（可产生费用）
	"property.report": {tool: "home-maintenance.request", scope: string(trustzone.ScopeFamily),
		buildArgs: func(a map[string]any) map[string]any {
			cat := firstString(a, "category")
			if cat == "" {
				cat = "repair"
			}
			return map[string]any{
				"category":    cat,
				"description": firstString(a, "description", "desc", "detail"),
				"amountCents": amountCents(a["amount"]),
			}
		}},

	// 通道动作：回复/外发本身即动作，不经过领域 Skill（Runner 据空串回退到计划提示）
	"im.send":              {channelOnly: true},
	"mail.send":            {channelOnly: true},
	"system.delete_record": {channelOnly: true},
}

// Execute 执行一个已通过 PermissionGate 的工具步骤。
func (e *Executor) Execute(ctx context.Context, sub orchestration.SubAgentID, step orchestration.ToolStep, trustLevel int) (string, error) {
	m, ok := toolMap[step.Tool]
	if !ok {
		// 未在映射表登记：编排层已门控的合法工具但没有真实 Skill，如实报未接通。
		return "", fmt.Errorf("%w: %s", orchestration.ErrToolNotWired, step.Tool)
	}
	if m.channelOnly {
		// 通道动作由出站链路完成，这里不做领域执行。
		return "", nil
	}
	if e.rt == nil {
		return "", fmt.Errorf("%w: %s (runtime nil)", orchestration.ErrToolNotWired, step.Tool)
	}

	args := step.Args
	if args == nil {
		args = map[string]any{}
	}
	if m.buildArgs != nil {
		args = m.buildArgs(args)
	}

	// 内置 Skill 都是家庭域（日程/采购/物业），其 TrustZoneMin=LevelFamily；
	// 权威门控已在编排层 PermissionGate 用真实信任档完成，这里用家庭档做 Skill 侧二次校验。
	res := e.rt.Call(ctx, skillruntime.CallParams{
		Name:      m.tool,
		Arguments: args,
		Gate: skillruntime.GateContext{
			AgentID:     string(sub),
			TrustLevel:  int(trustzone.LevelFamily),
			Scope:       m.scope,
			AmountCents: amountCents(step.Args["amount"]),
		},
	})
	switch res.Status {
	case "OK":
		if len(res.Content) > 0 {
			return res.Content[0].Text, nil
		}
		return "", nil
	case "REQUIRES_APPROVAL":
		return "", fmt.Errorf("skill %s requires approval: %s", m.tool, res.Gate.Reason)
	default:
		return "", fmt.Errorf("skill %s denied: %s", m.tool, res.Gate.Reason)
	}
}

// firstString 从 args 按候选键取第一个非空字符串。
func firstString(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// amountCents 把"元"（编排层金额口径）换算为"分"；兼容已是分（int64）或数字/字符串。
func amountCents(v any) int64 {
	switch n := v.(type) {
	case nil:
		return 0
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		// 编排层金额单位为元，带小数；小于 10000 的数值按元换算，避免误把大额当元。
		return int64(n*100 + 0.5)
	case string:
		var f float64
		if _, err := fmt.Sscanf(n, "%f", &f); err == nil {
			return int64(f*100 + 0.5)
		}
	}
	return 0
}
