// relation-svc：关系链与五档信任矩阵服务（§1.4 / §3.3）。
// 核心：权限矩阵 + Capability 门控（代码层强制，模型无法绕过）。
// 运行：go run ./services/relation-svc -selftest
//
//	go run ./services/relation-svc -port 8082
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"human-agent-im/internal/api"
	"human-agent-im/internal/config"
	"human-agent-im/internal/trustzone"
)

func main() {
	selftest := flag.Bool("selftest", false, "run self test and exit")
	cfg := config.Load("relation-svc")

	if *selftest {
		if err := selfTest(); err != nil {
			log.Fatalf("[relation-svc] SELF-TEST FAILED: %v", err)
		}
		return
	}

	deps, err := api.NewDepsFromEnv()
	if err != nil {
		log.Fatalf("[relation-svc] init deps: %v", err)
	}
	var handler http.Handler = deps.RelationMux(cfg.ServiceName)
	log.Printf("[relation-svc] listening on %s (env=%s)", cfg.Addr(), cfg.Env)
	log.Fatal(http.ListenAndServe(cfg.Addr(), handler))
}

// familyCapability 家庭子 Agent 能力声明（对应 §6 agent_capabilities）。
func familyCapability() trustzone.Capability {
	return trustzone.Capability{
		AgentID:                    "family-agent",
		TrustLevelMin:              trustzone.LevelFamily,
		AllowedSkills:              []string{"home-maintenance", "schedule", "family-purchase", "stranger-intent"},
		RequiresApprovalAboveCents: 200000, // ¥2000 以上自动转审批
		BudgetCentsPerDay:          1000000,
	}
}

func selfTest() error {
	cap := familyCapability()
	type tc struct {
		name string
		ctx  trustzone.CallContext
		want string // ALLOW / REQUIRE_APPROVAL / DENY
	}
	cases := []tc{
		{
			name: "家庭档位·物业报修 → 自主执行",
			ctx:  trustzone.CallContext{SkillID: "home-maintenance", Scope: trustzone.ScopeFamily, TrustLevel: trustzone.LevelFamily},
			want: "ALLOW",
		},
		{
			name: "家庭档位·日程协调 → 自主执行",
			ctx:  trustzone.CallContext{SkillID: "schedule", Scope: trustzone.ScopeSchedule, TrustLevel: trustzone.LevelFamily},
			want: "ALLOW",
		},
		{
			name: "家庭档位·大额采购 ¥5000 → 转审批（金额阈值）",
			ctx:  trustzone.CallContext{SkillID: "family-purchase", Scope: trustzone.ScopePurchase, TrustLevel: trustzone.LevelFamily, AmountCents: 500000},
			want: "REQUIRE_APPROVAL",
		},
		{
			name: "家庭档位·访问工作邮件 → 禁止",
			ctx:  trustzone.CallContext{SkillID: "workmail-read", Scope: trustzone.ScopeWorkMail, TrustLevel: trustzone.LevelFamily},
			want: "DENY",
		},
		{
			name: "陌生档位·仅结构化来意 → 需付费+审批",
			ctx:  trustzone.CallContext{SkillID: "stranger-intent", Scope: trustzone.ScopeStranger, TrustLevel: trustzone.LevelStranger},
			want: "REQUIRE_APPROVAL",
		},
		{
			name: "陌生档位·尝试自主执行家庭事务 → 禁止",
			ctx:  trustzone.CallContext{SkillID: "family-purchase", Scope: trustzone.ScopePurchase, TrustLevel: trustzone.LevelStranger},
			want: "DENY",
		},
		{
			name: "技能不在白名单 → 拒绝",
			ctx:  trustzone.CallContext{SkillID: "unknown-skill", Scope: trustzone.ScopeFamily, TrustLevel: trustzone.LevelFamily},
			want: "DENY",
		},
	}

	fail := 0
	for _, c := range cases {
		d := cap.Check(c.ctx)
		got := d.String()
		ok := got == c.want
		if !ok {
			fail++
		}
		fmt.Printf("  [%s] %-48s got=%s want=%s reason=%s\n", status(ok), c.name, got, c.want, d.Reason)
	}
	if fail > 0 {
		return fmt.Errorf("%d/%d cases failed", fail, len(cases))
	}
	fmt.Printf("[relation-svc] SELF-TEST PASS: %d cases, 五档信任矩阵门控符合 §1.4\n", len(cases))
	return nil
}

func status(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
