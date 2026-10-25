// SPDX-License-Identifier: MIT
package native

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PresenceRow 是 n_presence 快照的一行（观测用，非 presence 真相源）。
type PresenceRow struct {
	UserID      int64
	State       string
	DeviceCount int
	LastSeen    time.Time
}

// SnapshotStore 是 presence 快照存储（PG 实现；nil 可禁用）。
type SnapshotStore interface {
	UpsertPresence(ctx context.Context, rows []PresenceRow) error
}

// PGStore 是 native 域的 PG 实现（C1 只含 presence 快照；C2/C3 扩 principal/会话/消息）。
type PGStore struct{ db *sql.DB }

// NewPGStore 构造 PG 存储。
func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{db: db}
}

// UpsertPresence 批量 upsert n_presence（每 30s 一次，只写活跃 user）。
// 语义：n_presence 不参与读路径判定，允许滞后/丢失；失败只 WARN。
func (s *PGStore) UpsertPresence(ctx context.Context, rows []PresenceRow) error {
	if s == nil || s.db == nil || len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("native: begin presence snapshot tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range rows {
		if r.UserID <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO n_presence (user_id, state, device_count, last_seen_at, updated_at)
			 VALUES ($1, $2, $3, $4, now())
			 ON CONFLICT (user_id) DO UPDATE
			   SET state = EXCLUDED.state,
			       device_count = EXCLUDED.device_count,
			       last_seen_at = EXCLUDED.last_seen_at,
			       updated_at = now()`,
			r.UserID, r.State, r.DeviceCount, r.LastSeen); err != nil {
			return fmt.Errorf("native: upsert n_presence user=%d: %w", r.UserID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("native: commit presence snapshot: %w", err)
	}
	return nil
}

// 编译期断言：PGStore 实现 SnapshotStore。
var _ SnapshotStore = (*PGStore)(nil)
