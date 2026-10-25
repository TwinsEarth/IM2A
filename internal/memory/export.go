// 记忆导出/导入（v0.6.2，数据主权雏形）。
//
// 导出某用户下全部 Agent 的三层记忆为 JSON，支持：
//   - Context 可移植性（用户换设备/换服务商时带走自己的记忆）；
//   - 备份与审计；
//   - 为 v1.0「本地加密 + Context 导出」打基础。
//
// 安全：导出/导入按 userID 隔离；GuardedStore 层校验调用方是该用户的
// 总 Agent（master），不允许跨用户导出。
package memory

import (
	"context"
	"sort"

	"human-agent-im/internal/id"
)

// ScopeData 单个 Agent 的记忆快照。
type ScopeData struct {
	AgentID  string         `json:"agentId"`
	Working  map[string]any `json:"working"`
	Facts    []*Fact        `json:"facts"`
	Episodes []*Episode     `json:"episodes"`
}

// UserMemoryExport 一个用户的全部记忆导出包。
type UserMemoryExport struct {
	UserID uint64                `json:"userId"`
	Scopes map[string]*ScopeData `json:"scopes"` // agentID -> data
}

// ExportUser 导出某用户全部 Agent 的记忆。
func (m *MemoryStore) ExportUser(_ context.Context, userID uint64) *UserMemoryExport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := &UserMemoryExport{UserID: userID, Scopes: map[string]*ScopeData{}}
	prefix := scopeKeyRaw(userID, "")
	for k, d := range m.data {
		// k 形如 "123/agent-id"；只取属于该 userID 的 scope
		if !hasPrefix(k, prefix) {
			continue
		}
		agentID := k[len(prefix):]
		sd := &ScopeData{
			AgentID: agentID,
			Working: map[string]any{},
			Facts:   []*Fact{},
		}
		for key, e := range d.working {
			if e.expiresAt > 0 && now() > e.expiresAt {
				continue // 跳过已过期项
			}
			sd.Working[key] = e.value
		}
		for _, f := range d.facts {
			sd.Facts = append(sd.Facts, f)
		}
		sort.Slice(sd.Facts, func(i, j int) bool { return sd.Facts[i].Key < sd.Facts[j].Key })
		for _, ep := range d.episodes {
			sd.Episodes = append(sd.Episodes, ep)
		}
		sort.Slice(sd.Episodes, func(i, j int) bool { return sd.Episodes[i].Timestamp < sd.Episodes[j].Timestamp })
		out.Scopes[agentID] = sd
	}
	return out
}

// ImportUser 导入记忆包（覆盖同 key 的 fact；append episode；覆盖 working）。
func (m *MemoryStore) ImportUser(ctx context.Context, exp *UserMemoryExport) error {
	if exp == nil {
		return ErrNotFound
	}
	for agentID, sd := range exp.Scopes {
		sc := Scope{UserID: exp.UserID, AgentID: agentID}
		for k, v := range sd.Working {
			_ = m.PutWorking(ctx, sc, k, v)
		}
		for _, f := range sd.Facts {
			_ = m.UpsertFact(ctx, sc, f)
		}
		for _, ep := range sd.Episodes {
			_ = m.AppendEpisode(ctx, sc, ep)
		}
	}
	return nil
}

// UserStats 某用户的记忆统计。
type UserStats struct {
	UserID       uint64 `json:"userId"`
	AgentCount   int    `json:"agentCount"`
	FactCount    int    `json:"factCount"`
	EpisodeCount int    `json:"episodeCount"`
	WorkingCount int    `json:"workingCount"`
}

// StatsUser 统计某用户记忆规模。
func (m *MemoryStore) StatsUser(_ context.Context, userID uint64) UserStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	prefix := scopeKeyRaw(userID, "")
	st := UserStats{UserID: userID}
	for k, d := range m.data {
		if !hasPrefix(k, prefix) {
			continue
		}
		st.AgentCount++
		st.FactCount += len(d.facts)
		st.EpisodeCount += len(d.episodes)
		for _, e := range d.working {
			if e.expiresAt == 0 || now() <= e.expiresAt {
				st.WorkingCount++
			}
		}
	}
	return st
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

// Snapshot 某 scope 的事实快照（v0.6.5，可回滚）。
type Snapshot struct {
	ID        string  `json:"id"`
	Scope     Scope   `json:"scope"`
	Timestamp int64   `json:"timestamp"`
	Facts     []*Fact `json:"facts"`
}

// TakeSnapshot 对某 scope 的事实做快照（master 调用）。
func (m *MemoryStore) TakeSnapshot(_ context.Context, s Scope) (*Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.data[scopeKey(s)]
	if !ok {
		return nil, ErrNotFound
	}
	facts := make([]*Fact, 0, len(d.facts))
	for _, f := range d.facts {
		cp := *f
		facts = append(facts, &cp)
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].Key < facts[j].Key })
	return &Snapshot{
		ID:        id.UUIDv4(),
		Scope:     s,
		Timestamp: now(),
		Facts:     facts,
	}, nil
}

// RestoreSnapshot 用快照覆盖某 scope 的事实（删除现有 facts 后写入快照）。
func (m *MemoryStore) RestoreSnapshot(ctx context.Context, snap *Snapshot) error {
	if snap == nil {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.ns(snap.Scope)
	d.facts = map[string]*Fact{}
	for _, f := range snap.Facts {
		cp := *f
		d.facts[cp.Key] = &cp
	}
	return nil
}
