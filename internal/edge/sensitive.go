// v1.4.2: 端侧敏感信息前置过滤——关键词 + 正则双通道，命中即脱敏/拦截，不出端。
package edge

import (
	"regexp"
)

// Pattern 描述一条正则敏感规则（在原文中出现即视为敏感数据）。
type Pattern struct {
	Name    string
	Re      *regexp.Regexp
	MaskLen int // 替换为占位符时是否保留首尾若干位（默认整体替换）
}

type SensitiveFilter struct {
	keywordBlock []string // 命中即整体拦截的高危关键词
	patterns     []Pattern
}

// 预编译正则：手机号、身份证、银行卡、邮箱、长数字串。
var (
	reMobile   = regexp.MustCompile(`(?:\+?86)?1[3-9]\d{9}`)
	reIDCard   = regexp.MustCompile(`\d{17}[\dXx]`)
	reBankCard = regexp.MustCompile(`\b\d{16,19}\b`)
	reEmail    = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
)

func NewSensitiveFilter() *SensitiveFilter {
	return &SensitiveFilter{
		// 高危关键词：出现即整体拦截（可能是在谈论敏感动作本身）
		keywordBlock: []string{
			"密码", "身份证号", "银行卡号", "信用卡", "社保号", "家庭住址",
		},
		// 顺序很关键：长串（18位身份证/16-19位银行卡）必须先于 11 位手机号匹配，
		// 否则手机正则会从身份证/卡号中间截出 11 位，导致分类错误（RE2 不支持后行断言）。
		patterns: []Pattern{
			{Name: "idcard", Re: reIDCard},
			{Name: "bankcard", Re: reBankCard},
			{Name: "mobile", Re: reMobile},
			{Name: "email", Re: reEmail},
		},
	}
}

// Match 命中的敏感片段（用于审计/解释）。
type Match struct {
	Name string
	Text string
}

// Filter 返回 (blocked, redactedText, hits)。
// blocked=true 表示端侧应拦截、不允许出端；redactedText 为脱敏后的文本。
func (f *SensitiveFilter) Filter(text string) (blocked bool, redacted string, hits []Match) {
	redacted = text

	// 1) 高危关键词 → 整体拦截（blocked=true，脱敏后保留）
	for _, w := range f.keywordBlock {
		if containsCI(redacted, w) {
			blocked = true
			redacted = regexp.MustCompile(regexp.QuoteMeta(w)).ReplaceAllString(redacted, "[已拦截]")
			hits = append(hits, Match{Name: "keyword:" + w, Text: w})
		}
	}

	// 2) 正则命中号码/邮箱 → 脱敏（blocked=true，号码本身绝不能出端）
	for _, p := range f.patterns {
		loc := p.Re.FindAllString(redacted, -1)
		if len(loc) > 0 {
			blocked = true
			for _, m := range loc {
				hits = append(hits, Match{Name: p.Name, Text: m})
			}
			redacted = p.Re.ReplaceAllString(redacted, "[已脱敏]")
		}
	}
	return blocked, redacted, hits
}

func containsCI(s, sub string) bool {
	return regexp.MustCompile(regexp.QuoteMeta(sub)).MatchString(s)
}
