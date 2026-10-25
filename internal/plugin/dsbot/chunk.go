// SPDX-License-Identifier: Apache-2.0
//
// 移植自 Fei2411/DeepSeek-Bot ds-bot/src/host/text.js、ds-bot/src/host/delivery.js、
// ds-bot/src/host/constants.js 与 docs/plans/2026-10-16-long-messages.md (Apache-2.0)，
// 已修改为 Go 实现：把上游"超长消息转 DSH 附件、消息里只保留开头 1,000 字"的策略，
// 改为按字符安全边界切成多条消息（IM2A 的微信个人号通道没有 DSH 附件能力）。
package dsbotplugin

import (
	"unicode"
)

// 长消息阈值：逐字移植自上游 ds-bot/src/host/constants.js。
// 它们描述上游行为（state.json 截断、附件化门槛），本插件保留为调用方可选上限，
// 不在 Split 内部隐式套用（Split 的上限由调用方传入）。
const (
	// MessageInlineMax 上游"消息内联上限"：超过即转附件（= GROUP_LINE_READ）。
	MessageInlineMax = 20000
	// LongHead 转附件后消息里保留的开头字数。
	LongHead = 1000
	// TextKept state.json 里每条消息保留的开头字数。
	TextKept = 2000
	// GroupLineRead 群记录里成员可读的每行字数。
	GroupLineRead = 20000
	// TextShowMax 往来记录里单条消息最多展示的字数。
	TextShowMax = 200000
)

// zeroWidthJoiner 是 emoji ZWJ 序列的连接符；切在它旁边会把一个 emoji 拆坏。
const zeroWidthJoiner rune = 0x200D

// Split 把 text 按字符上限 limit 切成多条消息，返回的片段按顺序拼接后与原文本逐字节相同。
//
// 语义（limit 的单位是 Unicode 码点，不是字节；调用方按目标通道的限制换算）：
//   - limit <= 0 或文本不超限：返回单元素切片（不丢内容，limit<=0 视为"不切"）；
//   - 空串：返回 nil（调用方据此跳过发送）；
//   - 优先在自然边界切：空行 > 换行 > 句末（。！？；!?;）> 标点/逗号（，,、：:）>
//     空白；找不到边界才硬切（超长单词）；
//   - 不切断 URL：能整段放入当前片段的 URL 移到片段末尾，放不下的 URL 整体挪到下一片段；
//   - 不切断 grapheme 簇：emoji ZWJ 序列、肤色修饰符、变体选择符、组合符、keycap、
//     国旗（成对 regional indicator）都不会被从中间切开；仅当单个 grapheme 本身就超过
//     limit 时无可避免地按码点硬切（上游 headOf 对超长 surrogate 也是同样取舍）。
//
// 与上游的差异（契约 §5 要求声明修改）：上游不切分消息，而是把全文原样存成 DSH 附件
// 并让消息携带开头 LONG_HEAD 字 + 读取说明；本函数面向"只能发一条条普通消息"的通道，
// 因此保证 join(Split(text, limit)) == text，即切分而非截断。
func Split(text string, limit int) []string {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return []string{text}
	}
	chunks := make([]string, 0, len(runes)/limit+1)
	start := 0
	for start < len(runes) {
		if len(runes)-start <= limit {
			chunks = append(chunks, string(runes[start:]))
			break
		}
		hard := graphemeSafeCut(runes, start, start+limit)
		if hard <= start {
			hard = start + limit // 单 grapheme 超过 limit：无法兼顾，按码点硬切
		}
		cut := preferBoundary(runes, start, hard)
		if cut <= start || cut > hard {
			cut = hard
		}
		cut = avoidURLSplit(runes, start, cut, hard)
		if cut <= start {
			cut = hard
		}
		chunks = append(chunks, string(runes[start:cut]))
		start = cut
	}
	return chunks
}

// graphemeSafeCut 从 hard 向前找到最近的、不切开 grapheme 簇的切点。
// 若 limit 内找不到（单个簇比 limit 还长），返回 hard（由调用方硬切）。
func graphemeSafeCut(runes []rune, start, hard int) int {
	if hard >= len(runes) {
		return len(runes)
	}
	if hard <= start {
		return hard
	}
	for cut := hard; cut > start; cut-- {
		if isSafeBoundary(runes, cut) {
			return cut
		}
	}
	return hard
}

// isSafeBoundary 判断在 cut 处切开是否不破坏 grapheme 簇。
func isSafeBoundary(runes []rune, cut int) bool {
	if cut <= 0 || cut >= len(runes) {
		return true
	}
	// 下一个码点是延伸符（组合符/变体选择符/肤色/ZWJ/keycap）→ 会切开当前簇。
	if isGraphemeExtend(runes[cut]) {
		return false
	}
	// 切点前是 ZWJ → 切开 emoji ZWJ 序列。
	if runes[cut-1] == zeroWidthJoiner {
		return false
	}
	// 切点后是 regional indicator：前面连续 RI 个数为奇数说明正处在一面国旗中间。
	if isRegionalIndicator(runes[cut]) {
		count := 0
		for i := cut - 1; i >= 0 && isRegionalIndicator(runes[i]); i-- {
			count++
		}
		if count%2 == 1 {
			return false
		}
	}
	return true
}

// isGraphemeExtend 判断 r 是否是 grapheme 簇的延伸码点。
func isGraphemeExtend(r rune) bool {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
		return true
	}
	switch {
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r >= 0xE0100 && r <= 0xE01EF: // variation selectors supplement
		return true
	case r == zeroWidthJoiner:
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF: // emoji skin tone modifiers
		return true
	case r == 0x20E3: // combining enclosing keycap
		return true
	}
	return false
}

// isRegionalIndicator 判断 r 是否是国旗用的 regional indicator 符号。
func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

// boundaryClass 是切点优先级的类别，顺序即优先级。
type boundaryClass int

const (
	boundaryParagraph boundaryClass = iota // 空行
	boundaryLine                           // 换行
	boundarySentence                       // 句末标点
	boundaryPunct                          // 逗号/顿号/冒号
	boundarySpace                          // 任意空白
)

// preferBoundary 在 [start+(hard-start)/2, hard] 的后半窗口里按优先级找最靠后的自然边界；
// 找不到返回 hard。只回退半个窗口，避免片段过短。
func preferBoundary(runes []rune, start, hard int) int {
	if hard-start <= 1 {
		return hard
	}
	low := start + (hard-start)/2
	for class := boundaryParagraph; class <= boundarySpace; class++ {
		for i := hard - 1; i >= low; i-- {
			if isBoundaryRune(runes, i, class) {
				return i + 1
			}
		}
	}
	return hard
}

// isBoundaryRune 判断 runes[i] 是否构成 class 类边界（切点在 i+1）。
func isBoundaryRune(runes []rune, i int, class boundaryClass) bool {
	r := runes[i]
	switch class {
	case boundaryParagraph:
		if r != '\n' {
			return false
		}
		// \n\n 或 \r\n\r\n 的空行边界。
		if i >= 1 && runes[i-1] == '\n' {
			return true
		}
		return i >= 2 && runes[i-1] == '\r' && runes[i-2] == '\n'
	case boundaryLine:
		return r == '\n'
	case boundarySentence:
		return stringsContainsRune("。！？；!?;", r)
	case boundaryPunct:
		return stringsContainsRune("，,、：:", r)
	case boundarySpace:
		return unicode.IsSpace(r)
	}
	return false
}

// stringsContainsRune 判断 s 是否包含 r（小集合，不需要额外依赖）。
func stringsContainsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

// avoidURLSplit 修正切点，避免把一个 URL 切成两半。
// hard 是本片段允许的最大切点（已做 grapheme 安全处理）。
func avoidURLSplit(runes []rune, start, cut, hard int) int {
	if cut <= start || cut >= len(runes) {
		return cut
	}
	// 切点两侧都是非空白 → 位于某个 token 内部。
	if unicode.IsSpace(runes[cut]) || unicode.IsSpace(runes[cut-1]) {
		return cut
	}
	tokenStart := cut
	for tokenStart > start && !unicode.IsSpace(runes[tokenStart-1]) {
		tokenStart--
	}
	if urlStart := indexURLPrefix(runes[tokenStart:cut]); urlStart >= 0 {
		tokenEnd := cut
		for tokenEnd < len(runes) && !unicode.IsSpace(runes[tokenEnd]) {
			tokenEnd++
		}
		// URL 能整段放进当前片段 → 把切点移到 token 末尾。
		if tokenEnd <= hard {
			return tokenEnd
		}
		// 否则把切点移到 URL 起点，让 URL 整体进入下一片段。
		if abs := tokenStart + urlStart; abs > start {
			return abs
		}
	}
	return cut
}

// indexURLPrefix 返回 r 中第一个 http:// 或 https:// 前缀的起始下标（无则 -1）。
func indexURLPrefix(r []rune) int {
	for i := 0; i+7 <= len(r); i++ {
		if r[i] != 'h' && r[i] != 'H' {
			continue
		}
		rest := string(r[i:])
		if hasPrefixFold(rest, "http://") || hasPrefixFold(rest, "https://") {
			return i
		}
	}
	return -1
}

// hasPrefixFold 是 ASCII 大小写不敏感的前缀判断。
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != prefix[i] {
			return false
		}
	}
	return true
}
