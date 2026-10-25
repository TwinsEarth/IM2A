// Package connhub 是连接网关的连接管理核心（Wave B1）。
//
// 职责边界（CONN-GATEWAY-DESIGN.md v2 §8.1）：
//   * 只认帧、不认业务：payload 是 []byte，本包不解析；
//   * 负责每连接有界队列（256 槽 + 16KB 字节预算双约束）、背压动作
//     （80% 降级 / 100% 断开慢消费者）、按 connID 分片（64 shard）串行投递；
//   * 通过 Sink 抽象出传输层：生产实现是 cmd/conn-gateway 的 WSSink（gorilla 读 +
//     裸 net.Conn writev 写）；测试实现是内存 sink。这样零丢测试不需要真实 socket。
//
// 投递路径：
//
//	Hub.Deliver(userID, frames)
//	  → 解析该 user 的连接（多端）
//	  → 每连接一个 task 投进其所属 shard 的 in 队列（非阻塞）
//	  → shard goroutine 串行调 Conn.push（O(1)，只做 ring 入队/水位判定）
//	  → Conn 写循环异步写出，**写成功之后**才 ack（零丢契约）
//
// 为什么按 connID 分片而不是 userID：connID 是连接维度天然主键，注册/注销/投递只碰一个
// 分片；同一 user 的多设备连接分散在多个分片，由 Hub 层展开（多端上限 8，成本可忽略）。
package connhub

import (
	"encoding/binary"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"human-agent-im/internal/connproto"
)

// Sink 一帧（或一批帧）的最终出口。WriteFrames 返回 nil 才允许上报 ack。
type Sink interface {
	WriteFrames(frames []connproto.Frame) error
	Close() error
}

// Config hub 参数（默认值与设计 §1.2 一致）。
type Config struct {
	Shards     int // 必须 2 的幂；默认 64
	RingFrames int // 每连接 ring 槽位；默认 256
	// RingCriticalReserve 是 RingFrames 中**只给 critical 帧**预留的槽位数（默认 32）。
	// 理由（C5a）：普通帧最多用 RingFrames-Reserve，critical 可用满 RingFrames——这样在
	// "单连接积压大量小帧、字节预算还没满"的偏门场景下，critical（审批/信任）不会被普通帧挤掉。
	// **不改变字节预算**（16KB 仍是权威水位，超了照样丢弃/踢慢消费者），0 表示关闭预留。
	RingCriticalReserve int
	RingBytes           int // 每连接字节预算（权威水位）；默认 16384
	WriteBatch int // 一次 write 最多合并帧数；默认 48
	ShardQueue int // 每 shard 待处理 task 队列；默认 1024

	// DrainTimeout 关闭连接时等待写循环排空的上限；默认 5s。
	DrainTimeout time.Duration

	// OnAck 在**帧真正写出之后**上报累积确认（gateway → kernel）。
	OnAck func(connID uint64, userID int64, seq uint64)
	// OnSlowConsumer 队列达到 100% 字节水位、连接被判定慢消费者时回调（随后会断开）。
	OnSlowConsumer func(connID uint64, userID int64, queuedBytes int)
	// OnDegrade 队列越过 80% 水位、进入降级模式时回调（之后非关键帧被丢弃）。
	OnDegrade func(connID uint64, userID int64)
	Logf      func(format string, a ...any)
}

// DefaultConfig 生产默认值。
func DefaultConfig() Config {
	return Config{
		Shards:       64,
		RingFrames:          256,
		RingCriticalReserve: 32,
		RingBytes:           16384,
		WriteBatch:   48,
		ShardQueue:   1024,
		DrainTimeout: 5 * time.Second,
		Logf:         func(string, ...any) {},
	}
}

func (c Config) normalize() Config {
	d := DefaultConfig()
	if c.Shards <= 0 {
		c.Shards = d.Shards
	}
	// 分片数必须是 2 的幂，否则向上取整（保证 shardIndex 可用位与）。
	if c.Shards&(c.Shards-1) != 0 {
		n := 1
		for n < c.Shards {
			n <<= 1
		}
		c.Shards = n
	}
	if c.RingFrames <= 0 {
		c.RingFrames = d.RingFrames
	}
	if c.RingBytes <= 0 {
		c.RingBytes = d.RingBytes
	}
	if c.WriteBatch <= 0 {
		c.WriteBatch = d.WriteBatch
	}
	if c.ShardQueue <= 0 {
		c.ShardQueue = d.ShardQueue
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = d.DrainTimeout
	}
	if c.Logf == nil {
		c.Logf = d.Logf
	}
	return c
}

// DeliverResult 一次投递的结果。
type DeliverResult struct {
	TargetConns      int // 命中的连接数
	Enqueued         int // 成功投进 shard 队列的帧数（最终入队/丢弃见 Hub.Stats）
	DroppedSlow      int // 因 shard 队列满而丢弃的帧数
	TriggeredDegrade int // 保留字段（降级由 Conn 异步回调统计）
	TriggeredKick    int // 保留字段（慢消费者由 Conn 异步回调统计）
}

// Stats hub 快照（/metrics 用）。
type Stats struct {
	Conns      int    `json:"conns"`
	Shards     int    `json:"shards"`
	FramesIn   uint64 `json:"frames_in"`
	FramesOut  uint64 `json:"frames_out"`
	FramesDrop uint64 `json:"frames_drop"`
	SlowKicks  uint64 `json:"slow_kicks"`
	Degrades   uint64 `json:"degrades"`
}

// deliverTask 一次（连接, 帧批）投递任务。
type deliverTask struct {
	connID uint64
	frames []connproto.Frame
}

// Shard 一个连接分片：一个 goroutine 串行处理投递，保证同一连接的入队顺序。
type Shard struct {
	idx  int
	hub  *Hub
	in   chan *deliverTask
	mu   sync.RWMutex
	conn map[uint64]*Conn
}

func (s *Shard) loop() {
	for task := range s.in {
		s.mu.RLock()
		c := s.conn[task.connID]
		s.mu.RUnlock()
		if c == nil {
			// 连接已注销：这些帧未入队、未 ack，由内核从 lastAck+1 补发。
			s.hub.framesDrop.Add(uint64(len(task.frames)))
			continue
		}
		accepted, dropped, kicked := c.push(task.frames)
		if accepted > 0 {
			s.hub.framesIn.Add(uint64(accepted))
		}
		if dropped > 0 {
			s.hub.framesDrop.Add(uint64(dropped))
		}
		if kicked > 0 {
			s.hub.slowKicks.Add(uint64(kicked))
		}
	}
}

// Hub 连接表。
type Hub struct {
	cfg    Config
	shards []*Shard

	mu     sync.RWMutex
	conns  map[uint64]*Conn
	byUID  map[int64]map[uint64]*Conn
	closed bool

	nextID atomic.Uint64

	framesIn   atomic.Uint64
	framesOut  atomic.Uint64
	framesDrop atomic.Uint64
	slowKicks  atomic.Uint64
	degrades   atomic.Uint64

	loopWG sync.WaitGroup
}

// NewHub 创建并启动 hub。
func NewHub(cfg Config) *Hub {
	cfg = cfg.normalize()
	h := &Hub{
		cfg:   cfg,
		conns: make(map[uint64]*Conn, 1024),
		byUID: make(map[int64]map[uint64]*Conn, 256),
	}
	h.shards = make([]*Shard, cfg.Shards)
	for i := range h.shards {
		s := &Shard{idx: i, hub: h, in: make(chan *deliverTask, cfg.ShardQueue), conn: make(map[uint64]*Conn, 64)}
		h.shards[i] = s
		h.loopWG.Add(1)
		go func(s *Shard) { defer h.loopWG.Done(); s.loop() }(s)
	}
	return h
}

// shardIndex connID → 分片：FNV-1a(8B LE) & (N-1)，与设计文档一致。
func (h *Hub) shardIndex(connID uint64) int {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], connID)
	f := fnv.New64a()
	_, _ = f.Write(buf[:])
	return int(f.Sum64() & uint64(len(h.shards)-1))
}

// ShardCount 分片数（测试/诊断）。
func (h *Hub) ShardCount() int { return len(h.shards) }

// ShardOf 返回某连接所在分片下标（测试用）。
func (h *Hub) ShardOf(connID uint64) int { return h.shardIndex(connID) }

// Register 注册一条连接并启动其写循环。
func (h *Hub) Register(userID int64, deviceID string, sink Sink) *Conn {
	return h.RegisterWithID(h.nextID.Add(1), userID, deviceID, sink)
}

// NextConnID 预分配一个 connID（不注册）。
//
// C1（task-21）需要它：网关必须**先**拿到 connID 才能向内核发 ConnOpen 验明身份，
// 验签成功拿到权威 userID 之后才用 RegisterWithID 建索引 —— 这样绝不会出现
// "先按不可信身份注册、再改索引"的窗口（改索引要动 Conn.userID，会和写循环竞态）。
func (h *Hub) NextConnID() uint64 { return h.nextID.Add(1) }

// RegisterWithID 用调用方指定的 connID 注册（id 应来自 NextConnID）。
// connID 冲突时 panic：说明调用方逻辑错误，绝不静默覆盖别人的连接。
func (h *Hub) RegisterWithID(id uint64, userID int64, deviceID string, sink Sink) *Conn {
	h.mu.Lock()
	if _, exists := h.conns[id]; exists {
		h.mu.Unlock()
		panic("connhub: RegisterWithID: connID already in use")
	}
	closed := h.closed
	h.mu.Unlock()
	if closed {
		// hub 已关闭：注册一个立刻可读的"已关闭"连接没有意义，直接启动写循环让调用方
		// 自己 close（与 Register 在 Close 后的行为保持一致：仍注册，由 Close 时的清场兜底）。
		h.cfg.Logf("connhub: RegisterWithID on closed hub conn=%d user=%d", id, userID)
	}

	sh := h.shards[h.shardIndex(id)]
	c := newConn(h, id, userID, deviceID, sink, sh)

	sh.mu.Lock()
	sh.conn[id] = c
	sh.mu.Unlock()

	h.mu.Lock()
	h.conns[id] = c
	if h.byUID[userID] == nil {
		h.byUID[userID] = make(map[uint64]*Conn, 2)
	}
	h.byUID[userID][id] = c
	h.mu.Unlock()

	c.start()
	h.cfg.Logf("connhub: conn=%d registered user=%d device=%s shard=%d", id, userID, deviceID, sh.idx)
	return c
}


// remove 从连接表与分片表摘除（幂等，close/abort 都会调用）。
func (h *Hub) remove(connID uint64) {
	sh := h.shards[h.shardIndex(connID)]
	sh.mu.Lock()
	c := sh.conn[connID]
	delete(sh.conn, connID)
	sh.mu.Unlock()

	h.mu.Lock()
	if c != nil {
		delete(h.conns, connID)
		if set := h.byUID[c.userID]; set != nil {
			delete(set, connID)
			if len(set) == 0 {
				delete(h.byUID, c.userID)
			}
		}
	}
	h.mu.Unlock()
}

// Get 按 connID 取连接。
func (h *Hub) Get(connID uint64) *Conn {
	h.mu.RLock()
	c := h.conns[connID]
	h.mu.RUnlock()
	return c
}

// ConnCount 当前连接数。
func (h *Hub) ConnCount() int {
	h.mu.RLock()
	n := len(h.conns)
	h.mu.RUnlock()
	return n
}

// Deliver 向某用户的全部连接投递一批帧。
// 非阻塞：只做连接表读取 + 每连接一次 shard 队列入队；队列满则计数丢弃。
func (h *Hub) Deliver(userID int64, frames []connproto.Frame) DeliverResult {
	var res DeliverResult
	if len(frames) == 0 {
		return res
	}
	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		res.DroppedSlow = len(frames)
		return res
	}
	targets := make([]*Conn, 0, len(h.byUID[userID]))
	for _, c := range h.byUID[userID] {
		targets = append(targets, c)
	}
	res.TargetConns = len(targets)
	for _, c := range targets {
		task := &deliverTask{connID: c.id, frames: frames}
		select {
		case c.shard.in <- task:
			res.Enqueued += len(frames)
		default:
			// shard 队列满：该分片消化不过来，计丢弃（未 ack，内核会补发）。
			res.DroppedSlow += len(frames)
			h.framesDrop.Add(uint64(len(frames)))
		}
	}
	h.mu.RUnlock()
	return res
}

// DeliverConn 定向投递到某条连接。
func (h *Hub) DeliverConn(connID uint64, frames []connproto.Frame) DeliverResult {
	h.mu.RLock()
	c := h.conns[connID]
	closed := h.closed
	h.mu.RUnlock()
	if c == nil || closed || len(frames) == 0 {
		return DeliverResult{DroppedSlow: len(frames)}
	}
	task := &deliverTask{connID: connID, frames: frames}
	select {
	case c.shard.in <- task:
		return DeliverResult{TargetConns: 1, Enqueued: len(frames)}
	default:
		h.framesDrop.Add(uint64(len(frames)))
		return DeliverResult{DroppedSlow: len(frames)}
	}
}

// CloseConn 同步关闭一条连接：返回后该连接不会再产生 ack。
func (h *Hub) CloseConn(connID uint64, code uint32, reason string) {
	if c := h.Get(connID); c != nil {
		c.close(code, reason)
	}
}

// Close 优雅关闭：先 drain 所有连接，再停 shard 循环。
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	conns := make([]*Conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()

	for _, c := range conns {
		c.close(1001, "server_shutdown")
	}
	for _, s := range h.shards {
		close(s.in)
	}
	h.loopWG.Wait()
}

// Stats 快照。
func (h *Hub) Stats() Stats {
	h.mu.RLock()
	n := len(h.conns)
	h.mu.RUnlock()
	return Stats{
		Conns:      n,
		Shards:     len(h.shards),
		FramesIn:   h.framesIn.Load(),
		FramesOut:  h.framesOut.Load(),
		FramesDrop: h.framesDrop.Load(),
		SlowKicks:  h.slowKicks.Load(),
		Degrades:   h.degrades.Load(),
	}
}
