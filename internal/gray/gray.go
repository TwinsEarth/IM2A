// Package gray 提供 v1.9.8 灰度发布门控：按 userID/AgentID + feature 名做一致性
// 哈希分桶，决定该实体是否命中某 feature 的灰度百分比。纯 stdlib，确定性。
package gray

import (
	"hash/fnv"
	"strconv"
)

// Gate 描述一个 feature 的灰度配置。Percent 在 [0,100]；0 全关、100 全量。
type Gate struct {
	Feature string
	Percent int
}

// bucket 返回 entityID+feature 的稳定桶号 [0,99]。同一实体同一 feature 永远同桶。
func bucket(entityID string, feature string) int {
	h := fnv.New32a()
	h.Write([]byte(entityID + "|" + feature))
	return int(h.Sum32() % 100)
}

// Enabled 判定 entityID 是否命中该 feature 的灰度。
// percent<=0 恒 false；percent>=100 恒 true；否则按桶号 < percent 判定。
func (g Gate) Enabled(entityID string) bool {
	if g.Percent <= 0 {
		return false
	}
	if g.Percent >= 100 {
		return true
	}
	return bucket(entityID, g.Feature) < g.Percent
}

// EnabledInt 便捷重载：数字 userID。
func (g Gate) EnabledInt(userID int64) bool {
	return g.Enabled(strconv.FormatInt(userID, 10))
}

// SamplePercent 真实测量：给定一批实体，实际命中比例（用于验证分布均匀）。
func (g Gate) SamplePercent(ids []string) float64 {
	if len(ids) == 0 {
		return 0
	}
	hit := 0
	for _, id := range ids {
		if g.Enabled(id) {
			hit++
		}
	}
	return float64(hit) / float64(len(ids))
}
