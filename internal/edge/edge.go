// v1.4.0: 端侧智能统一入口——唤醒词 → 意图粗分类 → 敏感前置过滤 → 出端裁决。
// 全部在端侧完成；原始音频不经过本包，出端 body 必须过 EgressGuard 字节级裁决。
package edge

import (
	"encoding/json"
)

// Pipeline 端侧处理管道。
type Pipeline struct {
	classifier Classifier
	filter     *SensitiveFilter
	guard      *EgressGuard
	endpoint   string
}

// NewPipeline 端侧默认管道。allowedHosts 为端侧允许回连的后端 host。
func NewPipeline(allowedHosts []string, egressEndpoint string) *Pipeline {
	return &Pipeline{
		classifier: NewRuleClassifier(),
		filter:     NewSensitiveFilter(),
		guard:      NewEgressGuard(allowedHosts),
		endpoint:   egressEndpoint,
	}
}

// Result 端侧处理结果。
type Result struct {
	Wakeup       bool
	Category     string
	Confidence   float64
	Blocked      bool
	RedactedText string
	Hits         []Match
	ShouldUpload bool // 敏感命中则 false，禁止出端
}

// Process 纯端侧文本处理（不发网络请求）。
func (p *Pipeline) Process(text string) Result {
	intent := p.classifier.Classify(text)
	blocked, redacted, hits := p.filter.Filter(text)
	return Result{
		Wakeup:       intent.Category == "wakeup",
		Category:     intent.Category,
		Confidence:   intent.Confidence,
		Blocked:      blocked,
		RedactedText: redacted,
		Hits:         hits,
		// 只有非敏感、且不是纯未知的文本才允许进入出端裁决
		ShouldUpload: !blocked && intent.Category != "unknown",
	}
}

// EgressBody 构造将被发送的 JSON body（仅文本字段，绝无音频字节）。
type EgressBody struct {
	Category string `json:"category"`
	Text     string `json:"text"`
}

// Egress 对"即将出端"的字节做真实裁决。返回 (allowed, reason, bodyBytes)。
func (p *Pipeline) Egress(r Result) (bool, string, []byte) {
	if !r.ShouldUpload {
		return false, "blocked by edge filter", nil
	}
	body, _ := json.Marshal(EgressBody{Category: r.Category, Text: r.RedactedText})
	allowed, reason := p.guard.Allow(p.endpoint, body)
	return allowed, reason, body
}

// Guard 暴露出端审计报告（供 API 落库 / 导出）。
func (p *Pipeline) Guard() *EgressGuard { return p.guard }
