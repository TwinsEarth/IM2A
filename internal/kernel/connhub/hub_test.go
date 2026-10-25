package connhub

// hub_test.go：分片、投递、关闭语义的单元测试。

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"human-agent-im/internal/connproto"
)

// orderSink 按顺序记录收到的 seq（用于验证同连接内保序）。
type orderSink struct {
	mu  sync.Mutex
	seq []uint64
}

func (o *orderSink) WriteFrames(frames []connproto.Frame) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, f := range frames {
		o.seq = append(o.seq, f.Seq)
	}
	return nil
}
func (o *orderSink) Close() error { return nil }
func (o *orderSink) Seqs() []uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]uint64(nil), o.seq...)
}

// TestHubShardRouting connID 到分片的映射必须确定、且落在 [0,shards)。
func TestHubShardRouting(t *testing.T) {
	h := NewHub(DefaultConfig())
	defer h.Close()
	if h.ShardCount() != 64 {
		t.Fatalf("shards=%d, want 64", h.ShardCount())
	}
	// 同 connID 多次计算结果一致（纯函数）
	for _, id := range []uint64{1, 2, 64, 65, 1 << 40} {
		a, b := h.ShardOf(id), h.ShardOf(id)
		if a != b {
			t.Fatalf("connID=%d 分片不稳定: %d vs %d", id, a, b)
		}
		if a < 0 || a >= h.ShardCount() {
			t.Fatalf("connID=%d 分片越界: %d", id, a)
		}
	}
	// 64 个连续 connID 应基本均匀（至少覆盖 8 个以上分片）
	seen := map[int]bool{}
	for id := uint64(1); id <= 64; id++ {
		seen[h.ShardOf(id)] = true
	}
	if len(seen) < 8 {
		t.Fatalf("分片分布异常：64 个 connID 只落到 %d 个分片", len(seen))
	}
}

// TestHubDeliverToAllUserConns 同一 user 的多条连接（多端）都要收到。
func TestHubDeliverToAllUserConns(t *testing.T) {
	hub := NewHub(DefaultConfig())
	defer hub.Close()

	s1, s2 := &orderSink{}, &orderSink{}
	c1 := hub.Register(100, "dev-1", s1)
	c2 := hub.Register(100, "dev-2", s2)
	if c1.ID() == c2.ID() {
		t.Fatal("connID 必须唯一")
	}
	frames := []connproto.Frame{
		{Type: connproto.TypeMsg, Flags: connproto.FlagCritical, Seq: 1, Payload: []byte("a")},
		{Type: connproto.TypeEvt, Seq: 2, Payload: []byte("b")},
	}
	res := hub.Deliver(100, frames)
	if res.TargetConns != 2 {
		t.Fatalf("TargetConns=%d, want 2", res.TargetConns)
	}
	if !waitFor(func() bool { return len(s1.Seqs()) == 2 && len(s2.Seqs()) == 2 }, 2*time.Second) {
		t.Fatalf("多端未全部收到: s1=%v s2=%v", s1.Seqs(), s2.Seqs())
	}
	// 未知 user 投递不应报错，只是没有目标。
	if r := hub.Deliver(999, frames); r.TargetConns != 0 {
		t.Fatalf("未知 user TargetConns=%d, want 0", r.TargetConns)
	}
}

// TestHubPerConnOrdering 同一连接内必须**保序**（seq 单调递增）。
func TestHubPerConnOrdering(t *testing.T) {
	hub := NewHub(DefaultConfig())
	defer hub.Close()
	sink := &orderSink{}
	hub.Register(200, "dev-order", sink)

	// 注意：默认 ring 只有 256 槽，这里必须小于槽数才能"全部入队"；
	// 超过槽位会走 errSlotFull（合法背压，由内核补发），那是另一个测试的目标。
	const n = 200
	for i := 1; i <= n; i++ {
		hub.Deliver(200, []connproto.Frame{{
			Type: connproto.TypeMsg, Flags: connproto.FlagCritical,
			Seq: uint64(i), Payload: []byte(fmt.Sprintf("m%d", i)),
		}})
	}
	if !waitFor(func() bool { return len(sink.Seqs()) >= n }, 3*time.Second) {
		t.Fatalf("只收到 %d/%d", len(sink.Seqs()), n)
	}
	seqs := sink.Seqs()
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("乱序：seqs[%d]=%d <= seqs[%d]=%d", i, seqs[i], i-1, seqs[i-1])
		}
	}
}

// TestHubAckAfterWriteOnly ack 只能出现在写成功之后（写失败就不许 ack）。
func TestHubAckAfterWriteOnly(t *testing.T) {
	var acked atomic.Uint64
	cfg := DefaultConfig()
	cfg.OnAck = func(connID uint64, userID int64, seq uint64) { acked.Store(seq) }
	hub := NewHub(cfg)
	defer hub.Close()

	// 一个永远失败的 sink：任何帧都写不出去。
	hub.Register(300, "dev-fail", failingSink{})
	hub.Deliver(300, []connproto.Frame{{Type: connproto.TypeMsg, Flags: connproto.FlagCritical, Seq: 1, Payload: []byte("x")}})

	time.Sleep(100 * time.Millisecond)
	if got := acked.Load(); got != 0 {
		t.Fatalf("写失败却上报了 ack=%d（会丢消息）", got)
	}
}

type failingSink struct{}

func (failingSink) WriteFrames([]connproto.Frame) error { return fmt.Errorf("boom") }
func (failingSink) Close() error                        { return nil }

// TestHubCloseConnIsSynchronous CloseConn 返回后：连接从表中消失，且 ack 位点不再变化。
func TestHubCloseConnIsSynchronous(t *testing.T) {
	hub := NewHub(DefaultConfig())
	defer hub.Close()
	sink := &orderSink{}
	conn := hub.Register(400, "dev-close", sink)
	id := conn.ID()

	for i := 1; i <= 50; i++ {
		hub.Deliver(400, []connproto.Frame{{
			Type: connproto.TypeMsg, Flags: connproto.FlagCritical,
			Seq: uint64(i), Payload: []byte("x"),
		}})
	}
	hub.CloseConn(id, 1001, "test")
	if hub.Get(id) != nil {
		t.Fatal("CloseConn 返回后连接仍在表里")
	}
	after := conn.LastAck()
	time.Sleep(30 * time.Millisecond)
	if got := conn.LastAck(); got != after {
		t.Fatalf("CloseConn 返回后仍在推进 ack：%d -> %d（快照不安全）", after, got)
	}
	// 关闭后投递不应 panic、不应命中该连接。
	res := hub.Deliver(400, []connproto.Frame{{Type: connproto.TypeMsg, Seq: 999, Payload: []byte("x")}})
	if res.TargetConns != 0 {
		t.Fatalf("已关闭的连接仍被投递：%+v", res)
	}
}

// TestHubRegisterWithID C1（task-21）：网关先 NextConnID 拿号 → 向内核验明身份 →
// 再用同一个 connID 注册；注册后 Deliver 能按权威 userID 路由，connID 冲突必须 panic
// （不能静默覆盖别人的连接）。
func TestHubRegisterWithID(t *testing.T) {
	h := NewHub(DefaultConfig())
	defer h.Close()

	id := h.NextConnID()
	if id == 0 {
		t.Fatal("NextConnID 返回 0")
	}
	sink := &orderSink{}
	c := h.RegisterWithID(id, 777, "dev-c1", sink)
	if c.ID() != id {
		t.Fatalf("conn.ID()=%d, want %d", c.ID(), id)
	}
	if got := h.ConnCount(); got != 1 {
		t.Fatalf("ConnCount=%d", got)
	}
	// 按权威 userID 路由
	res := h.Deliver(777, []connproto.Frame{{Type: connproto.TypeMsg, Seq: 1, Payload: []byte("x")}})
	if res.TargetConns != 1 || res.Enqueued != 1 {
		t.Fatalf("Deliver 未路由到 RegisterWithID 的连接: %+v", res)
	}
	// 未注册的 userID 不应命中
	if res := h.Deliver(778, []connproto.Frame{{Type: connproto.TypeMsg, Seq: 2}}); res.TargetConns != 0 {
		t.Fatalf("错误 userID 命中: %+v", res)
	}

	// connID 复用必须 panic
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("重复 connID 应 panic")
			}
		}()
		h.RegisterWithID(id, 777, "dup", &orderSink{})
	}()
}