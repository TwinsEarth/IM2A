package api

// v2.3.27（task-12）：双账本合并的**判定性**测试（PG gated，PG_DSN 未设置时 skip）。
//
// bug 的验收条件就这一条：扣费一次之后，/v1/admin/stats 的 paid_total_cents /
// paid_charged 必须从基线变为实际值。修复前 /v1/ledger/charge 写的是退役表
// payment_ledger_legacy_v09，统计读 paid_message_ledger → 永远看不到这笔流水。
//
// v2.3.28（真库首验缺陷 1）根因修正：本文件此前用 `var s adminPaidStats` 直接解整个
// `/v1/admin/stats` 响应体，而该端点的契约是 `{"service","status","stats":{...}}`
// （internal/api/admin.go:58-60；同端点的 TestV176_AdminDashboardStats 就是解 out.Stats，
// 见 v176_real_test.go:36-42）。因此读到的永远是零值，与账本写没写无关。
// 现在按真实契约解 `stats` 子对象，并**额外直接查 paid_message_ledger** 证明写入路径：
//   count(*) WHERE biz_message_id=$1 → 1、status=1、amount=1200（扣费）；
//   退款后 status=2。这样即使将来统计口径再变，写路径破坏也会被这条 DB 断言抓住。
//
// 顺带断言：
//   * 同一 messageId 重复 charge 幂等（不二次计入金额）；
//   * 退款走 1→2 状态机，金额从 paid_total_cents 里扣回、paid_refunded +1；
//   * /v1/ledger/ 列表回显**原始** messageId（biz_message_id 往返），不是派生 UUID；
//   * 退款非法状态（pending）被 409 拒绝，而不是静默改成 refunded。
//
// 运行：
//   PG_DSN='postgres://...' go test ./internal/api -run TestLedgerConsolidation -v

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func realDepsPG(t *testing.T) *Deps {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN 未设置，跳过真实 PG 测试")
	}
	d, err := NewDeps("pgx", dsn)
	if err != nil || d == nil || d.DB == nil {
		t.Skipf("连接 PG 失败（跳过而不是失败）: %v", err)
	}
	// 关键：把这个测试自己的连接池关掉。池上限 25（store.defaultMaxOpenConns），而 E2E 的
	// PG13 max_connections=30（留 3 个超级用户位）——不关池会让同包后续测试直接 53300
	// "已保留的连接位置为执行非复制请求的超级用户预留"。t.Cleanup 是 LIFO，这里最早注册
	// 所以最后执行，不影响其他用到 DB 的 cleanup。
	t.Cleanup(func() { _ = d.DB.Close() })
	return d
}

type adminPaidStats struct {
	PaidTotalCents int64 `json:"paid_total_cents"`
	PaidCharged    int   `json:"paid_charged"`
	PaidRefunded   int   `json:"paid_refunded"`
}

// readPaidStats 读取 /v1/admin/stats，并按真实响应契约解出 stats 子对象。
// 契约来源：internal/api/admin.go:58-60 `writeJSON(..., map[string]any{"service","status","stats": stats})`。
func readPaidStats(t *testing.T, d *Deps) adminPaidStats {
	t.Helper()
	rec := httptest.NewRecorder()
	d.AdminMux().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/admin/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/admin/stats = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Stats adminPaidStats `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode stats: %v body=%s", err, rec.Body.String())
	}
	return out.Stats
}

// paidLedgerRow 直接查权威账本，证明扣费真的落到了统计读取的同一张表。
// 返回 (行数, 金额, 状态)。表结构见 internal/store/migrations/001_init.sql:103-117。
func paidLedgerRow(t *testing.T, d *Deps, bizMessageID string) (rows int, amount int64, status int16) {
	t.Helper()
	err := d.DB.QueryRow(`SELECT count(*), COALESCE(max(amount_cents),0), COALESCE(max(status),0)::smallint
		FROM paid_message_ledger WHERE biz_message_id=$1`, bizMessageID).Scan(&rows, &amount, &status)
	if err != nil {
		t.Fatalf("查 paid_message_ledger(biz_message_id=%s) 失败: %v", bizMessageID, err)
	}
	return rows, amount, status
}

func postLedger(t *testing.T, d *Deps, path string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	d.PaidMux().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestLedgerConsolidation_ChargeVisibleInAdminStats(t *testing.T) {
	d := realDepsPG(t)
	const (
		sender   = "700000001"
		receiver = "700000002"
		amount   = 1200 // ¥12.00
	)
	msgID := "task12-ledger-" + time.Now().Format("20060102150405.000000")

	t.Cleanup(func() {
		_, _ = d.DB.Exec(`DELETE FROM paid_message_events WHERE message_id =
			(SELECT message_id FROM paid_message_ledger WHERE biz_message_id=$1)`, msgID)
		_, _ = d.DB.Exec(`DELETE FROM paid_message_ledger WHERE biz_message_id=$1`, msgID)
	})

	before := readPaidStats(t, d)

	// 1) 扣费一次
	code, body := postLedger(t, d, "/v1/ledger/charge", map[string]any{
		"messageId": msgID, "senderId": sender, "receiverId": receiver,
		"amountCents": amount, "mode": "send",
	})
	if code != http.StatusOK || !strings.Contains(body, `"status":"charged"`) {
		t.Fatalf("charge = %d %s（want 200 charged）", code, body)
	}

	// 1b) 写路径证据：扣费必须真实落在 paid_message_ledger（= 统计读取的同一张表）。
	// 这条断言与上面的 stats 增量互为交叉验证：一个证明写进去了，一个证明统计看得见。
	if rows, amt, st := paidLedgerRow(t, d, msgID); rows != 1 || amt != amount || st != 1 {
		t.Fatalf("扣费未落到 paid_message_ledger：rows=%d amount=%d status=%d（want 1/%d/1）",
			rows, amt, st, amount)
	}

	// 2) 判定性断言：统计必须看见这笔钱（修复前恒为 before）。
	after := readPaidStats(t, d)
	if after.PaidTotalCents != before.PaidTotalCents+amount {
		t.Fatalf("paid_total_cents = %d, want %d（基线 %d + %d）——"+
			"扣费没有落到统计读取的 paid_message_ledger 上",
			after.PaidTotalCents, before.PaidTotalCents+amount, before.PaidTotalCents, amount)
	}
	if after.PaidCharged != before.PaidCharged+1 {
		t.Fatalf("paid_charged = %d, want %d", after.PaidCharged, before.PaidCharged+1)
	}

	// 3) 幂等：重复 charge 不二次计入
	code, body = postLedger(t, d, "/v1/ledger/charge", map[string]any{
		"messageId": msgID, "senderId": sender, "receiverId": receiver,
		"amountCents": amount, "mode": "send",
	})
	if code != http.StatusOK || !strings.Contains(body, `"idempotent":true`) {
		t.Fatalf("重复 charge = %d %s（want 200 idempotent）", code, body)
	}
	if s := readPaidStats(t, d); s.PaidTotalCents != after.PaidTotalCents {
		t.Fatalf("重复 charge 被二次计入：%d -> %d", after.PaidTotalCents, s.PaidTotalCents)
	}

	// 4) 列表回显原始 messageId（biz_message_id 往返，而不是派生 UUID）
	rec := httptest.NewRecorder()
	d.PaidMux().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/ledger/?userId="+sender, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), msgID) {
		t.Fatalf("列表未回显原始 messageId=%s（biz_message_id 未写入？）code=%d body=%s",
			msgID, rec.Code, rec.Body.String())
	}

	// 5) 退款：1→2，金额与计数同步回退
	code, body = postLedger(t, d, "/v1/ledger/refund", map[string]any{
		"messageId": msgID, "reason": "task-12 测试退款",
	})
	if code != http.StatusOK || !strings.Contains(body, `"refunded":true`) {
		t.Fatalf("refund = %d %s", code, body)
	}
	final := readPaidStats(t, d)
	if final.PaidTotalCents != before.PaidTotalCents {
		t.Fatalf("退款后 paid_total_cents = %d, want 回落到基线 %d", final.PaidTotalCents, before.PaidTotalCents)
	}
	if final.PaidRefunded != before.PaidRefunded+1 {
		t.Fatalf("paid_refunded = %d, want %d", final.PaidRefunded, before.PaidRefunded+1)
	}
	// 5b) 状态机证据：权威账本里的同一行必须变成 refunded(2)，而不是被删/被另起一行。
	if rows, _, st := paidLedgerRow(t, d, msgID); rows != 1 || st != 2 {
		t.Fatalf("退款未把权威账本置为 refunded：rows=%d status=%d（want 1/2）", rows, st)
	}

	// 6) 重复退款幂等
	code, body = postLedger(t, d, "/v1/ledger/refund", map[string]any{"messageId": msgID, "reason": "again"})
	if code != http.StatusOK || !strings.Contains(body, `"idempotent":true`) {
		t.Fatalf("重复 refund = %d %s（want 200 idempotent）", code, body)
	}
}

// TestLedgerConsolidation_RefundPendingRejected 证明状态机没被削弱：
// 只有 charged(1) 能退款，pending(0) 必须 409（老实现会直接改成 refunded）。
func TestLedgerConsolidation_RefundPendingRejected(t *testing.T) {
	d := realDepsPG(t)
	msgID := "task12-pending-" + time.Now().Format("20060102150405.000000")
	// 直接造一条 pending 行（模拟 pay_open 的"已预审未扣费"）。
	if _, err := d.DB.Exec(`INSERT INTO paid_message_ledger
		(message_id, sender_id, receiver_id, billing_mode, amount_cents, status, biz_message_id)
		VALUES (md5('im2a:ledger:biz:'||$1)::uuid, 700000011, 700000012, 2, 500, 0, $1)`, msgID); err != nil {
		t.Fatalf("造 pending 行失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.DB.Exec(`DELETE FROM paid_message_ledger WHERE biz_message_id=$1`, msgID)
	})

	code, body := postLedger(t, d, "/v1/ledger/refund", map[string]any{"messageId": msgID, "reason": "x"})
	if code != http.StatusConflict {
		t.Fatalf("pending 退款应 409, got %d %s", code, body)
	}

	// 非数字 senderId 必须 400（BIGINT 列不接受字符串，且不能静默哈希成假用户）。
	code, body = postLedger(t, d, "/v1/ledger/charge", map[string]any{
		"messageId": msgID + "-x", "senderId": "stranger1", "receiverId": "700000012", "amountCents": 100,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("非数字 senderId 应 400, got %d %s", code, body)
	}
}
