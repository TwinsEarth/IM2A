// SPDX-License-Identifier: MIT
//
// Package delivery 是内核侧「投递序号 / 投递日志 / 设备位点」持久层（B2-b，契约 B2B-DESIGN.md）。
//
// 三条硬约束（设计 §0，全部由本包落地）：
//  1. 先写位点再回执：Ack() 用 GREATEST 单调推进 im_device_cursor，落库成功才算确认；
//  2. 网关从不重投、只上报 ack：重发由 PlanReplay/Page 从 im_delivery_log 读出来；
//  3. 补发有上限，超限显式告知：PlanReplay 返回 Truncated/OldestAvailable/DroppedBefore，
//     禁止静默截断。
//
// 不变量：persisted_cursor(device) ≤ gateway.lastAck(conn) ≤ 持久化日志的最大连续 seq。
package delivery

import (
	"context"
	"errors"
	"time"
)

// 消息类型（B2B-DESIGN §1.2 msg_type，与 connproto.Frame.Type 的语义对齐）。
const (
	MsgTypeMSG      uint8 = 1 // 业务消息（critical）
	MsgTypeEVT      uint8 = 2 // 事件（非 critical）
	MsgTypePresence uint8 = 3 // 在线状态
	MsgTypeKick     uint8 = 4 // 踢下线
	MsgTypeERR      uint8 = 5 // 协议错误
)

// FlagCritical 对应 connproto.FlagCritical（bit0）：关键帧不可丢。
const FlagCritical uint8 = 1 << 0

// 默认值（B2B-DESIGN §3.2 / §4.3）。
const (
	DefaultMaxReplayFrames = 2000               // 单次重连最多补发帧数
	DefaultMaxReplayBytes  = 2 << 20            // 单次重连最多补发字节数（2 MiB）
	DefaultMaxReplayAge    = 7 * 24 * time.Hour // 日志可补发时效
	DefaultPageFrames      = 200                // 单页帧数
	DefaultPageBytes       = 256 << 10          // 单页字节数（256 KiB）
	DefaultRetention       = 7 * 24 * time.Hour // Trim 保留期
	DefaultTrimBatch       = 1000               // 单次 Trim 最多删除行数
)

// 错误。
var (
	// ErrEpochMismatch conn_epoch 不匹配：拒绝 resume，客户端应全量重连（设计 §1.1）。
	ErrEpochMismatch = errors.New("delivery: conn_epoch mismatch")
	// ErrInvalidArgument 参数非法（userID<=0 等）。
	ErrInvalidArgument = errors.New("delivery: invalid argument")
)

// AppendFrame 是一条待写日志的帧（尚未分配 seq）。
type AppendFrame struct {
	MsgType uint8
	Flags   uint8
	Payload []byte
}

// Entry 是 im_delivery_log 的一行（含已分配 seq）。
type Entry struct {
	UserID    int64
	Seq       uint64
	MsgType   uint8
	Flags     uint8
	Payload   []byte
	CreatedAt time.Time
}

// Critical 是否关键帧（不可丢，慢消费者场景优先保留）。
func (e Entry) Critical() bool { return e.Flags&FlagCritical != 0 }

// DeviceCursor 是 im_device_cursor 的一行（设备级位点）。
type DeviceCursor struct {
	DeviceID  string
	UserID    int64
	AckedSeq  uint64
	ConnEpoch int64
	UpdatedAt time.Time
}

// ReplayLimits 是补发的三个上限（任一触发即截断）。
type ReplayLimits struct {
	MaxFrames  int
	MaxBytes   int
	MaxAge     time.Duration
	PageFrames int // 单页帧数（Page 调用方使用）
	PageBytes  int // 单页字节数
}

// DefaultReplayLimits 返回设计默认值。
func DefaultReplayLimits() ReplayLimits {
	return ReplayLimits{
		MaxFrames:  DefaultMaxReplayFrames,
		MaxBytes:   DefaultMaxReplayBytes,
		MaxAge:     DefaultMaxReplayAge,
		PageFrames: DefaultPageFrames,
		PageBytes:  DefaultPageBytes,
	}
}

// normalized 把零值补成默认值。
func (l ReplayLimits) normalized() ReplayLimits {
	d := DefaultReplayLimits()
	if l.MaxFrames <= 0 {
		l.MaxFrames = d.MaxFrames
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxAge <= 0 {
		l.MaxAge = d.MaxAge
	}
	if l.PageFrames <= 0 {
		l.PageFrames = d.PageFrames
	}
	if l.PageBytes <= 0 {
		l.PageBytes = d.PageBytes
	}
	return l
}

// ReplayRequest 是一次重连补发计划请求。
// FromSeq 是客户端声明/内核持久位点（exclusive）：只补 seq > FromSeq。
type ReplayRequest struct {
	UserID    int64
	DeviceID  string
	ConnEpoch int64
	FromSeq   uint64
	Limits    ReplayLimits
}

// ReplayPlan 是补发计划的元数据（对应 connproto.ResumeResp 的字段）。
type ReplayPlan struct {
	OK              bool   // false 时 Reason 说明拒绝原因（epoch_mismatch 等）
	Reason          string // 拒绝原因
	From            uint64 // 本次补发起点（含）
	To              uint64 // 本次补发终点（含）；无补发时为 0
	Replayed        uint32 // 本次计划补发帧数
	Truncated       bool   // 是否不完整（客户端必须显示缺口并 REST 增量拉取）
	OldestAvailable uint64 // 服务端当前还能补发的最早 seq（0=无日志）
	DroppedBefore   uint64 // [FromSeq+1, OldestAvailable-1] 永久缺失帧数
	Epoch           int64  // 当前设备 conn_epoch（用于 resumeToken）
}

// Store 是投递持久层的抽象：PGStore（生产）与 MemStore（测试/无 PG 环境）。
//
// 并发约定：实现必须并发安全；Append 的序号分配在存储内串行（PG 走单语句 UPSERT 事务）。
type Store interface {
	// Append 为 userID 的 frames 分配连续 seq 并写日志（发号+写日志同一事务）。
	// 返回的 entries 与入参顺序一致，未提交的 seq 不得对外投递（由调用方保证 commit 后投递）。
	Append(ctx context.Context, userID int64, frames []AppendFrame) ([]Entry, error)

	// Ack 单调推进设备位点：ackedSeq 只增不减（GREATEST），重复/乱序 ack 幂等。
	// 设备不存在时按 (userID,deviceID) 建档（epoch=0）。返回推进后的位点。
	Ack(ctx context.Context, userID int64, deviceID string, ackedSeq uint64) (DeviceCursor, error)

	// Cursor 查询设备位点；不存在返回 (zero,false,nil)。
	Cursor(ctx context.Context, deviceID string) (DeviceCursor, bool, error)

	// EnsureDevice 保证设备位点存在（新设备 epoch=0），返回当前位点。
	EnsureDevice(ctx context.Context, userID int64, deviceID string) (DeviceCursor, error)

	// BumpEpoch 让旧 resumeToken 立即失效（踢人/解绑）：epoch+1 并返回新值。
	BumpEpoch(ctx context.Context, userID int64, deviceID string) (int64, error)

	// PlanReplay 生成补发计划：epoch 校验 + 三上限 + oldest/dropped_before/truncated。
	// 设备位点不存在时按"新设备"处理（不裁剪、从请求的 FromSeq 开始）。
	PlanReplay(ctx context.Context, req ReplayRequest) (ReplayPlan, error)

	// Page 取一页补发帧：seq 升序，范围 (fromSeq, toSeq]，受 maxFrames/maxBytes 约束。
	Page(ctx context.Context, userID int64, fromSeq, toSeq uint64, maxFrames, maxBytes int) ([]Entry, error)

	// Trim 裁剪日志：只删该用户所有设备 MIN 位点之前的、且超过保留期的行；无位点不裁。
	// 返回删除行数；maxDelete<=0 用 DefaultTrimBatch。
	Trim(ctx context.Context, userID int64, maxDelete int) (int64, error)

	// LastSeq 返回该用户当前已分配的最大 seq（0=从未发号）。
	LastSeq(ctx context.Context, userID int64) (uint64, error)
}

// Options 是存储构造参数（保留期与时钟可注入，便于测试）。
type Options struct {
	Retention time.Duration    // Trim 保留期，<=0 用 DefaultRetention
	Now       func() time.Time // 时钟，nil 用 time.Now
}

// normalized 返回补齐默认值后的 Options。
func (o Options) normalized() Options {
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}
