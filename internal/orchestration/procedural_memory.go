package orchestration

import (
	"context"
	"fmt"
	"strings"

	"human-agent-im/internal/memory"
)

// v2.3.14 D2：程序记忆（procedural memory）检索与 playbook 更新。
//
// 设计文档 §2.8：用户的审批决策（approve/reject/modify）是唯一的事实源，
// 写入子 Agent 的程序记忆。下次遇到相似触发条件，子 Agent 先查程序记忆，
// 而不是重新调大模型从头推理；连续驳回同类操作后自动收紧阈值。

type ProceduralMemory struct {
	store memory.Store
}

func NewProceduralMemory(store memory.Store) *ProceduralMemory {
	return &ProceduralMemory{store: store}
}

// 决策对置信度的调整（reject 比 approve 影响大）。
const (
	deltaApprove = 0.1
	deltaReject  = -0.3
	deltaModify  = -0.1
	minConf      = 0.3 // 低于此值视为"不再推荐"
	autoThresh   = 0.6 // 高于此值可自动执行；低于则上抛审批
)

// RecordDecision 记录一次审批决策。
//   - trigger：触发场景（如 "purchase.amount>500"）；
//   - suggestion：子 Agent 当时的建议（如 "buy:咖啡机"）；
//   - decision：approve / reject / modify；
//   - note：用户备注（修改意见等）。
func (p *ProceduralMemory) RecordDecision(ctx context.Context, sc memory.Scope, trigger, suggestion, decision, note string) error {
	if p == nil || p.store == nil || strings.TrimSpace(trigger) == "" {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(trigger))
	value := decisionLabel(decision)
	if strings.TrimSpace(note) != "" {
		value += "（" + strings.TrimSpace(note) + "）"
	}
	// 初次写入置信度 0.5；后续 ReinforceFact 按决策方向调整
	_ = p.store.UpsertFact(ctx, sc, &memory.Fact{
		Key:        key,
		Value:      value,
		Confidence: 0.5,
	})
	delta := 0.0
	switch strings.ToLower(decision) {
	case "approve", "approved":
		delta = deltaApprove
	case "reject", "rejected":
		delta = deltaReject
	case "modify", "modified":
		delta = deltaModify
	}
	if delta != 0 {
		_, _ = p.store.ReinforceFact(ctx, sc, key, delta)
	}
	return nil
}

// Suggestion 查询相似触发条件下的历史建议与置信度。
// 返回空串表示没记过（子 Agent 自由发挥）；返回字符串时子 Agent 应优先参考。
func (p *ProceduralMemory) Suggestion(ctx context.Context, sc memory.Scope, trigger string) (string, float64) {
	if p == nil || p.store == nil || strings.TrimSpace(trigger) == "" {
		return "", 0
	}
	facts, err := p.store.SearchFacts(ctx, sc, trigger)
	if err != nil || len(facts) == 0 {
		return "", 0
	}
	// 取置信度最高的一条
	best := facts[0]
	for _, f := range facts[1:] {
		if f != nil && f.Confidence > best.Confidence {
			best = f
		}
	}
	if best == nil || best.Confidence < minConf {
		return "", 0
	}
	return fmt.Sprintf("上次%s：%s（把握 %.0f%%）", best.Key, best.Value, best.Confidence*100), best.Confidence
}

// ShouldAutoExecute 判断该 trigger 是否可直接执行（不需要再上抛审批）。
// 依据：历史置信度 ≥ autoThresh 且决策为 approve。
func (p *ProceduralMemory) ShouldAutoExecute(ctx context.Context, sc memory.Scope, trigger string) bool {
	_, conf := p.Suggestion(ctx, sc, trigger)
	return conf >= autoThresh
}

func decisionLabel(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "approve", "approved":
		return "用户批准"
	case "reject", "rejected":
		return "用户驳回"
	case "modify", "modified":
		return "用户要求修改"
	default:
		return "未决"
	}
}
