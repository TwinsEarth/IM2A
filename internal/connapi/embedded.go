// SPDX-License-Identifier: MIT
//
// Embedded：用进程内 connhub.Hub 实现 EventSink（B2B-DESIGN §5）。
//
// 这是 CONN_MODE=embedded 时的投递出口：内核事件直接进 hub 的 64 shard 队列，
// 不经过 UDS、无跨进程代价。B2-a 的 /v1/ws 仍由 internal/stream.Hub 承载，
// 本实现供 B2-b 的投递链路在 embedded/dual 模式下复用同一 EventSink 抽象。
package connapi

import (
	"context"

	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

// Embedded 是 connhub.Hub 的 EventSink 适配器。
type Embedded struct {
	hub *connhub.Hub
}

// NewEmbedded 包装进程内 hub；hub 为 nil 时 Deliver/Stats 安全返回零值（不 panic）。
func NewEmbedded(hub *connhub.Hub) *Embedded { return &Embedded{hub: hub} }

// Deliver 投递到进程内 hub（非阻塞，B1 hub 语义）。
func (e *Embedded) Deliver(userID int64, frames []connproto.Frame) connhub.DeliverResult {
	if e == nil || e.hub == nil {
		return connhub.DeliverResult{}
	}
	return e.hub.Deliver(userID, frames)
}

// Stats 返回进程内 hub 的实时统计。
func (e *Embedded) Stats() connhub.Stats {
	if e == nil || e.hub == nil {
		return connhub.Stats{}
	}
	return e.hub.Stats()
}

// Close 无操作（hub 生命周期由装配方管理）。
func (e *Embedded) Close(_ context.Context) error { return nil }
