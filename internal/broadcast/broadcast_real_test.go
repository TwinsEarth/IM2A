package broadcast

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// 与 028_broadcast.sql 对齐，保证测试在未迁移环境也能独立跑（IF NOT EXISTS 幂等）。
const bcDDL = `
CREATE TABLE IF NOT EXISTS wechat_broadcasts (
    id             TEXT PRIMARY KEY,
    text           TEXT NOT NULL,
    sender_account TEXT NOT NULL DEFAULT '',
    sender_wxid    TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'pending',
    total          INTEGER NOT NULL DEFAULT 0,
    sent           INTEGER NOT NULL DEFAULT 0,
    failed         INTEGER NOT NULL DEFAULT 0,
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ,
    idempotency_key TEXT NOT NULL DEFAULT ''
);
-- 已存在的旧表补上幂等列（CREATE TABLE IF NOT EXISTS 不会改旧表）。
ALTER TABLE wechat_broadcasts ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_bc_idem_key
    ON wechat_broadcasts(idempotency_key)
    WHERE idempotency_key <> '';
CREATE TABLE IF NOT EXISTS wechat_broadcast_deliveries (
    id           BIGSERIAL PRIMARY KEY,
    broadcast_id TEXT NOT NULL REFERENCES wechat_broadcasts(id) ON DELETE CASCADE,
    to_wxid      TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    message_id   TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (broadcast_id, to_wxid)
);`

type fakeSender struct {
	mu       sync.Mutex
	calls    []string
	failWxid string
}

func (f *fakeSender) Send(ctx context.Context, accountID, targetWxid, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, accountID+">"+targetWxid)
	if targetWxid == f.failWxid {
		return "", errors.New("boom: simulated wechat failure")
	}
	return "fake_msg_" + targetWxid, nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeAccounts struct{ m map[string]string }

func (f *fakeAccounts) AllAccounts() (map[string]string, error) { return f.m, nil }

// TestBroadcastReal：真实 PG 落库 + 发送器 fake（不触达微信）。
//   - 排除发送方自身（3 个在线 binding，仅 2 个目标）
//   - 一条失败 → partial（sent=1/failed=1），逐条明细与 messageId/error 落库
//   - Run 重入幂等：已终态后再次 Run 不重复发送
func TestBroadcastReal(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(bcDDL); err != nil {
		t.Fatalf("ensure broadcast tables: %v", err)
	}

	ctx := context.Background()
	suf := fmt.Sprintf("bctest_%d", time.Now().UnixNano())
	grp := "grp_" + suf
	sender := suf + "_sender@im.wechat"
	t1 := suf + "_t1@im.wechat"
	t2 := suf + "_t2@im.wechat"

	if _, err := db.Exec(`INSERT INTO wechat_bot_groups(id,owner_user,name,status) VALUES($1,'test_bc','bc test','active')`, grp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// 级联删除该组的 bindings
		_, _ = db.Exec(`DELETE FROM wechat_bot_groups WHERE id=$1`, grp)
	})
	binds := [][3]string{
		{"bind_" + suf + "_w", grp, sender},
		{"bind_" + suf + "_1", grp, t1},
		{"bind_" + suf + "_2", grp, t2},
	}
	devs := []string{"windows", "ios", "android"}
	for i, b := range binds {
		if _, err := db.Exec(`INSERT INTO wechat_bindings(id,group_id,wxid,device_type,status) VALUES($1,$2,$3,$4,'online')`,
			b[0], b[1], b[2], devs[i]); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("BROADCAST_ACCOUNT", "acc_sender")
	fs := &fakeSender{failWxid: t2}
	fa := &fakeAccounts{m: map[string]string{"acc_sender": sender, "acc_other": "other@im.wechat"}}
	svc := New(db, fs, fa).WithConcurrency(8, time.Millisecond)

	// 预览目标：应排除 sender 自身，仅 t1/t2。
	targets, acc, swxid, err := svc.Targets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if acc != "acc_sender" || swxid != sender {
		t.Fatalf("resolve sender wrong: acc=%s wxid=%s", acc, swxid)
	}
	// 生产库可能已有真实在线用户，故只断言包含本测试目标、排除发送方自身。
	tset := map[string]bool{}
	for _, w := range targets {
		tset[w] = true
	}
	if !tset[t1] || !tset[t2] {
		t.Fatalf("missing injected targets: %v", targets)
	}
	if tset[sender] {
		t.Fatalf("sender wxid must be excluded, got targets=%v", targets)
	}
	wantTotal := len(targets)

	id, n, err := svc.Create(ctx, "【通知】测试广播 dry-run "+suf, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != wantTotal {
		t.Fatalf("create target count = %d, want %d", n, wantTotal)
	}
	t.Cleanup(func() {
		// 级联删除投递明细
		_, _ = db.Exec(`DELETE FROM wechat_broadcasts WHERE id=$1`, id)
	})

	// 等待终态。
	var bc map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for {
		bc, err = svc.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		st := bc["status"].(string)
		if st != "running" && st != "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("broadcast not finished: %v", bc["status"])
		}
		time.Sleep(100 * time.Millisecond)
	}

	if bc["status"].(string) != "partial" {
		t.Fatalf("status = %v, want partial (one failure)", bc["status"])
	}
	if bc["total"].(int) != wantTotal || bc["failed"].(int) != 1 || bc["sent"].(int) != wantTotal-1 {
		t.Fatalf("counts wrong: total=%v sent=%v failed=%v (want total=%d sent=%d failed=1)",
			bc["total"], bc["sent"], bc["failed"], wantTotal, wantTotal-1)
	}
	dels, _ := bc["deliveries"].([]map[string]any)
	if len(dels) != wantTotal {
		t.Fatalf("deliveries = %d, want %d", len(dels), wantTotal)
	}
	got := map[string]map[string]any{}
	for _, d := range dels {
		got[d["to_wxid"].(string)] = d
	}
	if got[t1]["status"].(string) != "sent" || !strings.HasPrefix(got[t1]["message_id"].(string), "fake_msg_") {
		t.Fatalf("t1 should be sent with message id: %v", got[t1])
	}
	if got[t2]["status"].(string) != "failed" || !strings.Contains(got[t2]["error"].(string), "boom") {
		t.Fatalf("t2 should be failed with error: %v", got[t2])
	}

	// 重入幂等：终态后再次 Run，无 pending，不应再调用发送器。
	before := fs.count()
	svc.Run(ctx, id)
	if fs.count() != before {
		t.Fatalf("re-run re-sent: before=%d after=%d", before, fs.count())
	}
	var sent2 int
	if err := db.QueryRow(`SELECT sent FROM wechat_broadcasts WHERE id=$1`, id).Scan(&sent2); err != nil || sent2 != wantTotal-1 {
		t.Fatalf("re-run changed sent count: %d (want %d, err=%v)", sent2, wantTotal-1, err)
	}
}

// TestBroadcastEmptyText：空文案直接拒绝，不产生任何任务。
func TestBroadcastEmptyText(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(bcDDL); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROADCAST_ACCOUNT", "acc_sender")
	fs := &fakeSender{}
	fa := &fakeAccounts{m: map[string]string{"acc_sender": "x@im.wechat"}}
	svc := New(db, fs, fa).WithConcurrency(2, time.Millisecond)
	if _, _, err := svc.Create(context.Background(), "   ", "test", ""); err == nil {
		t.Fatal("want error for empty text")
	}
}

// TestBroadcastIdempotency：同一幂等键在窗口内重复创建只返回同一条任务，且发送器只被调用一次；
// 不同幂等键则各自独立成任务。验证"浏览器丢响应后重试"绝不重复群发。
func TestBroadcastIdempotency(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(bcDDL); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	suf := fmt.Sprintf("bcidem_%d", time.Now().UnixNano())
	grp := "grp_" + suf
	sender := suf + "_sender@im.wechat"
	t1 := suf + "_t1@im.wechat"
	if _, err := db.Exec(`INSERT INTO wechat_bot_groups(id,owner_user,name,status) VALUES($1,'test_bc','idem','active')`, grp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM wechat_bindings WHERE group_id=$1`, grp)
		_, _ = db.Exec(`DELETE FROM wechat_bot_groups WHERE id=$1`, grp)
	})
	for _, b := range [][2]string{{"bind_" + suf + "_w", sender}, {"bind_" + suf + "_1", t1}} {
		dev := "windows"
		if b[1] == t1 {
			dev = "ios"
		}
		if _, err := db.Exec(`INSERT INTO wechat_bindings(id,group_id,wxid,device_type,status) VALUES($1,$2,$3,$4,'online')`,
			b[0], grp, b[1], dev); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BROADCAST_ACCOUNT", "acc_sender")
	fs := &fakeSender{}
	fa := &fakeAccounts{m: map[string]string{"acc_sender": sender}}
	svc := New(db, fs, fa).WithConcurrency(2, time.Millisecond)

	idem := "idem_" + suf
	id1, n1, err := svc.Create(ctx, "【通知】幂等测试 "+suf, "test", idem)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM wechat_broadcasts WHERE idempotency_key IN($1,$2)`, idem, idem+"_b")
	})
	if n1 < 1 {
		t.Fatalf("first create total = %d, want >=1", n1)
	}
	// 模拟浏览器丢失 202 响应后重试：相同幂等键 + 相同文案。
	id2, n2, err := svc.Create(ctx, "【通知】幂等测试 "+suf, "test", idem)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 || n1 != n2 {
		t.Fatalf("idempotent replay must return same task: %s/%d vs %s/%d", id1, n1, id2, n2)
	}
	// 不同幂等键创建（视为另一次群发），应得到新任务。
	id3, n3, err := svc.Create(ctx, "【通知】幂等测试 另一条 "+suf, "test", idem+"_b")
	if err != nil {
		t.Fatal(err)
	}
	if id3 == id1 {
		t.Fatalf("different idempotency key must create a new task, got same id %s", id3)
	}
	if n3 != n1 {
		t.Fatalf("second task target count = %d, want %d (same live target set)", n3, n1)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var cnt int
		_ = db.QueryRow(`SELECT count(*) FROM wechat_broadcasts WHERE id IN($1,$2) AND status IN('completed','partial','failed')`, id1, id3).Scan(&cnt)
		if cnt == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// 仅两条任务被创建（重试合并），发送器总调用 = 任务1目标数 + 任务2目标数，重试零多发。
	if got, want := fs.count(), n1+n3; got != want {
		t.Fatalf("sender calls = %d, want %d (retry must not resend)", got, want)
	}
}
