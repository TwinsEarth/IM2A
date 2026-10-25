package gray

import (
	"fmt"
	"math"
	"testing"
)

func TestGateBoundary(t *testing.T) {
	cases := []struct {
		pct   int
		ent   string
		must  bool
	}{
		{0, "user-1", false},    // 0% 全关
		{100, "user-1", true},   // 100% 全量
		{-10, "user-1", false},  // 负数全关
	}
	for _, c := range cases {
		g := Gate{Feature: "new-ui", Percent: c.pct}
		if got := g.Enabled(c.ent); got != c.must {
			t.Errorf("pct=%d ent=%s got=%v want=%v", c.pct, c.ent, got, c.must)
		}
	}
}

func TestStableDeterministic(t *testing.T) {
	g := Gate{Feature: "new-ui", Percent: 50}
	first := g.Enabled("user-42")
	for i := 0; i < 50; i++ {
		if g.Enabled("user-42") != first {
			t.Fatal("同一实体同一 feature 结果不稳定")
		}
	}
}

func TestFeatureIsolation(t *testing.T) {
	// 不同 feature 分桶独立：一个实体可能命中 A 但不命中 B
	gA := Gate{Feature: "feast-A", Percent: 100}
	gB := Gate{Feature: "feast-B", Percent: 0}
	if !gA.Enabled("user-7") {
		t.Error("A 100% 应命中")
	}
	if gB.Enabled("user-7") {
		t.Error("B 0% 不应命中")
	}
}

func TestDistributionEven(t *testing.T) {
	// 真实分布：10000 实体 50% 灰度，命中率应接近 50%（容差 5%）
	g := Gate{Feature: "dist", Percent: 50}
	ids := make([]string, 10000)
	for i := range ids {
		ids[i] = fmt.Sprintf("user-%d", i)
	}
	got := g.SamplePercent(ids)
	if math.Abs(got-0.5) > 0.05 {
		t.Errorf("50%% 灰度实际命中率 %.3f，偏离过大", got)
	}
}
