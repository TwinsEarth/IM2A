package connhub

// ring.go：每连接的有界下行队列。
//
// 双约束（CONN-GATEWAY-DESIGN.md v2 §5.2）：
//   * 槽位：固定的 RingFrames（默认 256）；
//   * 字节：RingBytes（默认 16384）——**字节预算才是权威水位**。
// 判定规则（顺序很重要）：
//   1) 字节将超预算：
//        - 非关键帧 → 丢弃（degraded/droppedNonCritical）
//        - 关键帧   → 返回 errByteFull，上层判定"慢消费者"并断开（100% 水位动作）
//   2) 槽位满但字节没满 → 返回 errSlotFull。**这不算"满水位"**，也不断开连接：
//      上层短暂背压（notify writer）后重试；仅当连续多次 slotFull 才升级处理。
//   3) 正常入队；入队后字节水位 >= 80% → 标记 degraded（只触发一次回调），
//      之后非关键帧一律丢弃（这就是"降级：合并/丢弃非关键帧"的落点）。
//
// 为什么不用 chan []byte：chan 没有字节水位，1000 个 64KB 元素 = 64MB/连接。
//
// **task-24 内存优化（惰性分配）**：旧实现 `make([]Frame, maxFrames)` 让每条**空闲**连接也
// 预占 256×64B = 16KB（5k 空闲连接 = 80MB 纯浪费）。现在改成惰性增长的队列：
//   * 空队列零分配（idle conn 的 ring = 0 字节）；
//   * 入队按需 append，物理长度受槽位上限约束（<= maxFrames，容量最多 2×maxFrames 的瞬时峰值）；
//   * 出队后按需压缩（head 越过一半时把尾部搬到头部），保证长连接不会无限增长；
//   * **双约束判定完全不变**：字节预算仍是权威水位、槽位上限仍是 maxFrames、丢弃语义一字不改
//     （所以"丢弃不 ack"的背压安全性没有被放松）。
//
// **C5a：critical 槽位预留**。`RingCriticalReserve`（默认 32）个槽位**只留给 critical 帧**：
// 普通帧最多用到 `maxFrames-reserve`（默认 224），critical 可以用满 `maxFrames`（256）。
// 为什么需要：字节预算（16KB）在"小帧洪流"下不会先满（256×~25B=6.4KB），于是**槽位**先成为瓶颈；
// 若不分级，审批/信任这类 critical 帧会被普通帧挤掉（虽不丢数据——不 ack 会重发，但会多一跳延迟）。
// **预留不放宽任何约束**：字节预算照旧独立生效；普通帧在 224 槽满时仍返回 errSlotFull（不 ack）；
// critical 灌满 256 后同样按既有 slot-full 语义处理（不 panic、不越界）。

import (
	"sync"

	"human-agent-im/internal/connproto"
)

// pushOutcome 入队结果。
type pushOutcome uint8

const (
	pushed             pushOutcome = iota // 已入队
	droppedNonCritical                    // 水位/降级期间丢弃非关键帧
	errSlotFull                           // 槽位满（字节未满）：背压，不断连
	errByteFull                           // 字节满：慢消费者，上层断开
)

// frameSize 一帧占用的字节数（按编码后大小计，含头与 topic）。
func frameSize(f connproto.Frame) int {
	return connproto.HeaderLen + len(f.Topic) + len(f.Payload)
}

type ring struct {
	mu sync.Mutex

	// q 是惰性增长的队列：q[head:] 是有效元素。空闲连接 q == nil（零分配）。
	q    []connproto.Frame
	head int // 下一个出队位置

	bytes       int // 已入队帧的编码字节总和
	maxBytes    int
	maxFrames   int
	critReserve int // 只给 critical 帧预留的槽位数（默认 32；0=不预留）

	degraded bool   // 已越过 80% 水位
	dropped  uint64 // 因水位丢弃的帧数
	peak     int    // 历史峰值字节（诊断用）
}

func newRing(maxFrames, maxBytes, critReserve int) *ring {
	if maxFrames <= 0 {
		maxFrames = DefaultConfig().RingFrames
	}
	if maxBytes <= 0 {
		maxBytes = DefaultConfig().RingBytes
	}
	if critReserve < 0 {
		critReserve = 0
	}
	if critReserve >= maxFrames {
		// 预留不能吃掉全部槽位：最多留 maxFrames-1（否则普通帧永远进不来）
		critReserve = maxFrames - 1
	}
	// 注意：**不预分配**（task-24）。惰性增长见 push/compact。
	return &ring{maxFrames: maxFrames, maxBytes: maxBytes, critReserve: critReserve}
}

// compactLocked 在出队后压缩：把 q[head:] 搬到头部，避免 q 无限增长。
// 只在 head 越过一半且剩余元素不多时做，摊还 O(1)。
func (r *ring) compactLocked() {
	if r.head == 0 {
		return
	}
	if r.head == len(r.q) {
		r.q = r.q[:0]
		r.head = 0
		return
	}
	if r.head < len(r.q)/2 {
		return
	}
	n := copy(r.q, r.q[r.head:])
	// 清掉尾部引用，避免已出队帧的 payload 被残留槽位保活。
	for i := n; i < len(r.q); i++ {
		r.q[i] = connproto.Frame{}
	}
	r.q = r.q[:n]
	r.head = 0
}

// push 尝试入队一帧。
func (r *ring) push(f connproto.Frame) pushOutcome {
	size := frameSize(f)
	r.mu.Lock()
	defer r.mu.Unlock()

	// 1) 字节约束优先（权威水位）。
	if r.bytes+size > r.maxBytes {
		if !f.Critical() {
			r.dropped++
			return droppedNonCritical
		}
		return errByteFull
	}
	// 降级期间：非关键帧直接丢（合并/丢弃非关键帧）。
	if r.degraded && !f.Critical() {
		r.dropped++
		return droppedNonCritical
	}
	// 2) 槽位约束（C5a 分级）。槽满但字节未满 → 只回背压信号，不算慢消费者。
	//    普通帧最多用到 maxFrames-critReserve；critical 可用满 maxFrames。
	slotLimit := r.maxFrames
	if !f.Critical() {
		slotLimit = r.maxFrames - r.critReserve
	}
	if len(r.q)-r.head >= slotLimit {
		return errSlotFull
	}

	r.q = append(r.q, f)
	r.bytes += size
	if r.bytes > r.peak {
		r.peak = r.bytes
	}
	// 3) 越 80% 水位 → 进入降级（只标记，回调由 Conn 侧发一次）。
	if !r.degraded && r.bytes*10 >= r.maxBytes*8 {
		r.degraded = true
	}
	return pushed
}

// popBatch 取出最多 max 帧（writer 独占调用）。
func (r *ring) popBatch(max int) []connproto.Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.q) - r.head
	if n == 0 || max <= 0 {
		return nil
	}
	if max > n {
		max = n
	}
	out := make([]connproto.Frame, max)
	copy(out, r.q[r.head:r.head+max])
	// 清引用（避免已出队帧的 payload 被 ring 保活）
	for i := r.head; i < r.head+max; i++ {
		r.q[i] = connproto.Frame{}
	}
	r.head += max
	for _, f := range out {
		r.bytes -= frameSize(f)
	}
	if r.bytes < 0 {
		r.bytes = 0
	}
	r.compactLocked()
	// 水位回落后允许重新累积降级（下一次再越 80% 会再触发一次回调）。
	if r.bytes*10 < r.maxBytes*8 {
		r.degraded = false
	}
	return out
}

// len 当前槽位占用。
func (r *ring) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.q) - r.head
}

// bytesUsed 当前字节占用。
func (r *ring) bytesUsed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}

// isDegraded 是否处于降级状态。
func (r *ring) isDegraded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.degraded
}

// droppedCount 累计丢弃帧数。
func (r *ring) droppedCount() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// snapshot 诊断快照（测试/日志用）。
func (r *ring) snapshot() (frames, bytes, peak int, dropped uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.q) - r.head, r.bytes, r.peak, r.dropped
}
