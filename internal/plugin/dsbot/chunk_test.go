// SPDX-License-Identifier: Apache-2.0
//
// 移植自 Fei2411/DeepSeek-Bot ds-bot/src/host/text.js 的 headOf 边界语义 (Apache-2.0)，
// 已修改为 Go 测试：覆盖中文/emoji/URL/超长词/空串/limit<=0 与切片不丢数据性质。

package dsbotplugin

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// assertSplitJoin 断言切分结果拼接后与原文逐字节相同。
func assertSplitJoin(t *testing.T, text string, chunks []string) {
	t.Helper()
	if got := strings.Join(chunks, ""); got != text {
		t.Fatalf("join(chunks) != original\n got=%q\nwant=%q", got, text)
	}
}

// assertChunkLimits 断言每块非空且码点数不超过 limit。
func assertChunkLimits(t *testing.T, chunks []string, limit int) {
	t.Helper()
	for i, chunk := range chunks {
		if chunk == "" {
			t.Fatalf("chunk %d is empty", i)
		}
		if n := utf8.RuneCountInString(chunk); n > limit {
			t.Fatalf("chunk %d has %d runes > limit %d: %q", i, n, limit, chunk)
		}
	}
}

// assertGraphemeSafe 从原文角度交叉验证：没有任何 grapheme 簇被切点拆开。
func assertGraphemeSafe(t *testing.T, text string, chunks []string) {
	t.Helper()
	runes := []rune(text)
	offset := 0
	for i, chunk := range chunks {
		cr := []rune(chunk)
		cut := offset + len(cr)
		if len(cr) == 0 {
			t.Fatalf("chunk %d is empty", i)
		}
		if isGraphemeExtend(cr[0]) {
			t.Fatalf("chunk %d starts with a grapheme extender: %q", i, chunk)
		}
		if cr[len(cr)-1] == zeroWidthJoiner {
			t.Fatalf("chunk %d ends with ZWJ: %q", i, chunk)
		}
		if cut < len(runes) && isRegionalIndicator(runes[cut]) {
			count := 0
			for k := cut - 1; k >= 0 && isRegionalIndicator(runes[k]); k-- {
				count++
			}
			if count%2 == 1 {
				t.Fatalf("chunk %d splits a flag (regional indicator pair)", i)
			}
		}
		offset = cut
	}
}

func TestSplitEmptyAndShort(t *testing.T) {
	if got := Split("", 10); got != nil {
		t.Fatalf("Split(empty) = %v, want nil", got)
	}
	for _, limit := range []int{10, 5, 2} {
		if got := Split("hi", limit); len(got) != 1 || got[0] != "hi" {
			t.Fatalf("Split(short, %d) = %v", limit, got)
		}
	}
	// limit<=0 视为"不切"：不丢内容，返回单元素。
	for _, limit := range []int{0, -1, -100} {
		if got := Split("hello world", limit); len(got) != 1 || got[0] != "hello world" {
			t.Fatalf("Split(text, %d) = %v, want one whole chunk", limit, got)
		}
	}
}

func TestSplitWhitespaceOnly(t *testing.T) {
	text := strings.Repeat(" ", 7)
	chunks := Split(text, 3)
	assertSplitJoin(t, text, chunks)
	if len(chunks) < 2 {
		t.Fatalf("whitespace text should still be split, got %v", chunks)
	}
}

func TestSplitChineseSentences(t *testing.T) {
	text := "第一句话。第二句话！第三句话？第四句话；最后一句没有标点"
	chunks := Split(text, 10)
	assertSplitJoin(t, text, chunks)
	assertChunkLimits(t, chunks, 10)
	if len(chunks) < 3 {
		t.Fatalf("expected several chunks, got %d: %v", len(chunks), chunks)
	}
	// 中文应尽量切在句末标点之后。
	boundary := 0
	for _, c := range chunks {
		boundary += len([]rune(c))
		if boundary < len([]rune(text)) {
			last := []rune(c)[len([]rune(c))-1]
			if !stringsContainsRune("。！？；", last) && last != '\n' && last != ' ' {
				// 允许找不到边界时硬切，但至少第一块应落在标点上。
				if boundary < 6 {
					t.Fatalf("chunk %q does not end on a natural Chinese boundary", c)
				}
			}
		}
	}
}

func TestSplitEnglishWords(t *testing.T) {
	text := strings.Repeat("word ", 20)
	chunks := Split(text, 20)
	assertSplitJoin(t, text, chunks)
	assertChunkLimits(t, chunks, 20)
	for i, chunk := range chunks {
		if i == len(chunks)-1 {
			continue
		}
		if !strings.HasSuffix(chunk, " ") {
			t.Fatalf("chunk %d = %q should end on a word boundary", i, chunk)
		}
	}
}

func TestSplitLongWord(t *testing.T) {
	text := strings.Repeat("a", 100)
	chunks := Split(text, 10)
	assertSplitJoin(t, text, chunks)
	assertChunkLimits(t, chunks, 10)
	if len(chunks) != 10 {
		t.Fatalf("long word chunks = %d, want 10", len(chunks))
	}
}

func TestSplitEmojiGraphemeSafety(t *testing.T) {
	family := "👨‍👩‍👧‍👦"             // ZWJ 序列，7 个码点
	flag := "🇨🇳"                      // 成对 regional indicator，2 个码点
	skin := "👍🏽"                      // 肤色修饰符，2 个码点
	thumb := family + "和" + flag + "以及" + skin + "谢谢"
	for _, limit := range []int{14, 10, 8, 7} {
		chunks := Split(thumb, limit)
		assertSplitJoin(t, thumb, chunks)
		assertChunkLimits(t, chunks, limit)
		assertGraphemeSafe(t, thumb, chunks)
	}
}

func TestSplitURLAvoided(t *testing.T) {
	url := "https://example.com/very/long/path?q=abc&x=1"
	text := "请查看 " + url + " 然后回复我，谢谢"
	chunks := Split(text, 60)
	assertSplitJoin(t, text, chunks)
	assertChunkLimits(t, chunks, 60)
	foundWhole := 0
	for _, chunk := range chunks {
		if strings.Contains(chunk, url) {
			foundWhole++
		}
	}
	if foundWhole != 1 {
		t.Fatalf("URL must stay in exactly one chunk, found %d: %v", foundWhole, chunks)
	}
}

func TestSplitURLLongerThanLimit(t *testing.T) {
	// URL 本身比 limit 长时无法完整保留：必须仍保证不丢数据、不超限。
	url := "https://example.com/" + strings.Repeat("segment/", 12)
	text := "前缀 " + url + " 后缀"
	chunks := Split(text, 30)
	assertSplitJoin(t, text, chunks)
	assertChunkLimits(t, chunks, 30)
}

func TestSplitMixedProperty(t *testing.T) {
	texts := []string{
		"Hello 世界！This is a mixed text with emoji 😀 and URLs https://a.example/x?y=z, plus 中文标点。还有\u00a0不换行空格。",
		"line1\nline2\n\npara2\n" + strings.Repeat("word ", 30),
		strings.Repeat("。", 20) + strings.Repeat("a", 20),
	}
	for _, text := range texts {
		for _, limit := range []int{3, 7, 19, 50, 120} {
			chunks := Split(text, limit)
			assertSplitJoin(t, text, chunks)
			assertChunkLimits(t, chunks, limit)
			assertGraphemeSafe(t, text, chunks)
		}
	}
}

func TestPortedThresholds(t *testing.T) {
	// 这些常量逐字移植自上游 ds-bot/src/host/constants.js，防止无人察觉地漂移。
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"MessageInlineMax", MessageInlineMax, 20000},
		{"LongHead", LongHead, 1000},
		{"TextKept", TextKept, 2000},
		{"GroupLineRead", GroupLineRead, 20000},
		{"TextShowMax", TextShowMax, 200000},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s = %d, want %d (upstream constant)", tc.name, tc.got, tc.want)
		}
	}
}

func TestSplitMaxChunkIsMessageInlineMax(t *testing.T) {
	// 用上游"消息内联上限"作为切分上限时，所有块都不超过 20,000 字。
	text := strings.Repeat("长消息测试", 6000) // 30,000 个码点
	chunks := Split(text, MessageInlineMax)
	assertSplitJoin(t, text, chunks)
	assertChunkLimits(t, chunks, MessageInlineMax)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2 for 30,000 chars with 20,000 limit", len(chunks))
	}
}
