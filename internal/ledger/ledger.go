// Package ledger 实现陌生人付费消息账本（§1.3 / §6 / §10-R2）。
//
// 设计要点：
//   - 不可变账本：仅 INSERT，不 UPDATE/DELETE（状态机迁移写新条目或原地推进状态但不删除）；
//   - 幂等：message_id 为唯一键，任何重试（支付回调/消息投递/审批状态变更）都不会二次扣费；
//   - 计费模式：发送即扣费（1）/ 回复才扣费（2）；
//   - 单日发送上限：防止付费方滥用（风控要点）。
package ledger

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Mode 计费模式。
type Mode int

const (
	// ModeSendCharge 发送即扣费：点击发送即扣，无论是否阅读。
	ModeSendCharge Mode = 1
	// ModeReplyCharge 回复才扣费：本人"查看并回复"才扣费。
	ModeReplyCharge Mode = 2
)

// Status 账本状态。
type Status int

const (
	StatusPending  Status = 0 // 已预审、待扣费
	StatusCharged  Status = 1 // 已扣费
	StatusRefunded Status = 2 // 已退款
)

// String 状态名。
func (s Status) String() string {
	switch s {
	case StatusPending:
		return "PENDING"
	case StatusCharged:
		return "CHARGED"
	case StatusRefunded:
		return "REFUNDED"
	default:
		return "UNKNOWN"
	}
}

var (
	// ErrAlreadyProcessed 幂等冲突：同一 message_id 已处理（重试安全，不产生二次扣费）。
	ErrAlreadyProcessed = errors.New("ledger: message already processed")
	// ErrDailyLimitExceeded 单日发送次数超限。
	ErrDailyLimitExceeded = errors.New("ledger: daily send limit exceeded")
	// ErrNotFound 账本条目不存在。
	ErrNotFound = errors.New("ledger: entry not found")
	// ErrInvalidTransition 非法状态迁移（如对已退款条目重复扣费）。
	ErrInvalidTransition = errors.New("ledger: invalid status transition")
)

// Entry 账本条目。
type Entry struct {
	MessageID      string
	SenderID       uint64
	ReceiverID     uint64
	Mode           Mode
	AmountCents    int64
	Status         Status
	ChargedAt      time.Time
	ApprovalCardID string
}

// Ledger 付费消息账本（进程内实现；生产为 PostgreSQL 事务 + 不可变审计表）。
type Ledger struct {
	mu         sync.Mutex
	entries    map[string]*Entry
	dailySent  map[string]int // key: senderID|YYYY-MM-DD → 当日已发数
	DailyLimit int
	now        func() time.Time
}

// New 创建账本。
func New(dailyLimit int) *Ledger {
	if dailyLimit <= 0 {
		dailyLimit = 10 // 默认单日上限
	}
	return &Ledger{entries: map[string]*Entry{}, dailySent: map[string]int{}, DailyLimit: dailyLimit, now: time.Now}
}

// Reserve 预审通过后占位（幂等：同一 message_id 重复 Reserve 返回 ErrAlreadyProcessed）。
// 同时执行单日发送上限检查。
func (l *Ledger) Reserve(messageID string, senderID, receiverID uint64, mode Mode, amountCents int64) (*Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.entries[messageID]; ok {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyProcessed, messageID)
	}
	dayKey := l.dayKey(senderID)
	if l.dailySent[dayKey] >= l.DailyLimit {
		return nil, fmt.Errorf("%w: sender=%d day=%s limit=%d", ErrDailyLimitExceeded, senderID, dayKey, l.DailyLimit)
	}
	l.dailySent[dayKey]++

	e := &Entry{
		MessageID:   messageID,
		SenderID:    senderID,
		ReceiverID:  receiverID,
		Mode:        mode,
		AmountCents: amountCents,
		Status:      StatusPending,
	}
	l.entries[messageID] = e
	return e, nil
}

// Charge 扣费：PENDING → CHARGED，恰好一次。
// 发送即扣费：Reserve 后立即 Charge；回复才扣费：用户"查看并回复"触发。
// 重复 Charge（如支付回调重试）返回 ErrAlreadyProcessed，绝不二次扣费。
func (l *Ledger) Charge(messageID, approvalCardID string) (*Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[messageID]
	if !ok {
		return nil, ErrNotFound
	}
	if e.Status == StatusCharged {
		return nil, fmt.Errorf("%w: %s already charged", ErrAlreadyProcessed, messageID)
	}
	if e.Status != StatusPending {
		return nil, fmt.Errorf("%w: %s -> CHARGED", ErrInvalidTransition, e.Status)
	}
	e.Status = StatusCharged
	e.ChargedAt = l.now()
	if approvalCardID != "" {
		e.ApprovalCardID = approvalCardID
	}
	return e, nil
}

// Refund 退款：CHARGED → REFUNDED（争议/违规）。
func (l *Ledger) Refund(messageID, reason string) (*Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[messageID]
	if !ok {
		return nil, ErrNotFound
	}
	if e.Status != StatusCharged {
		return nil, fmt.Errorf("%w: %s -> REFUNDED", ErrInvalidTransition, e.Status)
	}
	e.Status = StatusRefunded
	return e, nil
}

// Get 查询条目。
func (l *Ledger) Get(messageID string) (*Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[messageID]
	if !ok {
		return nil, ErrNotFound
	}
	return e, nil
}

// DailyUsage 查询发送方当日用量。
func (l *Ledger) DailyUsage(senderID uint64) (sent int, limit int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dailySent[l.dayKey(senderID)], l.DailyLimit
}

func (l *Ledger) dayKey(userID uint64) string {
	return fmt.Sprintf("%d|%s", userID, l.now().Format("2006-01-02"))
}
