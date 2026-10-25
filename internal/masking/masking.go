// Package masking 实现出口数据脱敏（v1.8.5）。
//
// 按"角色白名单"决定字段可见性：手机号/身份证/银行卡/邮箱/地址/金额，
// 以及自由文本（content/subject/feedback 等）中内嵌的敏感串。
// 默认角色 other = 全脱敏；非法角色一律按 other 处理（默认拒绝，最小暴露）。
package masking

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Role 角色（出口查看方）。
const (
	RoleSelf    = "self"    // 本人：全量
	RoleAdmin   = "admin"   // 管理员：全量
	RoleSupport = "support" // 客服：可联系手机号；金额到元；地址到市；证/卡/邮箱脱敏
	RoleFamily  = "family"  // 家庭：手机号脱敏；地址到区；金额隐藏
	RoleAgent   = "agent"   // 子 Agent：同 family，最小够用
	RoleOther   = "other"   // 其他/未知：全脱敏
)

// validRoles 角色白名单。
var validRoles = map[string]bool{
	RoleSelf: true, RoleAdmin: true, RoleSupport: true,
	RoleFamily: true, RoleAgent: true, RoleOther: true,
}

// Masker 按角色脱敏。
type Masker struct{ Role string }

// New 构造 Masker；非法角色回落到 other（最小暴露）。
func New(role string) *Masker {
	if !validRoles[role] {
		role = RoleOther
	}
	return &Masker{Role: role}
}

func (m *Masker) full() bool { return m.Role == RoleSelf || m.Role == RoleAdmin }

// ---- 单字段脱敏 ----

// Phone 13812341234 -> 138****1234（保留前 3 后 4）。
func Phone(s string) string {
	if len(s) < 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:3] + "****" + s[len(s)-4:]
}

// IDCard 18 位身份证 -> 前 6 后 4，中间 *。
func IDCard(s string) string {
	if len(s) <= 10 {
		return strings.Repeat("*", len(s))
	}
	return s[:6] + strings.Repeat("*", len(s)-10) + s[len(s)-4:]
}

// BankCard 银行卡 -> 前 4 后 4。
func BankCard(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

// Email abcd@x.com -> ab**@x.com。
func Email(s string) string {
	at := strings.Index(s, "@")
	if at <= 0 {
		return s
	}
	name := s[:at]
	keep := 2
	if len(name) < keep {
		keep = len(name)
	}
	stars := 3
	if len(name)-keep > 0 {
		stars = len(name) - keep
	}
	return name[:keep] + strings.Repeat("*", stars) + s[at:]
}

// Name 张三四 -> 张**（保留首字）。
func Name(s string) string {
	r := []rune(s)
	if len(r) <= 1 {
		return s
	}
	return string(r[0]) + strings.Repeat("*", len(r)-1)
}

// Address 截断到区/县；toCity=true 时只到市。
func Address(s string, toCity bool) string {
	mark := "****"
	if toCity {
		if i := strings.IndexAny(s, "市"); i >= 0 {
			return s[:i+len("市")] + mark
		}
		return mark
	}
	// 找省/自治区后第一个 区/县/旗 的位置
	idx := -1
	for _, q := range []string{"区", "县", "旗"} {
		if j := strings.Index(s, q); j >= 0 && (idx == -1 || j < idx) {
			idx = j
		}
	}
	if idx >= 0 {
		return s[:idx+3] + mark // 汉字按字节算 3 字节
	}
	if i := strings.IndexAny(s, "市"); i >= 0 {
		return s[:i+len("市")] + mark
	}
	return mark
}

// AmountCents 金额按角色：self/admin 精确到分；support 到元；其余隐藏。
func AmountCents(cents int64, role string) string {
	switch role {
	case RoleSelf, RoleAdmin:
		return fmt.Sprintf("%.2f", float64(cents)/100.0)
	case RoleSupport:
		return fmt.Sprintf("%d", cents/100)
	default:
		return "***"
	}
}

func (m *Masker) phone(s string) string {
	if m.full() || m.Role == RoleSupport {
		return s
	}
	return Phone(s)
}
func (m *Masker) addr(s string) string {
	if m.full() {
		return s
	}
	return Address(s, m.Role == RoleSupport)
}

// ---- 正则：自由文本扫描 ----

var (
	reIDCard = regexp.MustCompile(`\b\d{17}[\dXx]\b`)
	reBank   = regexp.MustCompile(`\b\d{16,19}\b`)
	rePhone  = regexp.MustCompile(`1[3-9]\d{9}`)
	reEmail  = regexp.MustCompile(`[0-9A-Za-z._\-]+@[0-9A-Za-z\-]+\.[0-9A-Za-z.\-]+`)
)

// Text 对自由文本中的敏感串做脱敏；self/admin 原样。
func (m *Masker) Text(s string) string {
	if m.full() {
		return s
	}
	// 顺序：身份证(18) -> 银行卡(16-19) -> 手机(11) -> 邮箱，前一步替换为 * 后不再被后续数字规则命中。
	s = reIDCard.ReplaceAllStringFunc(s, IDCard)
	s = reBank.ReplaceAllStringFunc(s, BankCard)
	s = rePhone.ReplaceAllStringFunc(s, func(p string) string { return m.phone(p) })
	s = reEmail.ReplaceAllStringFunc(s, Email)
	return s
}

// 字段名分类
func fieldKind(k string) string {
	lk := strings.ToLower(k)
	switch {
	case strings.Contains(lk, "idcard") || strings.Contains(lk, "id_card"):
		return "idcard"
	case strings.Contains(lk, "bank") || strings.Contains(lk, "cardno"):
		return "bank"
	case strings.Contains(lk, "email") || strings.Contains(lk, "mail"):
		return "email"
	case strings.Contains(lk, "phone") || strings.Contains(lk, "mobile") ||
		strings.Contains(lk, "tel") || lk == "contact" || strings.Contains(lk, "contact"):
		return "phone"
	case strings.Contains(lk, "address") || strings.Contains(lk, "addr"):
		return "address"
	case strings.Contains(lk, "amount") || strings.Contains(lk, "cents") ||
		strings.Contains(lk, "price") || strings.Contains(lk, "fee") || strings.Contains(lk, "balance"):
		return "amount"
	case strings.Contains(lk, "realname") || strings.Contains(lk, "real_name") || lk == "name":
		return "name"
	case lk == "content" || lk == "subject" || lk == "body" ||
		strings.Contains(lk, "text") || lk == "note" || lk == "feedback" || lk == "message":
		return "freetext"
	}
	return ""
}

// MaskValue 按字段名脱敏字符串值。
func (m *Masker) MaskValue(field, value string) string {
	switch fieldKind(field) {
	case "idcard":
		if m.full() {
			return value
		}
		return IDCard(value)
	case "bank":
		if m.full() {
			return value
		}
		return BankCard(value)
	case "email":
		if m.full() {
			return value
		}
		return Email(value)
	case "phone":
		return m.phone(value)
	case "address":
		return m.addr(value)
	case "name":
		if m.full() {
			return value
		}
		return Name(value)
	case "freetext":
		return m.Text(value)
	}
	return value
}

// MaskMap 递归脱敏 map（含嵌套 map / []any）。返回脱敏字段数。
func (m *Masker) MaskMap(obj map[string]any) (map[string]any, int) {
	n := 0
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			for k, vv := range t {
				if s, ok := vv.(string); ok {
					if kind := fieldKind(k); kind != "" {
						// 仅对已知名单字段（敏感字段 + 声明的自由文本字段）处理；
						// 未知字段（id/ticket_no/编号等）原样保留，避免误伤业务标识。
						mv := m.MaskValue(k, s)
						if mv != s {
							n++
						}
						t[k] = mv
					}
				} else {
					t[k] = walk(vv)
				}
			}
			return t
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
			return t
		}
		return v
	}
	return walk(obj).(map[string]any), n
}

// ---- 真实出口集成：客服工单 ----

// EgressTickets 从 support_tickets 读出工单，按角色脱敏后返回；
// 同时把一次出口脱敏写入 security_events（event_type=egress_masked）。
// 真实读 PG、真实脱敏、真实落审计。返回（脱敏工单，脱敏字段数，错误）。
func (m *Masker) EgressTickets(ctx context.Context, db *sql.DB, actor, scene string) ([]map[string]any, int, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, ticket_no, user_id, contact, category, subject, content, status FROM support_tickets ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []map[string]any{}
	total := 0
	for rows.Next() {
		var id, status int64
		var uid sql.NullInt64
		var ticketNo, contact, category, subject, content string
		if err := rows.Scan(&id, &ticketNo, &uid, &contact, &category, &subject, &content, &status); err != nil {
			return nil, 0, err
		}
		rec := map[string]any{
			"id": id, "ticket_no": ticketNo, "user_id": uid.Int64,
			"contact": contact, "category": category,
			"subject": subject, "content": content, "status": status,
		}
		_, n := m.MaskMap(rec)
		total += n
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	detail, _ := json.Marshal(map[string]any{
		"role": m.Role, "actor": actor, "scene": scene, "fields_masked": total, "tickets": len(out),
	})
	if _, err := db.ExecContext(ctx,
		`INSERT INTO security_events(event_type, method, path, status_code, actor, detail)
		 VALUES ('egress_masked','GET','/v1/egress/tickets',200,$1,$2)`,
		actor, string(detail)); err != nil {
		return nil, 0, fmt.Errorf("write egress audit: %w", err)
	}
	return out, total, nil
}
