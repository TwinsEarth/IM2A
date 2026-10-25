package compliance

import (
	"strings"
	"testing"

	"human-agent-im/internal/audit"
)

func TestGuard(t *testing.T) {
	g := NewGuard(audit.NewMemorySink(64))
	cases := []struct {
		name    string
		content string
		allowed bool
	}{
		{"正常商务", "希望就周三下午的拜访时间进行确认", true},
		{"诈骗关键词", "兼职刷单，日结三百，立即报名", false},
		{"手机号泄露", "联系我 13812345678 详谈", false},
		{"外部链接", "详情见 http://example.com/promo", false},
		{"汇款指令", "请先汇款到安全账户验资", false},
		{"中奖", "恭喜您中奖了，点击领取奖金", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := g.Check("m-"+c.name, c.content)
			if d.Allowed != c.allowed {
				t.Fatalf("content=%q allowed=%v want %v (reason=%s)",
					c.content, d.Allowed, c.allowed, d.Reason)
			}
		})
	}
}

// TestGuardNilSink 不传 sink 时不应 panic。
func TestGuardNilSink(t *testing.T) {
	g := NewGuard(nil)
	d := g.Check("m1", "正常内容")
	if !d.Allowed {
		t.Fatalf("nil sink check: %v", d)
	}
}

// TestGuardReasons 拒绝原因可读。
func TestGuardReasons(t *testing.T) {
	g := NewGuard(audit.NewMemorySink(64))
	d := g.Check("m", "涉及赌博网站")
	if d.Allowed || !strings.Contains(d.Reason, "keyword") {
		t.Fatalf("reason=%q", d.Reason)
	}
}
