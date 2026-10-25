package store

// 本文件锁定「没有 artifacts」的落库表示（v2.3.28 真库首验缺陷 2 的回归测试）。
//
// 不加 PG 依赖：直接单测归一函数，保证 `nil slice` 绝不会被当作 SQL NULL 绑定。
// 真库侧的端到端证明由 internal/api/v13_real_test.go 的 TestV132NegotiationTimeline
// 覆盖（它构造的 Turn 没有 ArtifactRefs，修复前在真库报 23502）。

import (
	"reflect"
	"testing"
)

func TestNormalizeArtifactRefs_NilBecomesEmptyArray(t *testing.T) {
	got := normalizeArtifactRefs(nil)
	if got == nil {
		t.Fatal("nil 必须归一为非 nil 空切片（database/sql 会把 nil slice 绑定成 SQL NULL，" +
			"而 negotiation_turns.artifacts 是 TEXT[] NOT NULL DEFAULT '{}'）")
	}
	if len(got) != 0 {
		t.Fatalf("归一后长度 = %d, want 0", len(got))
	}
}

func TestNormalizeArtifactRefs_PreservesValues(t *testing.T) {
	for _, in := range [][]string{
		{},
		{"artifact://a"},
		{"artifact://a", "artifact://b"},
	} {
		got := normalizeArtifactRefs(in)
		if !reflect.DeepEqual(got, in) {
			t.Fatalf("normalizeArtifactRefs(%v) = %v, want unchanged", in, got)
		}
	}
}
