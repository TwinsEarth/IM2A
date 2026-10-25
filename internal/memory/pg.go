package memory

import (
	"context"
	"database/sql"
	"sort"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PGStore 基于 PostgreSQL 的记忆存储实现（v1.0.0）。
type PGStore struct {
	db *sql.DB
}

// NewPGStore 创建 PG 记忆存储。
func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{db: db}
}

// ---- Working ----

func (p *PGStore) PutWorking(ctx context.Context, s Scope, key string, value any) error {
	return p.PutWorkingWithTTL(ctx, s, key, value, 0)
}

func (p *PGStore) PutWorkingWithTTL(ctx context.Context, s Scope, key string, value any, ttlSeconds int64) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("memory: marshal value: %w", err)
	}
	var expires interface{}
	if ttlSeconds > 0 {
		expires = time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO working_memory (user_id, agent_id, key, value_json, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, agent_id, key) DO UPDATE
		SET value_json = EXCLUDED.value_json, expires_at = EXCLUDED.expires_at, created_at = NOW()
	`, s.UserID, s.AgentID, key, b, expires)
	return err
}

func (p *PGStore) GetWorking(ctx context.Context, s Scope, key string) (any, error) {
	var b []byte
	var expires sql.NullTime
	err := p.db.QueryRowContext(ctx, `
		SELECT value_json, expires_at FROM working_memory
		WHERE user_id = $1 AND agent_id = $2 AND key = $3
	`, s.UserID, s.AgentID, key).Scan(&b, &expires)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if expires.Valid && expires.Time.Before(time.Now()) {
		p.DeleteWorking(ctx, s, key)
		return nil, ErrNotFound
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func (p *PGStore) DeleteWorking(ctx context.Context, s Scope, key string) error {
	_, err := p.db.ExecContext(ctx,
		`DELETE FROM working_memory WHERE user_id = $1 AND agent_id = $2 AND key = $3`,
		s.UserID, s.AgentID, key)
	return err
}

// ---- Episodic ----

func (p *PGStore) AppendEpisode(ctx context.Context, s Scope, e *Episode) error {
	if e.ID == "" {
		e.ID = fmt.Sprintf("ep_%d_%s", e.Timestamp, s.AgentID)
	}
	if e.Timestamp == 0 {
		e.Timestamp = now()
	}
	tagsArr := e.Tags
	if tagsArr == nil {
		tagsArr = []string{}
	}
	metaObj := e.Meta
	if metaObj == nil {
		metaObj = map[string]any{}
	}
	metaJSON, _ := json.Marshal(metaObj)
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO episodes (id, user_id, agent_id, timestamp, kind, summary, result, tags, meta)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, e.ID, s.UserID, s.AgentID, e.Timestamp, e.Kind, e.Summary, e.Result, tagsArr, string(metaJSON))
	return err
}

func (p *PGStore) ListEpisodes(ctx context.Context, s Scope, q EpisodeQuery) ([]*Episode, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, timestamp, kind, summary, result, tags, meta FROM episodes
		WHERE user_id = $1 AND agent_id = $2`
	args := []any{s.UserID, s.AgentID}
	if q.Kind != "" {
		args = append(args, q.Kind)
		query += ` AND kind = $` + fmt.Sprintf("%d", len(args))
	}
	if q.Tag != "" {
		args = append(args, q.Tag)
		query += ` AND $` + fmt.Sprintf("%d", len(args)) + ` = ANY(tags)`
	}
	args = append(args, limit)
	query += ` ORDER BY timestamp DESC LIMIT $` + fmt.Sprintf("%d", len(args))
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Episode
	for rows.Next() {
		var e Episode
		var tagsArr []string
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Kind, &e.Summary, &e.Result, &tagsArr, &metaJSON); err != nil {
			return nil, err
		}
		e.Tags = tagsArr
		json.Unmarshal(metaJSON, &e.Meta)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ---- Semantic ----

func (p *PGStore) UpsertFact(ctx context.Context, s Scope, f *Fact) error {
	if f.UpdatedAt == 0 {
		f.UpdatedAt = now()
	}
	EnsureEmbedding(f)
	meta, _ := json.Marshal(f.Meta)
	var emb interface{}
	if len(f.Embedding) > 0 {
		eb, _ := json.Marshal(f.Embedding)
		emb = string(eb)
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO facts (user_id, agent_id, key, value, confidence, source, updated_at, meta, embedding)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id, agent_id, key) DO UPDATE
		SET value = EXCLUDED.value, confidence = EXCLUDED.confidence,
		    source = EXCLUDED.source, updated_at = EXCLUDED.updated_at,
		    meta = EXCLUDED.meta, embedding = EXCLUDED.embedding
	`, s.UserID, s.AgentID, f.Key, f.Value, f.Confidence, f.Source, f.UpdatedAt, string(meta), emb)
	return err
}

func (p *PGStore) GetFact(ctx context.Context, s Scope, key string) (*Fact, error) {
	var f Fact
	var metaJSON, embJSON []byte
	err := p.db.QueryRowContext(ctx, `
		SELECT key, value, confidence, source, updated_at, meta, embedding FROM facts
		WHERE user_id = $1 AND agent_id = $2 AND key = $3
	`, s.UserID, s.AgentID, key).Scan(&f.Key, &f.Value, &f.Confidence, &f.Source, &f.UpdatedAt, &metaJSON, &embJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	json.Unmarshal(metaJSON, &f.Meta)
	f.Embedding = parseEmbedding(embJSON)
	return &f, nil
}

// SearchFacts v1.9.4：真实向量余弦相似度检索，替换 JSONB ILIKE 子串硬匹配。
// 拉取该 scope 全部事实，用确定性向量与查询向量算余弦距离，按相似度降序返回 topK。
func (p *PGStore) SearchFacts(ctx context.Context, s Scope, keyword string) ([]*Fact, error) {
	all, err := p.ListFacts(ctx, s)
	if err != nil {
		return nil, err
	}
	qv := Embed(keyword)
	type scored struct {
		f *Fact
		s float32
	}
	ss := make([]scored, 0, len(all))
	for _, f := range all {
		ef := f.Embedding
		if len(ef) == 0 { // 兼容历史无向量数据
			ef = Embed(f.Key + " " + f.Value)
		}
		ss = append(ss, scored{f, Cosine(qv, ef)})
	}
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].s != ss[j].s {
			return ss[i].s > ss[j].s
		}
		return ss[i].f.Confidence > ss[j].f.Confidence
	})
	const topK = 20
	if len(ss) < topK {
		out := make([]*Fact, 0, len(ss))
		for _, x := range ss {
			out = append(out, x.f)
		}
		return out, nil
	}
	out := make([]*Fact, 0, topK)
	for _, i := range ss[:topK] {
		out = append(out, i.f)
	}
	return out, nil
}

func (p *PGStore) ListFacts(ctx context.Context, s Scope) ([]*Fact, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT key, value, confidence, source, updated_at, meta, embedding FROM facts
		WHERE user_id = $1 AND agent_id = $2 ORDER BY confidence DESC
	`, s.UserID, s.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

func (p *PGStore) DecayConfidence(ctx context.Context, s Scope, factor float64, min float64) (decayed, removed int, err error) {
	res, err := p.db.ExecContext(ctx, `
		UPDATE facts SET confidence = confidence * $1, updated_at = $2
		WHERE user_id = $3 AND agent_id = $4 AND confidence >= $5
	`, factor, now(), s.UserID, s.AgentID, min)
	if err != nil {
		return 0, 0, err
	}
	decayed64, _ := res.RowsAffected()
	res2, err := p.db.ExecContext(ctx, `
		DELETE FROM facts WHERE user_id = $1 AND agent_id = $2 AND confidence < $3
	`, s.UserID, s.AgentID, min)
	if err != nil {
		return int(decayed64), 0, err
	}
	removed64, _ := res2.RowsAffected()
	return int(decayed64), int(removed64), nil
}

func (p *PGStore) ReinforceFact(ctx context.Context, s Scope, key string, delta float64) (*Fact, error) {
	_, err := p.db.ExecContext(ctx, `
		UPDATE facts SET confidence = LEAST(1, GREATEST(0, confidence + $1)), updated_at = $2
		WHERE user_id = $3 AND agent_id = $4 AND key = $5
	`, delta, now(), s.UserID, s.AgentID, key)
	if err != nil {
		return nil, err
	}
	return p.GetFact(ctx, s, key)
}

func scanFacts(rows *sql.Rows) ([]*Fact, error) {
	var out []*Fact
	for rows.Next() {
		var f Fact
		var metaJSON, embJSON []byte
		if err := rows.Scan(&f.Key, &f.Value, &f.Confidence, &f.Source, &f.UpdatedAt, &metaJSON, &embJSON); err != nil {
			return nil, err
		}
		json.Unmarshal(metaJSON, &f.Meta)
		f.Embedding = parseEmbedding(embJSON)
		out = append(out, &f)
	}
	return out, rows.Err()
}

// parseEmbedding 反序列化 JSONB 中的 float32 向量；为空或非法时返回 nil。
func parseEmbedding(b []byte) []float32 {
	if len(b) == 0 {
		return nil
	}
	var v []float32
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	return v
}
