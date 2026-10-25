package memory

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

// v2.3.23：外部渠道身份（微信 wxid，字符串）→ 记忆层 bigint user_id 的稳定映射。
//
// 记忆三层表的 user_id 是 bigint。为避免微信渠道与真实 users.id（从 1 起的小整数）
// 共用同一命名空间导致串用户，映射出的记忆 user_id 统一加 L4SubjectOffset 高位偏移。

// L4SubjectOffset 微信渠道记忆命名空间的高位偏移。
const L4SubjectOffset uint64 = 1_000_000_000

// SubjectResolver 把外部渠道字符串身份解析为稳定的记忆命名空间 user_id。
// 同一 externalID 多次解析必须返回同一值（幂等）。
type SubjectResolver interface {
	SubjectID(ctx context.Context, externalID string) (uint64, error)
}

// PGSubjectResolver 基于 l4_subjects 表的稳定身份解析。
type PGSubjectResolver struct {
	db *sql.DB
}

func NewPGSubjectResolver(db *sql.DB) *PGSubjectResolver {
	return &PGSubjectResolver{db: db}
}

// SubjectID 幂等解析 wxid → 记忆命名空间 user_id（高位偏移 + 序列 id）。
func (r *PGSubjectResolver) SubjectID(ctx context.Context, wxid string) (uint64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("memory: subject resolver db nil")
	}
	if wxid == "" {
		return 0, errors.New("memory: empty external id")
	}
	var seq int64
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO l4_subjects (wxid) VALUES ($1)
		ON CONFLICT (wxid) DO UPDATE SET wxid = EXCLUDED.wxid
		RETURNING id
	`, wxid).Scan(&seq)
	if err != nil {
		return 0, err
	}
	return L4SubjectOffset + uint64(seq), nil
}

// MemSubjectResolver 内存版身份解析（单测用，不跨进程）。
type MemSubjectResolver struct {
	mu   sync.Mutex
	next int64
	m    map[string]int64
}

func NewMemSubjectResolver() *MemSubjectResolver {
	return &MemSubjectResolver{next: 1, m: map[string]int64{}}
}

func (r *MemSubjectResolver) SubjectID(_ context.Context, wxid string) (uint64, error) {
	if wxid == "" {
		return 0, errors.New("memory: empty external id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.m[wxid]; ok {
		return L4SubjectOffset + uint64(id), nil
	}
	id := r.next
	r.next++
	r.m[wxid] = id
	return L4SubjectOffset + uint64(id), nil
}
