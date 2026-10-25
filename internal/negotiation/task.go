// Package negotiation 实现 Agent 间协商的 8 态 Task 状态机（§2.4）。
//
// 设计要点：
//   - 参考 A2A Task 状态机，但为平台内部高频协商定制，不走 A2A 完整规范；
//   - input-required / auth-required 是"卡住但未失败"的中断态：
//     input-required = 需要对方补充信息；auth-required = 需要用户审批下游动作，
//     这正是人机混合审批的协议级落点；
//   - 硬性约束：最大轮次（默认 5）+ token 预算，超限自动转 input-required 上抛用户，
//     防止双方 Agent 无限反提案消耗云端推理资源（§10-R6）；
//   - 协商过程不产生用户可见通知，只在终止时生成审批卡片注入 IM。
package negotiation

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// State Task 状态。
type State string

const (
	StateSubmitted     State = "SUBMITTED"
	StateWorking       State = "WORKING"
	StateInputRequired State = "INPUT_REQUIRED"
	StateAuthRequired  State = "AUTH_REQUIRED"
	StateCompleted     State = "COMPLETED"
	StateFailed        State = "FAILED"
	StateCanceled      State = "CANCELED"
	StateRejected      State = "REJECTED"
)

// String 状态名。
func (s State) String() string { return string(s) }

// Intent 协商意图。
type Intent string

const (
	IntentPropose     Intent = "PROPOSE"
	IntentCounter     Intent = "COUNTER"
	IntentAccept      Intent = "ACCEPT"
	IntentReject      Intent = "REJECT"
	IntentRequestInfo Intent = "REQUEST_INFO"
	IntentRequestAuth Intent = "REQUEST_AUTH"
)

// Turn 一轮协商消息。
type Turn struct {
	SessionID    string
	FromAgentID  string
	ToAgentID    string
	Intent       Intent
	Payload      map[string]any
	ArtifactRefs []string
	DeadlineUnix int64
}

// TokenCount 粗糙的 token 估算（payload JSON 长度 / 4），用于预算计量。
func TokenCount(payload map[string]any) uint64 {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0
	}
	return uint64(len(b)/4 + 1)
}

// DefaultMaxRounds 默认最大协商轮次（硬性上限）。
const DefaultMaxRounds = 5

// Session 协商会话。
type Session struct {
	ID               string
	State            State
	Turns            []Turn
	Round            int
	MaxRounds        int
	TokenUsed        uint64
	TokenBudget      uint64
	Deadline         time.Time
	Summary          map[string]any
	ApprovalTicketID string // AUTH_REQUIRED 时生成
	CreatedAt        time.Time
}

var (
	// ErrRoundLimit 协商轮次超限（转 INPUT_REQUIRED 上抛用户）。
	ErrRoundLimit = errors.New("negotiation: round limit exceeded")
	// ErrTokenBudget token 预算超限。
	ErrTokenBudget = errors.New("negotiation: token budget exceeded")
	// ErrSessionEnded 会话已进入终态，不能再接收 Turn。
	ErrSessionEnded = errors.New("negotiation: session ended")
)

// NewSession 创建协商会话。
func NewSession(id string, maxRounds int, tokenBudget uint64) *Session {
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}
	return &Session{
		ID:          id,
		State:       StateSubmitted,
		MaxRounds:   maxRounds,
		TokenBudget: tokenBudget,
		CreatedAt:   time.Now(),
	}
}

// Apply 处理一轮 Turn，返回迁移后的状态。
//
// 规则：
//   - ACCEPT → COMPLETED（协商终止，产出 Summary）
//   - REJECT → REJECTED
//   - REQUEST_AUTH → AUTH_REQUIRED（生成审批单，协商挂起等待用户）
//   - REQUEST_INFO → INPUT_REQUIRED（等待对方补充信息）
//   - 其余（PROPOSE/COUNTER）→ WORKING，round+1；超轮次/token → INPUT_REQUIRED（熔断上抛）
func (s *Session) Apply(t Turn) (State, error) {
	if isEnded(s.State) {
		return s.State, ErrSessionEnded
	}
	if s.Deadline.IsZero() && t.DeadlineUnix > 0 {
		s.Deadline = time.Unix(t.DeadlineUnix, 0)
	}
	if !s.Deadline.IsZero() && time.Now().After(s.Deadline) {
		s.State = StateFailed
		return s.State, fmt.Errorf("negotiation: session deadline exceeded")
	}
	s.Turns = append(s.Turns, t)
	s.TokenUsed += TokenCount(t.Payload)
	if s.TokenBudget > 0 && s.TokenUsed > s.TokenBudget {
		s.State = StateInputRequired
		return s.State, ErrTokenBudget
	}

	switch t.Intent {
	case IntentAccept:
		s.State = StateCompleted
		s.Summary = map[string]any{
			"agreed_at_round": s.Round + 1,
			"last_payload":    t.Payload,
		}
	case IntentReject:
		s.State = StateRejected
	case IntentRequestAuth:
		s.State = StateAuthRequired
		s.Summary = map[string]any{"awaiting_user_approval": true, "payload": t.Payload}
	case IntentRequestInfo:
		s.State = StateInputRequired
	default: // PROPOSE / COUNTER
		s.Round++
		if s.Round > s.MaxRounds {
			// 硬性熔断：转 INPUT_REQUIRED 上抛用户，防止无限循环（§10-R6）
			s.State = StateInputRequired
			s.Summary = map[string]any{"reason": "round_limit", "round": s.Round, "max_rounds": s.MaxRounds}
			return s.State, ErrRoundLimit
		}
		s.State = StateWorking
	}
	return s.State, nil
}

// BindApprovalTicket 在 AUTH_REQUIRED 时绑定审批单 ID。
func (s *Session) BindApprovalTicket(ticketID string) {
	s.ApprovalTicketID = ticketID
}

// SummaryJSON 终止时的协商摘要（注入审批卡片）。
func (s *Session) SummaryJSON() string {
	b, _ := json.Marshal(s.Summary)
	return string(b)
}

func isEnded(s State) bool {
	switch s {
	case StateCompleted, StateFailed, StateCanceled, StateRejected:
		return true
	}
	return false
}
