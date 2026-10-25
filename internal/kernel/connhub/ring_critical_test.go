package connhub

// ring_critical_test.go：C5a —— critical 帧预留 32 槽位（补背压分级最后缺口）。
//
// 背景：`ring.push` 的三层判定里，80% 降级与 100% 字节水位都已对 critical 分级；
// 只有"槽位满"这一层原来不看 `Critical()`。小帧洪流下 256×~25B≈6.4KB 先触到槽位上限
// （16KB 字节预算还没满），critical（审批/信任）会被普通帧挤掉——不丢数据（不 ack 会重发），
// 但会多一跳延迟。C5a 用"预留 32 槽位只给 critical"解决，**不放宽字节预算**。

import (
	"testing"
	"time"

	"human-agent-im/internal/connproto"
)

func ordFrame(seq uint64) connproto.Frame {
	return connproto.Frame{Type: connproto.TypeEvt, Seq: seq, Payload: []byte("x")} // frameSize=25B
}

func critFrame(seq uint64) connproto.Frame {
	return connproto.Frame{Type: connproto.TypeMsg, Flags: connproto.FlagCritical, Seq: seq, Payload: []byte("x")}
}

// TestRingCriticalReserveSlots 槽位分级：普通帧止步 224，critical 可用到 256。
func TestRingCriticalReserveSlots(t *testing.T) {
	const maxFrames, reserve = 256, 32
	r := newRing(maxFrames, 1<<20, reserve) // 字节预算足够大 → 只测槽位约束

	normalLimit := maxFrames - reserve // 224
	for i := 0; i < normalLimit; i++ {
		if got := r.push(ordFrame(uint64(i + 1))); got != pushed {
			t.Fatalf("第 %d 个普通帧应入队，got %v", i+1, got)
		}
	}
	if got := r.push(ordFrame(9001)); got != errSlotFull {
		t.Fatalf("普通帧在 %d 槽后应被槽位上限挡住（errSlotFull），got %v", normalLimit, got)
	}
	if n, _, _, _ := r.snapshot(); n != normalLimit {
		t.Fatalf("被挡住的普通帧不应入队：frames=%d want %d", n, normalLimit)
	}

	// 关键：此时 critical 仍可入队（用的是预留槽位）
	if got := r.push(critFrame(9002)); got != pushed {
		t.Fatalf("critical 帧应能使用预留槽位入队，got %v", got)
	}
	// 把 critical 灌满到 256
	for i := 0; i < reserve-1; i++ {
		if got := r.push(critFrame(uint64(9100 + i))); got != pushed {
			t.Fatalf("critical 第 %d 个应入队，got %v", i+2, got)
		}
	}
	if n, _, _, _ := r.snapshot(); n != maxFrames {
		t.Fatalf("critical 应能填满到 %d 槽，frames=%d", maxFrames, n)
	}
	// 加满后：无论 critical 还是普通帧都按既有 slot-full 语义处理（不 panic、不越界）
	if got := r.push(critFrame(9200)); got != errSlotFull {
		t.Fatalf("critical 灌满 %d 后应返回 errSlotFull，got %v", maxFrames, got)
	}
	if got := r.push(ordFrame(9201)); got != errSlotFull {
		t.Fatalf("普通帧在满槽时应返回 errSlotFull，got %v", got)
	}
	if n, _, _, _ := r.snapshot(); n > maxFrames {
		t.Fatalf("槽位数越界：%d > %d", n, maxFrames)
	}
}

// TestRingCriticalReserveByteBudgetOrthogonal 预留槽位**不放宽字节预算**：
// 构造"槽位还有余量、但字节将超预算" → critical 必须走 errByteFull（而不是因为预留槽位就放行）。
func TestRingCriticalReserveByteBudgetOrthogonal(t *testing.T) {
	const maxFrames, reserve = 256, 32
	// 设计：普通帧 25B，先填满普通槽位上限 224 → 5600B（低于 80% 降级线 5680B，避免降级干扰）；
	// 之后用 **大 critical 帧**（payload 200B → frameSize 224B）继续入队，把 7100B 预算打满。
	// 这样：槽位对 critical 还有余量（230 < 256），但字节预算先满 → 必须走 errByteFull。
	const maxBytes = 7100
	r := newRing(maxFrames, maxBytes, reserve)

	for i := 0; i < 224; i++ {
		if got := r.push(ordFrame(uint64(i + 1))); got != pushed {
			t.Fatalf("普通帧 %d 应入队，got %v", i+1, got)
		}
	}
	bigCrit := func(seq uint64) connproto.Frame {
		return connproto.Frame{Type: connproto.TypeMsg, Flags: connproto.FlagCritical, Seq: seq, Payload: make([]byte, 200)}
	}
	// 5600 + 6×224 = 6944 < 7100：这 6 个 critical 应入队（槽位/字节都还没满）
	for i := 0; i < 6; i++ {
		if got := r.push(bigCrit(uint64(1000 + i))); got != pushed {
			t.Fatalf("critical 第 %d 个应入队（字节未满），got %v", i+1, got)
		}
	}
	if n, bytes, _, _ := r.snapshot(); n != 230 || bytes != 6944 {
		t.Fatalf("预期 230 帧/6944B，实际 frames=%d bytes=%d", n, bytes)
	}
	// 槽位还有 26 个空位（230 < 256），但再塞一个 224B 会超 7100 → critical 必须 errByteFull
	if got := r.push(bigCrit(2000)); got != errByteFull {
		t.Fatalf("字节预算满时 critical 应返回 errByteFull（预留槽位不放宽字节预算），got %v", got)
	}
}

// TestRingNoReserveContrast 反向对照：**不预留**（reserve=0）时第 225 个普通帧会入队。
// 这条用来证明"是预留逻辑让 225 个普通帧被挡"，并锁住默认值存在时行为不同。
func TestRingNoReserveContrast(t *testing.T) {
	const maxFrames = 256
	r := newRing(maxFrames, 1<<20, 0)
	for i := 0; i < 225; i++ {
		if got := r.push(ordFrame(uint64(i + 1))); got != pushed {
			t.Fatalf("无预留时普通帧 %d 应入队，got %v", i+1, got)
		}
	}
}

// TestConnCriticalNotDroppedAndNeverAcked 连接级：普通帧在 224 上限被丢且**不产生 ack**，
// critical 仍能送达；被丢帧的 seq 绝不出现在写成功序列里。
func TestConnCriticalNotDroppedAndNeverAcked(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RingFrames = 8
	cfg.RingCriticalReserve = 3 // 普通帧最多 5 个
	cfg.RingBytes = 1 << 20
	h := NewHub(cfg)
	defer h.Close()

	sink := &orderSink{}
	c := h.Register(1, "dev-c5a", sink)

	// 普通帧 1..5 入队
	for i := uint64(1); i <= 5; i++ {
		acc, dropped, _ := c.push([]connproto.Frame{ordFrame(i)})
		if acc != 1 || dropped != 0 {
			t.Fatalf("普通帧 %d: accepted=%d dropped=%d", i, acc, dropped)
		}
	}
	// 第 6 个普通帧：槽位满（5 = 8-3）→ 丢弃
	acc, dropped, _ := c.push([]connproto.Frame{ordFrame(999)})
	if acc != 0 || dropped != 1 {
		t.Fatalf("普通帧应被丢弃: accepted=%d dropped=%d", acc, dropped)
	}
	// critical 仍可入队（用预留槽位）
	acc, dropped, _ = c.push([]connproto.Frame{critFrame(6)})
	if acc != 1 || dropped != 0 {
		t.Fatalf("critical 帧应入队: accepted=%d dropped=%d", acc, dropped)
	}

	// 等 writer 排空，断言：收到的 seq 是 1..6，被丢的 999 绝不出现；LastAck 只推进到 6。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sink.Seqs()) >= 6 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	seqs := sink.Seqs()
	for _, s := range seqs {
		if s == 999 {
			t.Fatalf("被丢弃的普通帧(seq=999) 竟然被写出去并 ack：seqs=%v", seqs)
		}
	}
	if got := c.LastAck(); got != 6 {
		t.Fatalf("LastAck=%d want 6（只有写成功的帧才 ack）", got)
	}
}
