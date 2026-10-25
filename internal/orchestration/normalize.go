package orchestration

import "strings"

// v2.3.26：模型常输出自由命名的工具（search_products / create_event / buy ...），
// 而 PermissionGate 是 deny-by-default 的白名单。这里在解析 Plan 时把常见别名
// 归一化到内置工具词汇表；无法识别的再交给门控拒绝（仍安全）。

var toolSynonyms = map[string]string{
	// 采购
	"search_products":   "purchase.search",
	"search_product":    "purchase.search",
	"compare":           "purchase.search",
	"compare_products":  "purchase.search",
	"compare_price":      "purchase.search",
	"shopping":          "purchase.search",
	"lookup":            "purchase.search",
	"price_check":       "purchase.search",
	"order":              "purchase.checkout",
	"buy":                "purchase.checkout",
	"checkout":           "purchase.checkout",
	"purchase":           "purchase.checkout",
	"place_order":        "purchase.checkout",
	"create_order":        "purchase.checkout",
	// 日程
	"add_event":          "calendar.add",
	"create_event":       "calendar.add",
	"schedule_event":     "calendar.add",
	"schedule":           "calendar.add",
	"remind_me":          "calendar.add",
	"set_reminder":       "calendar.add",
	"create_reminder":    "calendar.add",
	"list_events":        "calendar.list",
	"list_calendar":      "calendar.list",
	"query_calendar":     "calendar.list",
	// 物业
	"report_repair":      "property.report",
	"maintenance":        "property.report",
	"repair":              "property.report",
	"work_order":          "property.report",
	// 法律
	"draft_contract":     "legal.draft",
	"draft_document":    "legal.draft",
	"legal_review":      "legal.draft",
	// 亲子
	"remind":              "parenting.remind",
	// 通道
	"send_message":       "im.send",
	"send_im":             "im.send",
	"send_email":          "mail.send",
	"delete_record":      "system.delete_record",
}

// normalizeTool 把模型输出的工具名归一化到内置白名单；无法识别的原样返回。
func normalizeTool(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		return t
	}
	if _, ok := builtinTools[t]; ok {
		return t
	}
	if v, ok := toolSynonyms[t]; ok {
		return v
	}
	// 下划线/连字符统一
	alt := strings.ReplaceAll(t, "-", "_")
	if v, ok := toolSynonyms[alt]; ok {
		return v
	}
	// 模糊包含：含 product/buy/order 归采购，含 event/remind/schedule 归日程
	switch {
	case strings.Contains(t, "product") || strings.Contains(t, "buy") || strings.Contains(t, "order") || strings.Contains(t, "purchase"):
		return "purchase.search"
	case strings.Contains(t, "event") || strings.Contains(t, "remind") || strings.Contains(t, "calendar"):
		return "calendar.add"
	case strings.Contains(t, "repair") || strings.Contains(t, "maintenance") || strings.Contains(t, "property"):
		return "property.report"
	}
	return t
}
