package conformance_test

// conformance_test.go：一致性套件的自检（用本仓库 reference adapter 跑公开向量）。
//
// 这个测试是 CI 里 `go test ./conformance/...` 的判据，与
// `go run ./cmd/relation-conformance ...` 等价；两者任一失败都说明"向量与实现不一致"。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"human-agent-im/conformance"
	"human-agent-im/conformance/example"
)

// vectorsPath 定位仓库根下的公开向量（测试运行目录是 conformance/）。
func vectorsPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p := filepath.Join(wd, "..", "spec", "vectors", "relation-gating.v1.json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("找不到向量文件 %s: %v", p, err)
	}
	return p
}

// TestVectorsMatchReference 是核心判据：公开向量与 reference 实现必须 100% 一致。
func TestVectorsMatchReference(t *testing.T) {
	vf, err := conformance.LoadVectors(vectorsPath(t))
	if err != nil {
		t.Fatalf("load vectors: %v", err)
	}
	if got := len(vf.Vectors); got < 60 {
		t.Fatalf("向量数量 %d < 60（验收硬条件）", got)
	}
	rep := conformance.Run(conformance.Reference{}, vf)
	if rep.Failed != 0 {
		var b strings.Builder
		for _, f := range rep.Failures {
			b.WriteString("\n  ")
			b.WriteString(f.ID)
			b.WriteString(": want ")
			b.WriteString(f.Want.String())
			b.WriteString(", got ")
			b.WriteString(f.Got.String())
		}
		t.Fatalf("向量与 reference 不一致 %d 条:%s", rep.Failed, b.String())
	}
	t.Logf("relation-gating conformance: %d/%d PASS（category: %v）", rep.Passed, rep.Total, rep.ByCategory)
}

// TestVectorDistribution 校验验收要求的分布（35 矩阵 + ≥10 顺序 + ≥5 未知 + ≥5 边界 + ≥5 单调）。
func TestVectorDistribution(t *testing.T) {
	vf, err := conformance.LoadVectors(vectorsPath(t))
	if err != nil {
		t.Fatalf("load vectors: %v", err)
	}
	min := map[string]int{
		"matrix": 35, "order": 10, "unknown": 5, "boundary": 5, "monotonic": 5,
	}
	got := map[string]int{}
	for _, v := range vf.Vectors {
		got[v.Category]++
	}
	for cat, want := range min {
		if got[cat] < want {
			t.Fatalf("category %q 只有 %d 条，要求 ≥%d（统计: %v）", cat, got[cat], want, got)
		}
	}
}

// TestInvariantsHaveVectors 每条不变量都必须有向量锚定（ID 前缀约定）。
func TestInvariantsHaveVectors(t *testing.T) {
	vf, err := conformance.LoadVectors(vectorsPath(t))
	if err != nil {
		t.Fatalf("load vectors: %v", err)
	}
	ids := map[string]bool{}
	for _, v := range vf.Vectors {
		ids[v.ID] = true
	}
	// 规范 §5 的不变量锚点（I1/I2 见 unk-*；I3 inv-L4-no-auto-*；I4 inv-L0-all-auto-*；
	// I5 mono-*；I6 pure-*；I7 bnd-*）。
	required := []string{
		"unk-scope-at-L0", "unk-scope-at-L4", "unk-scope-with-amount",
		"unk-level-99", "unk-level-5", "unk-level-neg1-with-min0",
		"inv-L4-no-auto-schedule", "inv-L0-all-auto-finance",
		"mono-schedule-L4", "mono-schedule-L0", "mono-finance-L3", "mono-finance-L2",
		"pure-targetuid-ignored", "pure-repeatable",
		"bnd-amount-zero", "bnd-negative-amount", "bnd-threshold-equal", "bnd-budget-equal",
	}
	for _, id := range required {
		if !ids[id] {
			t.Fatalf("缺少锚定不变量所需的向量: %s", id)
		}
	}
}

// TestThirdPartyStubCompiles 第三方接入示例必须能满足 Implementer 接口（形参检查）。
func TestThirdPartyStubCompiles(t *testing.T) {
	var impl conformance.Implementer = example.Stub{}
	if got := impl.Decide(conformance.Input{}); got.ReasonCode == "" {
		t.Fatal("stub 返回空 reasonCode")
	}
}
