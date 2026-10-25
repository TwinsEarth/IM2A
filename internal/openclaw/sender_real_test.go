package openclaw

import (
	"context"
	"os"
	"strings"
	"testing"
)

// 真实 exec openclaw CLI（dry-run）：验证二进制可调用、参数组装、JSON 输出解析。
// 仅当 OPENCLAW_BIN 真实存在时运行；无 openclaw 的环境（CI）自动 skip。
// 设 OC_SEND_TEST_ACCOUNT / OC_SEND_TEST_TARGET 可指定真实账号目标（dry-run 仍不发微信）。
func TestSenderDryRunRealCLI(t *testing.T) {
	bin := os.Getenv("OPENCLAW_BIN")
	if bin == "" {
		bin = "/opt/node-v22.23.4-linux-x64/bin/openclaw"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("openclaw binary not present, skip real CLI test")
	}
	account := os.Getenv("OC_SEND_TEST_ACCOUNT")
	target := os.Getenv("OC_SEND_TEST_TARGET")
	if account == "" || target == "" {
		t.Skip("set OC_SEND_TEST_ACCOUNT / OC_SEND_TEST_TARGET to run real CLI dry-run test")
	}
	s := NewSender()
	s.dryRun = true
	mid, err := s.Send(context.Background(), account, target, "IM2A sender dry-run 自检（不会发出）")
	if err != nil {
		t.Fatalf("dry-run send: %v", err)
	}
	if !strings.HasPrefix(mid, "dry-run:") {
		t.Fatalf("want dry-run marker, got %q", mid)
	}
}
