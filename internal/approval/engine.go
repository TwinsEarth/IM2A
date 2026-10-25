// Package approval 实现审批工作流引擎（§7.3 / §9.2）。
//
// 设计要点：
//   - 显式状态机，不依赖模型临场规划审批顺序；
//   - 事件溯源：用户每次通过/驳回/修改作为事件写入，驱动 Agent Improvement Memory；
//   - 超时升级：30 分钟未审批推送手表，2 小时未审批推送短信（Escalate 通道）；
//   - 状态持久化到 PostgreSQL（v0.1 以内存 + 事件日志替代，接口可对接存储）。
package approval

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// State 审批单状态。
type State string

const (
	StatePending   State = "PENDING"
	StateReviewing State = "REVIEWING"
	StateApproved  State = "APPROVED"
	StateRejected  State = "REJECTED"
	StateModified  State = "MODIFIED"
	StateExecuting State = "EXECUTING"
	StateCompleted State = "COMPLETED"
	StateFailed    State = "FAILED"
)

// String 状态名。
func (s State) String() string { return string(s) }

// EventType 事件类型。
type EventType string

const (
	EventCreated   EventType = "CREATED"
	EventApproved  EventType = "APPROVED"
	EventRejected  EventType = "REJECTED"
	EventModified  EventType = "MODIFIED"
	EventExecuted  EventType = "EXECUTED"
	EventFailed    EventType = "FAILED"
	EventEscalated EventType = "ESCALATED"
	EventTimeout   EventType = "TIMEOUT"
)

// TicketType 审批单类型。
type TicketType string

const (
	TicketNegotiation    TicketType = "NEGOTIATION"
	TicketPayment        TicketType = "PAYMENT"
	TicketSkillCall      TicketType = "SKILL_CALL"
	TicketExternalAction TicketType = "EXTERNAL_ACTION"
)

// Event 一条不可变事件（事件溯源单元）。
type Event struct {
	At   time.Time
	Type EventType
	By   string // user / agent / system
	Note string
}

// Ticket 审批单（长生命周期、可寻址实体）。
type Ticket struct {
	ID             string
	UserID         uint64
	SourceAgentID  string
	Type           TicketType
	State          State
	Payload        map[string]any
	Deadline       time.Time
	CreatedAt      time.Time
	ResolvedAt     time.Time
	Events         []Event
	Escalation     int // 0 无 / 1 已推送手表 / 2 已推送短信
	EscalationNote string
	// v2.3.6 B2：ActionHash 把审批与执行强绑定，防"拿 A 的批准执行 B"。
	// BeginExecutingWithHash 校验调用方传入的 hash 必须与创建时一致。
	ActionHash string
}

// transitions 合法状态迁移表。
// PENDING 支持直接 APPROVED/MODIFIED：对应审批卡片"一键通过/修改"（§7.1，30 秒决策目标）。
var transitions = map[State]map[State]bool{
	StatePending:   {StateReviewing: true, StateApproved: true, StateModified: true, StateRejected: true, StateFailed: true},
	StateReviewing: {StatePending: true, StateApproved: true, StateRejected: true, StateModified: true, StateFailed: true},
	StateApproved:  {StateExecuting: true, StateRejected: true, StateModified: true, StateFailed: true},
	StateModified:  {StateExecuting: true, StateRejected: true, StateFailed: true},
	StateExecuting: {StateCompleted: true, StateFailed: true},
}

var (
	// ErrInvalidTransition 非法状态迁移。
	ErrInvalidTransition = errors.New("approval: invalid state transition")
	// ErrNotFound 审批单不存在。
	ErrNotFound = errors.New("approval: ticket not found")
)

// Store 审批单存储接口（v0.1 内存实现；生产为 PostgreSQL）。
type Store interface {
	Save(*Ticket) error
	Load(id string) (*Ticket, error)
	// ListPending 返回用户待审列表（不含已终态）。
	ListPending(userID uint64) ([]*Ticket, error)
	// All 返回全部审批单（超时扫描用；生产由 PostgreSQL 定时任务按 deadline 索引查询）。
	All() ([]*Ticket, error)
}

// MemoryStore 进程内存储（单机演示）。
type MemoryStore struct {
	mu     sync.Mutex
	byID   map[string]*Ticket
	byUser map[uint64][]string
}

// NewMemoryStore 创建内存存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[string]*Ticket{}, byUser: map[uint64][]string{}}
}

// Save 保存（按 ID upsert）。
func (s *MemoryStore) Save(t *Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[t.ID] = t
	// v2.3.22：维护 byUser 索引，否则 ListPending 永远为空（历史遗留：
	// 之前只建 key 不追加 id，调用方只能用 All() 绕过）。
	ids := s.byUser[t.UserID]
	found := false
	for _, x := range ids {
		if x == t.ID {
			found = true
			break
		}
	}
	if !found {
		s.byUser[t.UserID] = append(ids, t.ID)
	}
	return nil
}

// Load 按 ID 读取。
func (s *MemoryStore) Load(id string) (*Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	return t, nil
}

// ListPending 返回用户待审列表。
func (s *MemoryStore) ListPending(userID uint64) ([]*Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Ticket
	for _, id := range s.byUser[userID] {
		t := s.byID[id]
		if t != nil && !isTerminal(t.State) {
			out = append(out, t)
		}
	}
	return out, nil
}

// All 返回全部审批单（超时扫描用；生产环境由 PostgreSQL 定时任务按 deadline 索引查询）。
func (s *MemoryStore) All() ([]*Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Ticket, 0, len(s.byID))
	for _, t := range s.byID {
		out = append(out, t)
	}
	return out, nil
}

func isTerminal(s State) bool {
	switch s {
	case StateCompleted, StateFailed, StateRejected:
		return true
	}
	return false
}

// Engine 审批工作流引擎。
type Engine struct {
	mu    sync.Mutex
	store Store
	seq   uint64
	now   func() time.Time
}

// New 创建引擎。
func New(store Store) *Engine {
	return &Engine{store: store, now: time.Now}
}

// CreateTicket 创建审批单并写 CREATED 事件。
func (e *Engine) CreateTicket(userID uint64, sourceAgentID string, ticketType TicketType, payload map[string]any, deadline time.Time) (*Ticket, error) {
	e.mu.Lock()
	e.seq++
	id := fmt.Sprintf("t%06d", e.seq)
	e.mu.Unlock()

	t := &Ticket{
		ID:            id,
		UserID:        userID,
		SourceAgentID: sourceAgentID,
		Type:          ticketType,
		State:         StatePending,
		Payload:       payload,
		Deadline:      deadline,
		CreatedAt:     e.now(),
	}
	if h, ok := payload["__action_hash__"].(string); ok && h != "" {
		t.ActionHash = h
	}
	t.Events = append(t.Events, Event{At: t.CreatedAt, Type: EventCreated, By: "agent", Note: sourceAgentID})
	if err := e.store.Save(t); err != nil {
		return nil, err
	}
	return t, nil
}

// Transition 校验并执行一次状态迁移，追加事件。
func (e *Engine) Transition(id string, to State, by, note string) (*Ticket, error) {
	t, err := e.load(id)
	if err != nil {
		return nil, err
	}
	if t.State == to {
		return t, nil // 幂等：同状态重复操作不报错
	}
	if !transitions[t.State][to] {
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.State, to)
	}
	t.State = to
	if isTerminal(to) {
		t.ResolvedAt = e.now()
	}
	t.Events = append(t.Events, Event{At: e.now(), Type: evtFor(to), By: by, Note: note})
	if err := e.store.Save(t); err != nil {
		return nil, err
	}
	return t, nil
}

// Approve 通过：PENDING/REVIEWING → APPROVED。
func (e *Engine) Approve(id, by, note string) (*Ticket, error) {
	return e.Transition(id, StateApproved, by, note)
}

// Reject 驳回：任意非终态 → REJECTED。
func (e *Engine) Reject(id, by, reason string) (*Ticket, error) {
	return e.Transition(id, StateRejected, by, reason)
}

// Modify 用户修改后放行：REVIEWING/APPROVED → MODIFIED。
func (e *Engine) Modify(id string, patch map[string]any, by, note string) (*Ticket, error) {
	t, err := e.Transition(id, StateModified, by, note)
	if err != nil {
		return nil, err
	}
	// 合并 patch 进 payload
	for k, v := range patch {
		t.Payload[k] = v
	}
	if err := e.store.Save(t); err != nil {
		return nil, err
	}
	return t, nil
}

// BeginExecuting APPROVED/MODIFIED → EXECUTING。
func (e *Engine) BeginExecuting(id, by string) (*Ticket, error) {
	return e.Transition(id, StateExecuting, by, "agent begins execution")
}

// ErrActionHashMismatch v2.3.6 B2：执行时 action_hash 与审批时不一致（防审批洗白）。
var ErrActionHashMismatch = errors.New("approval: action hash mismatch (approval laundering)")

// BeginExecutingWithHash 进入 EXECUTING 前强校验 actionHash 与创建时一致。
// 只有 ticket 创建时声明了 ActionHash 才校验；未声明（老票据）按原逻辑放行。
func (e *Engine) BeginExecutingWithHash(id, by, actionHash string) (*Ticket, error) {
	t, err := e.load(id)
	if err != nil {
		return nil, err
	}
	if t.ActionHash != "" && t.ActionHash != actionHash {
		return nil, fmt.Errorf("%w: ticket=%s approved=%s executed=%s",
			ErrActionHashMismatch, id, t.ActionHash, actionHash)
	}
	return e.Transition(id, StateExecuting, by, "agent begins execution (hash-checked)")
}

// Complete EXECUTING → COMPLETED。
func (e *Engine) Complete(id, by string) (*Ticket, error) {
	return e.Transition(id, StateCompleted, by, "execution completed")
}

// Fail 任意态 → FAILED。
func (e *Engine) Fail(id, by, reason string) (*Ticket, error) {
	return e.Transition(id, StateFailed, by, reason)
}

// Escalate 超时升级：按已过期时长推送到手表（30min）或短信（2h）。
// 返回升级后的 Ticket 与升级通道。
func (e *Engine) Escalate(id string, now time.Time) (*Ticket, string, error) {
	t, err := e.load(id)
	if err != nil {
		return nil, "", err
	}
	if isTerminal(t.State) {
		return t, "", nil
	}
	over := now.Sub(t.Deadline)
	var channel string
	switch {
	case over >= 2*time.Hour && t.Escalation < 2:
		t.Escalation = 2
		channel = "sms"
	case over >= 30*time.Minute && t.Escalation < 1:
		t.Escalation = 1
		channel = "watch_push"
	default:
		return t, "", nil
	}
	t.Events = append(t.Events, Event{At: now, Type: EventEscalated, By: "system", Note: channel})
	if err := e.store.Save(t); err != nil {
		return nil, "", err
	}
	return t, channel, nil
}

// CheckTimeouts 扫描全部非终态审批单，对超过截止时间的执行超时升级
// （30 分钟 → 手表推送；2 小时 → 短信）。返回发生升级的审批单。
// 生产环境由 PostgreSQL 定时任务按 deadline 索引触发，此处为进程内全量扫描。
func (e *Engine) CheckTimeouts(now time.Time) ([]*Ticket, error) {
	all, err := e.store.All()
	if err != nil {
		return nil, err
	}
	var escalated []*Ticket
	for _, t := range all {
		if isTerminal(t.State) {
			continue
		}
		if now.After(t.Deadline) {
			_, channel, err := e.Escalate(t.ID, now)
			if err != nil {
				return nil, err
			}
			if channel != "" {
				escalated = append(escalated, t)
			}
		}
	}
	return escalated, nil
}

// ListPending 查询用户待审列表。
func (e *Engine) ListPending(userID uint64) ([]*Ticket, error) {
	return e.store.ListPending(userID)
}

// All 返回全部审批单（v2.3.7 B3：ResolveReply 用它按 userID 过滤，兼容 MemoryStore 未索引 byUser 的问题）。
func (e *Engine) All() ([]*Ticket, error) {
	return e.store.All()
}

// Get 读取审批单。
func (e *Engine) Get(id string) (*Ticket, error) {
	return e.load(id)
}

func (e *Engine) load(id string) (*Ticket, error) {
	t, err := e.store.Load(id)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func evtFor(s State) EventType {
	switch s {
	case StateApproved:
		return EventApproved
	case StateRejected:
		return EventRejected
	case StateModified:
		return EventModified
	case StateExecuting:
		return EventExecuted
	case StateCompleted:
		return EventExecuted
	case StateFailed:
		return EventFailed
	default:
		return EventType(s)
	}
}
