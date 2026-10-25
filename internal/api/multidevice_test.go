// v1.5.0-v1.5.3 重做：真实 PostgreSQL 集成测试。
// 需 PG_DSN 环境变量；未设置时 skip。验证真实落库、LWW 冲突裁决、离线重放、超时升级。
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"time"
)

func newDBDeps(t *testing.T) *Deps {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN 未设置，跳过真实 PG 集成测试")
	}
	deps, err := NewDeps("pgx", dsn)
	if err != nil || deps.DB == nil {
		t.Fatalf("连接 PG 失败: %v", err)
	}
	// 池上限 25，而 E2E PG 的 max_connections=30（留 3 个超级用户位）：
	// 不关池会让同包后续测试直接 53300（"已保留的连接位置…超级用户预留"）。
	t.Cleanup(func() { _ = deps.DB.Close() })
	// 保证表存在
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS ticket_state(ticket_id TEXT PRIMARY KEY, user_id TEXT NOT NULL,
		 decision TEXT NOT NULL, version BIGINT NOT NULL DEFAULT 0,
		 via_device TEXT NOT NULL, updated_at BIGINT NOT NULL DEFAULT 0,
		 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`CREATE TABLE IF NOT EXISTS devices(id TEXT PRIMARY KEY, user_id TEXT, type TEXT, last_seen TIMESTAMPTZ)`,
		`CREATE TABLE IF NOT EXISTS im_relations(owner_id TEXT, peer_id TEXT, trust_level INT NOT NULL DEFAULT 3, status TEXT NOT NULL DEFAULT 'friend', source TEXT NOT NULL DEFAULT 'webhook', updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(owner_id,peer_id))`,
		`CREATE TABLE IF NOT EXISTS im_read_receipts(owner_id TEXT, peer_id TEXT, msg_key TEXT, read_at TIMESTAMPTZ NOT NULL DEFAULT now(), source TEXT NOT NULL DEFAULT 'webhook', PRIMARY KEY(owner_id,peer_id,msg_key))`,
		`CREATE TABLE IF NOT EXISTS im_push_credentials(user_id TEXT, platform TEXT, cert_ref TEXT DEFAULT '', device_token TEXT DEFAULT '', updated_at TIMESTAMPTZ DEFAULT now(), PRIMARY KEY(user_id,platform))`,
		`CREATE TABLE IF NOT EXISTS im_messages(msg_key TEXT PRIMARY KEY, from_user TEXT NOT NULL, to_user TEXT NOT NULL, msg_type TEXT NOT NULL DEFAULT 'TIMTextElem', dir TEXT NOT NULL, text TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS im_push_tasks(id BIGSERIAL PRIMARY KEY, user_id TEXT, msg_key TEXT, title TEXT DEFAULT '', body TEXT DEFAULT '', status TEXT DEFAULT 'pending', attempts INT DEFAULT 0, last_error TEXT DEFAULT '', created_at TIMESTAMPTZ DEFAULT now(), updated_at TIMESTAMPTZ DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS offline_approvals(id SERIAL PRIMARY KEY, user_id TEXT,
		 ticket_id TEXT, decision TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), synced BOOLEAN NOT NULL DEFAULT false)`,
	}
	for _, q := range ddl {
		if _, err := deps.DB.Exec(q); err != nil {
			t.Fatalf("建表: %v", err)
		}
	}
	return deps
}

func postJSON(t *testing.T, hf http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/x", bytes.NewReader(b))
	w := httptest.NewRecorder()
	hf(w, req)
	return w
}

func getJSON(t *testing.T, hf http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	hf(w, req)
	return w
}

func unique(prefix string) string {
	return prefix + "-" + time.Now().Format("150405.000000")
}

// v1.5.0 设备注册真实写表
func TestV150DeviceRegister(t *testing.T) {
	d := newDBDeps(t)
	devID := unique("dev")
	t.Cleanup(func() { cleanDevice(t, d.DB, devID) })
	w := postJSON(t, d.RegisterDevice, map[string]string{"deviceId": devID, "userId": "alice", "type": "watch"})
	if w.Code != 200 {
		t.Fatalf("注册 code=%d body=%s", w.Code, w.Body.String())
	}
	var typ, uid string
	err := d.DB.QueryRow(`SELECT user_id, type FROM devices WHERE id=$1`, devID).Scan(&uid, &typ)
	if err != nil {
		t.Fatalf("设备未真实落库: %v", err)
	}
	if uid != "alice" || typ != "watch" {
		t.Fatalf("落库数据错 uid=%s type=%s", uid, typ)
	}
	// 非法 type
	if w2 := postJSON(t, d.RegisterDevice, map[string]string{"deviceId": "x1", "userId": "a", "type": "fridge"}); w2.Code != 400 {
		t.Fatalf("非法设备类型应 400, got %d", w2.Code)
	}
}

// v1.5.1 手表审批真实落库 + 审计 + 升级
func TestV151WatchApprove(t *testing.T) {
	d := newDBDeps(t)
	ticket := unique("tk")

	// approve
	w := postJSON(t, d.WatchApprove, map[string]string{"userId": "alice", "ticketId": ticket})
	if w.Code != 200 {
		t.Fatalf("approve code=%d %s", w.Code, w.Body.String())
	}
	var decision, via string
	var version int64
	if err := d.DB.QueryRow(`SELECT decision, version, via_device FROM ticket_state WHERE ticket_id=$1`, ticket).
		Scan(&decision, &version, &via); err != nil {
		t.Fatalf("approve 未真实落库: %v", err)
	}
	if decision != "approved" || version != 1 || via != "watch" {
		t.Fatalf("落库错 decision=%s version=%d via=%s", decision, version, via)
	}

	// reject 另一张
	ticket2 := unique("tk")
	postJSON(t, d.WatchReject, map[string]string{"userId": "alice", "ticketId": ticket2})
	d.DB.QueryRow(`SELECT decision FROM ticket_state WHERE ticket_id=$1`, ticket2).Scan(&decision)
	if decision != "rejected" {
		t.Fatalf("reject 落库错 decision=%s", decision)
	}

	// 审计真实写入
	var n int
	d.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action=$2 AND detail @> $1::jsonb`,
		`{"ticketId":"`+ticket+`"}`, "ticket_decision").Scan(&n)
	if n == 0 {
		t.Fatalf("手表审批未写审计")
	}

	// 升级：未过 deadline 不升级
	wFuture := postJSON(t, d.WatchEscalate, map[string]any{
		"userId": "alice", "ticketId": unique("pending"), "deadlineMs": time.Now().Add(time.Hour).UnixMilli()})
	if wFuture.Code != 200 || !bytes.Contains(wFuture.Body.Bytes(), []byte(`"escalated":false`)) {
		t.Fatalf("未到 deadline 不应升级: %s", wFuture.Body.String())
	}
	// 升级：已过 deadline 且仍 pending → 升级
	pending := unique("pending")
	wEsc := postJSON(t, d.WatchEscalate, map[string]any{
		"userId": "alice", "ticketId": pending, "deadlineMs": time.Now().Add(-time.Minute).UnixMilli()})
	if wEsc.Code != 200 || !bytes.Contains(wEsc.Body.Bytes(), []byte(`"escalated":true`)) {
		t.Fatalf("过 deadline 应升级: %s", wEsc.Body.String())
	}
	d.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action=$2 AND detail @> $1::jsonb`,
		`{"ticketId":"`+pending+`"}`, "approval_escalated").Scan(&n)
	if n == 0 {
		t.Fatalf("升级未写审计")
	}
	// 已决策的 ticket 不升级
	wDecided := postJSON(t, d.WatchEscalate, map[string]any{
		"userId": "alice", "ticketId": ticket, "deadlineMs": time.Now().Add(-time.Hour).UnixMilli()})
	if !bytes.Contains(wDecided.Body.Bytes(), []byte(`"escalated":false`)) {
		t.Fatalf("已决策不应升级: %s", wDecided.Body.String())
	}
	t.Cleanup(func() {
		cleanTicket(t, d.DB, ticket, ticket2, pending)
		// audit_events append-only：审批审计行保留，靠 ticketId 唯一标识隔离。
	})
}

// v1.5.2 离线提交真实入队 + 联网重放过 LWW
func TestV152OfflineSubmit(t *testing.T) {
	d := newDBDeps(t)
	user, ticket := unique("offline-user"), unique("off-tk")

	// 零残留：跑后删除本测试写入的离线缓存与权威状态
	t.Cleanup(func() {
		if _, err := d.DB.Exec(`DELETE FROM offline_approvals WHERE user_id=$1`, user); err != nil {
			t.Errorf("cleanup offline_approvals: %v", err)
		}
		cleanTicket(t, d.DB, ticket)
	})

	w := postJSON(t, d.OfflineSubmit, map[string]string{"userId": user, "ticketId": ticket, "decision": "approved"})
	if w.Code != 200 {
		t.Fatalf("submit code=%d %s", w.Code, w.Body.String())
	}
	// 真实列表 pending=1
	wl := getJSON(t, d.OfflineList, "/v1/offline/pending?userId="+user)
	if wl.Code != 200 || !bytes.Contains(wl.Body.Bytes(), []byte(`"pending":1`)) {
		t.Fatalf("队列应 pending=1: %s", wl.Body.String())
	}
	// 重放
	ws := postJSON(t, d.OfflineSync, map[string]string{"userId": user})
	if ws.Code != 200 || !bytes.Contains(ws.Body.Bytes(), []byte(`"applied":1`)) {
		t.Fatalf("重放应 applied=1: %s", ws.Body.String())
	}
	// 重放后 pending=0
	wl2 := getJSON(t, d.OfflineList, "/v1/offline/pending?userId="+user)
	if !bytes.Contains(wl2.Body.Bytes(), []byte(`"pending":0`)) {
		t.Fatalf("重放后应 pending=0: %s", wl2.Body.String())
	}
	// 真实落库到 ticket_state
	var decision string
	if err := d.DB.QueryRow(`SELECT decision FROM ticket_state WHERE ticket_id=$1`, ticket).Scan(&decision); err != nil {
		t.Fatalf("离线重放未应用到 ticket_state: %v", err)
	}
	if decision != "approved" {
		t.Fatalf("离线决策应用错 decision=%s", decision)
	}
}

// v1.5.3 多端 LWW 真实冲突裁决
func TestV153SyncState(t *testing.T) {
	d := newDBDeps(t)
	ticket := unique("lww")
	t.Cleanup(func() { cleanTicket(t, d.DB, ticket) })

	// 手机 t=1000 approve
	if w := postJSON(t, d.SyncState, map[string]any{"userId": "alice", "ticketId": ticket, "decision": "approved", "deviceId": "phone", "ts": 1000}); w.Code != 200 {
		t.Fatalf("首次同步 code=%d %s", w.Code, w.Body.String())
	}
	// 手表旧 ts=900 reject → 409 冲突，服务端保持 approved
	wOld := postJSON(t, d.SyncState, map[string]any{"userId": "alice", "ticketId": ticket, "decision": "rejected", "deviceId": "watch", "ts": 900})
	if wOld.Code != 409 {
		t.Fatalf("旧时间戳应 409, got %d %s", wOld.Code, wOld.Body.String())
	}
	var decision string
	d.DB.QueryRow(`SELECT decision FROM ticket_state WHERE ticket_id=$1`, ticket).Scan(&decision)
	if decision != "approved" {
		t.Fatalf("旧写入不应覆盖，decision=%s", decision)
	}
	// PC 新 ts=2000 reject → 覆盖
	wNew := postJSON(t, d.SyncState, map[string]any{"userId": "alice", "ticketId": ticket, "decision": "rejected", "deviceId": "pc", "ts": 2000})
	if wNew.Code != 200 {
		t.Fatalf("新时间戳应接受, got %d", wNew.Code)
	}
	var version int64
	var via string
	d.DB.QueryRow(`SELECT decision, version, via_device FROM ticket_state WHERE ticket_id=$1`, ticket).Scan(&decision, &version, &via)
	if decision != "rejected" || version != 2 || via != "pc" {
		t.Fatalf("新写入覆盖错 decision=%s version=%d via=%s", decision, version, via)
	}
	// 幂等：同参数重复提交
	wIdem := postJSON(t, d.SyncState, map[string]any{"userId": "alice", "ticketId": ticket, "decision": "rejected", "deviceId": "pc", "ts": 2000})
	if wIdem.Code != 200 || !bytes.Contains(wIdem.Body.Bytes(), []byte(`"idempotent":true`)) {
		t.Fatalf("重复提交应幂等: %s", wIdem.Body.String())
	}
	// 拉取权威状态
	wGet := getJSON(t, d.GetState, "/v1/sync/state/"+ticket)
	if wGet.Code != 200 || !bytes.Contains(wGet.Body.Bytes(), []byte(`"found":true`)) {
		t.Fatalf("GetState 应返回权威状态: %s", wGet.Body.String())
	}
}
