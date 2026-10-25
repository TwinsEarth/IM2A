// SPDX-License-Identifier: MIT
//
// 验证「超长文本切分发送」这个替换点：未注入切分器时行为与历史一致（单条发送），
// 注入后按上限切成多条且拼接无损。切分器本身的能力由 internal/plugin/dsbot 的测试覆盖。

package openclaw

import (
	"strings"
	"testing"
)

// withSplitter 临时替换包级扩展点，测试结束恢复，避免污染其它用例。
func withSplitter(t *testing.T, fn func(string, int) []string, limit int) {
	t.Helper()
	oldFn, oldLimit := TextSplitter, OutboundCharLimit
	t.Cleanup(func() { TextSplitter, OutboundCharLimit = oldFn, oldLimit })
	TextSplitter, OutboundCharLimit = fn, limit
}

func TestSplitForSend_NoSplitterKeepsSingleMessage(t *testing.T) {
	withSplitter(t, nil, 10)
	got := SplitForSend("这是一段远超上限的长文本")
	if len(got) != 1 || got[0] != "这是一段远超上限的长文本" {
		t.Fatalf("未注入切分器时必须原样单条发送，got=%q", got)
	}
}

func TestSplitForSend_ZeroLimitKeepsSingleMessage(t *testing.T) {
	called := false
	withSplitter(t, func(s string, n int) []string { called = true; return nil }, 0)
	got := SplitForSend("abcdef")
	if called {
		t.Fatal("limit<=0 时不应调用切分器")
	}
	if len(got) != 1 || got[0] != "abcdef" {
		t.Fatalf("limit<=0 必须原样返回，got=%q", got)
	}
}

func TestSplitForSend_ShortTextNotSplit(t *testing.T) {
	withSplitter(t, func(s string, n int) []string { return []string{"SHOULD-NOT-BE-USED"} }, 10)
	got := SplitForSend("短")
	if len(got) != 1 || got[0] != "短" {
		t.Fatalf("未超限不应切分，got=%q", got)
	}
}

func TestSplitForSend_UsesInjectedSplitterLosslessly(t *testing.T) {
	// 按 rune 每 3 个切一刀，验证上限被传入且拼接无损。
	withSplitter(t, func(s string, n int) []string {
		if n != 3 {
			t.Fatalf("切分器收到的 limit=%d，期望 3", n)
		}
		r := []rune(s)
		out := make([]string, 0, (len(r)+n-1)/n)
		for i := 0; i < len(r); i += n {
			end := i + n
			if end > len(r) {
				end = len(r)
			}
			out = append(out, string(r[i:end]))
		}
		return out
	}, 3)
	const text = "abcdefghij" // 10 runes
	got := SplitForSend(text)
	if len(got) != 4 {
		t.Fatalf("期望切成 4 条，got=%d (%q)", len(got), got)
	}
	if joined := strings.Join(got, ""); joined != text {
		t.Fatalf("拼接必须等于原文：got=%q want=%q", joined, text)
	}
}

func TestSplitForSend_EmptySplitterResultFallsBack(t *testing.T) {
	// 切分器返回空切片时必须回退到原文单条，绝不静默丢消息。
	withSplitter(t, func(s string, n int) []string { return nil }, 2)
	got := SplitForSend("abcd")
	if len(got) != 1 || got[0] != "abcd" {
		t.Fatalf("切分器返回空时应回退原文，got=%q", got)
	}
}
