package store

// v1.5 真实 PG 账本测试：连真库断言 paid_message_ledger 状态机、事件溯源、
// 幂等、日限、退款落 refunded_at。PG_DSN 空则 skip。

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"human-agent-im/internal/ledger"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func newRealDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("pg unreachable: %v", err)
	}
	return db
}

// 清理指定 message_id 残留（测试前后）。
func cleanupMsg(t *testing.T, db *sql.DB, msgID string) {
	t.Helper()
	db.Exec(`DELETE FROM paid_message_events WHERE message_id=$1`, msgID)
	db.Exec(`DELETE FROM paid_message_ledger WHERE message_id=$1`, msgID)
}

func TestRealLedger_SendCharge_Persists(t *testing.T) {
	db := newRealDB(t)
	l := &pgLedger{db: db, dailyLimit: 50}
	msg := "00000000-0000-4000-8000-00000000f151"
	cleanupMsg(t, db, msg)
	defer cleanupMsg(t, db, msg)

	if _, err := l.Reserve(msg, 9001, 9002, ledger.ModeSendCharge, 500); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := l.Charge(msg, "card-1"); err != nil {
		t.Fatalf("charge: %v", err)
	}

	var status int
	var charged, card sql.NullString
	err := db.QueryRow(
		`SELECT status, charged_at::text, approval_card_id FROM paid_message_ledger WHERE message_id=$1`, msg).
		Scan(&status, &charged, &card)
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	if status != int(ledger.StatusCharged) {
		t.Errorf("status want CHARGED(1), got %d", status)
	}
	if !charged.Valid {
		t.Error("charged_at 应非空")
	}
	if !card.Valid || card.String != "card-1" {
		t.Errorf("approval_card_id want card-1, got %v", card)
	}

	// 事件溯源：reserved + charged 两条
	var evCount int
	db.QueryRow(`SELECT count(*) FROM paid_message_events WHERE message_id=$1`, msg).Scan(&evCount)
	if evCount < 2 {
		t.Errorf("paid_message_events 应 >=2 条(reserved+charged), got %d", evCount)
	}
}

func TestRealLedger_IdempotentReserve(t *testing.T) {
	db := newRealDB(t)
	l := &pgLedger{db: db, dailyLimit: 50}
	msg := "00000000-0000-4000-8000-00000000f152"
	cleanupMsg(t, db, msg)
	defer cleanupMsg(t, db, msg)

	if _, err := l.Reserve(msg, 9003, 9004, ledger.ModeSendCharge, 100); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	// 重复 reserve 同一 message_id：必须幂等拒绝，不产生第二行
	_, err := l.Reserve(msg, 9003, 9004, ledger.ModeSendCharge, 100)
	if !errors.Is(err, ledger.ErrAlreadyProcessed) {
		t.Errorf("重复 reserve 应 ErrAlreadyProcessed, got %v", err)
	}
	var rows int
	db.QueryRow(`SELECT count(*) FROM paid_message_ledger WHERE message_id=$1`, msg).Scan(&rows)
	if rows != 1 {
		t.Errorf("幂等后账本应仍 1 行, got %d", rows)
	}
	// 重复 charge 也不二次扣费
	if _, err := l.Charge(msg, ""); err != nil {
		t.Fatalf("charge: %v", err)
	}
	if _, err := l.Charge(msg, ""); !errors.Is(err, ledger.ErrAlreadyProcessed) {
		t.Errorf("重复 charge 应 ErrAlreadyProcessed, got %v", err)
	}
}

func TestRealLedger_DailyLimit(t *testing.T) {
	db := newRealDB(t)
	l := &pgLedger{db: db, dailyLimit: 1} // 每天只允许 1 笔
	sender := int64(9099)
	cleanupSender := func() {
		db.Exec(`DELETE FROM paid_message_events WHERE message_id IN (SELECT message_id FROM paid_message_ledger WHERE sender_id=$1)`, sender)
		db.Exec(`DELETE FROM paid_message_ledger WHERE sender_id=$1`, sender)
	}
	cleanupSender()
	defer cleanupSender()

	m1 := "00000000-0000-4000-8000-00000000f153"
	if _, err := l.Reserve(m1, uint64(sender), 9002, ledger.ModeSendCharge, 100); err != nil {
		t.Fatalf("first reserve should pass: %v", err)
	}
	// 当天第二笔应被日限拦截
	m2 := "00000000-0000-4000-8000-00000000f154"
	_, err := l.Reserve(m2, uint64(sender), 9002, ledger.ModeSendCharge, 100)
	if !errors.Is(err, ledger.ErrDailyLimitExceeded) {
		t.Errorf("第二笔应触发日限, got %v", err)
	}
}

func TestRealLedger_Refund_Persists(t *testing.T) {
	db := newRealDB(t)
	l := &pgLedger{db: db, dailyLimit: 50}
	msg := "00000000-0000-4000-8000-00000000f155"
	cleanupMsg(t, db, msg)
	defer cleanupMsg(t, db, msg)

	l.Reserve(msg, 9005, 9006, ledger.ModeSendCharge, 200)
	l.Charge(msg, "")
	if _, err := l.Refund(msg, "user requested"); err != nil {
		t.Fatalf("refund: %v", err)
	}
	var status int
	var refundedAt sql.NullString
	var reason sql.NullString
	err := db.QueryRow(
		`SELECT status, refunded_at::text, refund_reason FROM paid_message_ledger WHERE message_id=$1`, msg).
		Scan(&status, &refundedAt, &reason)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != int(2) {
		t.Errorf("退款后 status want 2(refunded), got %d", status)
	}
	if !refundedAt.Valid || refundedAt.String == "" {
		t.Error("refunded_at 应非空")
	}
	if !strings.Contains(reason.String, "user requested") {
		t.Errorf("refund_reason 应保留原因, got %q", reason.String)
	}
}
