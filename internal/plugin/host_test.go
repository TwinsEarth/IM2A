// SPDX-License-Identifier: MIT

package plugin

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
)

// recorder 记录插件生命周期事件顺序（并发安全）。
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

// fakePlugin 是 Host 单测用的可控插件。
type fakePlugin struct {
	id      string
	version string
	caps    []Capability
	attr    Attribution

	startFn  func(context.Context) error
	stopFn   func(context.Context) error
	healthFn func(context.Context) Health

	mu          sync.Mutex
	startCount  int
	stopCount   int
	healthy     bool
	rec         *recorder
	startDetail string
}

func (f *fakePlugin) ID() string                  { return f.id }
func (f *fakePlugin) Version() string             { return f.version }
func (f *fakePlugin) Capabilities() []Capability  { return f.caps }
func (f *fakePlugin) Attribution() Attribution    { return f.attr }

func (f *fakePlugin) Start(ctx context.Context) error {
	f.mu.Lock()
	f.startCount++
	f.mu.Unlock()
	if f.rec != nil {
		f.rec.add("start:" + f.id)
	}
	if f.startFn != nil {
		return f.startFn(ctx)
	}
	f.mu.Lock()
	f.healthy = true
	f.mu.Unlock()
	return nil
}

func (f *fakePlugin) Stop(ctx context.Context) error {
	f.mu.Lock()
	f.stopCount++
	f.mu.Unlock()
	if f.rec != nil {
		f.rec.add("stop:" + f.id)
	}
	if f.stopFn != nil {
		return f.stopFn(ctx)
	}
	f.mu.Lock()
	f.healthy = false
	f.mu.Unlock()
	return nil
}

func (f *fakePlugin) Health(ctx context.Context) Health {
	if f.healthFn != nil {
		return f.healthFn(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.healthy {
		return Health{State: StateOK, Detail: "fake ok"}
	}
	return Health{State: StateDegraded, Detail: "fake not started"}
}

func newFake(id string) *fakePlugin {
	return &fakePlugin{
		id:      id,
		version: "0.0.1",
		caps:    []Capability{{ID: "cap." + id, Kind: KindRuntime, Version: "1.0.0", Modes: []string{"m1"}}},
		attr: Attribution{
			Upstream:     "https://example.com/" + id,
			UpstreamName: id,
			License:      "MIT",
			Copyright:    "Copyright (c) 2026 Example",
			NoticePath:   "THIRD_PARTY_LICENSES/" + id + ".txt",
			Derived:      true,
			Usage:        []string{"test usage"},
		},
	}
}

func discardHost() *Host {
	return NewHost(log.New(io.Discard, "", 0))
}

func TestHostRegister(t *testing.T) {
	h := discardHost()
	if err := h.Register(nil); !errors.Is(err, ErrNilPlugin) {
		t.Fatalf("nil plugin: want ErrNilPlugin, got %v", err)
	}
	empty := newFake("")
	if err := h.Register(empty); !errors.Is(err, ErrEmptyPluginID) {
		t.Fatalf("empty id: want ErrEmptyPluginID, got %v", err)
	}
	a := newFake("alpha")
	if err := h.Register(a); err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	if err := h.Register(newFake("alpha")); !errors.Is(err, ErrDuplicatePlugin) {
		t.Fatalf("duplicate: want ErrDuplicatePlugin, got %v", err)
	}
	got, ok := h.Get("alpha")
	if !ok || got != a {
		t.Fatalf("Get(alpha) = %v, %v; want registered plugin", got, ok)
	}
	if _, ok := h.Get("missing"); ok {
		t.Fatal("Get(missing) should be false")
	}
}

func TestHostDeterministicStartAndReverseStop(t *testing.T) {
	rec := &recorder{}
	h := discardHost()
	for _, id := range []string{"gamma", "alpha", "beta"} {
		p := newFake(id)
		p.rec = rec
		if err := h.Register(p); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, want := rec.list(), []string{"start:alpha", "start:beta", "start:gamma"}; !equalStrings(got, want) {
		t.Fatalf("start order = %v, want %v", got, want)
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got, want := rec.list(), []string{
		"start:alpha", "start:beta", "start:gamma",
		"stop:gamma", "stop:beta", "stop:alpha",
	}; !equalStrings(got, want) {
		t.Fatalf("stop order = %v, want %v", got, want)
	}
}

func TestHostStartFailureDoesNotBlockOthers(t *testing.T) {
	h := discardHost()
	bad := newFake("bad")
	bad.startFn = func(context.Context) error { return errors.New("boom") }
	good := newFake("good")
	for _, p := range []*fakePlugin{good, bad} {
		if err := h.Register(p); err != nil {
			t.Fatalf("register %s: %v", p.id, err)
		}
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start must return nil so gateway keeps running, got %v", err)
	}
	if good.startCount != 1 {
		t.Fatalf("good.startCount = %d, want 1", good.startCount)
	}
	snaps := h.Snapshots()
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snaps))
	}
	if snaps[0].ID != "bad" || snaps[0].State != StateDegraded {
		t.Fatalf("bad snapshot = %+v, want id=bad state=degraded", snaps[0])
	}
	if !contains(snaps[0].Detail, "boom") {
		t.Fatalf("bad detail = %q, want start error", snaps[0].Detail)
	}
	if snaps[1].ID != "good" || snaps[1].State != StateOK {
		t.Fatalf("good snapshot = %+v, want id=good state=ok", snaps[1])
	}
}

func TestHostStartPanicIsolated(t *testing.T) {
	h := discardHost()
	panicky := newFake("panicky")
	panicky.startFn = func(context.Context) error { panic("kaboom") }
	ok := newFake("ok")
	_ = h.Register(panicky)
	_ = h.Register(ok)
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snaps := h.Snapshots()
	// 注意：Snapshots 按 ID 字典序，本用例中 "ok" < "panicky"，故不能按下标断言。
	panicSnap := snapshotByID(t, snaps, "panicky")
	if panicSnap.State != StateDegraded || !contains(panicSnap.Detail, "panic") {
		t.Fatalf("panicky snapshot = %+v, want degraded with panic detail", panicSnap)
	}
	okSnap := snapshotByID(t, snaps, "ok")
	if okSnap.State != StateOK {
		t.Fatalf("ok snapshot = %+v, want ok (panic must not block others)", okSnap)
	}
}

// snapshotByID 按 ID 取快照，避免用下标依赖排序细节。
func snapshotByID(t *testing.T, snaps []Snapshot, id string) Snapshot {
	t.Helper()
	for _, s := range snaps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("snapshot %q not found in %+v", id, snaps)
	return Snapshot{}
}

func TestHostStopOnlyStartedPlugins(t *testing.T) {
	rec := &recorder{}
	h := discardHost()
	a, b, c := newFake("a"), newFake("b"), newFake("c")
	a.rec, b.rec, c.rec = rec, rec, rec
	b.startFn = func(context.Context) error { return errors.New("nope") }
	for _, p := range []*fakePlugin{a, b, c} {
		_ = h.Register(p)
	}
	_ = h.Start(context.Background())
	rec.events = nil // 只看 Stop 顺序
	if err := h.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got, want := rec.list(), []string{"stop:c", "stop:a"}; !equalStrings(got, want) {
		t.Fatalf("stop order = %v, want %v (b never started)", got, want)
	}
	if b.stopCount != 0 {
		t.Fatalf("b.stopCount = %d, want 0", b.stopCount)
	}
}

func TestHostStopAggregatesErrors(t *testing.T) {
	h := discardHost()
	good := newFake("good")
	bad := newFake("bad")
	bad.stopFn = func(context.Context) error { return errors.New("stop boom") }
	_ = h.Register(good)
	_ = h.Register(bad)
	_ = h.Start(context.Background())
	err := h.Stop(context.Background())
	if err == nil {
		t.Fatal("Stop should aggregate plugin stop errors")
	}
	if !contains(err.Error(), "stop boom") {
		t.Fatalf("Stop error = %v, want stop boom", err)
	}
}

func TestHostStartIdempotent(t *testing.T) {
	h := discardHost()
	a := newFake("a")
	_ = h.Register(a)
	_ = h.Start(context.Background())
	_ = h.Start(context.Background())
	if a.startCount != 1 {
		t.Fatalf("startCount = %d, want 1 (Start must be idempotent)", a.startCount)
	}
}

func TestHostSnapshotsDeterministicAndComplete(t *testing.T) {
	h := discardHost()
	for _, id := range []string{"zeta", "alpha", "mike"} {
		_ = h.Register(newFake(id))
	}
	_ = h.Start(context.Background())
	snaps := h.Snapshots()
	ids := make([]string, len(snaps))
	for i, s := range snaps {
		ids[i] = s.ID
	}
	if !equalStrings(ids, []string{"alpha", "mike", "zeta"}) {
		t.Fatalf("snapshot order = %v", ids)
	}
	for _, s := range snaps {
		if s.Version != "0.0.1" {
			t.Fatalf("%s version = %q", s.ID, s.Version)
		}
		if len(s.Capabilities) != 1 || s.Capabilities[0].ID != "cap."+s.ID {
			t.Fatalf("%s capabilities = %+v", s.ID, s.Capabilities)
		}
		if s.Attribution.License != "MIT" || s.Attribution.NoticePath == "" {
			t.Fatalf("%s attribution incomplete: %+v", s.ID, s.Attribution)
		}
		if s.State == "" {
			t.Fatalf("%s has empty state", s.ID)
		}
	}
}

func TestHostHealthPanicSafe(t *testing.T) {
	h := discardHost()
	p := newFake("panicky-health")
	p.healthFn = func(context.Context) Health { panic("health kaboom") }
	_ = h.Register(p)
	snaps := h.Snapshots()
	if len(snaps) != 1 || snaps[0].State != StateFailed {
		t.Fatalf("snapshots = %+v, want single failed snapshot", snaps)
	}
	if !contains(snaps[0].Detail, "Health panic") {
		t.Fatalf("detail = %q, want Health panic", snaps[0].Detail)
	}
}

func TestHostConcurrentAccess(t *testing.T) {
	h := discardHost()
	_ = h.Register(newFake("base"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = h.Register(newFake("p-" + string(rune('a'+i)) + "-" + string(rune('a'+j%26))))
				_, _ = h.Get("base")
				_ = h.Snapshots()
			}
		}(i)
	}
	wg.Wait()
	_ = h.Start(context.Background())
	_ = h.Snapshots()
	_ = h.Stop(context.Background())
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
