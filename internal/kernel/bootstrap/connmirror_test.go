// SPDX-License-Identifier: MIT
package bootstrap

import (
	"context"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"human-agent-im/internal/connapi"
	"human-agent-im/internal/connapi/delivery"
	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
	"human-agent-im/internal/stream"
)

// slowStore 包装 MemStore，让 Append 变慢：用于证明 EventTap 不阻塞 Broadcast 主路径。
type slowStore struct {
	*delivery.MemStore
	delay time.Duration
}

func (s slowStore) Append(ctx context.Context, userID int64, frames []delivery.AppendFrame) ([]delivery.Entry, error) {
	time.Sleep(s.delay)
	return s.MemStore.Append(ctx, userID, frames)
}

func newTap(t *testing.T, bridge *connapi.Bridge, queue int) *EventTap {
	t.Helper()
	tap := newEventTap(bridge, queue, log.New(io.Discard, "", 0), nil)
	t.Cleanup(tap.Close)
	return tap
}

func TestEncodeTapPayload(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "null"},
		{[]byte(`{"a":1}`), `{"a":1}`},
		{"hello", "hello"},
		{map[string]any{"type": "x", "n": float64(2)}, `{"n":2,"type":"x"}`},
	}
	for _, c := range cases {
		got, err := encodeTapPayload(c.in)
		if err != nil {
			t.Fatalf("encodeTapPayload(%#v): %v", c.in, err)
		}
		if string(got) != c.want {
			t.Fatalf("encodeTapPayload(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEventTapNonBlockingAndCounters(t *testing.T) {
	store := slowStore{MemStore: delivery.NewMemStore(delivery.Options{Now: time.Now}), delay: 20 * time.Millisecond}
	bridge := connapi.NewBridge(store, log.New(io.Discard, "", 0))
	tap := newTap(t, bridge, 2)
	hub := stream.NewHubWithIntervals(time.Minute, 30*time.Second, time.Minute)
	hub.SetMirror(tap.Enqueue)
	defer hub.SetMirror(nil)

	// 小队列 + 慢 Append：Broadcast 必须仍然微秒级（只入队），不能等 PG。
	const n = 50
	start := time.Now()
	for i := 0; i < n; i++ {
		hub.Broadcast("trust:424242", map[string]any{"type": "trust_granted", "n": i})
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Millisecond*time.Duration(n) {
		t.Fatalf("Broadcast 主路径被 tap 拖慢：%d 次耗时 %v", n, elapsed)
	}
	// 队列只有 2、worker 每条 20ms → 必然发生丢弃（且被计数）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, dropped, _, _ := tap.Stats()
		if dropped > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, dropped, _, _ := tap.Stats()
	if dropped == 0 {
		t.Fatalf("队列满未丢弃（dropped=0），非阻塞语义未生效")
	}
}

func TestEventTapUnmappedTopicsDropped(t *testing.T) {
	store := delivery.NewMemStore(delivery.Options{Now: time.Now})
	bridge := connapi.NewBridge(store, log.New(io.Discard, "", 0))
	tap := newTap(t, bridge, 8)
	hub := stream.NewHubWithIntervals(time.Minute, 30*time.Second, time.Minute)
	hub.SetMirror(tap.Enqueue)
	defer hub.SetMirror(nil)

	hub.Broadcast("subagent:planner", map[string]any{"type": "new_task"})
	hub.Broadcast("negotiation:sess-1", map[string]any{"type": "state_changed"})
	hub.Broadcast("trust:777", map[string]any{"type": "trust_granted"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		published, _, unmapped, _ := tap.Stats()
		if published >= 1 && unmapped >= 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	published, _, unmapped, _ := tap.Stats()
	t.Fatalf("tap stats = published=%d unmapped=%d, want published=1 unmapped=2", published, unmapped)
}

func TestEventTapPublishesToDeliveryStore(t *testing.T) {
	store := delivery.NewMemStore(delivery.Options{Now: time.Now})
	bridge := connapi.NewBridge(store, log.New(io.Discard, "", 0))
	tap := newTap(t, bridge, 8)
	hub := stream.NewHubWithIntervals(time.Minute, 30*time.Second, time.Minute)
	hub.SetMirror(tap.Enqueue)
	defer hub.SetMirror(nil)

	hub.Broadcast("approval:888", map[string]any{"type": "approval_needed", "id": float64(1)})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if published, _, _, _ := tap.Stats(); published >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if published, _, _, _ := tap.Stats(); published != 1 {
		t.Fatalf("published = %d, want 1", published)
	}
	last, err := store.LastSeq(context.Background(), 888)
	if err != nil || last != 1 {
		t.Fatalf("delivery log LastSeq=%d err=%v, want 1（事件必须先进 im_delivery_log）", last, err)
	}
}

func TestEventTapCloseIdempotent(t *testing.T) {
	tap := newTap(t, nil, 4)
	tap.Close()
	tap.Close() // 幂等
	tap.Enqueue("trust:1", nil)
}

// 并发 Enqueue + Close 不得 panic（竞态守卫；-race 下更严格）。
func TestEventTapConcurrentClose(t *testing.T) {
	store := delivery.NewMemStore(delivery.Options{Now: time.Now})
	bridge := connapi.NewBridge(store, log.New(io.Discard, "", 0))
	tap := newEventTap(bridge, 16, log.New(io.Discard, "", 0), nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tap.Enqueue("trust:1", map[string]any{"n": j})
			}
		}()
	}
	tap.Close()
	wg.Wait()
}

// c4RecordingSink 记录 Bridge.Publish 投递的帧（C4 critical 断言用）。
type c4RecordingSink struct {
	mu     sync.Mutex
	frames []connproto.Frame
}

func (s *c4RecordingSink) Deliver(_ int64, frames []connproto.Frame) connhub.DeliverResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, frames...)
	return connhub.DeliverResult{Enqueued: len(frames)}
}
func (s *c4RecordingSink) Stats() connhub.Stats { return connhub.Stats{} }
func (s *c4RecordingSink) snapshot() []connproto.Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]connproto.Frame(nil), s.frames...)
}

// TestC4EventTapCriticalAndUnmappedPrefixes：approval/trust 事件必须带 critical 标志；
// unmapped topic 必须按前缀计数（不许静默丢）。
func TestC4EventTapCriticalAndUnmappedPrefixes(t *testing.T) {
	store := delivery.NewMemStore(delivery.Options{Now: time.Now})
	bridge := connapi.NewBridge(store, log.New(io.Discard, "", 0))
	sink := &c4RecordingSink{}
	bridge.SetSink(sink)
	tap := newTap(t, bridge, 32) // newTap 传 nil resolver
	hub := stream.NewHubWithIntervals(time.Minute, 30*time.Second, time.Minute)
	hub.SetMirror(tap.Enqueue)
	defer hub.SetMirror(nil)

	hub.Broadcast("approval:7", map[string]any{"type": "approval_card"})
	hub.Broadcast("trust:7", map[string]any{"type": "trust_changed"})
	hub.Broadcast("unknown:abc", map[string]any{"type": "x"})
	hub.Broadcast("negotiation:sess-404", map[string]any{"type": "y"}) // resolver=nil → unmapped

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pub, _, _, _ := tap.Stats()
		prefixes := tap.UnmappedPrefixes()
		if pub >= 2 && prefixes["other"] >= 1 && prefixes["negotiation"] >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	frames := sink.snapshot()
	if len(frames) != 2 {
		t.Fatalf("投递帧数=%d, want 2（approval+trust）", len(frames))
	}
	for _, f := range frames {
		if f.Flags&connproto.FlagCritical == 0 {
			t.Fatalf("approval/trust 事件未带 critical 标志: %+v", f)
		}
	}
	prefixes := tap.UnmappedPrefixes()
	if prefixes["other"] < 1 || prefixes["negotiation"] < 1 {
		t.Fatalf("unmapped 前缀计数=%v, want other>=1 且 negotiation>=1", prefixes)
	}
	t.Logf("C4_CRITICAL_OK: approval/trust 帧带 FlagCritical；unmapped prefixes=%v", prefixes)
}