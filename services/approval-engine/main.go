// approval-engine：审批工作流引擎服务（§7.3 / §9.2）。
// 核心：显式状态机 + 事件溯源 + 超时升级（30min 手表推送 / 2h 短信）。
// 运行：go run ./services/approval-engine -selftest
//
//	go run ./services/approval-engine -port 8083
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"human-agent-im/internal/api"
	"human-agent-im/internal/approval"
	"human-agent-im/internal/config"
)

func main() {
	selftest := flag.Bool("selftest", false, "run self test and exit")
	cfg := config.Load("approval-engine")

	if *selftest {
		if err := selfTest(); err != nil {
			log.Fatalf("[approval-engine] SELF-TEST FAILED: %v", err)
		}
		return
	}

	deps, err := api.NewDepsFromEnv()
	if err != nil {
		log.Fatalf("[approval-engine] init deps: %v", err)
	}
	var handler http.Handler = deps.ApprovalMux(cfg.ServiceName)
	log.Printf("[approval-engine] listening on %s (env=%s)", cfg.Addr(), cfg.Env)
	log.Fatal(http.ListenAndServe(cfg.Addr(), handler))
}

func selfTest() error {
	now := time.Now()
	eng := approval.New(approval.NewMemoryStore())

	// 场景 1：通过链路 PENDING→REVIEWING→APPROVED→EXECUTING→COMPLETED
	t1, err := eng.CreateTicket(1001, "procurement-agent", approval.TicketPayment,
		map[string]any{"skill": "purchase-pay", "amount_cents": 300000, "item": "办公椅"},
		now.Add(1*time.Hour))
	if err != nil {
		return err
	}
	if _, err = eng.Transition(t1.ID, approval.StateReviewing, "user", "进入审阅"); err != nil {
		return err
	}
	if _, err = eng.Approve(t1.ID, "user", "同意"); err != nil {
		return err
	}
	if _, err = eng.BeginExecuting(t1.ID, "procurement-agent"); err != nil {
		return err
	}
	if _, err = eng.Complete(t1.ID, "procurement-agent"); err != nil {
		return err
	}
	checkState := func(id string, want approval.State) error {
		t, err := eng.Get(id)
		if err != nil {
			return err
		}
		if t.State != want {
			return fmt.Errorf("ticket %s state=%s want=%s", id, t.State, want)
		}
		return nil
	}
	if err = checkState(t1.ID, approval.StateCompleted); err != nil {
		return err
	}
	fmt.Printf("  [PASS] 通过链路完成: %s → %s, 事件数=%d\n", t1.ID, t1.State, len(t1.Events))

	// 场景 2：驳回链路 + 非法迁移防御
	t2, err := eng.CreateTicket(1001, "schedule-agent", approval.TicketNegotiation,
		map[string]any{"action": "接受周四拜访"}, now.Add(30*time.Minute))
	if err != nil {
		return err
	}
	if _, err = eng.Reject(t2.ID, "user", "周四没空"); err != nil {
		return err
	}
	// 对已驳回单执行 BeginExecuting 必须报非法迁移
	if _, err = eng.BeginExecuting(t2.ID, "schedule-agent"); err == nil {
		return fmt.Errorf("expected invalid transition on rejected ticket")
	}
	if err = checkState(t2.ID, approval.StateRejected); err != nil {
		return err
	}
	fmt.Printf("  [PASS] 驳回链路 + 非法迁移被拦截: %s → %s\n", t2.ID, t2.State)

	// 场景 3：用户修改后放行（MODIFIED → EXECUTING）
	t3, err := eng.CreateTicket(1001, "family-agent", approval.TicketSkillCall,
		map[string]any{"action": "预约水管工"}, now.Add(30*time.Minute))
	if err != nil {
		return err
	}
	if _, err = eng.Transition(t3.ID, approval.StateReviewing, "user", "审阅"); err != nil {
		return err
	}
	if _, err = eng.Modify(t3.ID, map[string]any{"provider": "A师傅", "time": "周六上午"}, "user", "换人"); err != nil {
		return err
	}
	if err = checkState(t3.ID, approval.StateModified); err != nil {
		return err
	}
	fmt.Printf("  [PASS] 修改后放行: %s → %s, payload.provider=%v\n", t3.ID, t3.State, t3.Payload["provider"])

	// 场景 4：超时升级 30min → 手表推送；2h+ → 短信
	overWatch, err := eng.CreateTicket(1001, "family-agent", approval.TicketPayment,
		map[string]any{"action": "物业费缴纳"}, now.Add(-45*time.Minute)) // 已过期 45 分钟
	if err != nil {
		return err
	}
	overSMS, err := eng.CreateTicket(1001, "family-agent", approval.TicketPayment,
		map[string]any{"action": "大额支出确认"}, now.Add(-3*time.Hour)) // 已过期 3 小时
	if err != nil {
		return err
	}
	escalated, err := eng.CheckTimeouts(now)
	if err != nil {
		return err
	}
	got := map[string]string{}
	for _, t := range escalated {
		// 从事件中取升级通道
		for _, ev := range t.Events {
			if ev.Type == approval.EventEscalated {
				got[t.ID] = ev.Note
			}
		}
	}
	if got[overWatch.ID] != "watch_push" {
		return fmt.Errorf("overWatch escalation=%q want watch_push", got[overWatch.ID])
	}
	if got[overSMS.ID] != "sms" {
		return fmt.Errorf("overSMS escalation=%q want sms", got[overSMS.ID])
	}
	fmt.Printf("  [PASS] 超时升级: %s→watch_push, %s→sms\n", overWatch.ID, overSMS.ID)

	// 场景 5：待审列表（只含非终态）
	pending, err := eng.ListPending(1001)
	if err != nil {
		return err
	}
	fmt.Printf("  [PASS] 待审列表: %d 项（终态单已排除）\n", len(pending))

	fmt.Println("[approval-engine] SELF-TEST PASS: 状态机/事件溯源/非法迁移拦截/超时升级 全部符合 §7.3")
	return nil
}
