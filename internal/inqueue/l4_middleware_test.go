package inqueue

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeInterceptor struct {
	reply   string
	handled bool
}

func (f *fakeInterceptor) MaybeHandle(ctx context.Context, wxid, text string) (string, bool) {
	return f.reply, f.handled
}

func TestWithL4_NilInterceptorPassesThrough(t *testing.T) {
	called := false
	h := WithL4(func(ctx context.Context, ev Event) error { called = true; return nil }, nil, nil)
	if h == nil {
		t.Fatal("handler should not be nil")
	}
	_ = h(context.Background(), Event{Text: "hi"})
	if !called {
		t.Fatal("must fall through to original handler")
	}
}

func TestWithL4_HandledShortCircuits(t *testing.T) {
	origCalled := false
	h := func(ctx context.Context, ev Event) error { origCalled = true; return nil }
	replied := false
	wrapped := WithL4(h, &fakeInterceptor{reply: "来自 L4", handled: true}, func(ctx context.Context, ev Event, reply string) error {
		if !strings.Contains(reply, "L4") {
			t.Fatalf("reply=%q", reply)
		}
		replied = true
		return nil
	})
	_ = wrapped(context.Background(), Event{Text: "你好"})
	if origCalled {
		t.Fatal("original handler must not run")
	}
	if !replied {
		t.Fatal("reply must be sent")
	}
}

func TestWithL4_NotHandledFallsThrough(t *testing.T) {
	origCalled := false
	h := func(ctx context.Context, ev Event) error { origCalled = true; return nil }
	wrapped := WithL4(h, &fakeInterceptor{handled: false}, nil)
	_ = wrapped(context.Background(), Event{Text: "hi"})
	if !origCalled {
		t.Fatal("must fall through")
	}
}

func TestWithL4_ReplyErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom")
	wrapped := WithL4(
		func(ctx context.Context, ev Event) error { return nil },
		&fakeInterceptor{reply: "x", handled: true},
		func(ctx context.Context, ev Event, reply string) error { return wantErr },
	)
	err := wrapped(context.Background(), Event{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v", err)
	}
}
