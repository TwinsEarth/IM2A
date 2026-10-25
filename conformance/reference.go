package conformance

// reference.go：本仓库 `internal/trustzone` 的 reference adapter。
//
// 协议不包含"人读消息文本"，所以这里负责把 trustzone 的 Reason 文本映射为协议 reasonCode。
// 映射规则（与 spec/relation-gating.md §7 一致）：
//   - allowed                     → OK
//   - requiresApproval + "scope " → SCOPE_REQUIRES_APPROVAL
//   - requiresApproval + "amount "→ AMOUNT_ABOVE_THRESHOLD
//   - deny + "skill "             → SKILL_NOT_ALLOWED
//   - deny + "trust "             → TRUST_BELOW_MIN
//   - deny + "scope "             → SCOPE_FORBIDDEN
//   - deny + "amount "            → AMOUNT_ABOVE_BUDGET
// 若出现未知文本（说明 trustzone 的判定被改过但向量没更新），返回 reasonUnknownImpl，
// 让一致性测试显式失败，而不是静默通过。

import (
	"strings"

	"human-agent-im/internal/trustzone"
)

// Reference 是 trustzone 的 adapter（内置 `-impl reference`）。
type Reference struct{}

// Decide 实现 Implementer。
func (Reference) Decide(in Input) Outcome {
	capability := trustzone.Capability{
		AgentID:                    in.Capability.AgentID,
		TrustLevelMin:              trustzone.Level(in.Capability.TrustLevelMin),
		AllowedSkills:              in.Capability.AllowedSkills,
		BudgetCentsPerDay:          in.Capability.BudgetCentsPerDay,
		RequiresApprovalAboveCents: in.Capability.RequiresApprovalAboveCents,
	}
	ctx := trustzone.CallContext{
		SkillID:     in.Call.SkillID,
		Scope:       trustzone.Scope(in.Call.Scope),
		TrustLevel:  trustzone.Level(in.Call.TrustLevel),
		AmountCents: in.Call.AmountCents,
		TargetUID:   in.Call.TargetUID,
	}
	return outcomeFromDecision(capability.Check(ctx))
}

// outcomeFromDecision 把 trustzone 的三态+文本映射成协议 outcome。
func outcomeFromDecision(d trustzone.Decision) Outcome {
	if d.Allowed {
		return Outcome{Allowed: true, RequiresApproval: false, ReasonCode: ReasonOK}
	}
	if d.RequiresApproval {
		switch {
		case strings.HasPrefix(d.Reason, "scope "):
			return Outcome{RequiresApproval: true, ReasonCode: ReasonScopeRequiresApproval}
		case strings.HasPrefix(d.Reason, "amount "):
			return Outcome{RequiresApproval: true, ReasonCode: ReasonAmountAboveThreshold}
		default:
			return Outcome{RequiresApproval: true, ReasonCode: reasonUnknownImpl}
		}
	}
	switch {
	case strings.HasPrefix(d.Reason, "skill "):
		return Outcome{ReasonCode: ReasonSkillNotAllowed}
	case strings.HasPrefix(d.Reason, "trust "):
		return Outcome{ReasonCode: ReasonTrustBelowMin}
	case strings.HasPrefix(d.Reason, "scope "):
		return Outcome{ReasonCode: ReasonScopeForbidden}
	case strings.HasPrefix(d.Reason, "amount "):
		return Outcome{ReasonCode: ReasonAmountAboveBudget}
	default:
		return Outcome{ReasonCode: reasonUnknownImpl}
	}
}
