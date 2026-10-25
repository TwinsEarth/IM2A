package orchestration

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestD3_EmptySnapshot(t *testing.T) {
	s := NewDistillStats()
	r := s.Snapshot()
	if r.Total != 0 || r.PassRate != 0 {
		t.Fatalf("empty stats wrong: %+v", r)
	}
	if !strings.Contains(r.HealthReason, "样本不足") {
		t.Fatalf("empty should say sample insufficient: %q", r.HealthReason)
	}
}

func TestD3_CountsAndRates(t *testing.T) {
	s := NewDistillStats()
	for i := 0; i < 60; i++ {
		s.Record("approved")
	}
	for i := 0; i < 30; i++ {
		s.Record("rejected")
	}
	for i := 0; i < 10; i++ {
		s.Record("modified")
	}
	s.Record("pending")
	r := s.Snapshot()
	if r.Total != 100 {
		t.Fatalf("total should be 100 (pending not counted), got %d", r.Total)
	}
	if r.Pending != 1 {
		t.Fatalf("pending should be 1: %+v", r)
	}
	if r.PassRate < 0.59 || r.PassRate > 0.61 {
		t.Fatalf("pass rate ~0.6, got %.2f", r.PassRate)
	}
	if r.RejectRate < 0.29 || r.RejectRate > 0.31 {
		t.Fatalf("reject rate ~0.3, got %.2f", r.RejectRate)
	}
	if !r.Healthy {
		t.Fatalf("should be healthy: %+v", r)
	}
}

func TestD3_RejectHighMarksUnhealthy(t *testing.T) {
	s := NewDistillStats()
	for i := 0; i < 60; i++ {
		s.Record("rejected")
	}
	for i := 0; i < 40; i++ {
		s.Record("approved")
	}
	r := s.Snapshot()
	if r.Healthy {
		t.Fatalf("reject rate 60%% should be unhealthy")
	}
	if !strings.Contains(r.HealthReason, "驳回率") {
		t.Fatalf("reason should mention reject: %q", r.HealthReason)
	}
}

func TestD3_JSONRoundtrip(t *testing.T) {
	s := NewDistillStats()
	s.Record("approved")
	s.Record("approved")
	s.Record("rejected")
	var m map[string]any
	if err := json.Unmarshal(s.JSON(), &m); err != nil {
		t.Fatal(err)
	}
	if m["total"].(float64) != 3 {
		t.Fatalf("total in json wrong: %v", m)
	}
}

func TestD3_ConcurrentSafe(t *testing.T) {
	s := NewDistillStats()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Record("approved")
			s.Record("rejected")
		}()
	}
	wg.Wait()
	r := s.Snapshot()
	if r.Total != 100 {
		t.Fatalf("concurrent total wrong: %d", r.Total)
	}
}

func TestD3_NilSafe(t *testing.T) {
	var s *DistillStats
	s.Record("approved")
	if out := s.JSON(); !strings.Contains(string(out), "未初始化") {
		t.Fatalf("nil should say uninitialized, got %s", out)
	}
}
