// SPDX-License-Identifier: MIT
package native

import (
	"context"
	"log"
	"sync"
	"time"

	"human-agent-im/internal/connproto"
)

// C1 presence 默认参数（与 CONN-GATEWAY-DESIGN §8.2 一致）。
const (
	DefaultPresenceTTL              = 45 * time.Second
	DefaultPresenceTick             = 5 * time.Second
	DefaultPresenceSnapshotInterval = 30 * time.Second
)

// Options 是 Presence 构造参数；零值补默认。
type Options struct {
	TTL              time.Duration
	Tick             time.Duration
	SnapshotInterval time.Duration
	Now              func() time.Time
	Logger           *log.Logger

	// Snapshot 可选（nil=不快照，仅内存）；PG 实现见 PGStore。
	Snapshot SnapshotStore

	// OnAggregate 在聚合状态**变化**时回调（bootstrap 接到 Bridge.DeliverEphemeral）。
	OnAggregate func(st connproto.PresenceState)
}

func (o Options) normalized() Options {
	if o.TTL <= 0 {
		o.TTL = DefaultPresenceTTL
	}
	if o.Tick <= 0 {
		o.Tick = DefaultPresenceTick
	}
	if o.SnapshotInterval <= 0 {
		o.SnapshotInterval = DefaultPresenceSnapshotInterval
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	return o
}

// deviceLease 是单设备租约（presence 真相只在内存）。
type deviceLease struct {
	state   string // online | away
	expires time.Time
}

// Presence 是内核侧 presence 状态机：租约刷新/过期、多端聚合、可见性由上层路由负责。
//
// 并发：所有状态在 mu 下读写；OnAggregate 回调在锁外调用，避免回调阻塞状态机。
type Presence struct {
	opt Options

	mu     sync.Mutex
	leases map[int64]map[string]*deviceLease // userID → deviceID → lease
	states map[int64]string                  // userID → 聚合态（online/away/offline，用于变化检测）
	counts map[int64]int                     // userID → 当前在线设备数（聚合态为 online 的设备数）

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup

	// 指标
	leasesSeen      uint64
	expired         uint64
	rejectedOffline uint64
	snapshots       uint64
	aggChanges      uint64
}

// NewPresence 构造状态机（不自动跑 tick；Start 后生效）。
func NewPresence(opt Options) *Presence {
	return &Presence{
		opt:    opt.normalized(),
		leases: map[int64]map[string]*deviceLease{},
		states: map[int64]string{},
		counts: map[int64]int{},
		stop:   make(chan struct{}),
	}
}

// Start 启动 TTL 清理 tick 与（可选）快照 upsert。ctx 取消或 Stop 时退出。
func (p *Presence) Start(ctx context.Context) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		tick := time.NewTicker(p.opt.Tick)
		defer tick.Stop()
		snap := time.NewTicker(p.opt.SnapshotInterval)
		defer snap.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stop:
				return
			case <-tick.C:
				p.expire(p.opt.Now())
			case <-snap.C:
				p.SnapshotOnce(ctx)
			}
		}
	}()
}

// Stop 停止后台循环（幂等）。
func (p *Presence) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	p.wg.Wait()
}

// OnLease 消费网关上行租约。只接受 online/away；offline 一律拒绝（只能由内核按 TTL 推断）。
func (p *Presence) OnLease(l connproto.PresenceLease) bool {
	if l.UserID <= 0 || l.DeviceID == "" {
		return false
	}
	state := l.State
	if state == "" {
		state = "online"
	}
	if state != "online" && state != "away" {
		p.mu.Lock()
		p.rejectedOffline++
		p.mu.Unlock()
		return false
	}
	now := p.opt.Now()
	ttl := p.opt.TTL
	if l.TTLS > 0 && time.Duration(l.TTLS)*time.Second < ttl {
		ttl = time.Duration(l.TTLS) * time.Second
	}
	p.mu.Lock()
	if p.leases[l.UserID] == nil {
		p.leases[l.UserID] = map[string]*deviceLease{}
	}
	p.leases[l.UserID][l.DeviceID] = &deviceLease{state: state, expires: now.Add(ttl)}
	p.leasesSeen++
	p.mu.Unlock()
	p.recompute(l.UserID, now)
	return true
}

// OnAck 客户端字节级 ack = 连接活着：续租并把该设备置回 online。
func (p *Presence) OnAck(userID int64, deviceID string) {
	if userID <= 0 || deviceID == "" {
		return
	}
	now := p.opt.Now()
	p.mu.Lock()
	if p.leases[userID] == nil {
		p.leases[userID] = map[string]*deviceLease{}
	}
	if cur, ok := p.leases[userID][deviceID]; ok {
		cur.state = "online"
		cur.expires = now.Add(p.opt.TTL)
	} else {
		p.leases[userID][deviceID] = &deviceLease{state: "online", expires: now.Add(p.opt.TTL)}
	}
	p.mu.Unlock()
	p.recompute(userID, now)
}

// OnConnClosed 网关上报连接关闭：注销该设备租约（offline 由聚合/TTL 推断）。
func (p *Presence) OnConnClosed(cl connproto.ConnClosed) {
	if cl.UserID <= 0 || cl.DeviceID == "" {
		return
	}
	now := p.opt.Now()
	p.mu.Lock()
	if m := p.leases[cl.UserID]; m != nil {
		delete(m, cl.DeviceID)
		if len(m) == 0 {
			delete(p.leases, cl.UserID)
		}
	}
	p.mu.Unlock()
	p.recompute(cl.UserID, now)
}

// Aggregate 返回某 user 的聚合态与"在线设备数"（online 状态的设备）。
// 聚合规则：ONLINE > AWAY > OFFLINE（多端取最强）；只返回聚合值给他人。
func (p *Presence) Aggregate(userID int64) (state string, onlineDevices int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.aggregateLocked(userID)
}

// Devices 返回某 user 的逐设备状态快照（**只返回给本人**；对他人只给聚合态）。
func (p *Presence) Devices(userID int64) map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.leases[userID]))
	for dev, l := range p.leases[userID] {
		out[dev] = l.state
	}
	return out
}

// aggregateLocked 计算聚合态；调用方需持锁。
func (p *Presence) aggregateLocked(userID int64) (string, int) {
	m := p.leases[userID]
	if len(m) == 0 {
		return "offline", 0
	}
	online := 0
	away := 0
	for _, l := range m {
		switch l.state {
		case "online":
			online++
		case "away":
			away++
		}
	}
	switch {
	case online > 0:
		return "online", online
	case away > 0:
		return "away", 0
	default:
		return "offline", 0
	}
}

// recompute 重新计算聚合态；变化时触发 OnAggregate（锁外）。
func (p *Presence) recompute(userID int64, now time.Time) {
	p.mu.Lock()
	state, devices := p.aggregateLocked(userID)
	prev, had := p.states[userID]
	prevCount := p.counts[userID]
	changed := !had || prev != state || prevCount != devices
	p.states[userID] = state
	p.counts[userID] = devices
	if changed {
		p.aggChanges++
	}
	p.mu.Unlock()
	if !changed {
		return
	}
	if p.opt.OnAggregate != nil {
		p.opt.OnAggregate(connproto.PresenceState{
			UserID:    userID,
			State:     state,
			UpdatedAt: now.Unix(),
			Devices:   devices,
		})
	}
}

// expire 清理过期租约（TTL 到 → 设备消失 → 可能 offline）。
func (p *Presence) expire(now time.Time) {
	p.mu.Lock()
	changed := make([]int64, 0, 8)
	for userID, m := range p.leases {
		removedAny := false
		for dev, l := range m {
			if !l.expires.After(now) {
				delete(m, dev)
				removedAny = true
				p.expired++
			}
		}
		if removedAny {
			changed = append(changed, userID)
		}
		if len(m) == 0 {
			delete(p.leases, userID)
		}
	}
	p.mu.Unlock()
	for _, uid := range changed {
		p.recompute(uid, now)
	}
}

// SnapshotOnce 把当前活跃（非 offline）聚合态 upsert 到快照存储（观测用，非真相源）。
func (p *Presence) SnapshotOnce(ctx context.Context) {
	if p.opt.Snapshot == nil {
		return
	}
	now := p.opt.Now()
	p.mu.Lock()
	rows := make([]PresenceRow, 0, len(p.states))
	for userID, state := range p.states {
		if state == "offline" {
			continue
		}
		count := p.counts[userID]
		rows = append(rows, PresenceRow{UserID: userID, State: state, DeviceCount: count, LastSeen: now})
	}
	p.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	if err := p.opt.Snapshot.UpsertPresence(ctx, rows); err != nil {
		p.opt.Logger.Printf("[native/presence] WARN: snapshot upsert failed: %v", err)
		return
	}
	p.mu.Lock()
	p.snapshots++
	p.mu.Unlock()
}

// TTL 返回配置的租约有效期（日志/自检用）。
func (p *Presence) TTL() time.Duration { return p.opt.TTL }

// Tick 返回配置的清理 tick（日志/自检用）。
func (p *Presence) Tick() time.Duration { return p.opt.Tick }

// Stats 返回 presence 指标快照（观测/验收用）。
type Stats struct {
	Users           int
	Leases          int
	LeasesSeen      uint64
	Expired         uint64
	RejectedOffline uint64
	Snapshots       uint64
	AggChanges      uint64
}

// Stats 返回指标快照。
func (p *Presence) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	leases := 0
	for _, m := range p.leases {
		leases += len(m)
	}
	return Stats{
		Users:           len(p.states),
		Leases:          leases,
		LeasesSeen:      p.leasesSeen,
		Expired:         p.expired,
		RejectedOffline: p.rejectedOffline,
		Snapshots:       p.snapshots,
		AggChanges:      p.aggChanges,
	}
}
