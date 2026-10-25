// compliance-gate：合规出口护栏服务（§8.2 / §3.3）。
// 核心：所有 Agent 外发内容（含协商摘要）统一过审，结果与消息 ID 绑定写入审计。
// 规则实现见 internal/compliance（服务与编排器共用，避免逻辑分叉）。
// 运行：go run ./services/compliance-gate -selftest
//
//	go run ./services/compliance-gate -port 8086
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"human-agent-im/internal/api"
	"human-agent-im/internal/audit"
	"human-agent-im/internal/compliance"
	"human-agent-im/internal/config"
)

func main() {
	selftest := flag.Bool("selftest", false, "run self test and exit")
	cfg := config.Load("compliance-gate")

	if *selftest {
		sink := audit.NewMemorySink(4096)
		if err := selfTest(sink); err != nil {
			log.Fatalf("[compliance-gate] SELF-TEST FAILED: %v", err)
		}
		return
	}

	deps, err := api.NewDepsFromEnv()
	if err != nil {
		log.Fatalf("[compliance-gate] init deps: %v", err)
	}
	var handler http.Handler = deps.ComplianceMux(cfg.ServiceName)
	log.Printf("[compliance-gate] listening on %s (env=%s)", cfg.Addr(), cfg.Env)
	log.Fatal(http.ListenAndServe(cfg.Addr(), handler))
}

func selfTest(sink audit.Sink) error {
	g := compliance.NewGuard(sink)
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"正常协商摘要", "双方已就周三 15:00 线上沟通达成一致", true},
		{"违规关键词", "先汇款到指定账户即可解锁优惠", false},
		{"手机号泄露", "我的电话是 13812345678，直接联系", false},
		{"外部链接", "详情见 https://example.com/offer", false},
	}
	fail := 0
	for _, c := range cases {
		d := g.Check("msg-check-"+c.name, c.content)
		ok := d.Allowed == c.want
		if !ok {
			fail++
		}
		fmt.Printf("  [%s] %-16s allowed=%v want=%v reason=%s\n", status(ok), c.name, d.Allowed, c.want, d.Reason)
	}
	if fail > 0 {
		return fmt.Errorf("%d/%d cases failed", fail, len(cases))
	}
	mem, ok := sink.(*audit.MemorySink)
	if !ok {
		return fmt.Errorf("self test requires memory sink")
	}
	counts := mem.Count()
	fmt.Printf("[compliance-gate] SELF-TEST PASS: 4 cases, 审计事件: %d 条 (allow=%d reject=%d)\n",
		counts[audit.ActionContentAllow]+counts[audit.ActionContentReject],
		counts[audit.ActionContentAllow], counts[audit.ActionContentReject])
	return nil
}

func status(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
