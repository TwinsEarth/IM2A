package privacy

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// 与 migrations/015_data_subject_requests.sql 一致（幂等）；测试直接连库不经过网关迁移。
const ddl = `
CREATE TABLE IF NOT EXISTS data_subject_requests (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT UNIQUE NOT NULL,
    user_id BIGINT NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    result JSONB,
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);`

func testDB(t *testing.T) (*sql.DB, string) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("ensure DSR table: %v", err)
	}
	suf := fmt.Sprintf("v186%d", time.Now().UnixNano())
	return db, suf
}

// setup 建两个真实用户（序列生成 ID），返回 id 及注销清理函数。
func setup(t *testing.T, db *sql.DB, suf string) (int64, int64, func()) {
	var uid, target int64
	if err := db.QueryRow(`INSERT INTO users(phone_encrypted,status) VALUES ('ENC_PHONE_001',1) RETURNING id`).Scan(&uid); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO users(phone_encrypted,status) VALUES ('ENC_PHONE_002',1) RETURNING id`).Scan(&target); err != nil {
		t.Fatalf("create target: %v", err)
	}
	us := strconv.FormatInt(uid, 10)
	ts := strconv.FormatInt(target, 10)
	must := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	must(`INSERT INTO working_memory(user_id,agent_id,key,value_json) VALUES ($1,'a','k','{}'::jsonb)`, uid)
	must(`INSERT INTO episodes(id,user_id,agent_id,timestamp,kind,summary) VALUES ($2,$1,'a',1,'k','sum')`, uid, suf+"-ep")
	must(`INSERT INTO facts(user_id,agent_id,key,value,updated_at) VALUES ($1,'a','k','v',1)`, uid)
	must(`INSERT INTO trust_levels(user_id,target_id,level) VALUES ($1,$2,1)`, uid, target)
	must(`INSERT INTO agent_directory(agent_id,owner_user,display_name) VALUES ($2,$1,'myname')`, us, suf+"-agent")
	must(`INSERT INTO devices(id,user_id) VALUES ($2,$1)`, us, suf+"-dev")
	must(`INSERT INTO account_identities(user_id,provider,open_id) VALUES ($1,'prov','openid')`, uid)
	must(`INSERT INTO paid_message_ledger(message_id,sender_id,receiver_id,billing_mode,amount_cents)
	      VALUES (gen_random_uuid(),$1,$2,1,100)`, uid, target)

	cleanup := func() {
		cl := func(q string, args ...any) {
			var err error
			for attempt := 0; attempt < 3; attempt++ {
				_, err = db.Exec(q, args...)
				if err == nil || (!strings.Contains(err.Error(), "deadlock") && !strings.Contains(err.Error(), "40P01")) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if err != nil {
				t.Errorf("cleanup %q: %v", q, err)
			}
		}
		cl(`DELETE FROM paid_message_ledger WHERE sender_id IN ($1,$2) OR receiver_id IN ($1,$2)`, uid, target)
		cl(`DELETE FROM data_subject_requests WHERE user_id IN ($1,$2)`, uid, target)
		cl(`DELETE FROM trust_levels WHERE user_id IN ($1,$2) OR target_id IN ($1,$2)`, uid, target)
		for _, tb := range []string{"account_identities", "trust_level_events", "agent_capabilities",
			"working_memory", "episodes", "facts", "improvements", "threshold_patches", "approval_tickets", "support_tickets"} {
			col := "user_id"
			if tb == "agent_capabilities" {
				col = "owner_id"
			}
			cl(fmt.Sprintf(`DELETE FROM %s WHERE %s IN ($1,$2)`, tb, col), uid, target)
		}
		for _, tc := range [][2]string{
			{"case_experiences", "user_id"}, {"propose_audit", "user_id"},
			{"offline_approvals", "user_id"}, {"ticket_state", "user_id"},
			{"agent_directory", "owner_user"}, {"devices", "user_id"},
			{"im_relations", "owner_id"}, {"trust_relations", "owner_id"},
			{"im_groups", "owner_id"}, {"im_group_members", "user_id"},
			{"im_read_receipts", "owner_id"}, {"im_push_credentials", "user_id"},
			{"im_push_tasks", "user_id"}, {"blacklist", "user_id"}} {
			cl(fmt.Sprintf(`DELETE FROM %s WHERE %s IN ($1,$2)`, tc[0], tc[1]), us, ts)
		}
		cl(`DELETE FROM users WHERE id IN ($1,$2)`, uid, target)
		db.Close()
	}
	return uid, target, cleanup
}

func cnt(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func TestV186ExportAndErase(t *testing.T) {
	db, suf := testDB(t)
	uid, target, cleanup := setup(t, db, suf)
	defer cleanup()
	_ = target
	ctx := context.Background()
	svc := New(db)

	// ---------- 导出（可携带权） ----------
	data, reqID, err := svc.Export(ctx, uid, "v186 export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if reqID == 0 {
		t.Fatal("export 应返回 DSR 记录 id")
	}
	get := func(cat, table string) map[string]any {
		return data[cat].(map[string]any)[table].(map[string]any)
	}
	if users := get("identity", "users"); users["count"].(int) != 1 {
		t.Errorf("导出 users count 应=1，实际 %v", users["count"])
	} else {
		row := users["rows"].([]map[string]any)[0]
		if int64(row["id"].(float64)) != uid {
			t.Errorf("导出 users id 不匹配: %v", row["id"])
		}
		if row["phone_encrypted"] != "ENC_PHONE_001" {
			t.Errorf("本人导出应含自己（加密的）手机号，实际 %v", row["phone_encrypted"])
		}
	}
	if get("memory", "working_memory")["count"].(int) != 1 {
		t.Error("导出 working_memory 应=1")
	}
	if get("identity", "devices")["count"].(int) != 1 {
		t.Error("导出 devices 应=1")
	}
	if get("agent", "agent_directory")["count"].(int) != 1 {
		t.Error("导出 agent_directory 应=1")
	}
	if get("relationship", "trust_levels")["count"].(int) != 1 {
		t.Error("导出 trust_levels 应=1")
	}
	if get("identity", "account_identities")["count"].(int) != 1 {
		t.Error("导出 account_identities 应=1")
	}
	if get("payment", "paid_message_ledger")["count"].(int) != 1 {
		t.Error("导出付费账本应=1（本人作为发送方）")
	}
	if n := cnt(t, db, `SELECT count(*) FROM data_subject_requests WHERE user_id=$1 AND kind='export' AND status='completed'`, uid); n != 1 {
		t.Errorf("export DSR 应落 1 条，实际 %d", n)
	}
	if reqs, err := svc.List(ctx, uid, 100); err != nil || len(reqs) != 1 {
		t.Errorf("List 应返回 1 条 DSR，实际 %d err=%v", len(reqs), err)
	}

	// ---------- 删除（被遗忘权） ----------
	report, delID, err := svc.Erase(ctx, uid, "v186 erase")
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if delID == 0 {
		t.Fatal("erase 应返回 DSR 记录 id")
	}
	for _, tb := range []string{"working_memory", "episodes", "facts", "agent_directory", "devices", "account_identities", "trust_levels"} {
		if report[tb] != 1 {
			t.Errorf("删除报告 %s 应=1，实际 %d", tb, report[tb])
		}
	}
	if report["users(anonymized)"] != 1 {
		t.Errorf("users 应匿名化 1 行，实际 %d", report["users(anonymized)"])
	}
	if n := cnt(t, db, `SELECT count(*) FROM working_memory WHERE user_id=$1`, uid); n != 0 {
		t.Errorf("working_memory 删除后应=0，实际 %d", n)
	}
	if n := cnt(t, db, `SELECT count(*) FROM agent_directory WHERE owner_user=$1`, strconv.FormatInt(uid, 10)); n != 0 {
		t.Errorf("agent_directory 删除后应=0，实际 %d", n)
	}
	if n := cnt(t, db, `SELECT count(*) FROM trust_levels WHERE user_id=$1 OR target_id=$1`, uid); n != 0 {
		t.Errorf("trust_levels 双向删除后应=0，实际 %d", n)
	}
	var phone sql.NullString
	var status int
	if err := db.QueryRow(`SELECT phone_encrypted,status FROM users WHERE id=$1`, uid).Scan(&phone, &status); err != nil {
		t.Fatalf("匿名化后用户应仍存在: %v", err)
	}
	if phone.Valid {
		t.Errorf("匿名化后 phone_encrypted 应为 NULL，实际 %q", phone.String)
	}
	if status != 2 {
		t.Errorf("匿名化后 status 应为 2（注销），实际 %d", status)
	}
	if n := cnt(t, db, `SELECT count(*) FROM paid_message_ledger WHERE sender_id=$1 OR receiver_id=$1`, uid); n != 1 {
		t.Errorf("付费账本应法定保留=1，实际 %d", n)
	}
	if n := cnt(t, db, `SELECT count(*) FROM data_subject_requests WHERE user_id=$1 AND kind='delete' AND status='completed'`, uid); n != 1 {
		t.Errorf("delete DSR 应落 1 条，实际 %d", n)
	}
}
