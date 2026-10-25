package orchestration

import (
	"context"

	"human-agent-im/internal/approval"
	"human-agent-im/internal/distill"
)

// v2.3.12 D1：审批反馈 → Improvement Memory 写入管道。
//
// 现有 approval.Engine 已经有完整状态机；distill.Engine 已经有"连续驳回收紧阈值"的逻辑。
// 但两边没接上：用户在微信里点了同意/驳回/修改，distill 不知道，不会收紧。
// 本文件就是这根桥：审批落定 → 调 distill.Record(agentID, key, delta)。

// FeedbackSink 是 distill 侧需要的最小接口（由 distill.Engine 实现）。
type FeedbackSink interface {
	Record(userID uint64, agentID, key string, delta float64)
}

// ApprovalFeedbackBridge 把审批决策翻译成蒸馏反馈。
type ApprovalFeedbackBridge struct {
	sink FeedbackSink
}

func NewApprovalFeedbackBridge(sink FeedbackSink) *ApprovalFeedbackBridge {
	return &ApprovalFeedbackBridge{sink: sink}
}

// OnTicketResolved 在审批终态时调用。
// 决策 → delta 约定：
//   - APPROVED：+0.1（强化该建议）
//   - REJECTED：-0.3（明显反对，倾向收紧阈值）
//   - MODIFIED：-0.1（方向对但细节不对，弱反对）
//   - FAILED / EXPIRED：不打分（不是用户主观决策）
func (b *ApprovalFeedbackBridge) OnTicketResolved(ctx context.Context, tk *approval.Ticket) {
	if b == nil || b.sink == nil || tk == nil {
		return
	}
	agent := tk.SourceAgentID
	if agent == "" {
		agent = "unknown"
	}
	key := string(tk.Type)
	if key == "" {
		key = "general"
	}
	var delta float64
	switch tk.State {
	case approval.StateApproved:
		delta = 0.1
	case approval.StateRejected:
		delta = -0.3
	case approval.StateModified:
		delta = -0.1
	default:
		return
	}
	b.sink.Record(tk.UserID, agent, key, delta)
}

// 编译期断言：*distill.Engine 满足 FeedbackSink。
var _ FeedbackSink = (*distill.Engine)(nil)
