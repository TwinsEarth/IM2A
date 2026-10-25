// SPDX-License-Identifier: MIT
//
// C4：topic → recipient userID 列表 Resolver（内核侧，网关永远不解析 topic）。
//
// 解析规则（与 WAVE-C-DESIGN §6.2 对应）：
//   - approval:<uid> / trust:<uid> / user:<uid>：纯函数解析（connapi.TopicUserID），单播；
//   - negotiation:<sessionID>：协商会话参与双方 **agent id → owner users.id** 多播
//     （通过 agent_registry + account_identities 映射；会话本身来自 store.NegotiationStore）；
//   - subagent:<name>：agent_registry 该 agent 的 owner（可能多行 → 多播）；
//   - 其它：unmapped（**必须计数**，EventTap 会写 eventtap_unmapped_total{topic_prefix=...}）。
//
// 缓存：mapped 结果 TTL 30s（Resolver 在 EventTap 单 worker 内调用，不能每次查库）；
// unmapped 结果 TTL 5s（避免恶意 topic 打爆 DB，又能较快恢复）。
package native

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"human-agent-im/internal/connapi"
	"human-agent-im/internal/negotiation"
)

// NegotiationLoader 是 store.NegotiationStore 的最小子集（便于测试注入）。
type NegotiationLoader interface {
	LoadSession(ctx context.Context, id string) (*negotiation.Session, error)
}

// Resolver 把 topic 解析为 recipient userID 列表（users.id / principal delivery key）。
type Resolver struct {
	db   *sql.DB
	nego NegotiationLoader
	now  func() time.Time

	mappedTTL   time.Duration
	unmappedTTL time.Duration

	mu    sync.Mutex
	cache map[string]resolverEntry
}

type resolverEntry struct {
	users   []int64
	mapped  bool
	expires time.Time
}

// NewResolver 构造 Resolver（nego 可为 nil：negotiation topic 将 unmapped）。
func NewResolver(db *sql.DB, nego NegotiationLoader) *Resolver {
	return &Resolver{
		db: db, nego: nego, now: time.Now,
		mappedTTL: 30 * time.Second, unmappedTTL: 5 * time.Second,
		cache: map[string]resolverEntry{},
	}
}

// SetTTL 测试用：调整缓存 TTL。
func (r *Resolver) SetTTL(mapped, unmapped time.Duration) {
	r.mappedTTL, r.unmappedTTL = mapped, unmapped
}

// Resolve 返回 topic 对应的 recipient userID 列表与是否解析成功。
// 返回多元素列表表示多播；mapped=false 表示应计入 unmapped 指标（不静默丢）。
func (r *Resolver) Resolve(ctx context.Context, topic string) ([]int64, bool) {
	now := r.now()
	r.mu.Lock()
	if e, ok := r.cache[topic]; ok && now.Before(e.expires) {
		users := append([]int64(nil), e.users...)
		r.mu.Unlock()
		return users, e.mapped
	}
	r.mu.Unlock()

	users, mapped := r.resolveUncached(ctx, topic)
	if !mapped || len(users) == 0 {
		// 负缓存有害：数据可能“稍后才写好”（如 agent_registry/会话刚插入），
		// 空结果绝不缓存；只有成功映射才进缓存。
		return nil, false
	}
	r.mu.Lock()
	r.cache[topic] = resolverEntry{users: append([]int64(nil), users...), mapped: true, expires: now.Add(r.mappedTTL)}
	if len(r.cache) > 4096 { // 防内存放大：简单清空（极端 topic 洪水时丢弃缓存）
		r.cache = map[string]resolverEntry{}
	}
	r.mu.Unlock()
	return users, mapped
}

func (r *Resolver) resolveUncached(ctx context.Context, topic string) ([]int64, bool) {
	switch {
	case strings.HasPrefix(topic, "approval:"), strings.HasPrefix(topic, "trust:"), strings.HasPrefix(topic, "user:"):
		if uid, ok := connapi.TopicUserID(topic); ok {
			return []int64{uid}, true
		}
		return nil, false
	case strings.HasPrefix(topic, "negotiation:"):
		return r.resolveNegotiation(ctx, strings.TrimPrefix(topic, "negotiation:"))
	case strings.HasPrefix(topic, "subagent:"):
		return r.resolveSubagent(ctx, strings.TrimPrefix(topic, "subagent:"))
	default:
		return nil, false
	}
}

// resolveNegotiation 取会话参与双方的 agent ids，再映射到各自 owner users.id（多播、去重）。
func (r *Resolver) resolveNegotiation(ctx context.Context, sessionID string) ([]int64, bool) {
	if sessionID == "" || r.nego == nil {
		return nil, false
	}
	sess, err := r.nego.LoadSession(ctx, sessionID)
	if err != nil || sess == nil {
		return nil, false
	}
	agentIDs := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, t := range sess.Turns {
		for _, id := range []string{t.FromAgentID, t.ToAgentID} {
			if id != "" && !seen[id] {
				seen[id] = true
				agentIDs = append(agentIDs, id)
			}
		}
	}
	if len(agentIDs) == 0 {
		return nil, false
	}
	users, err := r.ownersForAgentIDs(ctx, agentIDs)
	if err != nil || len(users) == 0 {
		return nil, false
	}
	return users, true
}

// resolveSubagent 查 agent_registry 该 agent 的 owner wxid → users.id（可能多行→多播）。
func (r *Resolver) resolveSubagent(ctx context.Context, name string) ([]int64, bool) {
	if name == "" || r.db == nil {
		return nil, false
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT ai.user_id
		   FROM agent_registry ar
		   JOIN account_identities ai ON ai.provider='wechat' AND ai.open_id = ar.user_wxid
		  WHERE ar.agent_id = $1 AND ar.status = 'active'`, name)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	return scanUserIDs(rows)
}

// ownersForAgentIDs 把 agent ids 映射到 owner users.id（多播、去重）。
func (r *Resolver) ownersForAgentIDs(ctx context.Context, agentIDs []string) ([]int64, error) {
	if r.db == nil || len(agentIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(agentIDs))
	args := make([]any, len(agentIDs))
	for i, id := range agentIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT ai.user_id
		   FROM agent_registry ar
		   JOIN account_identities ai ON ai.provider='wechat' AND ai.open_id = ar.user_wxid
		  WHERE ar.agent_id IN (`+strings.Join(placeholders, ",")+`) AND ar.status='active'`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users, _ := scanUserIDs(rows)
	return users, nil
}

func scanUserIDs(rows *sql.Rows) ([]int64, bool) {
	var out []int64
	seen := map[int64]bool{}
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			continue
		}
		if uid > 0 && !seen[uid] {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// Debug 不做缓存直接解析一次（排查/测试用：输出当前真实 recipients）。
func (r *Resolver) Debug(ctx context.Context, topic string) ([]int64, bool) {
	return r.resolveUncached(ctx, topic)
}

// TopicPrefix 返回 topic 的前缀（用于 unmapped 指标 label；未知前缀记 "other"）。
func TopicPrefix(topic string) string {
	if i := strings.IndexByte(topic, ':'); i > 0 {
		p := topic[:i]
		switch p {
		case "approval", "trust", "user", "negotiation", "subagent", "security":
			return p
		}
	}
	return "other"
}

// CriticalTopic 判断该 topic 的事件是否 critical（慢消费者时优先投递，不可丢语义）：
//   - approval:* / trust:* / security:* → true（审批/信任/安全事件必须送达）；
//   - 其余（subagent 进度、普通事件）→ false（可丢，有日志可补发）。
func CriticalTopic(topic string) bool {
	switch TopicPrefix(topic) {
	case "approval", "trust", "security":
		return true
	}
	return false
}
