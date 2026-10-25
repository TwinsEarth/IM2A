package memory

import (
	"context"
	"testing"

	"human-agent-im/internal/eventbus"
)

func TestMemoryEvents(t *testing.T) {
	bus := eventbus.New()
	g := NewGuarded(NewMemoryStore())
	g.SetBus(bus)
	ctx := context.Background()

	factsSeen := 0
	reinfSeen := 0
	bus.Subscribe(eventbus.EventTypeMemoryFactUpsert, func(_ context.Context, e *eventbus.Event) error {
		factsSeen++
		return nil
	})
	bus.Subscribe(eventbus.EventTypeMemoryReinforce, func(_ context.Context, e *eventbus.Event) error {
		reinfSeen++
		return nil
	})

	sc := Scope{UserID: 1, AgentID: "sub"}
	_ = g.UpsertFact(ctx, sc, &Fact{Key: "k", Value: "v", Confidence: 0.8})
	_, _ = g.ReinforceFact(ctx, sc, "k", 0.1)

	if factsSeen != 1 {
		t.Fatalf("fact upsert event should fire once, got %d", factsSeen)
	}
	if reinfSeen != 1 {
		t.Fatalf("reinforce event should fire once, got %d", reinfSeen)
	}

	// 无 bus 时不报错
	g2 := NewGuarded(NewMemoryStore())
	if err := g2.UpsertFact(ctx, sc, &Fact{Key: "k", Value: "v"}); err != nil {
		t.Fatalf("nil bus should not break: %v", err)
	}
}

func TestSnapshotRestore(t *testing.T) {
	g := NewGuarded(NewMemoryStore())
	g.RegisterMaster(1, "master")
	ctx := context.Background()
	master := Scope{UserID: 1, AgentID: "master"}
	sub := Scope{UserID: 1, AgentID: "sub"}

	_ = g.UpsertFact(ctx, sub, &Fact{Key: "a", Value: "1", Confidence: 0.9})
	_ = g.UpsertFact(ctx, sub, &Fact{Key: "b", Value: "2", Confidence: 0.7})

	// master 做快照
	snap, err := g.TakeSnapshot(ctx, master, sub)
	if err != nil {
		t.Fatalf("take snapshot: %v", err)
	}
	if len(snap.Facts) != 2 {
		t.Fatalf("snapshot should have 2 facts, got %d", len(snap.Facts))
	}

	// 修改事实
	_ = g.UpsertFact(ctx, sub, &Fact{Key: "a", Value: "changed", Confidence: 0.3})
	_ = g.UpsertFact(ctx, sub, &Fact{Key: "c", Value: "3", Confidence: 0.5})

	// 回滚
	if err := g.RestoreSnapshot(ctx, master, snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	f, _ := g.GetFact(ctx, sub, "a")
	if f.Value != "1" || f.Confidence != 0.9 {
		t.Fatalf("restore fact a wrong: %+v", f)
	}
	if _, err := g.GetFact(ctx, sub, "c"); err != ErrNotFound {
		t.Fatalf("fact c should be removed by restore")
	}

	// 非 master 不能快照
	if _, err := g.TakeSnapshot(ctx, sub, sub); err != ErrAccessDenied {
		t.Fatalf("non-master snapshot should be denied, got %v", err)
	}
}
