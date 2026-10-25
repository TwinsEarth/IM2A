// negotiation-svc：Agent 间协商服务（§2.4）。
// 核心：8 态 Task 状态机；轮次/token 硬性熔断；AUTH_REQUIRED 绑定审批单。
// 运行：go run ./services/negotiation-svc -selftest
//
//	go run ./services/negotiation-svc -port 8084
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"human-agent-im/internal/api"
	"human-agent-im/internal/config"
	"human-agent-im/internal/negotiation"
)

func main() {
	selftest := flag.Bool("selftest", false, "run self test and exit")
	cfg := config.Load("negotiation-svc")

	if *selftest {
		if err := selfTest(); err != nil {
			log.Fatalf("[negotiation-svc] SELF-TEST FAILED: %v", err)
		}
		return
	}

	deps, err := api.NewDepsFromEnv()
	if err != nil {
		log.Fatalf("[negotiation-svc] init deps: %v", err)
	}
	var handler http.Handler = deps.NegotiationMux(cfg.ServiceName)
	log.Printf("[negotiation-svc] listening on %s (env=%s)", cfg.Addr(), cfg.Env)
	log.Fatal(http.ListenAndServe(cfg.Addr(), handler))
}

func turn(session, from, to string, intent negotiation.Intent, payload map[string]any) negotiation.Turn {
	return negotiation.Turn{SessionID: session, FromAgentID: from, ToAgentID: to, Intent: intent, Payload: payload}
}

func selfTest() error {
	// 场景 1：来意协商 3 轮达成一致 → COMPLETED
	s1 := negotiation.NewSession("sess-intent", 5, 0)
	turns1 := []negotiation.Turn{
		turn("sess-intent", "stranger-agent", "master-agent", negotiation.IntentPropose,
			map[string]any{"who": "某猎头公司", "why": "商务合作", "expect": "约 20 分钟沟通", "urgency": "low"}),
		turn("sess-intent", "master-agent", "stranger-agent", negotiation.IntentCounter,
			map[string]any{"offer": "周三 15:00 线上沟通", "need": "先发公司介绍"}),
		turn("sess-intent", "stranger-agent", "master-agent", negotiation.IntentAccept,
			map[string]any{"agree": "周三 15:00", "attached": "company-intro.pdf"}),
	}
	for _, t := range turns1 {
		if _, err := s1.Apply(t); err != nil {
			return err
		}
	}
	if s1.State != negotiation.StateCompleted {
		return fmt.Errorf("s1 state=%s want COMPLETED", s1.State)
	}
	fmt.Printf("  [PASS] 来意协商 3 轮 → COMPLETED, 摘要=%s\n", s1.SummaryJSON())

	// 场景 2：无限反提案 → 轮次熔断 INPUT_REQUIRED（§10-R6）
	s2 := negotiation.NewSession("sess-loop", 5, 0)
	for i := 0; i < 6; i++ {
		st, err := s2.Apply(turn("sess-loop", "agent-a", "agent-b", negotiation.IntentCounter,
			map[string]any{"round": i + 1}))
		if err == negotiation.ErrRoundLimit {
			break
		}
		if err != nil {
			return err
		}
		if st == negotiation.StateInputRequired {
			break
		}
	}
	if s2.State != negotiation.StateInputRequired {
		return fmt.Errorf("s2 state=%s want INPUT_REQUIRED（熔断）", s2.State)
	}
	if s2.Summary["reason"] != "round_limit" {
		return fmt.Errorf("s2 summary=%v want round_limit", s2.Summary)
	}
	fmt.Printf("  [PASS] 6 轮反提案触发熔断 → INPUT_REQUIRED（round=%d/max=%d），上抛用户\n", s2.Round, s2.MaxRounds)

	// 场景 3：需要用户审批 → AUTH_REQUIRED + 绑定审批单（人机混合审批的协议级落点）
	s3 := negotiation.NewSession("sess-auth", 5, 0)
	if _, err := s3.Apply(turn("sess-auth", "stranger-agent", "master-agent",
		negotiation.IntentRequestAuth, map[string]any{"action": "发送付费留言"})); err != nil {
		return err
	}
	if s3.State != negotiation.StateAuthRequired {
		return fmt.Errorf("s3 state=%s want AUTH_REQUIRED", s3.State)
	}
	s3.BindApprovalTicket("t000042")
	if s3.ApprovalTicketID != "t000042" {
		return fmt.Errorf("approval ticket not bound")
	}
	fmt.Printf("  [PASS] 协商挂起 → AUTH_REQUIRED, 审批单=%s（注入 IM 审批卡片）\n", s3.ApprovalTicketID)

	// 场景 4：拒绝 → REJECTED
	s4 := negotiation.NewSession("sess-reject", 5, 0)
	if _, err := s4.Apply(turn("sess-reject", "master-agent", "stranger-agent",
		negotiation.IntentReject, map[string]any{"reason": "非工作相关"})); err != nil {
		return err
	}
	if s4.State != negotiation.StateRejected {
		return fmt.Errorf("s4 state=%s want REJECTED", s4.State)
	}
	fmt.Printf("  [PASS] 预审拒绝 → REJECTED（付费发生在 Agent 预审之后）\n")

	fmt.Println("[negotiation-svc] SELF-TEST PASS: 8 态状态机/轮次熔断/审批挂起 符合 §2.4")
	return nil
}
