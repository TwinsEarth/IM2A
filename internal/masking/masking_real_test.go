package masking

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func testDB(t *testing.T) (*sql.DB, string) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	suf := fmt.Sprintf("v185_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM support_tickets WHERE ticket_no LIKE $1`, suf+"%"); err != nil {
			t.Errorf("cleanup tickets: %v", err)
		}
		if _, err := db.Exec(`DELETE FROM security_events WHERE actor=$1 AND path='/v1/egress/tickets'`, suf); err != nil {
			t.Errorf("cleanup egress events: %v", err)
		}
		db.Close()
	})
	return db, suf
}

func TestV185FieldMaskers(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"phone", Phone("13812345678"), "138****5678"},
		{"idcard", IDCard("11010119900307123X"), "110101********123X"},
		{"bank", BankCard("6222021234567890123"), "6222***********0123"},
		{"email", Email("zhangsan@example.com"), "zh******@example.com"},
		{"name", Name("张三丰"), "张**"},
	}
	for _, c := range cases {
		if c.in != c.want {
			t.Errorf("%s: got %q want %q", c.name, c.in, c.want)
		}
	}
	// 地址：family/agent 到区；support 到市
	if got := Address("上海市浦东新区张江镇高科路100号", false); got != "上海市浦东新区****" {
		t.Errorf("地址到区: %q", got)
	}
	if got := Address("上海市浦东新区张江镇", true); got != "上海市****" {
		t.Errorf("地址到市: %q", got)
	}
	// 金额按角色
	if got := AmountCents(12345, RoleSelf); got != "123.45" {
		t.Errorf("金额 self: %q", got)
	}
	if got := AmountCents(12345, RoleSupport); got != "123" {
		t.Errorf("金额 support: %q", got)
	}
	if got := AmountCents(12345, RoleAgent); got != "***" {
		t.Errorf("金额 agent: %q", got)
	}
}

func TestV185RoleWhitelist(t *testing.T) {
	if New("hacker-role").Role != RoleOther {
		t.Fatal("非法角色必须回落 other（最小暴露）")
	}
	if New("").Role != RoleOther {
		t.Fatal("空角色必须回落 other")
	}
}

func TestV185FreeTextScan(t *testing.T) {
	in := "手机13812345678 身份证11010119900307123X 邮箱a@b.com"
	out := New(RoleAgent).Text(in)
	for _, leak := range []string{"13812345678", "11010119900307123X", "a@b.com"} {
		if strings.Contains(out, leak) {
			t.Errorf("脱敏后仍含明文 %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "138****5678") {
		t.Errorf("手机号应脱敏为 138****5678: %s", out)
	}
	// admin 原样
	if New(RoleAdmin).Text(in) != in {
		t.Error("admin 角色不应脱敏")
	}
}

func TestV185EgressTicketsReal(t *testing.T) {
	db, suf := testDB(t)
	ctx := context.Background()
	ticketNo := suf + "-001"
	contact := "13812345678"
	content := "我的手机13812345678，身份证11010119900307123X，邮箱user@corp.com"
	_, err := db.ExecContext(ctx,
		`INSERT INTO support_tickets(ticket_no,contact,category,subject,content,status)
		 VALUES ($1,$2,'test',$3,$4,1)`,
		ticketNo, contact, suf+"-subject", content)
	if err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	// agent 角色出口：真实读库 + 脱敏 + 落审计
	out, _, err := New(RoleAgent).EgressTickets(ctx, db, suf, "agent_review")
	if err != nil {
		t.Fatalf("egress: %v", err)
	}
	var mine map[string]any
	for _, r := range out {
		if r["ticket_no"] == ticketNo {
			mine = r
		}
	}
	if mine == nil {
		t.Fatal("出口结果中未找到测试工单")
	}
	if got := mine["contact"].(string); got != "138****5678" {
		t.Errorf("contact 应脱敏为 138****5678，实际 %q", got)
	}
	c := mine["content"].(string)
	for _, leak := range []string{"13812345678", "11010119900307123X", "user@corp.com"} {
		if strings.Contains(c, leak) {
			t.Errorf("content 仍含明文 %q: %s", leak, c)
		}
	}

	// 审计真实落库
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM security_events WHERE actor=$1 AND event_type='egress_masked' AND path='/v1/egress/tickets'`,
		suf).Scan(&n); err != nil {
		t.Fatalf("query egress audit: %v", err)
	}
	if n < 1 {
		t.Fatal("egress_masked 审计应至少落库 1 条")
	}

	// admin 角色出口：全量不脱敏
	out2, masked, err := New(RoleAdmin).EgressTickets(ctx, db, suf, "admin_review")
	if err != nil {
		t.Fatalf("egress admin: %v", err)
	}
	for _, r := range out2 {
		if r["ticket_no"] == ticketNo {
			if r["contact"].(string) != "13812345678" {
				t.Errorf("admin 应见明文 contact，实际 %q", r["contact"])
			}
		}
	}
	if masked != 0 {
		t.Errorf("admin 角色脱敏字段数应为 0，实际 %d", masked)
	}
}
