// 内置示例 Skill：日程查询、家庭物业报修、家庭采购。
// 均为演示实现（不接真实外部 API），用于验证"门控→执行"链路与 Skill 接入范式。
package skillruntime

import (
	"context"
	"fmt"
	"sort"
	"time"

	"human-agent-im/internal/trustzone"
)

// ============================================================================
// 日程 Skill（只读，schedule 域）
// ============================================================================

// CalendarSkill 日程查询。
type CalendarSkill struct {
	events []map[string]any
}

// NewCalendarSkill 构造示例日程 Skill。
func NewCalendarSkill() *CalendarSkill {
	now := time.Now()
	return &CalendarSkill{events: []map[string]any{
		{"start": now.Add(2 * time.Hour).Format(time.RFC3339), "title": "与对方管家确认周四拜访", "with": "家庭"},
		{"start": now.Add(26 * time.Hour).Format(time.RFC3339), "title": "家长会", "with": "学校"},
	}}
}

// Manifest 返回清单。
func (s *CalendarSkill) Manifest() Manifest {
	return Manifest{
		SkillID: "calendar", Version: "0.2.0", Name: "日程",
		Description:     "查询日程、可用时段",
		FinancialImpact: FinancialReadOnly,
		TrustZoneMin:    trustzone.LevelFamily,
		ApprovalPolicy:  ApprovalAuto,
	}
}

// Tools 工具。
func (s *CalendarSkill) Tools() []Tool {
	return []Tool{{
		Name: "query", Description: "查询指定日期的日程",
		Scope: trustzone.ScopeSchedule, ReadOnly: true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"date": map[string]any{"type": "string", "description": "YYYY-MM-DD，缺省今天"},
			},
		},
	}}
}

// Call 执行。
func (s *CalendarSkill) Call(_ context.Context, toolName string, _ map[string]any) (string, map[string]any, error) {
	if toolName != "query" {
		return "", nil, fmt.Errorf("unknown tool %s", toolName)
	}
	out := make([]map[string]any, 0, len(s.events))
	out = append(out, s.events...)
	sort.Slice(out, func(i, j int) bool { return out[i]["start"].(string) < out[j]["start"].(string) })
	return fmt.Sprintf("今日起共 %d 条日程", len(out)), map[string]any{"events": out}, nil
}

// ============================================================================
// 物业报修 Skill（家庭域，可产生费用，受金额阈值约束）
// ============================================================================

// HomeMaintenanceSkill 家庭物业报修。
type HomeMaintenanceSkill struct{}

// NewHomeMaintenanceSkill 构造。
func NewHomeMaintenanceSkill() *HomeMaintenanceSkill { return &HomeMaintenanceSkill{} }

// Manifest 清单。
func (s *HomeMaintenanceSkill) Manifest() Manifest {
	return Manifest{
		SkillID: "home-maintenance", Version: "0.2.0", Name: "物业报修",
		Description:     "提交水电气/物业报修工单",
		FinancialImpact: FinancialSpendLimit,
		TrustZoneMin:    trustzone.LevelFamily,
		ApprovalPolicy:  ApprovalAuto,
	}
}

// Tools 工具。
func (s *HomeMaintenanceSkill) Tools() []Tool {
	return []Tool{{
		Name: "request", Description: "提交报修工单（水/电/燃气/维修）",
		Scope: trustzone.ScopeFamily, Destructive: false,
		AmountFromArgs: "amountCents",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category":    map[string]any{"type": "string", "enum": []string{"water", "electric", "gas", "repair"}},
				"description": map[string]any{"type": "string"},
				"amountCents": map[string]any{"type": "integer", "description": "预估费用（分），超阈值转审批"},
			},
			"required": []string{"category", "description"},
		},
	}}
}

// Call 执行。
func (s *HomeMaintenanceSkill) Call(_ context.Context, toolName string, args map[string]any) (string, map[string]any, error) {
	if toolName != "request" {
		return "", nil, fmt.Errorf("unknown tool %s", toolName)
	}
	cat, _ := args["category"].(string)
	desc, _ := args["description"].(string)
	order := map[string]any{
		"orderId":  "WO-" + cat + "-demo",
		"category": cat, "description": desc,
		"status": "submitted", "createdAt": time.Now().Format(time.RFC3339),
	}
	return fmt.Sprintf("已提交%s报修工单，等待物业响应", cat), order, nil
}

// ============================================================================
// 家庭采购 Skill（采购域，产生费用，受金额阈值/预算约束）
// ============================================================================

// FamilyPurchaseSkill 家庭采购。
type FamilyPurchaseSkill struct{}

// NewFamilyPurchaseSkill 构造。
func NewFamilyPurchaseSkill() *FamilyPurchaseSkill { return &FamilyPurchaseSkill{} }

// Manifest 清单。
func (s *FamilyPurchaseSkill) Manifest() Manifest {
	return Manifest{
		SkillID: "family-purchase", Version: "0.2.0", Name: "家庭采购",
		Description:     "家庭日用品采购比价与下单",
		FinancialImpact: FinancialSpendLimit,
		TrustZoneMin:    trustzone.LevelFamily,
		ApprovalPolicy:  ApprovalAuto,
	}
}

// Tools 工具。
func (s *FamilyPurchaseSkill) Tools() []Tool {
	return []Tool{
		{
			Name: "compare", Description: "比价（只读）",
			Scope: trustzone.ScopePurchase, ReadOnly: true,
			InputSchema: map[string]any{"type": "object",
				"properties": map[string]any{"item": map[string]any{"type": "string"}}},
		},
		{
			Name: "order", Description: "下单（产生费用）",
			Scope: trustzone.ScopePurchase, AmountFromArgs: "amountCents",
			InputSchema: map[string]any{"type": "object",
				"properties": map[string]any{
					"item":        map[string]any{"type": "string"},
					"amountCents": map[string]any{"type": "integer"},
				}},
		},
	}
}

// Call 执行。
func (s *FamilyPurchaseSkill) Call(_ context.Context, toolName string, args map[string]any) (string, map[string]any, error) {
	switch toolName {
	case "compare":
		item, _ := args["item"].(string)
		quotes := []map[string]any{
			{"vendor": "甲", "priceCents": 9800}, {"vendor": "乙", "priceCents": 9500},
			{"vendor": "丙", "priceCents": 9900},
		}
		return fmt.Sprintf("「%s」三家比价，最低 ¥95.00", item), map[string]any{"quotes": quotes}, nil
	case "order":
		item, _ := args["item"].(string)
		return "已下单：" + item, map[string]any{"orderId": "PO-demo", "item": item, "status": "placed"}, nil
	default:
		return "", nil, fmt.Errorf("unknown tool %s", toolName)
	}
}

// DefaultRegistry 注册全部内置 Skill。
func DefaultRegistry() *Registry {
	reg := NewRegistry()
	reg.MustRegister(NewCalendarSkill())
	reg.MustRegister(NewHomeMaintenanceSkill())
	reg.MustRegister(NewFamilyPurchaseSkill())
	return reg
}
