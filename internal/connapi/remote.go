// SPDX-License-Identifier: MIT
//
// Remote 是 EventSink 的跨进程实现：内核 ⇄ cmd/conn-gateway 的 UDS 客户端
// （B2B-DESIGN §4/§5；线格式唯一权威是 internal/connproto/uds.go）。
//
// 硬约束（与契约逐条对应）：
//   - Deliver **非阻塞**：内部有界队列，满则丢弃并计数；agent turn 永不被网关拖慢；
//     丢掉的帧仍在 im_delivery_log 里，客户端重连时由位点补发（delivery.Store）；
//   - 握手必须协商：Hello/HelloAck 的 ProtoVersion 不匹配（Accepted=false）→ 进 degraded
//     并计数，不再投递；
//   - 批次上限：先按协商的 maxBatchFrames 切块，再用 connproto.SplitDeliverReq 保证
//     body ≤1MiB；**用返回值里的 BatchID 匹配 DeliverAck**（分片有独立 BatchID）；
//   - Drain（KindDrain）：停止投递新帧，但继续处理 AckUp（否则位点会退）；重连后恢复；
//   - AckUp 只由客户端字节级确认驱动；由上层（delivery.Store）校验 conn_epoch，
//     本层只负责回调与计数——`DeliverAck.accepted=true` 不代表客户端已收到。
package connapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

// C1 presence 下发错误（易失语义：未连接/Drain 时直接丢弃，由算方下一拍重算）。
var (
	errRemoteClosed   = errors.New("connapi: remote closed")
	errRemoteNotReady = errors.New("connapi: remote not connected to gateway")
)

// 默认配置（B2B-DESIGN §4.1）。
const (
	DefaultQueueFrames   = 4096
	DefaultQueueBytes    = 8 << 20 // 8MiB 出站批（L1）
	DefaultDialTimeout   = 3 * time.Second
	DefaultWriteTimeout  = 200 * time.Millisecond // UDS 写超时：超时即断连重试，不阻塞 turn
	DefaultAckTimeout    = 200 * time.Millisecond // 批次回执超时（只计数，不重发）
	DefaultFlushInterval = 2 * time.Millisecond
	DefaultReconnectMin  = 100 * time.Millisecond
	DefaultReconnectMax  = 5 * time.Second
)

// Remote 是 UDS 客户端（EventSink 的 standalone/dual 实现）。
type Remote struct {
	cfg RemoteConfig
	log *log.Logger

	dial func(ctx context.Context) (net.Conn, error)

	mu        sync.Mutex
	state     string // connecting|connected|degraded|draining|closed
	uconn     *connproto.UDSConn
	conn      net.Conn
	maxBatch  int // 协商后的单批帧数
	inflight  map[uint64]inflightBatch
	draining  bool
	queue     chan queuedFrame
	queueSize atomic.Int64 // 已入队字节估算

	closeOnce sync.Once
	closed    chan struct{}
	notify    chan struct{} // 唤醒 run 循环（Close 用）

	// 计数器（metrics 用）
	deliveredFrames       atomic.Uint64
	droppedSlow           atomic.Uint64
	droppedQueueFull      atomic.Uint64
	droppedCritical       atomic.Uint64
	droppedNotReady       atomic.Uint64
	batchSplit            atomic.Uint64
	deliverAcks           atomic.Uint64
	ackTimeouts           atomic.Uint64
	ackUps                atomic.Uint64
	ackUpsDropped         atomic.Uint64
	resumes               atomic.Uint64
	connOpens             atomic.Uint64
	drainCount            atomic.Uint64
	reconnects            atomic.Uint64
	negotiationReject     atomic.Uint64
	writeErrors           atomic.Uint64
	statsUpdates          atomic.Uint64
	presenceLeases        atomic.Uint64
	presenceLeasesDropped atomic.Uint64
	presenceStates        atomic.Uint64
	lastStats             atomic.Value // connproto.StatsResp
	lastConnectedAt       atomic.Int64 // unix nano
	startedAt             int64

	// C4：同一时刻只允许一条活跃 UDS 连接（单飞）；写操作串行化（reader/writer 并发写会坏帧）。
	wmu         sync.Mutex
	activeConns atomic.Int64
	connGen     atomic.Uint64
}

type queuedFrame struct {
	userID int64
	frame  connproto.Frame
}

type inflightBatch struct {
	userID int64
	frames int
	at     time.Time
}

// RemoteConfig 是 UDS 客户端配置；零值字段自动补默认。
type RemoteConfig struct {
	UDSPath        string // CONN_UDS_PATH（单网关模型；多网关路由表属 Wave C）
	Instance       string // Hello.instance（内核实例标识）
	MaxFrameBytes  uint32 // Hello 协商：内核侧单帧上限
	MaxBatchFrames uint16 // Hello 协商：内核侧单批上限

	QueueFrames   int
	QueueBytes    int
	DialTimeout   time.Duration
	WriteTimeout  time.Duration
	AckTimeout    time.Duration
	FlushInterval time.Duration
	ReconnectMin  time.Duration
	ReconnectMax  time.Duration

	Logger *log.Logger
	Now    func() time.Time

	// Dial 可选：测试注入（默认 net.DialTimeout("unix", UDSPath)）。
	Dial func(ctx context.Context, path string) (net.Conn, error)

	// 回调（全部可选；epoch 校验等业务语义由装配方注入）。
	OnAckUp      func(connproto.AckUp) bool // false=丢弃（如 epoch 不匹配）
	OnResume     func(context.Context, connproto.ResumeReq) (connproto.ResumeResp, []connproto.Frame)
	OnConnOpen   func(connproto.ConnOpen) connproto.ConnOpenAck
	OnConnClosed func(connproto.ConnClosed)
	OnKick       func(connproto.Kick)
	// C1：网关→内核上行 presence 租约（kind=26）。返回 false=丢弃（易失信息，下一拍重发）。
	OnPresenceLease func(connproto.PresenceLease) bool
}

func (c RemoteConfig) normalized() RemoteConfig {
	if c.QueueFrames <= 0 {
		c.QueueFrames = DefaultQueueFrames
	}
	if c.QueueBytes <= 0 {
		c.QueueBytes = DefaultQueueBytes
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = DefaultDialTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultWriteTimeout
	}
	if c.AckTimeout <= 0 {
		c.AckTimeout = DefaultAckTimeout
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = DefaultFlushInterval
	}
	if c.ReconnectMin <= 0 {
		c.ReconnectMin = DefaultReconnectMin
	}
	if c.ReconnectMax <= 0 {
		c.ReconnectMax = DefaultReconnectMax
	}
	if c.MaxFrameBytes == 0 {
		c.MaxFrameBytes = connproto.UDSDefaultMaxFrameBytes
	}
	if c.MaxBatchFrames == 0 {
		c.MaxBatchFrames = connproto.UDSMaxBatch
	}
	if c.Logger == nil {
		c.Logger = log.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Dial == nil {
		c.Dial = func(ctx context.Context, path string) (net.Conn, error) {
			d := net.Dialer{Timeout: c.DialTimeout}
			return d.DialContext(ctx, "unix", path)
		}
	}
	return c
}

// NewRemote 构造并启动 UDS 客户端。网关不在时不影响内核：后台按退避重连，
// Deliver 始终非阻塞（丢弃并计数）。
func NewRemote(cfg RemoteConfig) *Remote {
	cfg = cfg.normalized()
	r := &Remote{
		cfg:      cfg,
		log:      cfg.Logger,
		dial:     func(ctx context.Context) (net.Conn, error) { return cfg.Dial(ctx, cfg.UDSPath) },
		state:    "connecting",
		inflight: map[uint64]inflightBatch{},
		queue:    make(chan queuedFrame, cfg.QueueFrames),
		closed:   make(chan struct{}),
		notify:   make(chan struct{}, 1),
	}
	r.startedAt = cfg.Now().UnixNano()
	r.lastStats.Store(connproto.StatsResp{})
	go r.run()
	return r
}

// Deliver 非阻塞投递：入有界队列后由 writer goroutine 异步发 UDS。
// 队列满/未连接/已 Drain 时丢弃并计入内核侧指标（日志保证可补发）。
func (r *Remote) Deliver(userID int64, frames []connproto.Frame) connhub.DeliverResult {
	if r == nil || len(frames) == 0 {
		return connhub.DeliverResult{}
	}
	r.mu.Lock()
	ready := r.state == "connected" && !r.draining
	queueFull := len(r.queue) >= cap(r.queue) || r.queueSize.Load() >= int64(r.cfg.QueueBytes)
	r.mu.Unlock()
	if !ready {
		r.droppedNotReady.Add(uint64(len(frames)))
		return connhub.DeliverResult{TargetConns: 0, Enqueued: 0, DroppedSlow: len(frames)}
	}
	res := connhub.DeliverResult{TargetConns: 0}
	for i := range frames {
		f := frames[i]
		sz := int64(len(f.Payload) + 32)
		if queueFull || r.queueSize.Load()+sz > int64(r.cfg.QueueBytes) {
			if f.Critical() {
				r.droppedCritical.Add(1)
			} else {
				r.droppedQueueFull.Add(1)
			}
			res.DroppedSlow++
			continue
		}
		select {
		case r.queue <- queuedFrame{userID: userID, frame: f}:
			r.queueSize.Add(sz)
			res.Enqueued++
		default:
			if f.Critical() {
				r.droppedCritical.Add(1)
			} else {
				r.droppedQueueFull.Add(1)
			}
			res.DroppedSlow++
		}
	}
	return res
}

// Stats 返回最近网关心跳/统计缓存的映射（允许滞后，设计允许）。
func (r *Remote) Stats() connhub.Stats {
	if r == nil {
		return connhub.Stats{}
	}
	st, _ := r.lastStats.Load().(connproto.StatsResp)
	return connhub.Stats{
		FramesIn:   st.FramesIn,
		FramesOut:  st.FramesOut,
		FramesDrop: st.Dropped,
		SlowKicks:  st.SlowKicks,
	}
}

// Metrics 返回内核侧投递指标快照（供 /metrics 接线；B2-b 后续接入）。
func (r *Remote) Metrics() Metrics {
	if r == nil {
		return Metrics{}
	}
	r.mu.Lock()
	state := r.state
	queueFrames := len(r.queue)
	r.mu.Unlock()
	return Metrics{
		State:                 state,
		QueueFrames:           queueFrames,
		QueueBytes:            int(r.queueSize.Load()),
		DeliveredFrames:       r.deliveredFrames.Load(),
		DroppedSlow:           r.droppedSlow.Load(),
		DroppedQueueFull:      r.droppedQueueFull.Load(),
		DroppedCritical:       r.droppedCritical.Load(),
		DroppedNotReady:       r.droppedNotReady.Load(),
		BatchSplit:            r.batchSplit.Load(),
		DeliverAcks:           r.deliverAcks.Load(),
		AckTimeouts:           r.ackTimeouts.Load(),
		AckUps:                r.ackUps.Load(),
		AckUpsDropped:         r.ackUpsDropped.Load(),
		Resumes:               r.resumes.Load(),
		ConnOpens:             r.connOpens.Load(),
		DrainCount:            r.drainCount.Load(),
		Reconnects:            r.reconnects.Load(),
		NegotiationRejected:   r.negotiationReject.Load(),
		WriteErrors:           r.writeErrors.Load(),
		StatsUpdates:          r.statsUpdates.Load(),
		PresenceLeases:        r.presenceLeases.Load(),
		PresenceLeasesDropped: r.presenceLeasesDropped.Load(),
		PresenceStates:        r.presenceStates.Load(),
		Connected:             state == "connected",
		ActiveConns:           r.activeConns.Load(),
		Draining:              r.draining,
	}
}

// DeliverPresenceState 把内核聚合的 presence 状态（kind=27）下发网关 fanout。
// 易失：不经过投递队列/不写日志；Drain/未连接时直接丢弃并返回错误（下一拍会重算）。
func (r *Remote) DeliverPresenceState(st connproto.PresenceState) error {
	if r == nil {
		return errRemoteClosed
	}
	r.mu.Lock()
	uc, state, draining := r.uconn, r.state, r.draining
	r.mu.Unlock()
	if uc == nil || state != "connected" || draining {
		return errRemoteNotReady
	}
	body, err := connproto.EncodeJSONBody(st)
	if err != nil {
		return err
	}
	if err := r.writeFrame(uc, connproto.KindPresenceState, body); err != nil {
		return err
	}
	r.presenceStates.Add(1)
	return nil
}

// Metrics 是投递面指标快照。
type Metrics struct {
	State                 string
	QueueFrames           int
	QueueBytes            int
	DeliveredFrames       uint64
	DroppedSlow           uint64
	DroppedQueueFull      uint64
	DroppedCritical       uint64
	DroppedNotReady       uint64
	BatchSplit            uint64
	DeliverAcks           uint64
	AckTimeouts           uint64
	AckUps                uint64
	AckUpsDropped         uint64
	Resumes               uint64
	ConnOpens             uint64
	DrainCount            uint64
	Reconnects            uint64
	NegotiationRejected   uint64
	WriteErrors           uint64
	StatsUpdates          uint64
	PresenceLeases        uint64
	PresenceLeasesDropped uint64
	PresenceStates        uint64
	Connected             bool
	Draining              bool
	ActiveConns           int64
}

// Close 停止后台协程并关闭 UDS 连接（未 ack 帧留给位点补发）。
func (r *Remote) Close(_ context.Context) error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.state = "closed"
		uconn, conn := r.uconn, r.conn
		r.uconn, r.conn = nil, nil
		r.mu.Unlock()
		close(r.closed)
		if uconn != nil {
			_ = uconn.Close()
		}
		if conn != nil {
			_ = conn.Close()
		}
	})
	return nil
}

// run 是连接生命周期主循环：dial → Hello 协商 → 读写循环；失败按退避重连。
func (r *Remote) run() {
	backoff := r.cfg.ReconnectMin
	for {
		select {
		case <-r.closed:
			return
		default:
		}
		r.setState("connecting")
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.DialTimeout)
		conn, err := r.dial(ctx)
		cancel()
		if err != nil {
			r.setState("connecting")
			if !r.sleep(backoff) {
				return
			}
			backoff = nextBackoff(backoff, r.cfg.ReconnectMax)
			continue
		}
		uc := connproto.NewUDSConn(conn)
		if err := r.handshake(uc); err != nil {
			r.negotiationReject.Add(1)
			r.setState("degraded")
			r.log.Printf("[connapi] WARN: UDS handshake rejected/failed: %v (degraded, not delivering)", err)
			_ = conn.Close()
			if !r.sleep(r.cfg.ReconnectMax) {
				return
			}
			continue
		}
		backoff = r.cfg.ReconnectMin
		r.mu.Lock()
		r.conn, r.uconn = conn, uc
		r.state = "connected"
		r.draining = false // 新网关实例：恢复投递（Drain 只针对旧实例）
		r.lastConnectedAt.Store(r.cfg.Now().UnixNano())
		r.mu.Unlock()
		r.reconnects.Add(1)
		gen := r.connGen.Add(1)
		r.activeConns.Store(1)
		r.log.Printf("[connapi] UDS connected path=%s batch=%d gen=%d", r.cfg.UDSPath, r.currentMaxBatch(), gen)
		done := make(chan struct{})
		go r.writer(uc, done, gen)
		r.reader(uc) // 阻塞；读失败/连接关闭后重连
		// 旧连接必须确定性关闭：先停旧 writer（不再消费共享队列），再关 socket，
		// 保证进入下一轮 dial 时不存在任何陈旧连接（结构上消除并发多连接）。
		close(done)
		_ = uc.Close()
		_ = conn.Close()
		r.activeConns.Store(0)
		r.mu.Lock()
		r.uconn, r.conn = nil, nil
		r.state = "connecting"
		r.draining = false
		r.mu.Unlock()
		r.log.Printf("[connapi] UDS disconnected gen=%d; reconnect in %s", gen, backoff)
		if !r.sleep(backoff) {
			return
		}
		backoff = nextBackoff(backoff, r.cfg.ReconnectMax)
	}
}

// handshake 发送 Hello 并校验 HelloAck（版本协商）。
func (r *Remote) handshake(uc *connproto.UDSConn) error {
	hello := connproto.UDSHello{
		ProtoVersion:   connproto.UDSProtoVersion,
		Instance:       r.cfg.Instance,
		MaxFrameBytes:  r.cfg.MaxFrameBytes,
		MaxBatchFrames: r.cfg.MaxBatchFrames,
	}
	body, err := connproto.EncodeJSONBody(hello)
	if err != nil {
		return err
	}
	_ = uc.SetWriteDeadline(r.cfg.Now().Add(r.cfg.DialTimeout))
	if err := uc.WriteMsg(connproto.KindHello, body); err != nil {
		return fmt.Errorf("write hello: %w", err)
	}
	_ = uc.SetReadDeadline(r.cfg.Now().Add(r.cfg.DialTimeout))
	kind, _, raw, err := uc.ReadMsg()
	if err != nil {
		return fmt.Errorf("read hello_ack: %w", err)
	}
	if kind != connproto.KindHelloAck {
		return fmt.Errorf("expected HELLO_ACK, got %s", kind)
	}
	var ack connproto.UDSHelloAck
	if err := connproto.DecodeJSONBody(raw, &ack); err != nil {
		return fmt.Errorf("decode hello_ack: %w", err)
	}
	if !ack.Accepted {
		return fmt.Errorf("gateway rejected hello: %s", ack.Reason)
	}
	batch := int(r.cfg.MaxBatchFrames)
	if ack.MaxBatchFrames > 0 && int(ack.MaxBatchFrames) < batch {
		batch = int(ack.MaxBatchFrames)
	}
	if batch <= 0 {
		batch = 1
	}
	r.mu.Lock()
	r.maxBatch = batch
	r.mu.Unlock()
	// 清掉握手期的临时读超时：否则空闲 DialTimeout(3s) 后 ReadMsg 超时 → 反复重连。
	// （三类 deadline：握手临时 / 业务读（无）/ 写操作一次一设；读超时用完必须清零。）
	_ = uc.SetReadDeadline(time.Time{})
	return nil
}

// writer 从队列取帧 → 组批 → SplitDeliverReq → 写 UDS。连接断开即返回。
func (r *Remote) writer(uc *connproto.UDSConn, done <-chan struct{}, gen uint64) {
	ticker := time.NewTicker(r.cfg.FlushInterval)
	defer ticker.Stop()
	pending := make([]queuedFrame, 0, 64)
	var batchID uint64

	flush := func() {
		if len(pending) == 0 {
			return
		}
		items := pending
		pending = make([]queuedFrame, 0, 64)
		// 按 userID 归组（保持同一用户内的 seq 顺序）。
		grouped := map[int64][]connproto.Frame{}
		order := make([]int64, 0, 4)
		for _, it := range items {
			if _, ok := grouped[it.userID]; !ok {
				order = append(order, it.userID)
			}
			grouped[it.userID] = append(grouped[it.userID], it.frame)
		}
		for _, userID := range order {
			frames := grouped[userID]
			maxBatch := r.currentMaxBatch()
			for len(frames) > 0 {
				n := maxBatch
				if n > len(frames) {
					n = len(frames)
				}
				batchID++
				req := &connproto.DeliverReq{UserID: userID, BatchID: batchID, Frames: frames[:n]}
				shards, err := connproto.SplitDeliverReq(req)
				if err != nil {
					r.writeErrors.Add(1)
					r.droppedCritical.Add(uint64(n))
					r.log.Printf("[connapi] WARN: split deliver req: %v", err)
					frames = frames[n:]
					continue
				}
				if len(shards) > 1 {
					r.batchSplit.Add(1)
				}
				for _, sh := range shards {
					body, err := connproto.EncodeDeliverReq(sh)
					if err != nil {
						r.writeErrors.Add(1)
						r.droppedCritical.Add(uint64(len(sh.Frames)))
						continue
					}
					r.mu.Lock()
					r.inflight[sh.BatchID] = inflightBatch{userID: userID, frames: len(sh.Frames), at: r.cfg.Now()}
					r.mu.Unlock()
					if err := r.writeFrame(uc, connproto.KindDeliverReq, body); err != nil {
						r.writeErrors.Add(1)
						r.droppedCritical.Add(uint64(len(sh.Frames)))
						_ = uc.Close() // 触发 reader 退出 → 重连；帧留给位点补发
						return
					}
				}
				frames = frames[n:]
			}
		}
	}

	for {
		select {
		case <-r.closed:
			return
		case <-done:
			return
		case it := <-r.queue:
			r.queueSize.Add(-int64(len(it.frame.Payload) + 32))
			pending = append(pending, it)
			if len(pending) >= r.currentMaxBatch() {
				flush()
			}
		case <-ticker.C:
			flush()
			r.sweepAckTimeouts()
		}
	}
}

// writeFrame 串行化对当前连接的写（reader 的应答与 writer 的投递会并发写同一 UDSConn；
// 不加锁会让帧头/帧体交错，网关侧看到的就是坏帧）。
func (r *Remote) writeFrame(uc *connproto.UDSConn, kind connproto.UDSKind, body []byte) error {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	_ = uc.SetWriteDeadline(r.cfg.Now().Add(r.cfg.WriteTimeout))
	return uc.WriteMsg(kind, body)
}

// currentMaxBatch 读取协商后的批上限。
func (r *Remote) currentMaxBatch() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxBatch <= 0 {
		return int(connproto.UDSMaxBatch)
	}
	return r.maxBatch
}

// sweepAckTimeouts 统计超过 AckTimeout 仍未回执的批次（只计数：不重发，位点补发兜底）。
func (r *Remote) sweepAckTimeouts() {
	now := r.cfg.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, b := range r.inflight {
		if now.Sub(b.at) > r.cfg.AckTimeout {
			delete(r.inflight, id)
			r.ackTimeouts.Add(1)
		}
	}
}

// reader 处理网关→内核消息。返回即代表连接断开。
func (r *Remote) reader(uc *connproto.UDSConn) {
	for {
		kind, _, body, err := uc.ReadMsg()
		if err != nil {
			r.log.Printf("[connapi] WARN: UDS read closed: %v (将重连)", err)
			return
		}
		switch kind {
		case connproto.KindDeliverAck:
			var ack connproto.DeliverAck
			if err := connproto.DecodeJSONBody(body, &ack); err != nil {
				continue
			}
			r.deliverAcks.Add(1)
			r.deliveredFrames.Add(uint64(ack.Accepted))
			r.droppedSlow.Add(uint64(ack.DroppedSlow))
			r.mu.Lock()
			delete(r.inflight, ack.BatchID)
			r.mu.Unlock()
		case connproto.KindAckUp:
			var au connproto.AckUp
			if err := connproto.DecodeJSONBody(body, &au); err != nil {
				continue
			}
			if r.cfg.OnAckUp != nil && !r.cfg.OnAckUp(au) {
				r.ackUpsDropped.Add(1)
				continue
			}
			r.ackUps.Add(1)
		case connproto.KindResumeReq:
			var req connproto.ResumeReq
			if err := connproto.DecodeJSONBody(body, &req); err != nil {
				continue
			}
			r.resumes.Add(1)
			resp := connproto.ResumeResp{OK: false, Reason: "resume_unavailable"}
			var frames []connproto.Frame
			if r.cfg.OnResume != nil {
				resp, frames = r.cfg.OnResume(context.Background(), req)
			}
			// 铁律：先 ResumeResp，再补发 DeliverReq（同一连接顺序）。
			rb, err := connproto.EncodeJSONBody(resp)
			if err != nil {
				continue
			}
			if err := r.writeFrame(uc, connproto.KindResumeResp, rb); err != nil {
				return
			}
			if resp.OK && len(frames) > 0 {
				if err := r.writeReplayFrames(uc, req.UserID, frames); err != nil {
					return
				}
			}
		case connproto.KindConnOpen:
			var op connproto.ConnOpen
			if err := connproto.DecodeJSONBody(body, &op); err != nil {
				r.log.Printf("[connapi] WARN: decode ConnOpen: %v", err)
				continue
			}
			r.connOpens.Add(1)
			ack := connproto.ConnOpenAck{OK: true}
			if r.cfg.OnConnOpen != nil {
				ack = r.cfg.OnConnOpen(op)
			}
			b, err := connproto.EncodeJSONBody(ack)
			if err != nil {
				r.log.Printf("[connapi] WARN: encode ConnOpenAck: %v", err)
				continue
			}
			if err := r.writeFrame(uc, connproto.KindConnOpenAck, b); err != nil {
				r.log.Printf("[connapi] WARN: write ConnOpenAck: %v", err)
				return
			}
		case connproto.KindConnClosed:
			var cl connproto.ConnClosed
			if err := connproto.DecodeJSONBody(body, &cl); err != nil {
				continue
			}
			if r.cfg.OnConnClosed != nil {
				r.cfg.OnConnClosed(cl)
			}
		case connproto.KindPresenceLease:
			var l connproto.PresenceLease
			if err := connproto.DecodeJSONBody(body, &l); err != nil {
				continue
			}
			r.presenceLeases.Add(1)
			if r.cfg.OnPresenceLease != nil && !r.cfg.OnPresenceLease(l) {
				r.presenceLeasesDropped.Add(1)
			}
		case connproto.KindStatsReq:
			b, err := connproto.EncodeJSONBody(connproto.StatsResp{
				FramesIn:  r.Stats().FramesIn,
				FramesOut: r.Metrics().DeliveredFrames,
				Dropped:   r.Metrics().DroppedQueueFull + r.Metrics().DroppedCritical,
			})
			if err != nil {
				continue
			}
			if err := r.writeFrame(uc, connproto.KindStatsResp, b); err != nil {
				return
			}
		case connproto.KindPing:
			_ = r.writeFrame(uc, connproto.KindPong, nil)
		case connproto.KindDrain:
			var dr connproto.Drain
			_ = connproto.DecodeJSONBody(body, &dr)
			r.drainCount.Add(1)
			r.mu.Lock()
			r.draining = true
			r.state = "draining"
			r.mu.Unlock()
			r.log.Printf("[connapi] WARN: gateway draining (deadline_ms=%d): stop delivering new frames, keep AckUp", dr.DeadlineMS)
		case connproto.KindKick:
			var kick connproto.Kick
			if err := connproto.DecodeJSONBody(body, &kick); err == nil && r.cfg.OnKick != nil {
				r.cfg.OnKick(kick)
			}
		default:
			// 前向兼容：未知 kind 忽略（版本协商已保证同版本）。
		}
	}
}

// writeReplayFrames 把 resume 补发帧切批写回（先 ResumeResp 之后调用）。
func (r *Remote) writeReplayFrames(uc *connproto.UDSConn, userID int64, frames []connproto.Frame) error {
	maxBatch := r.currentMaxBatch()
	var batchID uint64
	for len(frames) > 0 {
		n := maxBatch
		if n > len(frames) {
			n = len(frames)
		}
		batchID++
		req := &connproto.DeliverReq{UserID: userID, BatchID: batchID, Frames: frames[:n]}
		shards, err := connproto.SplitDeliverReq(req)
		if err != nil {
			return err
		}
		if len(shards) > 1 {
			r.batchSplit.Add(1)
		}
		for _, sh := range shards {
			body, err := connproto.EncodeDeliverReq(sh)
			if err != nil {
				return err
			}
			r.mu.Lock()
			r.inflight[sh.BatchID] = inflightBatch{userID: userID, frames: len(sh.Frames), at: r.cfg.Now()}
			r.mu.Unlock()
			if err := r.writeFrame(uc, connproto.KindDeliverReq, body); err != nil {
				return err
			}
		}
		frames = frames[n:]
	}
	return nil
}

// setState 更新连接状态（并发安全）。
func (r *Remote) setState(state string) {
	r.mu.Lock()
	r.state = state
	r.mu.Unlock()
}

// sleep 等待 d；Close 时立即返回 false。
func (r *Remote) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-r.closed:
		return false
	case <-t.C:
		return true
	}
}

// nextBackoff 指数退避（上限 max）。
func nextBackoff(cur, max time.Duration) time.Duration {
	n := cur * 2
	if n <= 0 || n > max {
		return max
	}
	return n
}

// 编译期断言：Remote 实现 EventSink + Closer。
var (
	_ EventSink = (*Remote)(nil)
	_ Closer    = (*Remote)(nil)
)
