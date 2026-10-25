package memory

import (
	"context"
	"testing"
)

// TestV194VectorSearchReplacesILIKE 真实落库 + 真实余弦向量检索。
// 证明：(1) UpsertFact 自动生成并落库 256 维向量；(2) SearchFacts 按余弦相似度召回，
// 能命中 ILIKE 子串完全匹配不到的语义近邻。
func TestV194VectorSearchReplacesILIKE(t *testing.T) {
	db := v10OpenPG(t)
	st := NewPGStore(db)
	ctx := context.Background()
	sc := v10Scope(t, db)

	facts := []*Fact{
		{Key: "物业水电", Value: "每月代扣水电费", Confidence: 0.7, Source: "v194"},
		{Key: "工资卡", Value: "每月到账核对银行余额", Confidence: 0.8, Source: "v194"},
		{Key: "健身", Value: "每周户外跑步三次", Confidence: 0.6, Source: "v194"},
	}
	for _, f := range facts {
		if err := st.UpsertFact(ctx, sc, f); err != nil {
			t.Fatalf("upsert %s: %v", f.Key, err)
		}
	}

	// 1) 落库后向量真实写入，维度=256
	got, err := st.SearchFacts(ctx, sc, "物业水电")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应返回 3 条，实际 %d", len(got))
	}
	for _, f := range got {
		if len(f.Embedding) != VectorDim {
			t.Fatalf("fact %q 落库向量维度=%d, 期望 %d", f.Key, len(f.Embedding), VectorDim)
		}
	}

	// 2) query「交电费」：ILIKE '%交电费%' 对三条 value 全部不命中
	//    （value 里没有连续子串「交电费」），但向量余弦会把「物业水电/水电费」排第一。
	res, err := st.SearchFacts(ctx, sc, "交电费")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res[0].Key != "物业水电" {
		t.Fatalf("「交电费」余弦近邻第一应是物业水电，实际 %q（余弦=%v）", res[0].Key,
			Cosine(Embed("交电费"), Embed(res[0].Key+" "+res[0].Value)))
	}

	// 3) 余弦区分度：「交电费」对物业水电的相似度必须高于对健身
	cWater := Cosine(Embed("交电费"), Embed("物业水电 每月代扣水电费"))
	cGym := Cosine(Embed("交电费"), Embed("健身 每周户外跑步三次"))
	if cWater <= cGym {
		t.Fatalf("余弦区分度失败: 水电=%v 健身=%v", cWater, cGym)
	}

	// 4) 确定性：相同文本向量一致（可复现）
	if !eqVec(Embed("银行"), Embed("银行")) {
		t.Fatal("确定性向量不一致")
	}
	t.Logf("余弦 交电费→水电=%v  →健身=%v（向量检索已替换 ILIKE）", cWater, cGym)
}

func eqVec(a, b []float32) bool {
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
