// 记忆访问控制（v0.6）：子 Agent 隔离与跨 Agent 授权。
//
// 规则：
//   - 子 Agent 只能访问自己的 Scope（同 userID + agentID）；
//   - 总 Agent（master）可访问本用户下任意子 Agent 的记忆；
//   - 子 Agent 跨 Agent 访问，必须先由总 Agent 显式 Grant（读/写）；
//   - 任何跨用户（userID 不同）访问一律拒绝，即使是 master。
//
// 该层与 trustzone 门控理念一致：隔离是代码层强制，不依赖模型自觉。
package memory

import (
	"context"
	"sync"

	"human-agent-im/internal/eventbus"
)

// Access 访问级别。
type Access int

const (
	AccessNone Access = iota
	AccessRead
	AccessWrite
)

// GuardedStore 带访问控制的记忆存储。
type GuardedStore struct {
	inner Store
	mu    sync.RWMutex
	// masterAgents: userID -> 总 AgentID（该用户的管家）
	masterAgents map[uint64]string
	// grants: scopeKeyRaw(owner) -> 授权访客集合
	// 形如 "1/sub-procurement" -> {"1/sub-legal": AccessRead}
	grants map[string]map[string]Access
	// bus 可选：记忆变更事件发布（v0.6.5）
	bus *eventbus.Bus
}

// NewGuarded 包装一个底层 Store。
func NewGuarded(inner Store) *GuardedStore {
	return &GuardedStore{
		inner:        inner,
		masterAgents: map[uint64]string{},
		grants:       map[string]map[string]Access{},
	}
}

// SetBus 注入事件总线（可选，用于蒸馏/审计订阅）。
func (g *GuardedStore) SetBus(b *eventbus.Bus) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bus = b
}

// publish 发布记忆变更事件（失败不阻断主流程）。
func (g *GuardedStore) publish(ctx context.Context, sc Scope, t eventbus.EventType, payload map[string]any) {
	g.mu.RLock()
	bus := g.bus
	g.mu.RUnlock()
	if bus == nil {
		return
	}
	_ = bus.Publish(ctx, &eventbus.Event{
		Type: t, UserID: sc.UserID, AgentID: sc.AgentID, Payload: payload,
	})
}

// RegisterMaster 注册某用户的总 Agent（管家）。
func (g *GuardedStore) RegisterMaster(userID uint64, masterAgentID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.masterAgents[userID] = masterAgentID
}

// Grant 由总 Agent 给访客 Agent 授权访问目标 Scope。
// owner 是被访问的记忆归属；visitor 是发起访问的 Agent。
func (g *GuardedStore) Grant(owner Scope, visitorAgentID string, a Access) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ownerKey := scopeKey(owner)
	if g.grants[ownerKey] == nil {
		g.grants[ownerKey] = map[string]Access{}
	}
	g.grants[ownerKey][scopeKeyRaw(owner.UserID, visitorAgentID)] = a
}

// Revoke 撤销授权。
func (g *GuardedStore) Revoke(owner Scope, visitorAgentID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.grants[scopeKey(owner)], scopeKeyRaw(owner.UserID, visitorAgentID))
}

// check 判定 caller 对 target Scope 的访问级别。
func (g *GuardedStore) check(caller, target Scope) Access {
	// 跨用户永远拒绝
	if caller.UserID != target.UserID {
		return AccessNone
	}
	// 自己访问自己
	if caller.AgentID == target.AgentID {
		return AccessWrite
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	// 总 Agent 访问本用户任意子 Agent
	if master, ok := g.masterAgents[caller.UserID]; ok && caller.AgentID == master {
		return AccessWrite
	}
	// 显式授权
	if set, ok := g.grants[scopeKey(target)]; ok {
		return set[scopeKeyRaw(caller.UserID, caller.AgentID)]
	}
	return AccessNone
}

func (g *GuardedStore) need(caller, target Scope, want Access) error {
	if g.check(caller, target) >= want {
		return nil
	}
	return ErrAccessDenied
}

// ---- Working ----

func (g *GuardedStore) PutWorking(ctx context.Context, caller Scope, key string, v any) error {
	if err := g.need(caller, caller, AccessWrite); err != nil {
		return err
	}
	return g.inner.PutWorking(ctx, caller, key, v)
}

func (g *GuardedStore) PutWorkingWithTTL(ctx context.Context, caller Scope, key string, v any, ttl int64) error {
	if err := g.need(caller, caller, AccessWrite); err != nil {
		return err
	}
	return g.inner.PutWorkingWithTTL(ctx, caller, key, v, ttl)
}

func (g *GuardedStore) GetWorking(ctx context.Context, caller Scope, key string) (any, error) {
	if err := g.need(caller, caller, AccessRead); err != nil {
		return nil, err
	}
	return g.inner.GetWorking(ctx, caller, key)
}

// ReadWorkingFrom 跨 Agent 读取工作记忆（需授权）。
func (g *GuardedStore) ReadWorkingFrom(ctx context.Context, caller, target Scope, key string) (any, error) {
	if err := g.need(caller, target, AccessRead); err != nil {
		return nil, err
	}
	return g.inner.GetWorking(ctx, target, key)
}

func (g *GuardedStore) DeleteWorking(ctx context.Context, caller Scope, key string) error {
	if err := g.need(caller, caller, AccessWrite); err != nil {
		return err
	}
	return g.inner.DeleteWorking(ctx, caller, key)
}

// ---- Episodic ----

func (g *GuardedStore) AppendEpisode(ctx context.Context, caller Scope, e *Episode) error {
	if err := g.need(caller, caller, AccessWrite); err != nil {
		return err
	}
	if err := g.inner.AppendEpisode(ctx, caller, e); err != nil {
		return err
	}
	g.publish(ctx, caller, eventbus.EventTypeMemoryEpisodeAppend, map[string]any{
		"episodeId": e.ID, "kind": e.Kind, "summary": e.Summary, "result": e.Result,
	})
	return nil
}

func (g *GuardedStore) ListEpisodes(ctx context.Context, caller Scope, q EpisodeQuery) ([]*Episode, error) {
	return g.inner.ListEpisodes(ctx, caller, q)
}

// ListEpisodesOf 跨 Agent 读取情景记忆（需授权）。
func (g *GuardedStore) ListEpisodesOf(ctx context.Context, caller, target Scope, q EpisodeQuery) ([]*Episode, error) {
	if err := g.need(caller, target, AccessRead); err != nil {
		return nil, err
	}
	return g.inner.ListEpisodes(ctx, target, q)
}

// ---- Semantic ----

func (g *GuardedStore) UpsertFact(ctx context.Context, caller Scope, f *Fact) error {
	if err := g.need(caller, caller, AccessWrite); err != nil {
		return err
	}
	if err := g.inner.UpsertFact(ctx, caller, f); err != nil {
		return err
	}
	g.publish(ctx, caller, eventbus.EventTypeMemoryFactUpsert, map[string]any{
		"key": f.Key, "value": f.Value, "confidence": f.Confidence, "source": f.Source,
	})
	return nil
}

func (g *GuardedStore) GetFact(ctx context.Context, caller Scope, key string) (*Fact, error) {
	return g.inner.GetFact(ctx, caller, key)
}

func (g *GuardedStore) SearchFacts(ctx context.Context, caller Scope, kw string) ([]*Fact, error) {
	return g.inner.SearchFacts(ctx, caller, kw)
}

func (g *GuardedStore) ListFacts(ctx context.Context, caller Scope) ([]*Fact, error) {
	return g.inner.ListFacts(ctx, caller)
}

// ReadFactsOf 跨 Agent 读取语义记忆（需授权）。
func (g *GuardedStore) ReadFactsOf(ctx context.Context, caller, target Scope) ([]*Fact, error) {
	if err := g.need(caller, target, AccessRead); err != nil {
		return nil, err
	}
	return g.inner.ListFacts(ctx, target)
}

// ExportUser 导出某用户全部记忆（仅该用户的 master 可调）。
func (g *GuardedStore) ExportUser(ctx context.Context, caller Scope) (*UserMemoryExport, error) {
	g.mu.RLock()
	master, ok := g.masterAgents[caller.UserID]
	g.mu.RUnlock()
	if !ok || caller.AgentID != master {
		return nil, ErrAccessDenied
	}
	return g.inner.(*MemoryStore).ExportUser(ctx, caller.UserID), nil
}

// ImportUser 导入记忆包（仅该用户的 master 可调）。
func (g *GuardedStore) ImportUser(ctx context.Context, caller Scope, exp *UserMemoryExport) error {
	g.mu.RLock()
	master, ok := g.masterAgents[caller.UserID]
	g.mu.RUnlock()
	if !ok || caller.AgentID != master {
		return ErrAccessDenied
	}
	return g.inner.(*MemoryStore).ImportUser(ctx, exp)
}

// StatsUser 统计某用户记忆（仅该用户的 master 可调）。
func (g *GuardedStore) StatsUser(ctx context.Context, caller Scope) (UserStats, error) {
	g.mu.RLock()
	master, ok := g.masterAgents[caller.UserID]
	g.mu.RUnlock()
	if !ok || caller.AgentID != master {
		return UserStats{}, ErrAccessDenied
	}
	return g.inner.(*MemoryStore).StatsUser(ctx, caller.UserID), nil
}

// DecayConfidence 衰减某 Agent 自己的事实置信度。
func (g *GuardedStore) DecayConfidence(ctx context.Context, caller Scope, factor, min float64) (int, int, error) {
	if err := g.need(caller, caller, AccessWrite); err != nil {
		return 0, 0, err
	}
	return g.inner.DecayConfidence(ctx, caller, factor, min)
}

// ReinforceFact 强化/弱化某 Agent 自己的事实。
func (g *GuardedStore) ReinforceFact(ctx context.Context, caller Scope, key string, delta float64) (*Fact, error) {
	if err := g.need(caller, caller, AccessWrite); err != nil {
		return nil, err
	}
	f, err := g.inner.ReinforceFact(ctx, caller, key, delta)
	if err != nil {
		return nil, err
	}
	g.publish(ctx, caller, eventbus.EventTypeMemoryReinforce, map[string]any{
		"key": key, "delta": delta, "newConfidence": f.Confidence,
	})
	return f, nil
}

// GCWorking 清理某 Agent 自己的过期工作记忆。
func (g *GuardedStore) GCWorking(ctx context.Context, caller Scope) (int, error) {
	if err := g.need(caller, caller, AccessWrite); err != nil {
		return 0, err
	}
	return g.inner.(*MemoryStore).GCWorking(ctx, caller)
}

// TakeSnapshot 对某 scope 事实做快照（master 可调）。
func (g *GuardedStore) TakeSnapshot(ctx context.Context, caller, target Scope) (*Snapshot, error) {
	g.mu.RLock()
	master, ok := g.masterAgents[caller.UserID]
	g.mu.RUnlock()
	if !ok || caller.AgentID != master || caller.UserID != target.UserID {
		return nil, ErrAccessDenied
	}
	return g.inner.(*MemoryStore).TakeSnapshot(ctx, target)
}

// RestoreSnapshot 用快照回滚某 scope 事实（master 可调）。
func (g *GuardedStore) RestoreSnapshot(ctx context.Context, caller Scope, snap *Snapshot) error {
	g.mu.RLock()
	master, ok := g.masterAgents[caller.UserID]
	g.mu.RUnlock()
	if !ok || caller.AgentID != master || snap.Scope.UserID != caller.UserID {
		return ErrAccessDenied
	}
	return g.inner.(*MemoryStore).RestoreSnapshot(ctx, snap)
}

// SearchAllFacts 跨子 Agent 全局搜索事实（仅该用户 master 可调）。
func (g *GuardedStore) SearchAllFacts(ctx context.Context, caller Scope, keyword string) ([]*Fact, error) {
	g.mu.RLock()
	master, ok := g.masterAgents[caller.UserID]
	g.mu.RUnlock()
	if !ok || caller.AgentID != master {
		return nil, ErrAccessDenied
	}
	return g.inner.(*MemoryStore).SearchAllFacts(ctx, caller.UserID, keyword)
}

var _ Store = (*GuardedStore)(nil)
