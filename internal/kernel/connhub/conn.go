package connhub

// conn.go：单条连接的写循环与关闭语义。
//
// 关键不变量（零丢的地基）：
//  1. **ack 只在 sink.WriteFrames 成功之后上报**。抢在写之前 ack 会让重连补发从错误位点
//     开始（这正是 naive 版本被 zero_loss_test 抓到的 bug）。
//  2. **CloseConn 是同步的**：返回时写循环已退出、不会再产生 ack。调用方（内核）在
//     CloseConn 之后读持久化位点才是安全的快照。
//  3. 关闭时**先 drain 再退**：ring 里已入队的帧尽量写完并 ack；写不出去的留在 ring 里
//     不 ack，由内核从 lastAck+1 补发（网关不保存、不负责重投）。
//  4. 只有 writer 一个 goroutine 调 Sink.WriteFrames；读路径（cmd/conn-gateway）绝不写 socket。

import (
	"sync"
	"sync/atomic"
	"time"

	"human-agent-im/internal/connproto"
)

type connState int32

const (
	stateOpen connState = iota
	stateDraining
	stateClosed
)

// Conn 一条连接。
type Conn struct {
	id       uint64
	userID   int64
	deviceID string

	hub   *Hub
	shard *Shard
	sink  Sink
	cfg   Config

	ring   *ring
	notify chan struct{} // cap=1，唤醒 writer（信号量语义，绝不阻塞投递方）
	closing chan struct{}
	done    chan struct{} // writer 退出时关闭

	closeOnce sync.Once
	state     atomic.Int32
	closeCode atomic.Uint32

	lastAck atomic.Uint64 // 已写出并上报的最大 seq

	outFrames atomic.Uint64
	outBytes  atomic.Uint64
	dropped   atomic.Uint64
	degraded  atomic.Bool // 当前是否已发过降级回调（回落到 80% 以下会清）
	kicked    atomic.Bool // 是否已判定慢消费者（每连接只踢一次、只回调一次）
}

func newConn(h *Hub, id uint64, userID int64, deviceID string, sink Sink, sh *Shard) *Conn {
	return &Conn{
		id: id, userID: userID, deviceID: deviceID,
		hub: h, shard: sh, sink: sink, cfg: h.cfg,
		ring:    newRing(h.cfg.RingFrames, h.cfg.RingBytes, h.cfg.RingCriticalReserve),
		notify:  make(chan struct{}, 1),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (c *Conn) start() { go c.writeLoop() }

// ID 连接 id（进程内唯一，从 1 递增，不复用）。
func (c *Conn) ID() uint64 { return c.id }

// UserID 归属用户。
func (c *Conn) UserID() int64 { return c.userID }

// DeviceID 设备标识。
func (c *Conn) DeviceID() string { return c.deviceID }

// LastAck 已写出并上报的最大 seq。
func (c *Conn) LastAck() uint64 { return c.lastAck.Load() }

// Queue 当前队列 (帧数, 字节数) —— 运维/测试诊断用。
func (c *Conn) Queue() (frames, bytes int) {
	n, b, _, _ := c.ring.snapshot()
	return n, b
}

// push 把一批帧入队。返回 (入队数, 因水位丢弃数, 触发慢消费者断开数)。
// 非阻塞：这是内核 Deliver 热路径上唯一允许调用的入口。
func (c *Conn) push(frames []connproto.Frame) (accepted, dropped, kicked int) {
	if connState(c.state.Load()) != stateOpen {
		return 0, len(frames), 0
	}
	for _, f := range frames {
		switch c.ring.push(f) {
		case pushed:
			accepted++
		case droppedNonCritical:
			dropped++
		case errSlotFull:
			// 槽位满但字节未满：不算满水位，不断连；告诉 writer 尽快排空，本轮丢帧不 ack。
			dropped++
			select {
			case c.notify <- struct{}{}:
			default:
			}
		case errByteFull:
			// 100% 水位：连关键帧都放不下 → 慢消费者。
			// 关键：**每连接只判定一次**，且用户回调与关闭都放到独立 goroutine 里执行——
			// 绝不能在 shard 循环里同步调用用户回调（回调阻塞 = 整个分片停摆）。
			dropped++
			if c.kicked.CompareAndSwap(false, true) {
				kicked = 1
				_, used, _, _ := c.ring.snapshot()
				c.cfg.Logf("connhub: slow consumer conn=%d user=%d queuedBytes=%d", c.id, c.userID, used)
				cb := c.cfg.OnSlowConsumer
				go func() {
					if cb != nil {
						cb(c.id, c.userID, used)
					}
					c.close(1003, "slow_consumer")
				}()
			}
			return
		}
	}
	c.dropped.Add(uint64(dropped))
	if accepted > 0 {
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
	// 降级回调（每次进入降级只报一次；回落到 80% 以下 clear）。
	if c.ring.isDegraded() {
		if c.degraded.CompareAndSwap(false, true) {
			c.hub.degrades.Add(1)
			if cb := c.cfg.OnDegrade; cb != nil {
				// 同样异步：回调是用户代码，可能慢/可能阻塞。
				go cb(c.id, c.userID)
			}
		}
	} else {
		c.degraded.Store(false)
	}
	return
}

// writeLoop 是唯一调用 Sink.WriteFrames 的 goroutine。
func (c *Conn) writeLoop() {
	defer close(c.done)
	for {
		if batch := c.ring.popBatch(c.cfg.WriteBatch); len(batch) > 0 {
			if err := c.writeBatch(batch); err != nil {
				c.abort(err)
				return
			}
			continue
		}
		select {
		case <-c.notify:
			// 有新增帧，回到主循环取批。
		case <-c.closing:
			// drain：把 ring 里剩下的尽量写完并 ack，然后退出。
			if batch := c.ring.popBatch(c.cfg.WriteBatch); len(batch) > 0 {
				if err := c.writeBatch(batch); err != nil {
					c.abort(err)
					return
				}
				continue
			}
			return
		}
	}
}

// writeBatch 写一批并上报累积 ack。成功返回 nil。
func (c *Conn) writeBatch(batch []connproto.Frame) error {
	if err := c.sink.WriteFrames(batch); err != nil {
		return err
	}
	var maxSeq uint64
	size := 0
	for _, f := range batch {
		if f.Seq > maxSeq {
			maxSeq = f.Seq
		}
		size += frameSize(f)
	}
	c.outFrames.Add(uint64(len(batch)))
	c.outBytes.Add(uint64(size))
	c.hub.framesOut.Add(uint64(len(batch)))
	// 只有写成功之后才 ack —— 零丢契约的核心。
	if maxSeq > 0 && maxSeq > c.lastAck.Load() {
		c.lastAck.Store(maxSeq)
		if c.cfg.OnAck != nil {
			c.cfg.OnAck(c.id, c.userID, maxSeq)
		}
	}
	return nil
}

// abort 写失败时由 writer 自己调用（不能走 close()，否则等自己退出会死锁）。
func (c *Conn) abort(err error) {
	c.cfg.Logf("connhub: conn=%d write error: %v", c.id, err)
	c.state.Store(int32(stateClosed))
	_ = c.sink.Close()
	c.hub.remove(c.id)
}

// close 同步关闭：drain（有上限）→ 关 sink → 注销。返回时不会再产生 ack。
func (c *Conn) close(code uint32, reason string) {
	c.closeOnce.Do(func() {
		c.closeCode.Store(code)
		c.state.Store(int32(stateDraining))
		close(c.closing)

		timeout := c.cfg.DrainTimeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-c.done: // writer 已把 ring 排空并退出
		case <-timer.C:
			c.cfg.Logf("connhub: conn=%d drain timeout(%s), force close", c.id, timeout)
			_ = c.sink.Close() // 让阻塞中的写返回错误，writer 随后退出
			<-c.done
		}
		_ = c.sink.Close()
		c.state.Store(int32(stateClosed))
		c.hub.remove(c.id)
		c.cfg.Logf("connhub: conn=%d closed code=%d reason=%s lastAck=%d", c.id, code, reason, c.lastAck.Load())
	})
}
