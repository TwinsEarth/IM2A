// Package trustzone 实现熟人五档信任矩阵与代码层 capability 门控（§1.4 / §3.3）。
//
// 核心原则：信任边界是代码层强制，不依赖模型"自觉遵守"。
// 每次工具调用在 Skill Runtime 的 PreToolUse 钩子执行 Check()。
package trustzone

import (
	"fmt"
	"strings"
)

// Level 信任档位（与 proto common.TrustLevel 一一对应）。
type Level int

const (
	// LevelSelf L0 本人：全部自主。
	LevelSelf Level = 0
	// LevelFamily L1 家庭：日程/家庭采购/物业自主；大额支出与对外承诺需审批。
	LevelFamily Level = 1
	// LevelTrusted L2 信任：预约日程/留言转达；第三方信息查看需审批。
	LevelTrusted Level = 2
	// LevelKnown L3 已知：接收留言并摘要；所有对外回复需审批。
	LevelKnown Level = 3
	// LevelStranger L4 陌生：仅接收结构化来意；付费后查看；禁止自主执行。
	LevelStranger Level = 4
)

// String 档位名。
func (l Level) String() string {
	switch l {
	case LevelSelf:
		return "self"
	case LevelFamily:
		return "family"
	case LevelTrusted:
		return "trusted"
	case LevelKnown:
		return "known"
	case LevelStranger:
		return "stranger"
	default:
		return fmt.Sprintf("level(%d)", int(l))
	}
}

// ParseLevel 从字符串解析档位（配置/协议输入）。
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "self", "0":
		return LevelSelf, nil
	case "family", "1":
		return LevelFamily, nil
	case "trusted", "2":
		return LevelTrusted, nil
	case "known", "3":
		return LevelKnown, nil
	case "stranger", "4":
		return LevelStranger, nil
	default:
		return LevelStranger, fmt.Errorf("trustzone: unknown level %q", s)
	}
}

// Scope 子 Agent 领域。
type Scope string

const (
	ScopeSchedule Scope = "schedule" // 日程协调
	ScopeFamily   Scope = "family"   // 家庭事务/物业
	ScopePurchase Scope = "purchase" // 采购比价
	ScopeWorkMail Scope = "workmail" // 工作邮件
	ScopeStranger Scope = "stranger" // 陌生人过滤
	ScopeFinance  Scope = "finance"  // 资金/大额支出
	ScopeExternal Scope = "external" // 对外承诺/发送
)

// Permissions 某信任档位对某领域的执行权限（§1.4 矩阵的一格）。
type Permissions struct {
	AutoExecute      bool // Agent 可自主执行
	RequiresApproval bool // 需用户审批
	Forbidden        bool // 禁止
}

func (p Permissions) String() string {
	switch {
	case p.AutoExecute:
		return "auto_execute"
	case p.RequiresApproval:
		return "requires_approval"
	default:
		return "forbidden"
	}
}

// matrix 五档 × 领域权限矩阵（§1.4）。缺省 = forbidden。
var matrix = map[Level]map[Scope]Permissions{
	LevelSelf: {
		ScopeSchedule: {AutoExecute: true},
		ScopeFamily:   {AutoExecute: true},
		ScopePurchase: {AutoExecute: true},
		ScopeWorkMail: {AutoExecute: true},
		ScopeStranger: {AutoExecute: true},
		ScopeFinance:  {AutoExecute: true},
		ScopeExternal: {AutoExecute: true},
	},
	LevelFamily: {
		ScopeSchedule: {AutoExecute: true},
		ScopeFamily:   {AutoExecute: true},
		ScopePurchase: {AutoExecute: true},
		ScopeFinance:  {RequiresApproval: true},
		ScopeExternal: {RequiresApproval: true},
		ScopeWorkMail: {Forbidden: true},
		ScopeStranger: {RequiresApproval: true},
	},
	LevelTrusted: {
		ScopeSchedule: {AutoExecute: true},
		ScopePurchase: {RequiresApproval: true},
		ScopeFinance:  {RequiresApproval: true},
		ScopeExternal: {RequiresApproval: true},
		ScopeFamily:   {RequiresApproval: true},
		ScopeWorkMail: {Forbidden: true},
		ScopeStranger: {RequiresApproval: true},
	},
	LevelKnown: {
		ScopeSchedule: {RequiresApproval: true},
		ScopeExternal: {RequiresApproval: true},
		ScopePurchase: {Forbidden: true},
		ScopeFinance:  {Forbidden: true},
		ScopeFamily:   {Forbidden: true},
		ScopeWorkMail: {Forbidden: true},
		ScopeStranger: {RequiresApproval: true},
	},
	LevelStranger: {
		// 陌生：仅接收结构化来意（Agent 预审）；付费+审批后才可见原始消息；禁止自主执行。
		ScopeStranger: {RequiresApproval: true},
	},
}

// PermissionsFor 返回某档位对某领域的权限。
func PermissionsFor(l Level, scope Scope) Permissions {
	scopes, ok := matrix[l]
	if !ok {
		return Permissions{Forbidden: true}
	}
	p, ok := scopes[scope]
	if !ok {
		return Permissions{Forbidden: true}
	}
	return p
}

// ---- Capability 门控（PreToolUse 钩子的数据源，对应 §6 agent_capabilities 表） ----

// Capability Agent 能力声明。
type Capability struct {
	AgentID                    string
	TrustLevelMin              Level    // 允许执行的最低信任档位
	AllowedSkills              []string // Skill 白名单
	BudgetCentsPerDay          int64    // 单日预算（金额口径）
	RequiresApprovalAboveCents int64    // 超过该金额自动转人工审批（0 = 不启用）
}

// CallContext 一次工具调用的上下文。
type CallContext struct {
	SkillID     string
	Scope       Scope
	TrustLevel  Level // 当前任务的信任档位（由 relation-svc 在会话建立时注入）
	AmountCents int64 // 涉及金额（无则为 0）
	TargetUID   uint64
}

// Decision 门控决策。
type Decision struct {
	Allowed          bool
	RequiresApproval bool
	Reason           string
}

// String 供审计日志记录。
func (d Decision) String() string {
	if d.Allowed {
		return "ALLOW"
	}
	if d.RequiresApproval {
		return "REQUIRE_APPROVAL"
	}
	return "DENY"
}

// Check 执行 Trust Zone Capability 检查（代码层强制，模型无法绕过）。
// 检查顺序：Skill 白名单 → 信任档位下限 → 领域权限矩阵 → 金额阈值 → 预算。
func (c Capability) Check(ctx CallContext) Decision {
	// 1) Skill 白名单
	if len(c.AllowedSkills) > 0 && !contains(c.AllowedSkills, ctx.SkillID) {
		return Decision{Reason: fmt.Sprintf("skill %q not in whitelist", ctx.SkillID)}
	}
	// 2) 信任档位下限
	if ctx.TrustLevel < c.TrustLevelMin {
		return Decision{Reason: fmt.Sprintf("trust %s below min %s", ctx.TrustLevel, c.TrustLevelMin)}
	}
	// 3) 领域权限矩阵
	perm := PermissionsFor(ctx.TrustLevel, ctx.Scope)
	if perm.Forbidden {
		return Decision{Reason: fmt.Sprintf("scope %s forbidden at trust %s", ctx.Scope, ctx.TrustLevel)}
	}
	if perm.RequiresApproval {
		return Decision{RequiresApproval: true, Reason: fmt.Sprintf("scope %s requires approval at trust %s", ctx.Scope, ctx.TrustLevel)}
	}
	// 4) 金额阈值 → 自动转审批
	if c.RequiresApprovalAboveCents > 0 && ctx.AmountCents > c.RequiresApprovalAboveCents {
		return Decision{RequiresApproval: true, Reason: fmt.Sprintf("amount %d exceeds threshold %d", ctx.AmountCents, c.RequiresApprovalAboveCents)}
	}
	// 5) 预算
	if c.BudgetCentsPerDay > 0 && ctx.AmountCents > c.BudgetCentsPerDay {
		return Decision{Reason: fmt.Sprintf("amount %d exceeds daily budget %d", ctx.AmountCents, c.BudgetCentsPerDay)}
	}
	return Decision{Allowed: true, Reason: "ok"}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
