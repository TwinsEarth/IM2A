// SPDX-License-Identifier: MIT
//
// PGStore 是 delivery.Store 的 PostgreSQL 实现（B2B-DESIGN.md §1/§3/§4.3）。
//
// 并发/幂等要点：
//   - Append 用 `ON CONFLICT DO UPDATE ... RETURNING last_seq` 原子分配区间，再在同事务写日志；
//     未 commit 的 seq 不会被投递（调用方拿到返回才会 Deliver）；
//   - Ack 用 `GREATEST(acked_seq, EXCLUDED.acked_seq)`，乱序/重复 ack 幂等；
//   - PlanReplay 按 (user_id, seq) 主键 + created_at 过滤计算 oldest/dropped_before/truncated；
//   - Trim 只删该用户所有设备 MIN 位点之前且超过保留期的行；无位点不裁。
package delivery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PGStore 基于 PostgreSQL 的投递存储。
type PGStore struct {
	db  *sql.DB
	opt Options
}

// NewPGStore 构造 PG 存储；db 必须可用。opt 零值自动补默认。
func NewPGStore(db *sql.DB, opt Options) *PGStore {
	return &PGStore{db: db, opt: opt.normalized()}
}

// Append 同一事务内发号 + 写日志；返回与入参顺序一致的 entries。
func (s *PGStore) Append(ctx context.Context, userID int64, frames []AppendFrame) ([]Entry, error) {
	if userID <= 0 {
		return nil, ErrInvalidArgument
	}
	if len(frames) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("delivery: begin append tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var last uint64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO im_user_delivery_seq (user_id, last_seq)
		 VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE
		   SET last_seq = im_user_delivery_seq.last_seq + $2, updated_at = now()
		 RETURNING last_seq`,
		userID, len(frames)).Scan(&last); err != nil {
		return nil, fmt.Errorf("delivery: alloc seq: %w", err)
	}
	base := last - uint64(len(frames))
	now := s.opt.Now()
	out := make([]Entry, 0, len(frames))
	for i, f := range frames {
		seq := base + uint64(i) + 1
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO im_delivery_log (user_id, seq, msg_type, flags, payload, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			userID, seq, int(f.MsgType), int(f.Flags), f.Payload, now); err != nil {
			return nil, fmt.Errorf("delivery: append log seq=%d: %w", seq, err)
		}
		out = append(out, Entry{UserID: userID, Seq: seq, MsgType: f.MsgType, Flags: f.Flags,
			Payload: append([]byte(nil), f.Payload...), CreatedAt: now})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("delivery: commit append: %w", err)
	}
	return out, nil
}

// Ack 单调推进设备位点（GREATEST）；设备不存在则建档（epoch=0）。
func (s *PGStore) Ack(ctx context.Context, userID int64, deviceID string, ackedSeq uint64) (DeviceCursor, error) {
	if userID <= 0 || deviceID == "" {
		return DeviceCursor{}, ErrInvalidArgument
	}
	var cur DeviceCursor
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO im_device_cursor (device_id, user_id, acked_seq, conn_epoch, updated_at)
		 VALUES ($1, $2, $3, 0, now())
		 ON CONFLICT (device_id) DO UPDATE
		   SET acked_seq = GREATEST(im_device_cursor.acked_seq, EXCLUDED.acked_seq),
		       updated_at = now()
		 RETURNING device_id, user_id, acked_seq, conn_epoch, updated_at`,
		deviceID, userID, ackedSeq).
		Scan(&cur.DeviceID, &cur.UserID, &cur.AckedSeq, &cur.ConnEpoch, &cur.UpdatedAt)
	if err != nil {
		return DeviceCursor{}, fmt.Errorf("delivery: ack cursor: %w", err)
	}
	return cur, nil
}

// Cursor 查询设备位点。
func (s *PGStore) Cursor(ctx context.Context, deviceID string) (DeviceCursor, bool, error) {
	if deviceID == "" {
		return DeviceCursor{}, false, ErrInvalidArgument
	}
	var cur DeviceCursor
	err := s.db.QueryRowContext(ctx,
		`SELECT device_id, user_id, acked_seq, conn_epoch, updated_at FROM im_device_cursor WHERE device_id = $1`,
		deviceID).Scan(&cur.DeviceID, &cur.UserID, &cur.AckedSeq, &cur.ConnEpoch, &cur.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceCursor{}, false, nil
	}
	if err != nil {
		return DeviceCursor{}, false, fmt.Errorf("delivery: select cursor: %w", err)
	}
	return cur, true, nil
}

// EnsureDevice 保证位点存在（新设备 epoch=0）。
func (s *PGStore) EnsureDevice(ctx context.Context, userID int64, deviceID string) (DeviceCursor, error) {
	if userID <= 0 || deviceID == "" {
		return DeviceCursor{}, ErrInvalidArgument
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO im_device_cursor (device_id, user_id, acked_seq, conn_epoch, updated_at)
		 VALUES ($1, $2, 0, 0, now()) ON CONFLICT (device_id) DO NOTHING`,
		deviceID, userID); err != nil {
		return DeviceCursor{}, fmt.Errorf("delivery: ensure device: %w", err)
	}
	cur, _, err := s.Cursor(ctx, deviceID)
	return cur, err
}

// BumpEpoch 让旧 resumeToken 失效（踢人/解绑）：epoch+1。
func (s *PGStore) BumpEpoch(ctx context.Context, userID int64, deviceID string) (int64, error) {
	if userID <= 0 || deviceID == "" {
		return 0, ErrInvalidArgument
	}
	var epoch int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO im_device_cursor (device_id, user_id, acked_seq, conn_epoch, updated_at)
		 VALUES ($1, $2, 0, 1, now())
		 ON CONFLICT (device_id) DO UPDATE
		   SET conn_epoch = im_device_cursor.conn_epoch + 1, updated_at = now()
		 RETURNING conn_epoch`,
		deviceID, userID).Scan(&epoch)
	if err != nil {
		return 0, fmt.Errorf("delivery: bump epoch: %w", err)
	}
	return epoch, nil
}

// LastSeq 返回已分配最大 seq。
func (s *PGStore) LastSeq(ctx context.Context, userID int64) (uint64, error) {
	var last uint64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_seq FROM im_user_delivery_seq WHERE user_id = $1`, userID).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("delivery: last seq: %w", err)
	}
	return last, nil
}

// PlanReplay 生成补发计划（epoch 校验 + 三上限 + oldest/dropped_before/truncated）。
func (s *PGStore) PlanReplay(ctx context.Context, req ReplayRequest) (ReplayPlan, error) {
	if req.UserID <= 0 {
		return ReplayPlan{}, ErrInvalidArgument
	}
	limits := req.Limits.normalized()

	from := req.FromSeq
	epoch := req.ConnEpoch
	if req.DeviceID != "" {
		cur, ok, err := s.Cursor(ctx, req.DeviceID)
		if err != nil {
			return ReplayPlan{}, err
		}
		if ok {
			if cur.ConnEpoch != req.ConnEpoch {
				return ReplayPlan{OK: false, Reason: "epoch_mismatch", Epoch: cur.ConnEpoch}, ErrEpochMismatch
			}
			from = cur.AckedSeq
			epoch = cur.ConnEpoch
		} else {
			// 新设备：按客户端声明的 last_ack 建档（epoch 采用请求值）。
			if _, err := s.db.ExecContext(ctx,
				`INSERT INTO im_device_cursor (device_id, user_id, acked_seq, conn_epoch, updated_at)
				 VALUES ($1, $2, $3, $4, now()) ON CONFLICT (device_id) DO NOTHING`,
				req.DeviceID, req.UserID, req.FromSeq, req.ConnEpoch); err != nil {
				return ReplayPlan{}, fmt.Errorf("delivery: create cursor: %w", err)
			}
		}
	}
	plan := ReplayPlan{OK: true, Epoch: epoch}

	cutoff := s.opt.Now().Add(-limits.MaxAge)
	// 最早可用 seq（在时效内且 > from）。
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(seq) FROM im_delivery_log
		  WHERE user_id = $1 AND seq > $2 AND created_at >= $3`,
		req.UserID, from, cutoff).Scan(&oldest); err != nil {
		return ReplayPlan{}, fmt.Errorf("delivery: oldest available: %w", err)
	}
	if !oldest.Valid {
		last, err := s.LastSeq(ctx, req.UserID)
		if err != nil {
			return ReplayPlan{}, err
		}
		if last > from {
			plan.Truncated = true
			plan.DroppedBefore = last - from
		}
		return plan, nil
	}
	plan.OldestAvailable = uint64(oldest.Int64)
	if plan.OldestAvailable > from+1 {
		plan.DroppedBefore = plan.OldestAvailable - from - 1
	}
	plan.From = plan.OldestAvailable

	// 取候选行（≤ MaxFrames）计算本次实际补发区间（再受字节上限约束）。
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, octet_length(payload) FROM im_delivery_log
		  WHERE user_id = $1 AND seq > $2 AND created_at >= $3
		  ORDER BY seq LIMIT $4`,
		req.UserID, from, cutoff, limits.MaxFrames)
	if err != nil {
		return ReplayPlan{}, fmt.Errorf("delivery: replay plan rows: %w", err)
	}
	defer rows.Close()
	frames, bytes := 0, 0
	var to uint64
	for rows.Next() {
		var seq uint64
		var size int
		if err := rows.Scan(&seq, &size); err != nil {
			return ReplayPlan{}, fmt.Errorf("delivery: scan replay plan: %w", err)
		}
		if frames > 0 && bytes+size > limits.MaxBytes {
			break
		}
		frames++
		bytes += size
		to = seq
	}
	if err := rows.Err(); err != nil {
		return ReplayPlan{}, fmt.Errorf("delivery: replay plan rows: %w", err)
	}
	plan.To = to
	plan.Replayed = uint32(frames)
	// 是否还有更多（帧数上限之外）：用 COUNT 对比判断截断。
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM im_delivery_log
		  WHERE user_id = $1 AND seq > $2 AND created_at >= $3`,
		req.UserID, from, cutoff).Scan(&total); err != nil {
		return ReplayPlan{}, fmt.Errorf("delivery: replay count: %w", err)
	}
	if plan.DroppedBefore > 0 || frames < total {
		plan.Truncated = true
	}
	return plan, nil
}

// Page 取一页补发帧。
func (s *PGStore) Page(ctx context.Context, userID int64, fromSeq, toSeq uint64, maxFrames, maxBytes int) ([]Entry, error) {
	if userID <= 0 {
		return nil, ErrInvalidArgument
	}
	if maxFrames <= 0 {
		maxFrames = DefaultPageFrames
	}
	if maxBytes <= 0 {
		maxBytes = DefaultPageBytes
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, seq, msg_type, flags, payload, created_at FROM im_delivery_log
		  WHERE user_id = $1 AND seq > $2 AND ($3 = 0 OR seq <= $3)
		  ORDER BY seq LIMIT $4`,
		userID, fromSeq, toSeq, maxFrames)
	if err != nil {
		return nil, fmt.Errorf("delivery: replay page: %w", err)
	}
	defer rows.Close()
	out := make([]Entry, 0, maxFrames)
	bytes := 0
	for rows.Next() {
		var e Entry
		var msgType, flags int
		if err := rows.Scan(&e.UserID, &e.Seq, &msgType, &flags, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("delivery: scan replay page: %w", err)
		}
		e.MsgType, e.Flags = uint8(msgType), uint8(flags)
		if len(out) > 0 && bytes+len(e.Payload) > maxBytes {
			break
		}
		out = append(out, e)
		bytes += len(e.Payload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delivery: replay page rows: %w", err)
	}
	return out, nil
}

// Trim 只删"所有设备 MIN 位点之前 + 超过保留期"的日志；无位点不裁。
func (s *PGStore) Trim(ctx context.Context, userID int64, maxDelete int) (int64, error) {
	if userID <= 0 {
		return 0, ErrInvalidArgument
	}
	if maxDelete <= 0 {
		maxDelete = DefaultTrimBatch
	}
	// 无位点 → MIN 为 NULL → DELETE 不命中任何行（设计 §4.3：不裁剪）。
	res, err := s.db.ExecContext(ctx,
		`WITH min_ack AS (
		     SELECT MIN(acked_seq) AS seq FROM im_device_cursor WHERE user_id = $1
		 ),
		 victims AS (
		     SELECT l.ctid FROM im_delivery_log l, min_ack
		      WHERE l.user_id = $1
		        AND min_ack.seq IS NOT NULL
		        AND l.seq <= min_ack.seq
		        AND l.created_at < now() - make_interval(secs => $3)
		      LIMIT $2
		 )
		 DELETE FROM im_delivery_log d USING victims v WHERE d.ctid = v.ctid`,
		userID, maxDelete, s.opt.Retention.Seconds())
	if err != nil {
		return 0, fmt.Errorf("delivery: trim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delivery: trim rows affected: %w", err)
	}
	return n, nil
}

// 编译期断言：PGStore 实现 Store。
var _ Store = (*PGStore)(nil)
