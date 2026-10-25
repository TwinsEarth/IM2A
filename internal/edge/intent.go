// Package edge v1.4.1: 端侧意图分类（规则可插拔，全程不出端）。
package edge

import (
	"strings"
	"unicode"
)

// Classifier 意图分类器接口——规则、小模型、云端 stub 都可插拔实现。
type Classifier interface {
	Classify(text string) Intent
}

// Intent 端侧粗分类结果。
type Intent struct {
	Category   string  // wakeup / command / chat / unknown
	Confidence float64 // 0..1
}

// 产品自有唤醒词（不绑定任何第三方助手）。
var defaultWakeWords = []string{"你好管家", "嘿管家", "管家管家", "助手"}

// RuleClassifier 基于关键词/句式的端侧规则分类器（默认实现）。
type RuleClassifier struct {
	wakeWords []string
	commandRE []string
}

// NewRuleClassifier 返回默认规则分类器。
func NewRuleClassifier() *RuleClassifier {
	return &RuleClassifier{
		wakeWords: defaultWakeWords,
		commandRE: []string{"打开", "关闭", "暂停", "播放", "设置", "提醒", "定个闹钟", "帮我", "把"},
	}
}

// NewCustomClassifier 允许上层覆盖唤醒词/命令词（可插拔，便于测试与定制）。
func NewCustomClassifier(wakeWords, commandWords []string) *RuleClassifier {
	if len(wakeWords) == 0 {
		wakeWords = defaultWakeWords
	}
	return &RuleClassifier{wakeWords: wakeWords, commandRE: commandWords}
}

func hasAny(text string, words []string) bool {
	for _, w := range words {
		if w == "" {
			continue
		}
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// Classify 顺序：唤醒词 > 命令 > 闲聊(问句) > 未知。命令优先于问句，
// 避免"帮我打开空调？"被误判为闲聊。
func (r *RuleClassifier) Classify(text string) Intent {
	t := strings.TrimSpace(text)
	if t == "" {
		return Intent{Category: "unknown", Confidence: 0.0}
	}
	if hasAny(t, r.wakeWords) {
		return Intent{Category: "wakeup", Confidence: 0.95}
	}
	if hasAny(t, r.commandRE) {
		return Intent{Category: "command", Confidence: 0.82}
	}
	if strings.Contains(t, "?") || strings.Contains(t, "？") || strings.Contains(t, "吗") || strings.Contains(t, "呢") {
		return Intent{Category: "chat", Confidence: 0.7}
	}
	// 纯短文本（如一句陈述句）归为 chat，过长无关键词归 unknown
	if utf8RuneCount(t) <= 40 {
		return Intent{Category: "chat", Confidence: 0.45}
	}
	return Intent{Category: "unknown", Confidence: 0.3}
}

func utf8RuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

var _ = unicode.IsLetter // 预留：后续做分词/停用词时使用
