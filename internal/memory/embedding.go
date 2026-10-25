package memory

import (
	"math"
	"unicode/utf8"
)

// VectorDim 是 v1.9.4 事实向量的固定维度（确定性本地嵌入，可复现）。
const VectorDim = 256

// Embed 把文本映射为 L2 归一化的 256 维向量。
// 采用字符 n-gram（rune bigram）哈希分桶：相同/相近文本共享大量 bigram，
// 余弦距离近；这是真实可计算的向量检索基础，不依赖外部 embedding 服务。
// 后续接入云端 embedding 时，只需替换本函数输出维度并写入同一列。
func Embed(text string) []float32 {
	v := make([]float32, VectorDim)
	if text == "" {
		return v
	}
	runes := []rune(text)
	// unigram
	for _, r := range runes {
		h := hashRune(r)
		v[h%VectorDim] += 1.0
	}
	// bigram（覆盖中英文相邻字）
	for i := 0; i+1 < len(runes); i++ {
		h := hashPair(runes[i], runes[i+1])
		v[h%VectorDim] += 2.0 // 邻接权重更高，强化短词组匹配
	}
	norm := 0.0
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm > 0 {
		inv := 1.0 / math.Sqrt(norm)
		for i := range v {
			v[i] = float32(float64(v[i]) * inv)
		}
	}
	return v
}

func hashRune(r rune) uint32 {
	var h uint32 = 2166136261
	h = (h ^ uint32(r)) * 16777619
	return h
}

func hashPair(a, b rune) uint32 {
	var h uint32 = 2166136261
	h = (h ^ uint32(a)) * 16777619
	h = (h ^ uint32(b)) * 16777619
	return h
}

// Cosine 返回两个向量的余弦相似度（[-1,1]）；长度不一致或任一为零向量时返回 0。
func Cosine(a, b []float32) float32 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// EnsureEmbedding 在 f.Embedding 为空时，基于 key+value 生成向量。
func EnsureEmbedding(f *Fact) {
	if len(f.Embedding) == 0 {
		f.Embedding = Embed(f.Key + " " + f.Value)
	}
}

var _ = utf8.RuneLen
