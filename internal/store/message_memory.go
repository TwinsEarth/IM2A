// 内存版 MessageStore（v0.5.1）。
package store

import (
	"context"
	"sort"
	"sync"
)

// MemoryMessage 内存消息存储。
type MemoryMessage struct {
	mu       sync.RWMutex
	messages map[string]*Message
	byUser   map[uint64][]string // userID -> messageIDs
}

// NewMemoryMessage 创建内存消息存储。
func NewMemoryMessage() *MemoryMessage {
	return &MemoryMessage{
		messages: map[string]*Message{},
		byUser:   map[uint64][]string{},
	}
}

// SaveMessage 保存消息。
func (m *MemoryMessage) SaveMessage(_ context.Context, msg *Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages[msg.MessageID] = msg
	m.byUser[msg.SenderID] = append(m.byUser[msg.SenderID], msg.MessageID)
	m.byUser[msg.ReceiverID] = append(m.byUser[msg.ReceiverID], msg.MessageID)
	return nil
}

// LoadMessage 按 ID 加载消息。
func (m *MemoryMessage) LoadMessage(_ context.Context, id string) (*Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	msg, ok := m.messages[id]
	if !ok {
		return nil, nil
	}
	return msg, nil
}

// ListMessages 列出用户消息（按时间倒序）。
func (m *MemoryMessage) ListMessages(_ context.Context, userID uint64, limit int) ([]*Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := m.byUser[userID]
	if limit > len(ids) {
		limit = len(ids)
	}
	result := make([]*Message, 0, limit)
	for i := len(ids) - 1; i >= 0 && len(result) < limit; i-- {
		if msg, ok := m.messages[ids[i]]; ok {
			result = append(result, msg)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt > result[j].CreatedAt
	})
	return result, nil
}
