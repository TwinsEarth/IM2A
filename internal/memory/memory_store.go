// 内存版记忆存储（v0.6，默认实现，离线自测零外部依赖）。
//
// 生产环境由 PG 实现替换（v0.11），接口不变。
// 隔离方式：map 以 scopeKey（userID/agentID）为顶层键。
package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"human-agent-im/internal/id"
)

// scopeKey 双键命名空间的序列化键。
func scopeKey(s Scope) string {
	return scopeKeyRaw(s.UserID, s.AgentID)
}

func scopeKeyRaw(userID uint64, agentID string) string {
	return uintToStr(userID) + "/" + agentID
}

func uintToStr(n uint64) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

type workingEntry struct {
	value     any
	expiresAt int64 // 0 = 永不过期；>0 为 unix 秒
}

type nsData struct {
	working  map[string]*workingEntry
	episodes []*Episode
	facts    map[string]*Fact
}

// MemoryStore 内存记忆存储。
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]*nsData
}

// NewMemoryStore 创建内存记忆存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: map[string]*nsData{}}
}

func (m *MemoryStore) ns(s Scope) *nsData {
	k := scopeKey(s)
	d, ok := m.data[k]
	if !ok {
		d = &nsData{
			working: map[string]*workingEntry{},
			facts:   map[string]*Fact{},
		}
		m.data[k] = d
	}
	return d
}

// ---- Working ----

func (m *MemoryStore) PutWorking(_ context.Context, s Scope, key string, value any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ns(s).working[key] = &workingEntry{value: value, expiresAt: 0}
	return nil
}

func (m *MemoryStore) PutWorkingWithTTL(_ context.Context, s Scope, key string, value any, ttlSeconds int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var exp int64
	if ttlSeconds > 0 {
		exp = now() + ttlSeconds
	}
	m.ns(s).working[key] = &workingEntry{value: value, expiresAt: exp}
	return nil
}

func (m *MemoryStore) GetWorking(_ context.Context, s Scope, key string) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.ns(s)
	e, ok := d.working[key]
	if !ok {
		return nil, ErrNotFound
	}
	if e.expiresAt > 0 && now() > e.expiresAt {
		delete(d.working, key) // 惰性过期
		return nil, ErrNotFound
	}
	return e.value, nil
}

func (m *MemoryStore) DeleteWorking(_ context.Context, s Scope, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.ns(s).working, key)
	return nil
}

// ---- Episodic ----

func (m *MemoryStore) AppendEpisode(_ context.Context, s Scope, e *Episode) error {
	if e == nil {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.ns(s)
	// v0.6.3：按 ID 幂等（导入时重复 episode 不重复追加）
	if e.ID != "" {
		for _, existing := range d.episodes {
			if existing.ID == e.ID {
				return nil
			}
		}
	}
	if e.ID == "" {
		e.ID = id.UUIDv4()
	}
	if e.Timestamp == 0 {
		e.Timestamp = now()
	}
	d.episodes = append(d.episodes, e)
	return nil
}

func (m *MemoryStore) ListEpisodes(_ context.Context, s Scope, q EpisodeQuery) ([]*Episode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d := m.ns(s)
	result := make([]*Episode, 0, len(d.episodes))
	for _, e := range d.episodes {
		if q.Kind != "" && e.Kind != q.Kind {
			continue
		}
		if q.Tag != "" && !containsTag(e.Tags, q.Tag) {
			continue
		}
		result = append(result, e)
	}
	// 时间倒序（最新在前）
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp > result[j].Timestamp
	})
	if q.Limit > 0 && q.Limit < len(result) {
		result = result[:q.Limit]
	}
	return result, nil
}

// ---- Semantic ----

func (m *MemoryStore) UpsertFact(_ context.Context, s Scope, f *Fact) error {
	if f == nil || f.Key == "" {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if f.UpdatedAt == 0 {
		f.UpdatedAt = now()
	}
	m.ns(s).facts[f.Key] = f
	return nil
}

func (m *MemoryStore) GetFact(_ context.Context, s Scope, key string) (*Fact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.ns(s).facts[key]
	if !ok {
		return nil, ErrNotFound
	}
	return f, nil
}

func (m *MemoryStore) ListFacts(_ context.Context, s Scope) ([]*Fact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d := m.ns(s)
	result := make([]*Fact, 0, len(d.facts))
	for _, f := range d.facts {
		result = append(result, f)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})
	return result, nil
}

// SearchFacts 简单子串匹配（内存实现；PG 版用 pgvector 语义检索）。
func (m *MemoryStore) SearchFacts(_ context.Context, s Scope, keyword string) ([]*Fact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d := m.ns(s)
	kw := strings.ToLower(keyword)
	result := make([]*Fact, 0)
	for _, f := range d.facts {
		if keyword == "" ||
			strings.Contains(strings.ToLower(f.Key), kw) ||
			strings.Contains(strings.ToLower(f.Value), kw) {
			result = append(result, f)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Confidence > result[j].Confidence
	})
	return result, nil
}

// DecayConfidence 衰减旧事实置信度（v0.6.3，模拟记忆遗忘）。
// 未被近期审批/强化的事实会逐渐降权，低于 min 的删除。
func (m *MemoryStore) DecayConfidence(_ context.Context, s Scope, factor float64, min float64) (int, int, error) {
	if factor <= 0 || factor > 1 {
		factor = 0.9
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.ns(s)
	decayed, removed := 0, 0
	for k, f := range d.facts {
		f.Confidence *= factor
		decayed++
		if f.Confidence < min {
			delete(d.facts, k)
			removed++
			decayed--
		}
	}
	return decayed, removed, nil
}

// SearchAllFacts 遍历某用户下所有 Agent 的事实（v0.6.3，master 全局搜索）。
func (m *MemoryStore) SearchAllFacts(_ context.Context, userID uint64, keyword string) ([]*Fact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	prefix := scopeKeyRaw(userID, "")
	kw := strings.ToLower(keyword)
	result := make([]*Fact, 0)
	for k, d := range m.data {
		if !hasPrefix(k, prefix) {
			continue
		}
		for _, f := range d.facts {
			if keyword == "" ||
				strings.Contains(strings.ToLower(f.Key), kw) ||
				strings.Contains(strings.ToLower(f.Value), kw) {
				result = append(result, f)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Confidence > result[j].Confidence
	})
	return result, nil
}

// ReinforceFact 强化/弱化事实置信度（v0.6.4，审批反馈调节）。
func (m *MemoryStore) ReinforceFact(_ context.Context, s Scope, key string, delta float64) (*Fact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.ns(s).facts[key]
	if !ok {
		return nil, ErrNotFound
	}
	f.Confidence += delta
	if f.Confidence < 0 {
		f.Confidence = 0
	}
	if f.Confidence > 1 {
		f.Confidence = 1
	}
	f.UpdatedAt = now()
	return f, nil
}

// GCWorking 清理某 scope 下所有已过期工作记忆，返回清理数量（v0.6.4）。
func (m *MemoryStore) GCWorking(_ context.Context, s Scope) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.ns(s)
	removed := 0
	for k, e := range d.working {
		if e.expiresAt > 0 && now() > e.expiresAt {
			delete(d.working, k)
			removed++
		}
	}
	return removed, nil
}

func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// 编译期断言：MemoryStore 实现 Store 接口。
var _ Store = (*MemoryStore)(nil)
