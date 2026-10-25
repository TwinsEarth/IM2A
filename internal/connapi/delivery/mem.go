// SPDX-License-Identifier: MIT
//
// MemStore 是 delivery.Store 的内存实现：用于单元测试与无 PG 环境（selftest/本地）。
// 语义与 PGStore 保持一致（GREATEST 单调、epoch 校验、三上限截断、MIN 位点裁剪）。
package delivery

import (
	"context"
	"sync"
)

// MemStore 内存投递存储。并发安全。
type MemStore struct {
	mu      sync.Mutex
	opt     Options
	lastSeq map[int64]uint64
	logs    map[int64][]Entry
	cursors map[string]DeviceCursor
}

// NewMemStore 构造内存存储；opt 零值自动补默认（保留期 7 天、time.Now）。
func NewMemStore(opt Options) *MemStore {
	return &MemStore{
		opt:     opt.normalized(),
		lastSeq: map[int64]uint64{},
		logs:    map[int64][]Entry{},
		cursors: map[string]DeviceCursor{},
	}
}

// Append 分配连续 seq 并写日志（内存实现天然原子；PG 实现走单事务）。
func (m *MemStore) Append(_ context.Context, userID int64, frames []AppendFrame) ([]Entry, error) {
	if userID <= 0 {
		return nil, ErrInvalidArgument
	}
	if len(frames) == 0 {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.opt.Now()
	base := m.lastSeq[userID]
	out := make([]Entry, 0, len(frames))
	for i, f := range frames {
		seq := base + uint64(i) + 1
		e := Entry{
			UserID:    userID,
			Seq:       seq,
			MsgType:   f.MsgType,
			Flags:     f.Flags,
			Payload:   append([]byte(nil), f.Payload...),
			CreatedAt: now,
		}
		out = append(out, e)
		m.logs[userID] = append(m.logs[userID], e)
	}
	m.lastSeq[userID] = base + uint64(len(frames))
	return out, nil
}

// Ack 单调推进设备位点（GREATEST 语义，重复/乱序幂等）。
func (m *MemStore) Ack(_ context.Context, userID int64, deviceID string, ackedSeq uint64) (DeviceCursor, error) {
	if userID <= 0 || deviceID == "" {
		return DeviceCursor{}, ErrInvalidArgument
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.cursors[deviceID]
	if !ok {
		cur = DeviceCursor{DeviceID: deviceID, UserID: userID}
	}
	if ackedSeq > cur.AckedSeq {
		cur.AckedSeq = ackedSeq
	}
	cur.UpdatedAt = m.opt.Now()
	m.cursors[deviceID] = cur
	return cur, nil
}

// Cursor 查询设备位点。
func (m *MemStore) Cursor(_ context.Context, deviceID string) (DeviceCursor, bool, error) {
	if deviceID == "" {
		return DeviceCursor{}, false, ErrInvalidArgument
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.cursors[deviceID]
	return cur, ok, nil
}

// EnsureDevice 保证位点存在（新设备 epoch=0）。
func (m *MemStore) EnsureDevice(_ context.Context, userID int64, deviceID string) (DeviceCursor, error) {
	if userID <= 0 || deviceID == "" {
		return DeviceCursor{}, ErrInvalidArgument
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.cursors[deviceID]
	if !ok {
		cur = DeviceCursor{DeviceID: deviceID, UserID: userID, UpdatedAt: m.opt.Now()}
		m.cursors[deviceID] = cur
	}
	return cur, nil
}

// BumpEpoch 让旧 resumeToken 失效（踢人/解绑）：epoch+1。
func (m *MemStore) BumpEpoch(_ context.Context, userID int64, deviceID string) (int64, error) {
	if userID <= 0 || deviceID == "" {
		return 0, ErrInvalidArgument
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.cursors[deviceID]
	if !ok {
		cur = DeviceCursor{DeviceID: deviceID, UserID: userID}
	}
	cur.ConnEpoch++
	cur.UpdatedAt = m.opt.Now()
	m.cursors[deviceID] = cur
	return cur.ConnEpoch, nil
}

// LastSeq 返回已分配最大 seq。
func (m *MemStore) LastSeq(_ context.Context, userID int64) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastSeq[userID], nil
}

// PlanReplay 生成补发计划（epoch 校验 + 三上限 + oldest/dropped_before/truncated）。
func (m *MemStore) PlanReplay(_ context.Context, req ReplayRequest) (ReplayPlan, error) {
	if req.UserID <= 0 {
		return ReplayPlan{}, ErrInvalidArgument
	}
	limits := req.Limits.normalized()
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, exists := m.cursors[req.DeviceID]
	if exists && cur.ConnEpoch != req.ConnEpoch {
		return ReplayPlan{OK: false, Reason: "epoch_mismatch", Epoch: cur.ConnEpoch}, ErrEpochMismatch
	}
	from := req.FromSeq
	if exists {
		from = cur.AckedSeq
	} else if req.DeviceID != "" {
		// 新设备（无位点）：按客户端声明的 last_ack 建档，epoch 采用请求值。
		cur = DeviceCursor{DeviceID: req.DeviceID, UserID: req.UserID, AckedSeq: req.FromSeq, ConnEpoch: req.ConnEpoch, UpdatedAt: m.opt.Now()}
		m.cursors[req.DeviceID] = cur
	}
	plan := ReplayPlan{OK: true, Epoch: cur.ConnEpoch}

	cutoff := m.opt.Now().Add(-limits.MaxAge)
	available := make([]Entry, 0, 16)
	for _, e := range m.logs[req.UserID] {
		if e.Seq <= from {
			continue
		}
		if e.CreatedAt.Before(cutoff) {
			continue // 超过 7 天，不可补发
		}
		available = append(available, e)
	}
	if len(available) == 0 {
		last := m.lastSeq[req.UserID]
		if last > from {
			plan.Truncated = true
			plan.DroppedBefore = last - from
		}
		return plan, nil
	}
	plan.OldestAvailable = available[0].Seq
	if plan.OldestAvailable > from+1 {
		plan.DroppedBefore = plan.OldestAvailable - from - 1
	}
	plan.From = available[0].Seq

	frames, bytes := 0, 0
	var to uint64
	var replayed uint32
	for i, e := range available {
		pl := len(e.Payload)
		if frames > 0 && (frames >= limits.MaxFrames || bytes+pl > limits.MaxBytes) {
			break
		}
		if frames == 0 && limits.MaxFrames < 1 {
			break
		}
		frames++
		bytes += pl
		to = e.Seq
		replayed++
		_ = i
	}
	plan.To = to
	plan.Replayed = replayed
	if plan.DroppedBefore > 0 || frames < len(available) {
		plan.Truncated = true
	}
	return plan, nil
}

// Page 取一页补发帧：seq ∈ (fromSeq, toSeq]，升序，受 maxFrames/maxBytes 约束（至少 1 帧）。
func (m *MemStore) Page(_ context.Context, userID int64, fromSeq, toSeq uint64, maxFrames, maxBytes int) ([]Entry, error) {
	if userID <= 0 {
		return nil, ErrInvalidArgument
	}
	if maxFrames <= 0 {
		maxFrames = DefaultPageFrames
	}
	if maxBytes <= 0 {
		maxBytes = DefaultPageBytes
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, 0, 16)
	bytes := 0
	for _, e := range m.logs[userID] {
		if e.Seq <= fromSeq || (toSeq > 0 && e.Seq > toSeq) {
			continue
		}
		pl := len(e.Payload)
		if len(out) > 0 && (len(out) >= maxFrames || bytes+pl > maxBytes) {
			break
		}
		out = append(out, e)
		bytes += pl
	}
	return out, nil
}

// Trim 只删"该用户所有设备 MIN 位点之前 + 超过保留期"的日志；无位点不裁。
func (m *MemStore) Trim(_ context.Context, userID int64, maxDelete int) (int64, error) {
	if userID <= 0 {
		return 0, ErrInvalidArgument
	}
	if maxDelete <= 0 {
		maxDelete = DefaultTrimBatch
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var minAck uint64
	found := false
	for _, cur := range m.cursors {
		if cur.UserID != userID {
			continue
		}
		if !found || cur.AckedSeq < minAck {
			minAck = cur.AckedSeq
			found = true
		}
	}
	if !found || minAck == 0 {
		return 0, nil // 没有位点（或位点还是 0）→ 不裁剪，避免误删待补发
	}
	cutoff := m.opt.Now().Add(-m.opt.Retention)
	logs := m.logs[userID]
	var deleted int64
	cut := 0
	for _, e := range logs {
		if deleted >= int64(maxDelete) {
			break
		}
		if e.Seq <= minAck && e.CreatedAt.Before(cutoff) {
			cut++
			deleted++
			continue
		}
		break // seq 升序：遇到第一条不满足就停
	}
	if cut > 0 {
		m.logs[userID] = append([]Entry(nil), logs[cut:]...)
	}
	return deleted, nil
}
