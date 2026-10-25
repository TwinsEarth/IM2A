// payment-svc：陌生人付费消息账本服务（§1.3 / §6 / §10-R2）。
// 核心：不可变账本 + message_id 幂等扣费（重试不二次扣费）+ 单日发送上限。
// 运行：go run ./services/payment-svc -selftest
//
//	go run ./services/payment-svc -port 8085
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"

	"human-agent-im/internal/api"
	"human-agent-im/internal/config"
	"human-agent-im/internal/ledger"
)

func main() {
	selftest := flag.Bool("selftest", false, "run self test and exit")
	cfg := config.Load("payment-svc")

	if *selftest {
		if err := selfTest(); err != nil {
			log.Fatalf("[payment-svc] SELF-TEST FAILED: %v", err)
		}
		return
	}

	deps, err := api.NewDepsFromEnv()
	if err != nil {
		log.Fatalf("[payment-svc] init deps: %v", err)
	}
	var handler http.Handler = deps.PaymentMux(cfg.ServiceName)
	log.Printf("[payment-svc] listening on %s (env=%s)", cfg.Addr(), cfg.Env)
	log.Fatal(http.ListenAndServe(cfg.Addr(), handler))
}

func selfTest() error {
	l := ledger.New(10)

	// 场景 1：发送即扣费 + 幂等（重复扣费回调不二次扣费）
	_, err := l.Reserve("msg-0001", 100, 200, ledger.ModeSendCharge, 100) // ¥1
	if err != nil {
		return err
	}
	e, err := l.Charge("msg-0001", "")
	if err != nil {
		return err
	}
	if e.Status != ledger.StatusCharged {
		return fmt.Errorf("status=%s want CHARGED", e.Status)
	}
	// 支付回调重试：必须返回 ErrAlreadyProcessed，且不产生二次扣费
	if _, err = l.Charge("msg-0001", ""); !errors.Is(err, ledger.ErrAlreadyProcessed) {
		return fmt.Errorf("duplicate charge err=%v want ErrAlreadyProcessed", err)
	}
	fmt.Printf("  [PASS] 发送即扣费 ¥1 → CHARGED；重复回调返回 ErrAlreadyProcessed（不二次扣费）\n")

	// 场景 2：回复才扣费（用户"查看并回复"触发，关联审批卡片）
	if _, err = l.Reserve("msg-0002", 300, 100, ledger.ModeReplyCharge, 5000); err != nil {
		return err
	}
	e2, err := l.Charge("msg-0002", "t000099")
	if err != nil {
		return err
	}
	if e2.ApprovalCardID != "t000099" {
		return fmt.Errorf("approval card not linked")
	}
	fmt.Printf("  [PASS] 回复才扣费 ¥50 → CHARGED, 审批卡片=%s\n", e2.ApprovalCardID)

	// 场景 3：退款（争议/违规）
	e3, err := l.Refund("msg-0002", "用户争议")
	if err != nil {
		return err
	}
	if e3.Status != ledger.StatusRefunded {
		return fmt.Errorf("status=%s want REFUNDED", e3.Status)
	}
	// 已退款条目重复扣费必须失败
	if _, err = l.Charge("msg-0002", ""); !errors.Is(err, ledger.ErrInvalidTransition) {
		return fmt.Errorf("charge refunded err=%v want ErrInvalidTransition", err)
	}
	fmt.Printf("  [PASS] 退款 → REFUNDED；已退款条目不可再扣费\n")

	// 场景 4：单日发送上限（防滥用风控）
	var lastErr error
	for i := 0; i < 12; i++ {
		mid := fmt.Sprintf("msg-daily-%02d", i+1)
		_, lastErr = l.Reserve(mid, 500, 600, ledger.ModeSendCharge, 50)
	}
	if !errors.Is(lastErr, ledger.ErrDailyLimitExceeded) {
		return fmt.Errorf("daily limit not enforced, lastErr=%v", lastErr)
	}
	sent, limit := l.DailyUsage(500)
	if sent != limit {
		return fmt.Errorf("daily sent=%d limit=%d", sent, limit)
	}
	fmt.Printf("  [PASS] 单日发送上限: %d/%d 次后拦截（%v）\n", sent, limit, ledger.ErrDailyLimitExceeded)

	fmt.Println("[payment-svc] SELF-TEST PASS: 幂等扣费/退款/日限 符合 §1.3 与 §10-R2")
	return nil
}
