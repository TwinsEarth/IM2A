package orchestration

import (
	"encoding/json"
	"sync"
)

// v2.3.15 D3：蒸馏效果监控。
//
// 蒸馏闭环（D1 反馈桥 / D2 程序记忆）跑起来之后，需要一个能回答
// "Agent 最近到底变聪明没"的仪表盘。本包只做一件事：
// 把每次审批终态收进内存计数器，对外暴露通过率/驳回率/修改率快照。
//
// 不依赖 PG / HTTP，纯内存，goroutine 安全；后续 /v1/ops/distill
// 直接读 Snapshot()。

type DistillStats struct {
	mu sync.Mutex

	Total    int `json:"total"`
	Approved int `json:"approved"`
	Rejected int `json:"rejected"`
	Modified int `json:"modified"`
	Failed   int `json:"failed"`
	Pending  int `json:"pending"`
}

// NewDistillStats 空计数器。
func NewDistillStats() *DistillStats {
	return &DistillStats{}
}

// Record 记录一次审批终态（approve/reject/modify/fail/pending）。
// 大小写不敏感；未知状态归入 Pending。
func (d *DistillStats) Record(state string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch lower(state) {
	case "approved", "approve":
		d.Total++
		d.Approved++
	case "rejected", "reject":
		d.Total++
		d.Rejected++
	case "modified", "modify":
		d.Total++
		d.Modified++
	case "failed", "fail":
		d.Total++
		d.Failed++
	case "pending":
		d.Pending++
	}
}

// Rates 快照（含百分比 0..1）。
type Rates struct {
	Total    int     `json:"total"`
	Approved int     `json:"approved"`
	Rejected int     `json:"rejected"`
	Modified int     `json:"modified"`
	Failed   int     `json:"failed"`
	Pending  int     `json:"pending"`
	PassRate    float64 `json:"pass_rate"`
	RejectRate   float64 `json:"reject_rate"`
	ModifyRate   float64 `json:"modify_rate"`
	Healthy      bool    `json:"healthy"`
	HealthReason string  `json:"health_reason,omitempty"`
}

func (d *DistillStats) Snapshot() Rates {
	if d == nil {
		return Rates{HealthReason: "未初始化"}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	r := Rates{
		Total: d.Total, Approved: d.Approved, Rejected: d.Rejected,
		Modified: d.Modified, Failed: d.Failed, Pending: d.Pending,
	}
	if r.Total > 0 {
		r.PassRate = float64(r.Approved) / float64(r.Total)
		r.RejectRate = float64(r.Rejected) / float64(r.Total)
		r.ModifyRate = float64(r.Modified) / float64(r.Total)
	}
	// 健康线：样本≥50 且驳回率 < 40% 且通过率 > 40%
	if r.Total >= 50 && r.RejectRate < 0.4 && r.PassRate > 0.4 {
		r.Healthy = true
		r.HealthReason = "样本充足，驳回率正常"
	} else if r.Total < 50 {
		r.HealthReason = "样本不足（<50），继续观察"
	} else if r.RejectRate >= 0.4 {
		r.HealthReason = "驳回率过高（≥40%），Agent 提案质量下降"
	} else {
		r.HealthReason = "通过率过低（≤40%），可能过度收紧"
	}
	return r
}

// JSON 一行快照，供日志/HTTP 直接吐。
func (d *DistillStats) JSON() []byte {
	b, _ := json.Marshal(d.Snapshot())
	return b
}

func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
