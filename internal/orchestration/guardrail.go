package orchestration

import (
	"strings"
)

// v2.3.11 C4：出口护栏。
//
// 所有发给微信的文本在离开编排层之前必须过一道：
//   - 敏感词/政治敏感粗过滤（规则集，可后续接小模型审核）；
//   - 不出现 wxid / 内部 URL / 单号；
//   - 长度限制微信友好（≤500 字）；
//   - 违规内容替换为一句安全的兜底文案，不直接拒绝（拒绝会让用户体验崩塌）。

// ForbiddenSubstrings 粗过滤黑名单（运行期可通过 SetForbidden 扩展）。
var defaultForbidden = []string{
	"wxid_",
	"gh_",
	"http://",
	"https://",
	"internal://",
	"ticket_",
	"msg_id_",
	"trace_",
}

// GuardResult 护栏输出。
type GuardResult struct {
	OK      bool   // 是否可以原文发出
	Rewrite string // 改写后文案（OK=false 时用）
	HitRule string // 命中的规则（审计用）
}

// ExitGuard 出口护栏（无状态，可并发）。
type ExitGuard struct {
	forbidden []string
}

func NewExitGuard() *ExitGuard {
	g := &ExitGuard{}
	g.SetForbidden(defaultForbidden)
	return g
}

func (g *ExitGuard) SetForbidden(words []string) {
	// 拷贝一份，防止外部数组被改
	g.forbidden = append([]string(nil), words...)
}

// AuditMessage 审一条即将外发的消息。
func (g *ExitGuard) AuditMessage(text string) GuardResult {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return GuardResult{Rewrite: "已处理。", HitRule: "empty"}
	}
	// 1. 黑名单
	lower := strings.ToLower(trimmed)
	for _, w := range g.forbidden {
		if strings.Contains(lower, strings.ToLower(w)) {
			return GuardResult{
				Rewrite: "这件事我晚点再回你。",
				HitRule: "forbidden:" + w,
			}
		}
	}
	// 2. 长度
	if r := []rune(trimmed); len(r) > 500 {
		return GuardResult{
			OK:      false,
			Rewrite: strings.TrimRight(string(r[:500]), " .,;:，。；：") + "…",
			HitRule: "length",
		}
	}
	return GuardResult{OK: true, Rewrite: trimmed}
}

// Sanitize 一行调用：返回可以直接发给微信的文本。
func (g *ExitGuard) Sanitize(text string) string {
	r := g.AuditMessage(text)
	return r.Rewrite
}
