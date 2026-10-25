// SPDX-License-Identifier: MIT
package native

import (
	"context"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"human-agent-im/internal/connproto"
)

// fakeClock 可推进的测试时钟。
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// fakeSnapshot 记录快照写入。
type fakeSnapshot struct {
	mu   sync.Mutex
	rows []PresenceRow
}

func (f *fakeSnapshot) UpsertPresence(_ context.Context, rows []PresenceRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, rows...)
	return nil
}

func (f *fakeSnapshot) snapshot() []PresenceRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]PresenceRow, len(f.rows))
	copy(out, f.rows)
	return out
}

func newPresence(t *testing.T) (*Presence, *fakeClock, *fakeSnapshot) {
	t.Helper()
	clk := newFakeClock()
	snap := &fakeSnapshot{}
	p := NewPresence(Options{
		TTL:      45 * time.Second,
		Tick:     time.Second,
		Now:      clk.Now,
		Logger:   log.New(io.Discard, "", 0),
		Snapshot: snap,
	})
	return p, clk, snap
}

func TestPresenceLeaseTTLExpiry(t *testing.T) {
	p, clk, _ := newPresence(t)
	if !p.OnLease(connproto.PresenceLease{UserID: 7, DeviceID: "d1", TTLS: 45, State: "online"}) {
		t.Fatal("lease rejected")
	}
	if state, devs := p.Aggregate(7); state != "online" || devs != 1 {
		t.Fatalf("aggregate = (%s,%d), want (online,1)", state, devs)
	}
	// TTL 内仍然 online。
	clk.Advance(44 * time.Second)
	p.expire(clk.Now())
	if state, _ := p.Aggregate(7); state != "online" {
		t.Fatalf("44s state=%s, want online", state)
	}
	// 超过 TTL → offline（只能由内核推断）。
	clk.Advance(2 * time.Second)
	p.expire(clk.Now())
	if state, devs := p.Aggregate(7); state != "offline" || devs != 0 {
		t.Fatalf("expired aggregate = (%s,%d), want (offline,0)", state, devs)
	}
	if st := p.Stats(); st.Expired != 1 {
		t.Fatalf("expired counter = %d, want 1", st.Expired)
	}
}

func TestPresenceMultiDeviceAggregate(t *testing.T) {
	p, _, _ := newPresence(t)
	p.OnLease(connproto.PresenceLease{UserID: 7, DeviceID: "phone", State: "online"})
	p.OnLease(connproto.PresenceLease{UserID: 7, DeviceID: "tablet", State: "online"})
	if state, devs := p.Aggregate(7); state != "online" || devs != 2 {
		t.Fatalf("two devices = (%s,%d), want (online,2)", state, devs)
	}
	// 断开其一仍 ONLINE（多端聚合）。
	p.OnConnClosed(connproto.ConnClosed{UserID: 7, DeviceID: "phone"})
	if state, devs := p.Aggregate(7); state != "online" || devs != 1 {
		t.Fatalf("after one closed = (%s,%d), want (online,1)", state, devs)
	}
	// 全断 → OFFLINE。
	p.OnConnClosed(connproto.ConnClosed{UserID: 7, DeviceID: "tablet"})
	if state, _ := p.Aggregate(7); state != "offline" {
		t.Fatalf("all closed = %s, want offline", state)
	}
}

func TestPresenceRejectsOfflineReport(t *testing.T) {
	p, _, _ := newPresence(t)
	// 网关/客户端不得直接上报 offline（只能由内核 TTL 推断，防伪造）。
	if p.OnLease(connproto.PresenceLease{UserID: 7, DeviceID: "d1", State: "offline"}) {
		t.Fatal("offline lease 应被拒绝")
	}
	if state, _ := p.Aggregate(7); state != "offline" {
		// 拒绝上报后该 user 从未有 lease → offline 是自然值（不是被上报污染的）。
		t.Fatalf("state=%s", state)
	}
	if st := p.Stats(); st.RejectedOffline != 1 {
		t.Fatalf("RejectedOffline = %d, want 1", st.RejectedOffline)
	}
}

func TestPresenceAwayAndAck(t *testing.T) {
	p, _, _ := newPresence(t)
	p.OnLease(connproto.PresenceLease{UserID: 7, DeviceID: "d1", State: "away"})
	if state, _ := p.Aggregate(7); state != "away" {
		t.Fatalf("away aggregate = %s", state)
	}
	// ack 即活 → online。
	p.OnAck(7, "d1")
	if state, devs := p.Aggregate(7); state != "online" || devs != 1 {
		t.Fatalf("after ack = (%s,%d), want (online,1)", state, devs)
	}
	// 没先收到 lease 的设备，ack 也应视为 online。
	p.OnAck(8, "d-new")
	if state, _ := p.Aggregate(8); state != "online" {
		t.Fatalf("ack without lease state=%s, want online", state)
	}
}

func TestPresenceAggregateCallback(t *testing.T) {
	var mu sync.Mutex
	var events []connproto.PresenceState
	p := NewPresence(Options{
		TTL:    45 * time.Second,
		Now:    newFakeClock().Now,
		Logger: log.New(io.Discard, "", 0),
		OnAggregate: func(st connproto.PresenceState) {
			mu.Lock()
			events = append(events, st)
			mu.Unlock()
		},
	})
	p.OnLease(connproto.PresenceLease{UserID: 7, DeviceID: "d1", State: "online"})
	p.OnLease(connproto.PresenceLease{UserID: 7, DeviceID: "d2", State: "online"}) // devices 1→2，应触发
	p.OnLease(connproto.PresenceLease{UserID: 7, DeviceID: "d3", State: "online"}) // devices 2→3
	mu.Lock()
	defer mu.Unlock()
	if len(events) < 3 {
		t.Fatalf("aggregate events = %d, want >=3", len(events))
	}
	last := events[len(events)-1]
	if last.State != "online" || last.Devices != 3 {
		t.Fatalf("last aggregate = %+v, want online/3", last)
	}
}

func TestPresenceSnapshotOnlyActive(t *testing.T) {
	p, clk, snap := newPresence(t)
	p.OnLease(connproto.PresenceLease{UserID: 7, DeviceID: "d1", State: "online"})
	ctx := context.Background()
	p.SnapshotOnce(ctx)
	rows := snap.snapshot()
	if len(rows) != 1 || rows[0].UserID != 7 || rows[0].State != "online" {
		t.Fatalf("first snapshot = %+v, want 1 row online user7", rows)
	}
	// 7 过期 → offline；快照不再包含 offline 用户。
	clk.Advance(46 * time.Second)
	p.expire(clk.Now())
	// 另起一个活跃的 away 用户，快照应只写它。
	p.OnLease(connproto.PresenceLease{UserID: 8, DeviceID: "d2", State: "away"})
	p.SnapshotOnce(ctx)
	rows = snap.snapshot()
	if len(rows) != 2 { // 第一次 1 行 + 第二次 1 行（只写活跃，offline 的 7 不写）
		t.Fatalf("snapshot rows after expiry = %d, want 2 (只写活跃)", len(rows))
	}
	last := rows[len(rows)-1]
	if last.UserID != 8 || last.State != "away" {
		t.Fatalf("last snapshot row = %+v, want user 8 away", last)
	}
}

func TestPresenceStartTicksAndStop(t *testing.T) {
	clk := newFakeClock()
	p := NewPresence(Options{TTL: 10 * time.Millisecond, Tick: 5 * time.Millisecond, Now: clk.Now, Logger: log.New(io.Discard, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	p.OnLease(connproto.PresenceLease{UserID: 1, DeviceID: "d1", State: "online"})
	// 用真实时间等 tick 清掉一个已过期 lease（TTL 10ms，fake clock 不前进 → 不删）；
	// 把 fake clock 推过头，逐 tick 自然过期。
	clk.Advance(20 * time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && p.Stats().Expired == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if p.Stats().Expired == 0 {
		t.Fatal("tick 未清理过期租约")
	}
	p.Stop()
	p.Stop() // 幂等
}
