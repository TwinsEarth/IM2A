package eventbus

import (
	"context"
	"testing"
)

func TestPublishSubscribe(t *testing.T) {
	bus := New()
	received := make([]*Event, 0)
	bus.Subscribe(EventTypeGateCheck, func(_ context.Context, e *Event) error {
		received = append(received, e)
		return nil
	})

	ctx := context.Background()
	err := bus.Publish(ctx, &Event{Type: EventTypeGateCheck, UserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(received) != 1 {
		t.Fatalf("want 1 event, got %d", len(received))
	}
	if received[0].UserID != 1 {
		t.Errorf("want user 1, got %d", received[0].UserID)
	}
	if received[0].Timestamp == 0 {
		t.Error("timestamp not set")
	}
}

func TestListByType(t *testing.T) {
	bus := New()
	ctx := context.Background()
	_ = bus.Publish(ctx, &Event{Type: EventTypeGateCheck})
	_ = bus.Publish(ctx, &Event{Type: EventTypeApprovalCreate})
	_ = bus.Publish(ctx, &Event{Type: EventTypeGateCheck})

	if len(bus.List()) != 3 {
		t.Errorf("want 3 total, got %d", len(bus.List()))
	}
	if len(bus.ByType(EventTypeGateCheck)) != 2 {
		t.Errorf("want 2 gate_check, got %d", len(bus.ByType(EventTypeGateCheck)))
	}
}
