// Package memory 实现 IM2A 的 Context 记忆分层（v0.6）。
//
// 对应技术设计基线 v2.0「Context 飞轮」：Context 是核心资产，模型可租、
// Context 不可租。记忆分为三层：
//
//   - Working（工作记忆）：当前会话/任务的临时上下文，短 TTL、会话级；
//   - Episodic（情景记忆）：办事轨迹、审批历史，时间线、追加写；
//   - Semantic（语义记忆）：偏好、底线、习惯，可检索、可蒸馏，带置信度与来源。
//
// 隔离规则：所有记忆按 userID + agentID 双键命名空间归属。子 Agent 只能读写
// 自己的记忆；跨 Agent 访问必须经总 Agent 授权（复用 trustzone Capability 门控）。
package memory

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound 记忆项不存在。
var ErrNotFound = errors.New("memory: key not found")

// ErrAccessDenied 跨 Agent 越权访问被拒绝。
var ErrAccessDenied = errors.New("memory: cross-agent access denied")

// Scope 记忆命名空间（用户 + Agent 双键）。
type Scope struct {
	UserID  uint64 `json:"userId"`
	AgentID string `json:"agentId"`
}

// Episode 情景记忆条目：一次办事轨迹（任务/审批/工具调用）。
type Episode struct {
	ID        string         `json:"id"`
	Timestamp int64          `json:"timestamp"`
	Kind      string         `json:"kind"` // task / approval / tool_call / message
	Summary   string         `json:"summary"`
	Result    string         `json:"result"` // success / failed / pending
	Tags      []string       `json:"tags,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// Fact 语义记忆条目：一条沉淀下来的偏好/底线/习惯。
type Fact struct {
	Key        string         `json:"key"`
	Value      string         `json:"value"`
	Confidence float64        `json:"confidence"` // 0..1
	Source     string         `json:"source"`     // manual / approval / distilled
	UpdatedAt  int64          `json:"updatedAt"`
	Meta       map[string]any `json:"meta,omitempty"`
	// Embedding 语义向量（v1.0.2 预留列）。pgvector 未启用时由 PG 以 JSONB 持久化，
	// 应用可真实存取；装好 pgvector 后可平滑迁移为 vector(n) 并启用向量检索。
	Embedding []float32 `json:"embedding,omitempty"`
}

// EpisodeQuery 情景记忆查询。
type EpisodeQuery struct {
	Kind  string
	Tag   string // v0.6.4：按标签过滤
	Limit int
}

// Store 记忆存储统一接口。
//
// 全部方法显式接收 Scope：实现层必须保证不同 Scope 之间数据隔离，
// 不允许出现跨用户/跨 Agent 的数据泄露。
type Store interface {
	// ---- Working：工作记忆（KV，会话级）----
	PutWorking(ctx context.Context, s Scope, key string, value any) error
	// PutWorkingWithTTL 写入带过期时间的工作记忆（ttlSeconds<=0 永不过期）。
	PutWorkingWithTTL(ctx context.Context, s Scope, key string, value any, ttlSeconds int64) error
	GetWorking(ctx context.Context, s Scope, key string) (any, error)
	DeleteWorking(ctx context.Context, s Scope, key string) error

	// ---- Episodic：情景记忆（追加写时间线）----
	AppendEpisode(ctx context.Context, s Scope, e *Episode) error
	ListEpisodes(ctx context.Context, s Scope, q EpisodeQuery) ([]*Episode, error)

	// ---- Semantic：语义记忆（事实/偏好）----
	UpsertFact(ctx context.Context, s Scope, f *Fact) error
	GetFact(ctx context.Context, s Scope, key string) (*Fact, error)
	SearchFacts(ctx context.Context, s Scope, keyword string) ([]*Fact, error)
	ListFacts(ctx context.Context, s Scope) ([]*Fact, error)
	// DecayConfidence 衰减旧事实置信度：factor 为衰减因子(0,1]，低于 min 则删除。
	DecayConfidence(ctx context.Context, s Scope, factor float64, min float64) (decayed, removed int, err error)
	// ReinforceFact 强化/弱化事实置信度（delta 正负；裁剪到 [0,1]）。
	ReinforceFact(ctx context.Context, s Scope, key string, delta float64) (*Fact, error)
}

// now 便于测试替换。
var now = func() int64 { return time.Now().Unix() }
