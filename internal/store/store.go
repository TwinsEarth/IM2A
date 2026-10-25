// Package store 定义各业务领域的持久化接口（仓储层），并提供内存实现与 PostgreSQL 实现。
//
// 分层目的（对应技术设计基线 §4 数据层）：
//   - 关系链 / 五档信任 / Agent 能力 / 付费账本 / 审批 / 协商 / 审计 必须自有、可持久化；
//   - 服务只依赖接口，不关心底层是内存（自测/演示）还是 PostgreSQL（生产）；
//   - 保持 stdlib-only 可编译：PostgreSQL 实现只依赖 database/sql，
//     具体驱动（pgx/lib/pq）在运行时由服务主程序注入，编译期不引入任何外部模块。
//
// 离线约束：本机无网络，go.mod 不引入任何外部依赖。PG 实现仍可编译（database/sql 是标准库），
// 但运行需要应用通过 blank import 注册 "postgres" 驱动，否则 Open 返回明确错误。
package store

import (
	"context"
	"errors"

	"human-agent-im/internal/approval"
	"human-agent-im/internal/audit"
	"human-agent-im/internal/ledger"
	"human-agent-im/internal/negotiation"
	"human-agent-im/internal/trustzone"
)

// ErrNotFound 存储层统一"未找到"。
var ErrNotFound = errors.New("store: not found")

// RelationStore 关系链与五档信任档位（§1.4 / §4.2）。
type RelationStore interface {
	// GrantTrust 授予/变更 userID 对 targetID 的信任档位（upsert）。
	GrantTrust(ctx context.Context, userID, targetID uint64, level trustzone.Level) error
	// RevokeTrust 撤销关系（删除授权）。
	RevokeTrust(ctx context.Context, userID, targetID uint64) error
	// GetTrust 读取档位；无记录时返回 ErrNotFound（调用方按陌生档处理）。
	GetTrust(ctx context.Context, userID, targetID uint64) (trustzone.Level, error)
	// ListTrust 列出某用户授予的全部关系。
	ListTrust(ctx context.Context, userID uint64) (map[uint64]trustzone.Level, error)
}

// CapabilityStore Agent 能力声明（权限门控的数据来源，§6）。
type CapabilityStore interface {
	UpsertCapability(ctx context.Context, cap trustzone.Capability) error
	GetCapability(ctx context.Context, agentID string) (*trustzone.Capability, error)
	ListCapabilities(ctx context.Context) ([]trustzone.Capability, error)
}

// LedgerStore 付费消息账本（*ledger.Ledger 与 PG 实现均满足此接口）。
type LedgerStore interface {
	Reserve(messageID string, senderID, receiverID uint64, mode ledger.Mode, amountCents int64) (*ledger.Entry, error)
	Charge(messageID, approvalCardID string) (*ledger.Entry, error)
	Refund(messageID, reason string) (*ledger.Entry, error)
	Get(messageID string) (*ledger.Entry, error)
	DailyUsage(senderID uint64) (sent, limit int)
}

// NegotiationStore Agent 协商会话（§2.4）。
type NegotiationStore interface {
	SaveSession(ctx context.Context, s *negotiation.Session) error
	LoadSession(ctx context.Context, id string) (*negotiation.Session, error)
	ListSessions(ctx context.Context, limit int) ([]*negotiation.Session, error)
}

// Message 消息正文（v0.5.1 新增）。
type Message struct {
	MessageID    string         `json:"messageId"`
	SenderID     uint64         `json:"senderId"`
	ReceiverID   uint64         `json:"receiverId"`
	BizType      string         `json:"bizType,omitempty"` // text / approval_card / agent_summary / paid ...
	Content      string         `json:"content"`
	AgentContext map[string]any `json:"agentContext,omitempty"` // Agent 摘要/审批卡业务上下文
	Seq          int64          `json:"seq"`
	CreatedAt    int64          `json:"createdAt"`
}

// MessageStore 消息正文存储（v0.5.1 新增）。
type MessageStore interface {
	SaveMessage(ctx context.Context, m *Message) error
	LoadMessage(ctx context.Context, id string) (*Message, error)
	ListMessages(ctx context.Context, userID uint64, limit int) ([]*Message, error)
}

// Store 聚合全部领域存储（服务依赖注入一个聚合体）。
type Store struct {
	Relation    RelationStore
	Capability  CapabilityStore
	Ledger      LedgerStore
	Approval    approval.Store
	Negotiation NegotiationStore
	Audit       audit.Sink
	Message     MessageStore
}

// NewMemoryStore 构建全内存 Store（自测/演示/离线）。
// dailyLedgerLimit 为付费消息单日发送上限（<=0 用 ledger 默认 10）。
func NewMemoryStore(dailyLedgerLimit int) *Store {
	return &Store{
		Relation:    NewMemoryRelation(),
		Capability:  NewMemoryCapability(),
		Ledger:      ledger.New(dailyLedgerLimit),
		Approval:    approval.NewMemoryStore(),
		Negotiation: NewMemoryNegotiation(),
		Audit:       audit.NewMemorySink(4096),
		Message:     NewMemoryMessage(),
	}
}
