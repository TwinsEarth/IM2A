package skillruntime

import (
	"fmt"
	"strings"
)

// v2.3.9 C1：Skill 返回结果的微信渠道格式归一化。
//
// 问题：Skill 返回的是结构化 JSON（带 wxid / ticket_id / message_id / 内部 URL），
// 直接塞进微信会被截断、出现机器味、泄露内部 ID。
// 本文件把 CallResult 翻译成 200 字以内、人类可读、无内部 ID 的短文案。

// wechatMaxLen 微信单条友好长度。
const wechatMaxLen = 200

// hiddenKeys 必须从输出里剥离的内部字段。
var hiddenKeys = map[string]bool{
	"wxid":        true,
	"ticket_id":   true,
	"message_id":  true,
	"msg_id":      true,
	"task_id":     true,
	"internal_id": true,
	"request_id":  true,
	"trace_id":    true,
	"user_id":     true,
	"agent_id":    true,
	"url":         true,
	"raw":         true,
	"debug":       true,
}

// labelMap 把常见字段名翻译成自然中文。
var labelMap = map[string]string{
	"amount":    "金额",
	"price":     "价格",
	"title":     "事项",
	"summary":   "摘要",
	"result":    "结果",
	"status":    "状态",
	"time":      "时间",
	"date":      "日期",
	"location":  "地点",
	"address":   "地址",
	"phone":     "电话",
	"note":      "备注",
	"count":     "数量",
	"category":  "分类",
	"deadline":  "截止",
	"approved":  "已批准",
	"rejected":  "已驳回",
	"purchased": "已下单",
	"scheduled": "已排期",
}

// FormatForWechat 把 CallResult 翻译成微信友好短文案。
func FormatForWechat(r CallResult) string {
	switch r.Status {
	case "DENIED":
		if r.Gate != nil && strings.Contains(r.Gate.Reason, "trust") {
			return "这件事需要你再确认一下。"
		}
		return "这件事我不能替你做主，需要再说一声。"
	case "REQUIRES_APPROVAL":
		return "我拟好了方案，等你回复 1 同意 / 2 驳回 / 3 修改。"
	}
	// Status == OK：优先取 Content 里的文本
	for _, b := range r.Content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return truncate(b.Text)
		}
	}
	// 再从 Structured 拼
	if len(r.Structured) == 0 {
		return "已处理。"
	}
	for _, k := range []string{"summary", "result", "message", "text"} {
		if v, ok := r.Structured[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return truncate(s)
			}
		}
	}
	var parts []string
	for k, v := range r.Structured {
		if hiddenKeys[strings.ToLower(k)] {
			continue
		}
		label := labelMap[strings.ToLower(k)]
		if label == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s：%v", label, v))
	}
	if len(parts) == 0 {
		return "已处理。"
	}
	return truncate(strings.Join(parts, "；"))
}

// FormatErrorForWechat 把内部错误翻译成用户能懂的话。
func FormatErrorForWechat(err error) string {
	if err == nil {
		return "已处理。"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return "响应有点慢，我稍后再试。"
	case strings.Contains(msg, "permission"), strings.Contains(msg, "denied"), strings.Contains(msg, "trust"):
		return "这件事需要你再确认一下。"
	case strings.Contains(msg, "quota"), strings.Contains(msg, "limit"):
		return "今天额度用完了，明天再试。"
	default:
		return "这件事我暂时没办成，稍后再试。"
	}
}

func truncate(s string) string {
	r := []rune(s)
	if len(r) <= wechatMaxLen {
		return s
	}
	return strings.TrimRight(string(r[:wechatMaxLen]), " .,;:，。；：") + "…"
}
