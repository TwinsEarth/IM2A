package connhub

// zero_loss_test.go —— Wave B1 的**判定性**测试（task-14 要求"先红后绿"）。
//
// 它要证明的只有一件事：**已经 ack 的消息，在任何一方被 kill 之后都不会丢**。
// 做法用一个内存 sink 模拟"持久化 + 消费者"，用内存 store 模拟内核的离线箱：
//
//	生产者：store.Append(payload) → 得到 seq → hub.Deliver(userID, frame{seq})
//	消费者：sink.WriteFrames 成功 → hub 才允许 OnAck(seq) → store 记录已确认位点
//	kill  ：CloseConn（等写循环退出）→ 重连 → 从 store.LastAck()+1 补发 store 里的帧
//	断言  ：两次连接收到的 seq 并集 == 1..200，且无重复、无空洞
//
// 这个测试在 naive 实现（ack 先于写、无有界队列）下会红——红的输出就是"丢了多少条、丢在哪"。
// 先红后绿的证据记录在交付回报里。

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"human-agent-im/internal/connproto"
)

const testUser int64 = 42

// ---- 内存"内核"：持久化消息 + 已确认位点 ----

type memStore struct {
	mu     sync.Mutex
	frames []connproto.Frame // 按 Append 顺序，seq 从 1 开始
	seq    uint64
	acked  uint64
}

func newMemStore() *memStore { return &memStore{} }

// Append 持久化一条消息并分配 seq（模拟内核的 deliverySeq 发号）。
func (s *memStore) Append(payload []byte) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	cp := append([]byte(nil), payload...)
	s.frames = append(s.frames, connproto.Frame{
		Type:    connproto.TypeMsg,
		Flags:   connproto.FlagCritical,
		Seq:     s.seq,
		Payload: cp,
	})
	return s.seq
}

// Ack 记录累积确认位点（只前进）。
func (s *memStore) Ack(seq uint64) {
	s.mu.Lock()
	if seq > s.acked {
		s.acked = seq
	}
	s.mu.Unlock()
}

// LastAck 当前已确认到哪（重连补发的起点 = LastAck+1）。
func (s *memStore) LastAck() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acked
}

// Since 返回 seq >= from 的帧（补发用）。
func (s *memStore) Since(from uint64) []connproto.Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]connproto.Frame, 0, len(s.frames))
	for _, f := range s.frames {
		if f.Seq >= from {
			out = append(out, f)
		}
	}
	return out
}

// Count 已持久化条数（生产者重启后从这里继续）。
func (s *memStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

// ---- 内存 sink：模拟一条"消费者连接" ----

type memSink struct {
	mu    sync.Mutex
	delay time.Duration
	dead  bool
	seqs  []uint64
	wrote atomic.Int64
}

func newMemSink(delay time.Duration) *memSink { return &memSink{delay: delay} }

func (m *memSink) WriteFrames(frames []connproto.Frame) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dead {
		return fmt.Errorf("memSink: closed")
	}
	for _, f := range frames {
		if f.Seq > 0 {
			m.seqs = append(m.seqs, f.Seq)
		}
	}
	m.wrote.Add(int64(len(frames)))
	return nil
}

func (m *memSink) Close() error {
	m.mu.Lock()
	m.dead = true
	m.mu.Unlock()
	return nil
}

// Got 已"写出"的 seq 列表（未排序、可能重复）。
func (m *memSink) Got() []uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]uint64(nil), m.seqs...)
}

// kill 模拟消费者进程/网络消失（等价于对端拔线）。
func (m *memSink) kill() {
	m.mu.Lock()
	m.dead = true
	m.mu.Unlock()
}

// ---- 测试 ----

// TestZeroLoss_ConsumerKilledAtRandomPoint 消费者在随机位置被 kill：
// 已 ack 的消息必须全部在重连补发后到达，且不重复。
func TestZeroLoss_ConsumerKilledAtRandomPoint(t *testing.T) {
	rng := rand.New(rand.NewSource(20261025))
	points := []int{0, 37, 1 + rng.Intn(198), 199} // 首/尾/随机，确定性种子便于复现
	for _, kp := range points {
		kp := kp
		t.Run(fmt.Sprintf("kill_at_%03d", kp), func(t *testing.T) {
			runZeroLoss(t, kp)
		})
	}
}

func runZeroLoss(t *testing.T, killAt int) {
	t.Helper()
	const total = 200

	store := newMemStore()
	cfg := DefaultConfig()
	cfg.OnAck = func(connID uint64, userID int64, seq uint64) {
		// 只有当帧真正写出后 hub 才会回调这里 —— 这是"零丢"的契约核心。
		store.Ack(seq)
	}
	hub := NewHub(cfg)
	defer hub.Close()

	sinkA := newMemSink(200 * time.Microsecond) // 放大"已 ack 但未写出"的窗口
	connA := hub.Register(testUser, "dev-a", sinkA)

	var sinkB *memSink
	resumed := false

	for i := 0; i < total; i++ {
		payload := []byte(fmt.Sprintf("msg-%03d", i))
		seq := store.Append(payload)
		hub.Deliver(testUser, []connproto.Frame{{
			Type:    connproto.TypeMsg,
			Flags:   connproto.FlagCritical,
			Seq:     seq,
			Payload: payload,
		}})

		if !resumed && i == killAt {
			// 1) 消费者消失：CloseConn 返回后该连接不会再产生 ack。
			hub.CloseConn(connA.ID(), 1003, "simulated-slow-consumer-kill")
			sinkA.kill()

			// 2) 重连 + 从持久化游标补发（B1 由测试模拟内核；B2 由内核自己做）。
			from := store.LastAck() + 1
			sinkB = newMemSink(0)
			hub.Register(testUser, "dev-b", sinkB)
			if from <= uint64(store.Count()) {
				hub.Deliver(testUser, store.Since(from))
			}
			resumed = true
		}
	}
	if !resumed { // killAt == total 的边界
		hub.CloseConn(connA.ID(), 1003, "kill-after-all")
		sinkA.kill()
		from := store.LastAck() + 1
		sinkB = newMemSink(0)
		hub.Register(testUser, "dev-b", sinkB)
		hub.Deliver(testUser, store.Since(from))
	}

	// 等所有帧都被确认（或超时——超时本身就是失败信号）。
	if !waitFor(func() bool { return store.LastAck() == total }, 5*time.Second) {
		t.Fatalf("5s 内未确认完：lastAck=%d, persisted=%d, wroteA=%d",
			store.LastAck(), store.Count(), sinkA.wrote.Load())
	}
	// CloseConn 是同步的（等写循环退出），但最后一条可能还在 writer 里；再等一拍。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, want := len(sinkA.Got())+len(sinkB.Got()), total; got >= want {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	got := append(sinkA.Got(), sinkB.Got()...)
	assertExactlyOnce(t, got, total)
}

// TestZeroLoss_ProducerRestart 生产者中途崩溃重启：已落库的不重不漏。
func TestZeroLoss_ProducerRestart(t *testing.T) {
	const total = 200
	store := newMemStore()
	cfg := DefaultConfig()
	cfg.OnAck = func(connID uint64, userID int64, seq uint64) { store.Ack(seq) }
	hub := NewHub(cfg)
	defer hub.Close()

	sink := newMemSink(0)
	hub.Register(testUser, "dev-a", sink)

	produceFrom := func(from, to int) {
		for i := from; i < to; i++ {
			payload := []byte(fmt.Sprintf("p-%03d", i))
			seq := store.Append(payload)
			hub.Deliver(testUser, []connproto.Frame{{
				Type: connproto.TypeMsg, Flags: connproto.FlagCritical, Seq: seq, Payload: payload,
			}})
		}
	}
	produceFrom(0, 90) // 生产到 90 后"崩溃"
	resumeAt := store.Count()
	produceFrom(resumeAt, total) // 重启后从持久化条数继续

	if !waitFor(func() bool { return store.LastAck() == total }, 5*time.Second) {
		t.Fatalf("生产者重启后未确认完：lastAck=%d", store.LastAck())
	}
	if store.Count() != total {
		t.Fatalf("持久化条数 = %d, want %d（重启后重复 Append？）", store.Count(), total)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(sink.Got()) < total {
		time.Sleep(2 * time.Millisecond)
	}
	assertExactlyOnce(t, sink.Got(), total)
}

// ---- 断言工具 ----

// assertExactlyOnce 断言 seq 集合恰好是 1..want：无丢失、无重复。
// 失败信息会打印缺失与重复的具体序号，这就是"红的证据"。
func assertExactlyOnce(t *testing.T, got []uint64, want int) {
	t.Helper()
	seen := make(map[uint64]int, len(got))
	for _, s := range got {
		seen[s]++
	}
	var missing, dup []uint64
	for s := uint64(1); s <= uint64(want); s++ {
		switch n := seen[s]; {
		case n == 0:
			missing = append(missing, s)
		case n > 1:
			dup = append(dup, s)
		}
	}
	extra := 0
	for s, n := range seen {
		if s == 0 || s > uint64(want) {
			extra += n
		}
	}
	if len(missing) == 0 && len(dup) == 0 && extra == 0 {
		return
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	sort.Slice(dup, func(i, j int) bool { return dup[i] < dup[j] })
	preview := func(xs []uint64) string {
		if len(xs) > 10 {
			return fmt.Sprintf("%v…(共 %d)", xs[:10], len(xs))
		}
		return fmt.Sprintf("%v", xs)
	}
	t.Fatalf("零丢断言失败：收到 %d 条，期望 %d 条\n  丢失 %d 条: %s\n  重复 %d 条: %s\n  越界 %d 条",
		len(got), want, len(missing), preview(missing), len(dup), preview(dup), extra)
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}
