package trustzone

import "testing"

func TestCapabilityCheck(t *testing.T) {
	family := Capability{
		AgentID:                    "family-agent",
		TrustLevelMin:              LevelFamily,
		AllowedSkills:              []string{"home-maintenance", "family-purchase"},
		RequiresApprovalAboveCents: 200000, // ¥2000
		BudgetCentsPerDay:          1000000,
	}
	cases := []struct {
		name string
		ctx  CallContext
		want string // ALLOW / REQUIRE_APPROVAL / DENY
	}{
		{"家庭物业报修自主", CallContext{SkillID: "home-maintenance", Scope: ScopeFamily, TrustLevel: LevelFamily}, "ALLOW"},
		{"白名单外 Skill 拒绝", CallContext{SkillID: "other", Scope: ScopeFamily, TrustLevel: LevelFamily}, "DENY"},
		{"信任档位低于下限拒绝", CallContext{SkillID: "home-maintenance", Scope: ScopeFamily, TrustLevel: LevelKnown}, "DENY"},
		{"采购超阈值转审批", CallContext{SkillID: "family-purchase", Scope: ScopePurchase, TrustLevel: LevelFamily, AmountCents: 500000}, "REQUIRE_APPROVAL"},
		{"采购阈值内自主", CallContext{SkillID: "family-purchase", Scope: ScopePurchase, TrustLevel: LevelFamily, AmountCents: 10000}, "ALLOW"},
		{"家庭档位工作邮件被矩阵禁止", CallContext{SkillID: "home-maintenance", Scope: ScopeWorkMail, TrustLevel: LevelFamily}, "DENY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := family.Check(c.ctx).String()
			if got != c.want {
				t.Fatalf("got %s, want %s (reason=%s)", got, c.want, family.Check(c.ctx).Reason)
			}
		})
	}
}

// TestBudgetDeny 金额阈值关闭（0）时，超日预算直接拒绝。
func TestBudgetDeny(t *testing.T) {
	cap := Capability{
		AgentID:                    "agent",
		TrustLevelMin:              LevelFamily,
		AllowedSkills:              []string{"family-purchase"},
		RequiresApprovalAboveCents: 0, // 不启用金额阈值
		BudgetCentsPerDay:          100000,
	}
	d := cap.Check(CallContext{SkillID: "family-purchase", Scope: ScopePurchase,
		TrustLevel: LevelFamily, AmountCents: 200000})
	if d.String() != "DENY" {
		t.Fatalf("got %s (%s), want DENY", d.String(), d.Reason)
	}
	// 预算内放行
	ok := cap.Check(CallContext{SkillID: "family-purchase", Scope: ScopePurchase,
		TrustLevel: LevelFamily, AmountCents: 50000})
	if ok.String() != "ALLOW" {
		t.Fatalf("within budget got %s (%s), want ALLOW", ok.String(), ok.Reason)
	}
}

func TestSelfLevelAllowsEverything(t *testing.T) {
	master := Capability{AgentID: "master", TrustLevelMin: LevelSelf}
	for _, scope := range []Scope{ScopeSchedule, ScopeFamily, ScopePurchase, ScopeWorkMail, ScopeStranger, ScopeFinance, ScopeExternal} {
		d := master.Check(CallContext{SkillID: "anything", Scope: scope, TrustLevel: LevelSelf, AmountCents: 1 << 40})
		if d.String() != "ALLOW" {
			t.Fatalf("self scope %s: %s (%s)", scope, d.String(), d.Reason)
		}
	}
}

func TestParseLevel(t *testing.T) {
	if l, err := ParseLevel("family"); err != nil || l != LevelFamily {
		t.Fatalf("parse family: %v %v", l, err)
	}
	if _, err := ParseLevel("nope"); err == nil {
		t.Fatal("unknown level should error")
	}
}
