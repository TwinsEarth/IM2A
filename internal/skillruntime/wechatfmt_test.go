package skillruntime

import (
	"errors"
	"strings"
	"testing"
)

func TestC1_OKTextPassthrough(t *testing.T) {
	r := CallResult{
		Status:  "OK",
		Content: []ContentBlock{{Type: "text", Text: "已帮你把咖啡机加进购物车。"}},
	}
	if got := FormatForWechat(r); !strings.Contains(got, "购物车") {
		t.Fatalf("got %q", got)
	}
}

func TestC1_DeniedShowsFriendly(t *testing.T) {
	r := CallResult{Status: "DENIED", Gate: &GateDecision{Reason: "trust level too low"}}
	got := FormatForWechat(r)
	if !strings.Contains(got, "确认") {
		t.Fatalf("denied should mention confirm, got %q", got)
	}
}

func TestC1_RequiresApprovalHint(t *testing.T) {
	r := CallResult{Status: "REQUIRES_APPROVAL"}
	got := FormatForWechat(r)
	if !strings.Contains(got, "1 同意") {
		t.Fatalf("approval hint missing, got %q", got)
	}
}

func TestC1_StripsInternalIDs(t *testing.T) {
	r := CallResult{
		Status: "OK",
		Structured: map[string]any{
			"wxid":      "wx_abc123",
			"ticket_id": "t000001",
			"amount":    1200,
			"title":     "咖啡机",
		},
	}
	got := FormatForWechat(r)
	if strings.Contains(got, "wx_abc") || strings.Contains(got, "t000001") {
		t.Fatalf("internal IDs leaked: %q", got)
	}
	if !strings.Contains(got, "事项") || !strings.Contains(got, "金额") {
		t.Fatalf("friendly fields missing: %q", got)
	}
}

func TestC1_TruncateLongText(t *testing.T) {
	long := strings.Repeat("很长的内容。", 100)
	r := CallResult{Status: "OK", Content: []ContentBlock{{Type: "text", Text: long}}}
	got := FormatForWechat(r)
	if n := len([]rune(got)); n > wechatMaxLen+2 {
		t.Fatalf("too long: %d runes", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated text should end with ellipsis: %q", got)
	}
}

func TestC1_ErrorMapping(t *testing.T) {
	if got := FormatErrorForWechat(errors.New("context deadline exceeded")); !strings.Contains(got, "慢") {
		t.Fatalf("timeout mapping: %q", got)
	}
	if got := FormatErrorForWechat(errors.New("permission denied")); !strings.Contains(got, "确认") {
		t.Fatalf("permission mapping: %q", got)
	}
	if got := FormatErrorForWechat(errors.New("unknown boom")); !strings.Contains(got, "暂时") {
		t.Fatalf("default mapping: %q", got)
	}
}
