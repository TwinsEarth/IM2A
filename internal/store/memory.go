// 内存实现：关系链 / Agent 能力 / 协商会话（其余领域复用各自 internal 包的内存实现）。
package store

import (
	"context"
	"sort"
	"sync"

	"human-agent-im/internal/negotiation"
	"human-agent-im/internal/trustzone"
)

// ---- 关系链 / 信任档位 ----

// MemoryRelation 进程内关系链存储。
type MemoryRelation struct {
	mu     sync.RWMutex
	grants map[uint64]map[uint64]trustzone.Level // userID -> targetID -> level
}

// NewMemoryRelation 创建内存关系链。
func NewMemoryRelation() *MemoryRelation {
	return &MemoryRelation{grants: map[uint64]map[uint64]trustzone.Level{}}
}

// GrantTrust 授予/变更信任档位。
func (m *MemoryRelation) GrantTrust(_ context.Context, userID, targetID uint64, level trustzone.Level) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.grants[userID] == nil {
		m.grants[userID] = map[uint64]trustzone.Level{}
	}
	m.grants[userID][targetID] = level
	return nil
}

// RevokeTrust 撤销授权。
func (m *MemoryRelation) RevokeTrust(_ context.Context, userID, targetID uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if targets, ok := m.grants[userID]; ok {
		delete(targets, targetID)
	}
	return nil
}

// GetTrust 读取档位。
func (m *MemoryRelation) GetTrust(_ context.Context, userID, targetID uint64) (trustzone.Level, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if targets, ok := m.grants[userID]; ok {
		if lv, ok2 := targets[targetID]; ok2 {
			return lv, nil
		}
	}
	return trustzone.LevelStranger, ErrNotFound
}

// ListTrust 列出某用户全部关系。
func (m *MemoryRelation) ListTrust(_ context.Context, userID uint64) (map[uint64]trustzone.Level, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[uint64]trustzone.Level{}
	for t, lv := range m.grants[userID] {
		out[t] = lv
	}
	return out, nil
}

// ---- Agent 能力声明 ----

// MemoryCapability 进程内 Agent 能力存储。
type MemoryCapability struct {
	mu   sync.RWMutex
	caps map[string]trustzone.Capability
}

// NewMemoryCapability 创建内存能力存储。
func NewMemoryCapability() *MemoryCapability {
	return &MemoryCapability{caps: map[string]trustzone.Capability{}}
}

// UpsertCapability 写入能力声明。
func (m *MemoryCapability) UpsertCapability(_ context.Context, cap trustzone.Capability) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.caps[cap.AgentID] = cap
	return nil
}

// GetCapability 读取能力声明。
func (m *MemoryCapability) GetCapability(_ context.Context, agentID string) (*trustzone.Capability, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.caps[agentID]
	if !ok {
		return nil, ErrNotFound
	}
	return &c, nil
}

// ListCapabilities 列出全部能力声明。
func (m *MemoryCapability) ListCapabilities(_ context.Context) ([]trustzone.Capability, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]trustzone.Capability, 0, len(m.caps))
	for _, c := range m.caps {
		out = append(out, c)
	}
	return out, nil
}

// ---- 协商会话 ----

// MemoryNegotiation 进程内协商会话存储。
type MemoryNegotiation struct {
	mu       sync.RWMutex
	sessions map[string]*negotiation.Session
}

// NewMemoryNegotiation 创建内存协商存储。
func NewMemoryNegotiation() *MemoryNegotiation {
	return &MemoryNegotiation{sessions: map[string]*negotiation.Session{}}
}

// SaveSession 保存会话（upsert）。
func (m *MemoryNegotiation) SaveSession(_ context.Context, s *negotiation.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
	return nil
}

// LoadSession 读取会话。
func (m *MemoryNegotiation) LoadSession(_ context.Context, id string) (*negotiation.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

// ListSessions 最近协商会话（按 CreatedAt 降序）。
func (m *MemoryNegotiation) ListSessions(_ context.Context, limit int) ([]*negotiation.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*negotiation.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}
