package connhub

// backpressure_test.go：验证 ring 的**双约束**语义与两个水位动作。
// 设计要求（v2 §5.2）：
//   * 256 槽 **且** 16KB 字节预算；**字节预算是权威水位，槽数先到不算满**；
//   * 80% → 降级（丢非关键帧，保留关键帧）；
//   * 100% → 断开慢消费者。

import (
	"sync"
	"testing"
	"time"

	"human-agent-im/internal/connproto"
)

func testFrame(payloadLen int, critical bool) connproto.Frame {
	f := connproto.Frame{Type: connproto.TypeEvt, Seq: 1, Payload: make([]byte, payloadLen)}
	if critical {
		f.Type = connproto.TypeMsg
		f.Flags = connproto.FlagCritical
	}
	return f
}

// TestRingSlotFullIsNotWatermark 槽位先满但字节远未满：只回 errSlotFull（背压），
// 不算慢消费者、不触发降级。
func TestRingSlotFullIsNotWatermark(t *testing.T) {
	r := newRing(8, 1024, 0) // 8 槽，1024 字节；C5a 预留=0（本用例专测"纯槽位满"语义）
	for i := 0; i < 8; i++ {
		if out := r.push(testFrame(8, false)); out != pushed { // size=24+8=32
			t.Fatalf("第 %d 帧应入队, got %v", i, out)
		}
	}
	if got := r.bytesUsed(); got != 8*32 {
		t.Fatalf("bytes=%d, want %d", got, 8*32)
	}
	if r.isDegraded() {
		t.Fatal("字节水位仅 25%%，不应进入降级")
	}
	if out := r.push(testFrame(8, true)); out != errSlotFull {
		t.Fatalf("第 9 帧应 errSlotFull（槽满但字节未满）, got %v", out)
	}
	if r.droppedCount() != 0 {
		t.Fatalf("errSlotFull 不应计入丢弃, dropped=%d", r.droppedCount())
	}
}

// TestRingDegradeAt80Percent 字节越 80% → 降级；非关键丢、关键仍收。
func TestRingDegradeAt80Percent(t *testing.T) {
	r := newRing(256, 1000, 32) // 每帧 124B（24+100）
	n := 0
	for !r.isDegraded() {
		if out := r.push(testFrame(100, false)); out != pushed {
			t.Fatalf("入队失败 at %d: %v", n, out)
		}
		n++
		if n > 20 {
			t.Fatal("始终未进入降级：80% 水位判定失效")
		}
	}
	if got := r.bytesUsed(); got*10 < 1000*8 {
		t.Fatalf("降级时 bytes=%d，未达 80%%", got)
	}
	// 降级中：非关键丢
	before := r.bytesUsed()
	if out := r.push(testFrame(100, false)); out != droppedNonCritical {
		t.Fatalf("降级中非关键帧应被丢弃, got %v", out)
	}
	if r.bytesUsed() != before {
		t.Fatal("丢弃不应改变字节占用")
	}
	if r.droppedCount() != 1 {
		t.Fatalf("dropped=%d, want 1", r.droppedCount())
	}
	// 降级中：关键帧仍可入队（未到 100%）
	if out := r.push(testFrame(100, true)); out != pushed {
		t.Fatalf("降级中关键帧应可入队, got %v", out)
	}
}

// TestRingByteFullKicks 关键帧也放不下 → errByteFull（100% 水位动作）。
func TestRingByteFullKicks(t *testing.T) {
	r := newRing(256, 1000, 32)
	for {
		out := r.push(testFrame(100, true))
		if out == errByteFull {
			break
		}
		if out != pushed {
			t.Fatalf("unexpected outcome %v", out)
		}
	}
	if got := r.bytesUsed(); got+124 <= 1000 {
		t.Fatalf("errByteFull 时 bytes=%d 仍能放下下一帧，判定过早", got)
	}
}

// TestRingPopReleasesBytes 出队后字节归还，降级状态回落。
func TestRingPopReleasesBytes(t *testing.T) {
	r := newRing(16, 400, 2) // 每帧 124B；3 帧=372=93% → 降级
	for i := 0; i < 3; i++ {
		if out := r.push(testFrame(100, true)); out != pushed {
			t.Fatalf("push %d: %v", i, out)
		}
	}
	if !r.isDegraded() {
		t.Fatal("应已降级")
	}
	batch := r.popBatch(2)
	if len(batch) != 2 {
		t.Fatalf("pop 出 %d 帧, want 2", len(batch))
	}
	if got := r.bytesUsed(); got != 124 {
		t.Fatalf("after pop bytes=%d, want 124", got)
	}
	if r.isDegraded() {
		t.Fatal("回落到 80% 以下后应退出降级")
	}
	if got := r.len(); got != 1 {
		t.Fatalf("len=%d, want 1", got)
	}
}

// blockingSink 第一次写就阻塞，用来制造"队列被灌满"。
type blockingSink struct {
	release chan struct{}
	once    sync.Once
	writes  int
	mu      sync.Mutex
}

func newBlockingSink() *blockingSink { return &blockingSink{release: make(chan struct{})} }

func (b *blockingSink) WriteFrames(frames []connproto.Frame) error {
	<-b.release
	b.mu.Lock()
	b.writes += len(frames)
	b.mu.Unlock()
	return nil
}

func (b *blockingSink) Close() error {
	b.once.Do(func() { close(b.release) })
	return nil
}

// TestHubSlowConsumerKicked 端到端验证 100% 水位动作：连接被断开、回调被调用、连接表清理。
func TestHubSlowConsumerKicked(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RingFrames = 4
	cfg.RingBytes = 128 // 每帧 124B → 第 2 帧就撞 100%
	cfg.WriteBatch = 1
	cfg.DrainTimeout = 200 * time.Millisecond

	kicked := make(chan int, 1)
	cfg.OnSlowConsumer = func(connID uint64, userID int64, queued int) { kicked <- queued }
	hub := NewHub(cfg)
	defer hub.Close()

	sink := newBlockingSink()
	conn := hub.Register(7, "dev-slow", sink)

	// 关键帧（MSG）才会撞 errByteFull；投 50 帧足够灌满 128B。
	for i := 0; i < 50; i++ {
		hub.Deliver(7, []connproto.Frame{{
			Type: connproto.TypeMsg, Flags: connproto.FlagCritical,
			Seq: uint64(i + 1), Payload: make([]byte, 100),
		}})
	}

	select {
	case <-kicked:
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内未触发 OnSlowConsumer（100% 水位判定失效）")
	}
	// 断开是异步的：等连接从表里消失。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hub.ConnCount() > 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if hub.ConnCount() != 0 {
		t.Fatalf("慢消费者未被摘除：conns=%d", hub.ConnCount())
	}
	_ = conn
	if s := hub.Stats(); s.SlowKicks == 0 {
		t.Fatal("Stats.SlowKicks 未计数")
	}
}

// TestHubDegradeCallback 80% 水位触发 OnDegrade（只触发一次）。
func TestHubDegradeCallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RingFrames = 256
	cfg.RingBytes = 400 // 每帧 124B
	cfg.WriteBatch = 1

	var degrades int
	var mu sync.Mutex
	cfg.OnDegrade = func(connID uint64, userID int64) {
		mu.Lock()
		degrades++
		mu.Unlock()
	}
	hub := NewHub(cfg)
	defer hub.Close()

	sink := newBlockingSink()
	hub.Register(8, "dev-degrade", sink)
	defer sink.Close()

	for i := 0; i < 10; i++ {
		hub.Deliver(8, []connproto.Frame{{
			Type: connproto.TypeEvt, Flags: 0, Seq: uint64(i + 1), Payload: make([]byte, 100),
		}})
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := degrades
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	n := degrades
	mu.Unlock()
	if n != 1 {
		t.Fatalf("降级回调 %d 次, want 1（一次降级只报一次）", n)
	}
}
