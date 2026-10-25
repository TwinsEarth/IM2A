// Package compliance 是合规出口护栏（§8.2 / §3.3）：所有 Agent 外发内容统一过审。
//
// v0.2 为本地规则引擎（关键词/手机号/外链）；生产接机审 API + 人工复核队列。
// 结果与消息 ID 绑定写入审计。
package compliance

import (
	"regexp"
	"strings"
	"time"

	"human-agent-im/internal/audit"
)

// Guard 出口护栏。
type Guard struct {
	prohibited []string
	phoneRe    *regexp.Regexp
	urlRe      *regexp.Regexp
	sink       audit.Sink
}

// NewGuard 创建护栏。
func NewGuard(sink audit.Sink) *Guard {
	return &Guard{
		prohibited: []string{"诈骗", "汇款到", "中奖", "兼职刷单", "赌博"},
		phoneRe:    regexp.MustCompile(`1[3-9]\d{9}`),
		urlRe:      regexp.MustCompile(`https?://`),
		sink:       sink,
	}
}

// Decision 审核决策。
type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// Check 检查一段 Agent 外发内容，写审计事件（结果与消息 ID 绑定）。
func (g *Guard) Check(messageID, content string) Decision {
	low := strings.ToLower(content)
	for _, p := range g.prohibited {
		if strings.Contains(low, p) {
			g.log(messageID, audit.ActionContentReject, "prohibited:"+p)
			return Decision{Allowed: false, Reason: "prohibited keyword: " + p}
		}
	}
	if g.phoneRe.MatchString(content) {
		g.log(messageID, audit.ActionContentReject, "phone_number_leak")
		return Decision{Allowed: false, Reason: "phone number leak"}
	}
	if g.urlRe.MatchString(low) {
		g.log(messageID, audit.ActionContentReject, "external_url")
		return Decision{Allowed: false, Reason: "external url not whitelisted"}
	}
	g.log(messageID, audit.ActionContentAllow, "ok")
	return Decision{Allowed: true, Reason: "ok"}
}

func (g *Guard) log(messageID string, action audit.Action, reason string) {
	if g.sink == nil {
		return
	}
	_ = g.sink.Write(audit.Entry{
		At: time.Now(), MessageID: messageID, AgentID: "outbound",
		Action: action, Decision: reason,
	})
}
