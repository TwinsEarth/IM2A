package distill

import (
	"context"
	"testing"

	"human-agent-im/internal/eventbus"
)

func TestTightenOnNegStreak(t *testing.T) {
	e := New(nil)
	e.Record(1, "sub-proc", "采购.平台", 0.1)  // 通过
	e.Record(1, "sub-proc", "采购.平台", -0.2) // 驳回 1
	if e.RequireApproval("sub-proc", "采购.平台") {
		t.Fatal("1 次驳回不应收紧")
	}
	e.Record(1, "sub-proc", "采购.平台", -0.3) // 驳回 2
	if !e.RequireApproval("sub-proc", "采购.平台") {
		t.Fatal("连续 2 次驳回应收紧")
	}
	// 收紧后再通过不放宽
	e.Record(1, "sub-proc", "采购.平台", 0.5)
	if !e.RequireApproval("sub-proc", "采购.平台") {
		t.Fatal("只收紧不放宽")
	}
}

func TestTightenOnNegRatio(t *testing.T) {
	e := New(nil)
	// 4 样本里 2 次驳回 = 50% > 30%
	e.Record(1, "sub-legal", "合同.模板", 0.1)
	e.Record(1, "sub-legal", "合同.模板", -0.2)
	e.Record(1, "sub-legal", "合同.模板", 0.1)
	e.Record(1, "sub-legal", "合同.模板", -0.3)
	if !e.RequireApproval("sub-legal", "合同.模板") {
		t.Fatal("负反馈占比 50% 应收紧")
	}
}

func TestColdStartNoTighten(t *testing.T) {
	e := New(nil)
	// 3 样本：负-正-负，streak=1，占比 67% 但 < minSamples=4，不收紧
	e.Record(1, "a", "k", -0.1)
	e.Record(1, "a", "k", 0.1)
	e.Record(1, "a", "k", -0.1)
	if e.RequireApproval("a", "k") {
		t.Fatal("冷启动期（streak=1 且样本<4）不应收紧")
	}
}

func TestSubscribeReinforceEvent(t *testing.T) {
	bus := eventbus.New()
	e := New(bus)
	ctx := context.Background()
	_ = bus.Publish(ctx, &eventbus.Event{
		Type:    eventbus.EventTypeMemoryReinforce,
		UserID:  1,
		AgentID: "sub-proc",
		Payload: map[string]any{"key": "k", "delta": -0.2},
	})
	_ = bus.Publish(ctx, &eventbus.Event{
		Type:    eventbus.EventTypeMemoryReinforce,
		UserID:  1,
		AgentID: "sub-proc",
		Payload: map[string]any{"key": "k", "delta": -0.2},
	})
	if !e.RequireApproval("sub-proc", "k") {
		t.Fatal("事件订阅驱动的蒸馏应收紧")
	}
}

func TestReset(t *testing.T) {
	e := New(nil)
	e.Record(1, "a", "k", -0.1)
	e.Record(1, "a", "k", -0.1)
	if !e.RequireApproval("a", "k") {
		t.Fatal("should be tightened")
	}
	if err := e.Reset("a", "k"); err != nil {
		t.Fatal(err)
	}
	if e.RequireApproval("a", "k") {
		t.Fatal("reset 后应解除收紧")
	}
}

func TestTightenReason(t *testing.T) {
	e := New(nil)
	e.Record(1, "a", "streak", -0.1)
	e.Record(1, "a", "streak", -0.2)
	if reason := e.TightenReason("a", "streak"); reason == "" {
		t.Fatal("streak tighten should have reason")
	}

	// 占比收紧
	e2 := New(nil)
	for i := 0; i < 4; i++ {
		e2.Record(2, "b", "ratio", -0.1)
	}
	reason := e2.TightenReason("b", "ratio")
	if reason == "" {
		t.Fatal("ratio tighten should have reason")
	}
}

func TestTightenEventPublished(t *testing.T) {
	bus := eventbus.New()
	e := New(bus)
	called := 0
	bus.Subscribe(eventbus.EventTypeDistillTighten, func(_ context.Context, ev *eventbus.Event) error {
		called++
		if ev.Payload["reason"] == "" {
			t.Fatal("event should carry reason")
		}
		return nil
	})
	e.Record(1, "a", "k", -0.1)
	e.Record(1, "a", "k", -0.1) // 触发收紧
	if called != 1 {
		t.Fatalf("tighten event should fire once, got %d", called)
	}
	// 再 Record 不重复发
	e.Record(1, "a", "k", -0.1)
	if called != 1 {
		t.Fatalf("只收紧一次，不应重复发事件，got %d", called)
	}
}

func TestConfigAdjustable(t *testing.T) {
	e := New(nil)
	// 默认连续 2 次收紧
	e.Record(1, "a", "k", -0.1)
	e.Record(1, "a", "k", -0.1)
	if !e.RequireApproval("a", "k") {
		t.Fatal("default: 2 streak should tighten")
	}
	// 重置并调严：连续 1 次就收紧
	_ = e.Reset("a", "k")
	e.SetConfig(Config{NegStreakThreshold: 1, NegRatioThreshold: 0.5, MinSamples: 2})
	e.Record(1, "a", "k2", -0.1)
	if !e.RequireApproval("a", "k2") {
		t.Fatal("strict config: 1 streak should tighten")
	}
	// 非法配置不覆盖
	e.SetConfig(Config{NegStreakThreshold: -1, NegRatioThreshold: 5, MinSamples: 0})
	if e.GetConfig().NegStreakThreshold != 1 {
		t.Fatal("invalid config should not override")
	}
}

func TestExportImportUser(t *testing.T) {
	e := New(nil)
	e.Record(1, "sub-proc", "采购.平台", -0.2)
	e.Record(1, "sub-proc", "采购.平台", -0.2) // 收紧
	e.Record(1, "sub-legal", "合同.模板", 0.1)
	e.Record(2, "other-agent", "k", -0.1) // 别用户

	// 导出 user=1
	exported := e.ExportUser(1)
	if len(exported) != 2 {
		t.Fatalf("user1 should have 2 entries, got %d", len(exported))
	}
	if exported[0].UserID != 1 {
		t.Fatal("export should carry userId")
	}

	// 导入到新引擎
	e2 := New(nil)
	e2.ImportUser(exported)
	if !e2.RequireApproval("sub-proc", "采购.平台") {
		t.Fatal("imported tighten should persist")
	}
	if e2.RequireApproval("other-agent", "k") {
		t.Fatal("cross-user data should not import")
	}
}

func TestReportUser(t *testing.T) {
	e := New(nil)
	e.Record(1, "a", "k1", -0.1)
	e.Record(1, "a", "k1", -0.2) // 收紧
	e.Record(1, "b", "k2", 0.3)
	e.Record(1, "b", "k2", 0.2)
	e.Record(2, "c", "k3", -0.1) // 别用户

	r := e.ReportUser(1)
	if r.TotalKeys != 2 {
		t.Fatalf("expected 2 keys, got %d", r.TotalKeys)
	}
	if r.TightenedKeys != 1 {
		t.Fatalf("expected 1 tightened, got %d", r.TightenedKeys)
	}
	if r.TotalNeg != 2 || r.TotalPos != 2 {
		t.Fatalf("expected pos=2 neg=2, got pos=%d neg=%d", r.TotalPos, r.TotalNeg)
	}
	if r.PassRate != 0.5 {
		t.Fatalf("expected passRate 0.5, got %.2f", r.PassRate)
	}
	if len(r.TightenedList) != 1 {
		t.Fatalf("expected tightened list 1, got %d", len(r.TightenedList))
	}
}
