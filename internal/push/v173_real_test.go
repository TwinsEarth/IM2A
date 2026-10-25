package push

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func v173DB(t *testing.T) *sql.DB {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS im_push_credentials(user_id TEXT, platform TEXT, cert_ref TEXT DEFAULT '', device_token TEXT DEFAULT '', updated_at TIMESTAMPTZ DEFAULT now(), PRIMARY KEY(user_id,platform))`,
		`CREATE TABLE IF NOT EXISTS im_push_tasks(id BIGSERIAL PRIMARY KEY, user_id TEXT, msg_key TEXT, title TEXT DEFAULT '', body TEXT DEFAULT '', status TEXT DEFAULT 'pending', attempts INT DEFAULT 0, last_error TEXT DEFAULT '', created_at TIMESTAMPTZ DEFAULT now(), updated_at TIMESTAMPTZ DEFAULT now())`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// pushTestLockKey 是推送调度测试专用的会话级 advisory lock 常量（'PUSH' 四字节）。
// go test 跨包并行时，push 包与 api 包的调度测试是不同测试进程、扫同一张物理表，
// 两处用同一常量加锁可串行化，杜绝各自的假腾讯云服务器互相抢占对方的 pending 任务。
const pushTestLockKey int64 = 0x50555348

func acquirePushTestLock(t *testing.T, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("push lock conn: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", pushTestLockKey); err != nil {
		_ = conn.Close()
		t.Fatalf("acquire push advisory lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", pushTestLockKey)
		_ = conn.Close()
	})
}

// v1.7.3：推送任务真实落 PG；有凭据时真实 HTTP 出站被腾讯云端点捕获；无凭据/失败不丢消息。
func TestV173_OfflinePushQueueAndDispatch(t *testing.T) {
	db := v173DB(t)
	acquirePushTestLock(t, db)
	ctx := context.Background()
	suf := fmt.Sprintf("%d", time.Now().UnixNano())
	// 测试隔离：清空本测试专属的推送表，避免历史残留任务污染计数断言
	if _, err := db.Exec(`TRUNCATE im_push_tasks, im_push_credentials`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// fake 腾讯云：记录收到的真实出站请求
	var gotPath, gotBody string
	var gotStatus int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath = r.URL.Path
		gotBody = string(b)
		w.WriteHeader(gotStatus)
		_, _ = w.Write([]byte(`{"ActionStatus":"OK"}`))
	}))
	defer srv.Close()
	cl := &Client{BaseURL: srv.URL, SDKAppID: "1600168829", HTTP: srv.Client()}

	// 用户A：已配证书 → 应成功
	userA := "v173A_" + suf
	// 用户B：无凭据 → 应保持 pending
	userB := "v173B_" + suf
	// 用户C：配了空 cert_ref → cert_not_configured
	userC := "v173C_" + suf
	// 隔离清理：仅删本测试 v173 前缀数据，不再 TRUNCATE 全表。
	t.Cleanup(func() {
		db.Exec(`DELETE FROM im_push_tasks WHERE user_id LIKE 'v173%'`)
		db.Exec(`DELETE FROM im_push_credentials WHERE user_id LIKE 'v173%'`)
	})

	if _, err := db.Exec(`INSERT INTO im_push_credentials(user_id,platform,cert_ref,device_token) VALUES($1,'apns','cert-abc','tok-123')`, userA); err != nil {
		t.Fatalf("cred A: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO im_push_credentials(user_id,platform,cert_ref) VALUES($1,'apns','')`, userC); err != nil {
		t.Fatalf("cred C: %v", err)
	}

	// 1) 三个用户各落一条 pending 任务
	for _, u := range []string{userA, userB, userC} {
		if err := Enqueue(ctx, db, OfflinePushMsg{UserID: u, MsgKey: "mk_" + u, Title: "审批", Body: "待批"}); err != nil {
			t.Fatalf("enqueue %s: %v", u, err)
		}
	}
	var pending int
	db.QueryRow(`SELECT count(*) FROM im_push_tasks WHERE status='pending'`).Scan(&pending)
	if pending < 3 {
		t.Fatalf("enqueue must persist 3 pending, got %d", pending)
	}

	// 2) 第一次 dispatch：A 成功(200)，B no_credential，C cert_not_configured
	gotStatus = 200
	sent, attempted, err := DispatchPending(ctx, db, cl)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if sent != 1 || attempted != 3 {
		t.Fatalf("want sent=1 attempted=3, got sent=%d attempted=%d", sent, attempted)
	}
	// 真实出站断言：fake 腾讯云确实收到离线推送请求，且 body 含 OfflinePushInfo
	if gotPath != "/v4/openim/offlinepush" {
		t.Fatalf("outbound path wrong: %s", gotPath)
	}
	if !strings.Contains(gotBody, "OfflinePushInfo") || !strings.Contains(gotBody, userA) {
		t.Fatalf("outbound body missing OfflinePushInfo/userA: %s", gotBody)
	}

	// A 必须是 sent
	var stA, lastB, lastC string
	var attB int
	db.QueryRow(`SELECT status FROM im_push_tasks WHERE user_id=$1`, userA).Scan(&stA)
	db.QueryRow(`SELECT last_error FROM im_push_tasks WHERE user_id=$1`, userB).Scan(&lastB)
	db.QueryRow(`SELECT last_error, attempts FROM im_push_tasks WHERE user_id=$1`, userC).Scan(&lastC, &attB)
	if stA != "sent" {
		t.Fatalf("A must be sent, got %s", stA)
	}
	if lastB != "no_credential" {
		t.Fatalf("B must record no_credential, got %s", lastB)
	}
	if lastC != "cert_not_configured" {
		t.Fatalf("C must record cert_not_configured, got %s", lastC)
	}

	// 3) 模拟腾讯云 500：再给 B 补凭据后 dispatch，失败累加 attempts
	if _, err := db.Exec(`INSERT INTO im_push_credentials(user_id,platform,cert_ref,device_token) VALUES($1,'apns','cert-b','tok-b')`, userB); err != nil {
		t.Fatalf("cred B add: %v", err)
	}
	gotStatus = 500
	sent2, _, _ := DispatchPending(ctx, db, cl)
	if sent2 != 0 {
		t.Fatalf("500 must not mark sent, got %d", sent2)
	}
	var stB string
	db.QueryRow(`SELECT status FROM im_push_tasks WHERE user_id=$1`, userB).Scan(&stB)
	if stB != "pending" {
		t.Fatalf("B must stay pending after 500, got %s", stB)
	}
}
