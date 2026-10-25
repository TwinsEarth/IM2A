package approval

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// v2.3.7 B3：微信文本审批卡片解析。
//
// 微信没有可交互按钮，审批卡片只能是纯文本：
//   "采购助理请求批准：买咖啡机 ¥1200。回复 1 同意 / 2 驳回 / 3 修改。"
// 用户下一条消息若命中规则，则被解释为对最近一条 pending ticket 的决策。

// ErrNotAnApprovalReply 不是审批回复（正常聊天）。
var ErrNotAnApprovalReply = errors.New("approval: not an approval reply")

// Decision 解析结果。
type Decision struct {
	TicketID string
	Outcome  string // approve / reject / modify
	Note     string // 用户原话（修改时是修改内容）
}

// 关键词表（小写包含匹配）。
var (
	approveWords = []string{"同意", "通过", "批准", "ok", "yes", "好的", "可以", "行", "1"}
	rejectWords  = []string{"驳回", "拒绝", "不要", "太贵", "no", "不行", "2"}
	modifyWords  = []string{"修改", "改成", "换", "调整", "3"}
)

// ResolveReply 把一条微信文本解释成审批决策。
// userID：当前微信用户（对应 Ticket.UserID）；
// listAll：返回全部审批单（生产由 PG 按 user_id 过滤；此处兼容 MemoryStore.ListPending 不索引的问题）。
// 命中规则返回 Decision；否则返回 ErrNotAnApprovalReply（按普通聊天处理）。
func ResolveReply(ctx context.Context, userID uint64, text string, listAll func() ([]*Ticket, error)) (*Decision, error) {
	clean := strings.ToLower(strings.TrimSpace(text))
	if clean == "" {
		return nil, ErrNotAnApprovalReply
	}

	// 必须先有 pending ticket 才把用户消息解释为审批。
	all, err := listAll()
	if err != nil {
		return nil, err
	}
	// 取该用户最近一条 pending/reviewing ticket。
	var latest *Ticket
	for _, t := range all {
		if t == nil || t.UserID != userID {
			continue
		}
		if t.State != StatePending && t.State != StateReviewing {
			continue
		}
		if latest == nil || t.CreatedAt.After(latest.CreatedAt) {
			latest = t
		}
	}
	if latest == nil {
		return nil, ErrNotAnApprovalReply
	}

	// 纯数字优先："1"/"2"/"3"
	switch clean {
	case "1":
		return &Decision{TicketID: latest.ID, Outcome: "approve", Note: text}, nil
	case "2":
		return &Decision{TicketID: latest.ID, Outcome: "reject", Note: text}, nil
	case "3":
		return &Decision{TicketID: latest.ID, Outcome: "modify", Note: text}, nil
	}

	// 关键词命中（注意顺序：modify 含"换/调整"比 reject 的"不要"优先级低时先 reject）
	hit := func(words []string) bool {
		for _, w := range words {
			if strings.Contains(clean, w) {
				return true
			}
		}
		return false
	}
	switch {
	case hit(rejectWords):
		return &Decision{TicketID: latest.ID, Outcome: "reject", Note: text}, nil
	case hit(approveWords):
		return &Decision{TicketID: latest.ID, Outcome: "approve", Note: text}, nil
	case hit(modifyWords):
		return &Decision{TicketID: latest.ID, Outcome: "modify", Note: text}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrNotAnApprovalReply, text)
}

// Apply 把 Decision 应用到 Engine。
func (d *Decision) Apply(e *Engine, by string) (*Ticket, error) {
	switch d.Outcome {
	case "approve":
		return e.Approve(d.TicketID, by, d.Note)
	case "reject":
		return e.Reject(d.TicketID, by, d.Note)
	case "modify":
		// 修改：用户文本作为 patch note，后续可由调用方解析成 patch
		return e.Modify(d.TicketID, map[string]any{"user_note": d.Note}, by, d.Note)
	}
	return nil, fmt.Errorf("approval: unknown outcome %q", d.Outcome)
}
