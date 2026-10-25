// Package eventbus 提供进程内事件总线（v0.5.2）。
//
// 对应架构图 ③ 审批与审计事件流：每次工具调用/门控/扣费写事件，
// 与业务消息分离，不混用。生产切换到 Kafka，接口不变。
package eventbus

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"sync"
	"time"
)

// EventType 事件类型枚举。
type EventType string

const (
	EventTypeGateCheck        EventType = "gate_check"
	EventTypeApprovalCreate   EventType = "approval_create"
	EventTypeApprovalDecide   EventType = "approval_decide"
	EventTypeLedgerCharge     EventType = "ledger_charge"
	EventTypeComplianceReject EventType = "compliance_reject"

	// v0.6.5：记忆变更事件（供蒸馏/审计订阅）
	EventTypeMemoryFactUpsert    EventType = "memory_fact_upsert"
	EventTypeMemoryEpisodeAppend EventType = "memory_episode_append"
	EventTypeMemoryReinforce     EventType = "memory_reinforce"

	// v0.7.2：蒸馏收紧事件（可观测/可审计）
	EventTypeDistillTighten EventType = "distill_tighten"

	// v0.8.3：模型调用事件（每次 LLM 调用写审计）
	EventTypeModelCall EventType = "model_call"
)

// Event 审计事件。
type Event struct {
	ID        string         `json:"id"`
	Type      EventType      `json:"type"`
	Timestamp int64          `json:"timestamp"`
	UserID    uint64         `json:"userId,omitempty"`
	AgentID   string         `json:"agentId,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// Handler 事件处理器。
type Handler func(ctx context.Context, e *Event) error

// Bus 事件总线。
type Bus struct {
	mu       sync.RWMutex
	handlers map[EventType][]Handler
	events   []*Event
	db       *sql.DB // v1.0.4：可选 PG 审计持久化
}

// New 创建事件总线。
func New() *Bus {
	return &Bus{
		handlers: map[EventType][]Handler{},
		events:   make([]*Event, 0, 1024),
	}
}

// SetPG 注入 PG 审计持久化（v1.0.4）。
func (b *Bus) SetPG(db *sql.DB) { b.db = db }

// Subscribe 订阅事件类型。
func (b *Bus) Subscribe(t EventType, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[t] = append(b.handlers[t], h)
}

// Publish 发布事件（同步调用所有 handler）。
func (b *Bus) Publish(ctx context.Context, e *Event) error {
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().Unix()
	}
	b.mu.Lock()
	b.events = append(b.events, e)
	handlers := append([]Handler(nil), b.handlers[e.Type]...)
	b.mu.Unlock()
	// v1.0.4：异步落 PG audit_events（best-effort）
	if b.db != nil {
		go func(ev *Event) {
			detail, _ := json.Marshal(ev.Payload)
			if _, err := b.db.Exec(
				`INSERT INTO audit_events (user_id, agent_id, action, detail) VALUES ($1,$2,$3,$4)`,
				ev.UserID, ev.AgentID, string(ev.Type), string(detail),
			); err != nil {
				log.Printf("eventbus: persist audit_event: %v", err)
			}
		}(e)
	}
	for _, h := range handlers {
		if err := h(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// List 列出所有事件（按时间倒序）。
func (b *Bus) List() []*Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]*Event, len(b.events))
	copy(result, b.events)
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// ByType 按类型过滤。
func (b *Bus) ByType(t EventType) []*Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]*Event, 0)
	for _, e := range b.events {
		if e.Type == t {
			result = append(result, e)
		}
	}
	return result
}
