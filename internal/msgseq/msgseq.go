// Package msgseq 实现会话内消息序号的 64 分片锁生成器（§2.3）。
//
// 背景：IM 的核心并发瓶颈是消息序号的互斥生成，全局互斥锁在 2000 QPS 时
// 延迟可飙升至 300ms。方案：用户 ID 经 FNV-1a 哈希映射到 64 个分片，每个
// 分片独立加锁，锁竞争相对全局锁降低约 64 倍；分片队列超阈值时由编排层
// 动态拆片（v0.1 保留计数信号，扩容逻辑在 Phase 2 的 msg-svc 完整实现）。
package msgseq

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
)

// defaultShards 分片数。
const defaultShards = 64

var (
	// ErrInvalidBatch n<=0。
	ErrInvalidBatch = errors.New("msgseq: batch size must be positive")
	// ErrShardOverflow 单分片序号溢出（2^64，实际不可达）。
	ErrShardOverflow = errors.New("msgseq: shard sequence overflow")
)

// shard 单分片：独立互斥锁 + 分片内「每用户」序号 + 累计调用计数（拆片信号）。
// 注意：序号按用户（会话）维度连续，不按分片共享——多个用户可能哈希到同一分片，
// 若共用一个计数器会导致每个用户的序号出现空洞（跨用户抢占）。
type shard struct {
	mu    sync.Mutex
	seq   map[uint64]uint64 // userID → 该用户当前最大序号
	calls uint64
}

// Generator 分片锁消息序号生成器。
type Generator struct {
	shards      []shard
	splitThresh uint64 // 拆片信号阈值
}

// New 创建生成器。splitThresh<=0 时默认 1024（单分片累计调用次数）。
func New(splitThresh uint64) *Generator {
	if splitThresh == 0 {
		splitThresh = 1024
	}
	shards := make([]shard, defaultShards)
	for i := range shards {
		shards[i].seq = make(map[uint64]uint64)
	}
	return &Generator{shards: shards, splitThresh: splitThresh}
}

// Next 为指定用户生成下一条消息序号（会话内单调递增、连续无空洞）。
// 同一用户的序号在同一个分片内递增；不同用户并行无锁竞争。
func (g *Generator) Next(userID uint64) (uint64, error) {
	s := &g.shards[g.shardIndex(userID)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq[userID] == ^uint64(0) {
		return 0, ErrShardOverflow
	}
	s.seq[userID]++
	s.calls++
	return s.seq[userID], nil
}

// NextBatch 批量生成 n 条连续序号（离线消息补拉场景）。
func (g *Generator) NextBatch(userID uint64, n int) ([]uint64, error) {
	if n <= 0 {
		return nil, ErrInvalidBatch
	}
	s := &g.shards[g.shardIndex(userID)]
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.seq[userID]
	if cur+uint64(n) < cur { // 溢出检测
		return nil, ErrShardOverflow
	}
	out := make([]uint64, 0, n)
	for i := 0; i < n; i++ {
		cur++
		out = append(out, cur)
	}
	s.seq[userID] = cur
	s.calls += uint64(n)
	return out, nil
}

// HotShardIDs 返回累计调用次数超过拆片阈值的分片（拆片信号，供监控/编排层消费）。
func (g *Generator) HotShardIDs() []int {
	var hot []int
	for i := range g.shards {
		s := &g.shards[i]
		s.mu.Lock()
		if s.calls > g.splitThresh {
			hot = append(hot, i)
		}
		s.mu.Unlock()
	}
	return hot
}

// Stats 返回各分片累计生成序号（分片内所有用户序号之和，运维监控用）。
func (g *Generator) Stats() map[int]uint64 {
	out := make(map[int]uint64, len(g.shards))
	for i := range g.shards {
		s := &g.shards[i]
		s.mu.Lock()
		var sum uint64
		for _, v := range s.seq {
			sum += v
		}
		out[i] = sum
		s.mu.Unlock()
	}
	return out
}

// shardIndex 用户 ID → 分片：FNV-1a 哈希后取模。
func (g *Generator) shardIndex(userID uint64) int {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], userID)
	h := fnv.New64a()
	_, _ = h.Write(buf[:])
	return int(h.Sum64() % uint64(len(g.shards)))
}

// =============================================================================
// 进程级单例与启动播种（v2.3.27 / task-12：修"重启归零导致撞号"）
// =============================================================================
//
// 背景：仓库里曾有 3 处 `msgseq.New(1024)`（internal/api/msg.go 两处、
// services/msg-svc/main.go 一处）。注意 1024 是**拆片阈值 splitThresh**，分片数
// 固定为 defaultShards=64（见文件头与 shardIndex）。内存计数器在进程重启后从 0
// 重新开始，而 messages.seq 里已经有历史值，于是同一 sender 会拿到重复 seq
// （messages 表此前也没有 (sender_id,seq) 唯一约束，重复被静默接受）。
//
// 修复分三层，各管一段，互不替代：
//   1) 单例：同一进程内只有一个分配器，避免同进程两套计数器；
//   2) 播种 SeedFromDB：启动时把每个 sender 已用的最大 seq 灌回其所属分片，
//      使 Next 从 MAX+1 继续（分片算术不变：shard=fnv64a(LE(userID))%64）；
//   3) 唯一索引（迁移 037）：把"跨进程/重启仍撞号"从静默错序变成显式失败。
//
// 适用范围（v3 决策 D8=B，2026-10-25）：
//   本分配器服务的是**本地消息与 Agent 消息**（本地 /v1/msg/*、扣费流水、审批/事件等
//   需要本地有序与幂等的路径）；**人类社交消息的投递序与断线续传由腾讯云 IM PaaS 负责**。
//   因此：不要用这里的 seq 去和 PaaS 的 unread/游标对齐，也不要把它当作人类会话的续传位点；
//   同理 037 的唯一索引只约束本地 messages 表，不约束 PaaS。
var defaultGen atomic.Pointer[Generator]

// Default 返回进程级分配器；首次调用时创建（splitThresh 取 1024，与既有调用一致）。
func Default() *Generator {
	if g := defaultGen.Load(); g != nil {
		return g
	}
	g := New(1024)
	if defaultGen.CompareAndSwap(nil, g) {
		return g
	}
	return defaultGen.Load()
}

// SetDefault 替换进程级分配器并返回旧值（测试用；调用方负责用 defer 还原）。
// 生产代码不应在运行期替换——替换会让已发出的序号与内存状态脱节。
func SetDefault(g *Generator) *Generator {
	if g == nil {
		g = New(1024)
	}
	return defaultGen.Swap(g)
}

// Seed 设置某 sender 的"已用序号"，使后续 Next 返回 lastUsed+1。
// 只升不降：播种值小于当前内存值时忽略（防止重复调用把游标退回去）。
// 分片归属由 shardIndex 决定，与 Next 完全一致——不会把序号算到别的分片。
func (g *Generator) Seed(userID, lastUsed uint64) {
	if lastUsed == 0 {
		return
	}
	s := &g.shards[g.shardIndex(userID)]
	s.mu.Lock()
	if s.seq[userID] < lastUsed {
		s.seq[userID] = lastUsed
	}
	s.mu.Unlock()
}

// SeedFromDB 用 messages 表的实际最大序号播种。返回播种的 sender 数。
//
// 为什么是"每个 sender 各自 MAX"而不是全局 MAX：
//   序号是 **按 sender 独立** 的（shard.seq 是 map[userID]uint64，Next 只递增自己那一项）。
//   取全局 MAX 会让所有 sender 一起跳到最大发送者的序号，产生巨大空洞，并破坏
//   "同一 sender 在本地连续"的既有语义（本地 /v1/msg/* 与客户端去重都假设连续性；
//   人类消息的跨端连续性不归这里，见上方"适用范围"）。
//
// db 为 nil（内存模式）时直接返回 0,nil。表不存在时返回错误，由调用方决定是否降级。
func (g *Generator) SeedFromDB(ctx context.Context, db *sql.DB) (int, error) {
	if db == nil {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT sender_id, MAX(seq) FROM messages WHERE seq IS NOT NULL GROUP BY sender_id`)
	if err != nil {
		return 0, fmt.Errorf("msgseq: seed query: %w", err)
	}
	defer rows.Close()

	seeded := 0
	for rows.Next() {
		var senderID, maxSeq int64
		if err := rows.Scan(&senderID, &maxSeq); err != nil {
			return seeded, fmt.Errorf("msgseq: seed scan: %w", err)
		}
		if senderID <= 0 || maxSeq <= 0 {
			continue
		}
		g.Seed(uint64(senderID), uint64(maxSeq))
		seeded++
	}
	if err := rows.Err(); err != nil {
		return seeded, fmt.Errorf("msgseq: seed rows: %w", err)
	}
	return seeded, nil
}

// Summary 供自测/日志输出。
func (g *Generator) Summary() string {
	stats := g.Stats()
	var total uint64
	for _, v := range stats {
		total += v
	}
	return fmt.Sprintf("shards=%d total_seq=%d hot=%v", len(stats), total, g.HotShardIDs())
}
