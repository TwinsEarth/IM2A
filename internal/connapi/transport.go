// SPDX-License-Identifier: MIT
//
// Package connapi 是内核侧的连接投递出口（B2-b，契约 B2B-DESIGN.md §5）。
//
// 设计要点：
//   - 内核只认 userID 寻址：topic（approval:<uid>/trust:<uid>/…）→ userID 的映射在内核侧完成
//     （见 topic.go），网关侧没有 topic 语义；
//   - EventSink 有两种实现：embedded = 进程内 connhub.Hub（B1 已交付，见 embedded.go）；
//     standalone/dual = remote UDS 客户端（remote.go，等 connproto UDS 协议定稿后实现）；
//   - Deliver 必须非阻塞：内核 agent turn 绝不能因为 conn-gateway 不在/变慢而被拖住。
//     投递失败只增长降级计数——数据本身已在 im_delivery_log 里，客户端重连时按位点补发
//     （ack 持久化与补发由 internal/connapi/delivery 负责）；
//   - 网关从不重投、只上报"写到哪"，重投由内核的 delivery store 负责。
//
// UDS 线格式的唯一权威是 internal/connproto/uds.go（openclaw-arch 交付，B2-b 共享面）：
// 6B 外壳 len:u32|kind:u8|flags:u8 + body；DeliverReq 为二进制，其余控制消息为 JSON。
// 本包不重复定义任何帧格式。
package connapi

import (
	"context"

	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

// EventSink 是内核→网关的投递出口（B2B-DESIGN.md §5）。
//
//   - embedded：connhub.Hub 直接满足（Deliver 返回真实的 DeliverResult）；
//   - standalone/dual：remote 实现非阻塞入队，返回本地估算结果，Stats 为最近一次
//     网关 StatsResp 的缓存（允许滞后，设计允许）。
type EventSink interface {
	// Deliver 非阻塞：内部有界队列，满则丢弃非关键帧并计数（关键帧因日志已持久化，
	// 可安全等待补发；这里只计数不阻塞调用方）。
	Deliver(userID int64, frames []connproto.Frame) connhub.DeliverResult
	// Stats 返回投递面统计（remote 实现允许滞后于网关真实值）。
	Stats() connhub.Stats
}

// Closer 是可选优雅关闭能力（remote 会 flush 后关连接；embedded 无操作）。
type Closer interface {
	Close(ctx context.Context) error
}
