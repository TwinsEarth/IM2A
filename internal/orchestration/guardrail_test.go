package orchestration

import (
	"strings"
	"testing"
)

func TestC4_CleanTextPasses(t *testing.T) {
	g := NewExitGuard()
	got := g.AuditMessage("已帮你把咖啡机加进购物车。")
	if !got.OK || got.Rewrite != "已帮你把咖啡机加进购物车。" {
		t.Fatalf("clean text should pass through: %+v", got)
	}
}

func TestC4_EmptyReplaced(t *testing.T) {
	g := NewExitGuard()
	got := g.AuditMessage("   ")
	if got.OK {
		t.Fatalf("empty should not be OK: %+v", got)
	}
	if !strings.Contains(got.Rewrite, "已处理") {
		t.Fatalf("empty rewrite bad: %+v", got)
	}
}

func TestC4_StripsInternalID(t *testing.T) {
	g := NewExitGuard()
	got := g.AuditMessage("已下单 wxid_abc123 注意查收")
	if got.OK {
		t.Fatalf("wxid must be flagged: %+v", got)
	}
	if strings.Contains(got.Rewrite, "wxid") {
		t.Fatalf("rewrite must not leak wxid: %+v", got)
	}
	if got.HitRule == "" {
		t.Fatalf("HitRule must be recorded for audit")
	}
}

func TestC4_StripsURL(t *testing.T) {
	g := NewExitGuard()
	got := g.AuditMessage("点这里 https://evil.example.com/x")
	if got.OK {
		t.Fatalf("url must be flagged: %+v", got)
	}
	if strings.Contains(got.Rewrite, "http") {
		t.Fatalf("rewrite must not leak url: %+v", got)
	}
}

func TestC4_TruncatesLongText(t *testing.T) {
	g := NewExitGuard()
	long := strings.Repeat("很长的内容。", 200) // 1200 字
	got := g.AuditMessage(long)
	if got.OK {
		t.Fatalf("long text should be rewritten")
	}
	if n := len([]rune(got.Rewrite)); n > 505 {
		t.Fatalf("rewrite too long: %d", n)
	}
	if !strings.HasSuffix(got.Rewrite, "…") {
		t.Fatalf("truncated should end with ellipsis: %q", got.Rewrite)
	}
}

func TestC4_ForbiddenExtendable(t *testing.T) {
	g := NewExitGuard()
	g.SetForbidden(append(defaultForbidden, "违禁词"))
	got := g.AuditMessage("这是违禁词测试")
	if got.OK {
		t.Fatalf("extended forbidden should hit: %+v", got)
	}
}
