// SPDX-License-Identifier: MIT
//
// C5b（task-29）判定性 E2E：灰度三态 + 回滚演练四步。
//
//  1. TestC5bRollbackDrillE2E：
//     Phase1 NATIVE_ENABLED=on  → 产生数据（会话/消息/outbox）→ 记录 id/seq/计数；
//     Phase2 NATIVE_ENABLED=off → ① /healthz、/v1/plugins 200；② /v1/native/* = 404 NATIVE_DISABLED；
//     ③ 人类链路 /v1/msg/seq 仍 200；④ DB 数据仍在（未删/未迁移）；
//     Phase3 重新 on            → 会话/消息/读位点/outbox 完好，biz_seq 不变。
//  2. TestC5bRolloutThreeStatesE2E：
//     off → 404 NATIVE_DISABLED；on 但不在名单 → 421 NATIVE_NOT_ROLLED_OUT（可观测计数）；
//     在名单 → 200 正常；同样验证人类链路不受限。
//
// 默认 skip：IM2A_E2E=1 + PG_DSN。复跑：
//
//	source /root/im2a-work/e2e-env.sh
//	IM2A_E2E=1 PG_DSN="$PG_DSN" go test -p 1 ./internal/kernel/bootstrap -run TestC5b -v -count=1
package bootstrap_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/kernel/routes"
)

// c5bEnv 每个 Phase 用完整环境重装（避免前一 Phase 的 env 泄漏）。
func c5bEnv(t *testing.T, dsn string, nativeOn bool, rollout string) {
	t.Helper()
	on := ""
	if nativeOn {
		on = "on"
	}
	b2cEnv(t, map[string]string{
		"OPENCLAW_ENABLED": "off", "DSBOT_ENABLED": "off", "PG_DSN": dsn,
		"CONN_MODE": "", "CONN_UDS_PATH": "", "GATEWAY_TOKEN": "",
		"IM2A_ADMIN_TOKEN": "c5b-admin", "IM2A_JWT_SECRET": "c5b-jwt-secret",
		"NATIVE_ENABLED": on, "NATIVE_ROLLOUT": rollout,
	})
}

// c5bAssemble 按当前 env 装配一个 kernel + httptest mux。
func c5bAssemble(t *testing.T) (bootstrap.Kernel, *httptest.Server, *http.ServeMux) {
	t.Helper()
	k, err := bootstrap.Assemble(bootstrap.Load(0))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	mux := http.NewServeMux()
	routes.Register(mux, k)
	srv := httptest.NewServer(mux)
	return *k, srv, mux
}

// c5bToken 签发测试 JWT。
func c5bToken(t *testing.T, uid int64) string {
	t.Helper()
	tok, err := auth.Issue("c5b-jwt-secret", uid, time.Hour, "c5b-e2e")
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}
	return tok
}

// c5bCleanup 删除测试会话/消息/outbox/principal。
func c5bCleanup(t *testing.T, db *sql.DB, convID string, users []int64) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM n_outbox WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_messages WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_read_cursor WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_conversation_members WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_conversations WHERE id=$1`, convID)
		for _, uid := range users {
			_, _ = db.Exec(`DELETE FROM n_principal WHERE kind='human' AND user_id=$1`, uid)
		}
	})
}

func c5bCount(t *testing.T, db *sql.DB, convID string) (msgs, outbox int) {
	t.Helper()
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_messages WHERE conversation_id=$1`, convID).Scan(&msgs); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_outbox WHERE conversation_id=$1`, convID).Scan(&outbox); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return msgs, outbox
}

func TestC5bRollbackDrillE2E(t *testing.T) {
	dsn := b2cGate(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()

	base := time.Now().UnixNano()%100_000_000*10 + 9_000_000
	owner, peer := base+1, base+2
	users := []int64{owner, peer}
	tokenOwner := c5bToken(t, owner)
	tokenPeer := c5bToken(t, peer)

	// ===== Phase 1：开启 native，产生数据 =====
	c5bEnv(t, dsn, true, "")
	k1, srv1, _ := c5bAssemble(t)
	if k1.NativeConv == nil || k1.NativeMsg == nil {
		t.Fatalf("Phase1 装配不完整: conv=%v msg=%v", k1.NativeConv != nil, k1.NativeMsg != nil)
	}
	status, body := c2Do(t, http.MethodPost, srv1.URL+"/v1/native/conversations", tokenOwner, map[string]any{
		"kind": "DIRECT", "title": "c5b-rollback", "member_user_ids": []int64{peer},
	})
	if status != http.StatusCreated {
		t.Fatalf("Phase1 create conv status=%d body=%v", status, body)
	}
	convID := body["conversation"].(map[string]any)["id"].(string)
	c5bCleanup(t, db, convID, users)
	status, body = c2Do(t, http.MethodPost, srv1.URL+"/v1/native/conversations/"+convID+"/messages", tokenOwner, map[string]any{
		"client_msg_id": "c5b-msg-1", "body": "before rollback",
	})
	if status != http.StatusCreated {
		t.Fatalf("Phase1 send status=%d body=%v", status, body)
	}
	msgBefore := body["message"].(map[string]any)
	msgID, _ := msgBefore["id"].(string)
	bizSeq := int64(msgBefore["biz_seq"].(float64))
	createdAt, _ := msgBefore["created_at"].(string) // 用于 Phase3 证明"同一行"而非重新插入
	status, body = c2Do(t, http.MethodPost, srv1.URL+"/v1/native/conversations/"+convID+"/read", tokenPeer, map[string]any{"biz_seq": bizSeq})
	if status != http.StatusOK {
		t.Fatalf("Phase1 mark read status=%d body=%v", status, body)
	}
	msgsBefore, outboxBefore := c5bCount(t, db, convID)
	if msgsBefore != 1 || outboxBefore != 1 {
		t.Fatalf("Phase1 数据计数 messages=%d outbox=%d, want 1/1", msgsBefore, outboxBefore)
	}
	_ = k1.Close(context.Background())
	srv1.Close()
	t.Logf("C5B_PHASE1_OK: conv=%s msg=%s biz_seq=%d created_at=%s messages=1 outbox=1", convID, msgID, bizSeq, createdAt)

	// ===== Phase 2：关闭 NATIVE_ENABLED（回滚）=====
	c5bEnv(t, dsn, false, "")
	k2, srv2, _ := c5bAssemble(t)
	defer func() { _ = k2.Close(context.Background()) }()
	defer srv2.Close()
	if k2.NativeConv != nil || k2.NativeMsg != nil {
		t.Fatalf("Phase2 关闭后 native 服务不应装配")
	}
	// 断言①：网关仍可用。
	// /healthz 受 admin token 保护（生产语义），用回滚同环境的管理 token 断言 200；
	// /v1/plugins 为公开端点（无 token 也应 200）。
	if status, _ := c2Do(t, http.MethodGet, srv2.URL+"/healthz", "c5b-admin", nil); status != http.StatusOK {
		t.Fatalf("Phase2 /healthz status=%d, want 200（回滚后网关必须仍可用）", status)
	}
	if status, _ := c2Do(t, http.MethodGet, srv2.URL+"/v1/plugins", "", nil); status != http.StatusOK {
		t.Fatalf("Phase2 /v1/plugins status=%d, want 200（回滚后插件端点必须仍可用）", status)
	}
	// 断言②：/v1/native/* → 404 + NATIVE_DISABLED（不是 500，不是裸 404 文本）。
	status, body = c2Do(t, http.MethodGet, srv2.URL+"/v1/native/conversations", tokenOwner, nil)
	if status != http.StatusNotFound {
		t.Fatalf("Phase2 native status=%d, want 404", status)
	}
	if code := c2ErrCode(body); code != "NATIVE_DISABLED" {
		t.Fatalf("Phase2 native code=%q, want NATIVE_DISABLED (body=%v)", code, body)
	}
	beforeHits := routes.NativeDisabledHits()
	_, body = c2Do(t, http.MethodGet, srv2.URL+"/v1/native/presence/self", tokenOwner, nil)
	if routes.NativeDisabledHits() <= beforeHits {
		t.Fatalf("Phase2 NATIVE_DISABLED 命中计数未增长")
	}
	// 断言③：人类链路不受影响（D8=B 边界）。
	if status, _ := c2Do(t, http.MethodPost, srv2.URL+"/v1/msg/seq", tokenOwner, map[string]any{"userId": owner}); status != http.StatusOK {
		t.Fatalf("Phase2 人类链路 /v1/msg/seq status=%d, want 200", status)
	}
	// D8=B 边界：人类 IM 走腾讯云 PaaS；回滚期间即便无 PaaS 凭据也必须"优雅降级"（不能 500）。
	webhookStatus, _ := c2Do(t, http.MethodPost, srv2.URL+"/v1/im/webhook", "", map[string]any{})
	if webhookStatus == http.StatusInternalServerError {
		t.Fatalf("Phase2 人类 PaaS webhook 回滚后 500（应优雅降级）")
	}
	t.Logf("C5B_PHASE2_HUMAN_CHAIN: /v1/msg/seq=200, /v1/im/webhook=%d（非 500，PaaS 降级路径正常）", webhookStatus)
	// 断言④a：关闭期间数据仍在。
	msgsOff, outboxOff := c5bCount(t, db, convID)
	if msgsOff != msgsBefore || outboxOff != outboxBefore {
		t.Fatalf("Phase2 数据丢失: messages %d→%d outbox %d→%d", msgsBefore, msgsOff, outboxBefore, outboxOff)
	}
	t.Logf("C5B_PHASE2_OK: healthz/plugins=200, native=404 NATIVE_DISABLED (hits+1), msg/seq=200, DB 数据完好")

	// ===== Phase 3：重新开启，数据完好且 biz_seq 不变 =====
	c5bEnv(t, dsn, true, "")
	k3, srv3, _ := c5bAssemble(t)
	defer func() { _ = k3.Close(context.Background()) }()
	defer srv3.Close()
	status, body = c2Do(t, http.MethodGet, srv3.URL+"/v1/native/conversations/"+convID, tokenPeer, nil)
	if status != http.StatusOK {
		t.Fatalf("Phase3 重开后读会话 status=%d body=%v", status, body)
	}
	if id := body["conversation"].(map[string]any)["id"]; id != convID {
		t.Fatalf("Phase3 会话 id=%v, want %s", id, convID)
	}
	status, body = c2Do(t, http.MethodGet, srv3.URL+"/v1/native/conversations/"+convID+"/messages", tokenPeer, nil)
	if status != http.StatusOK {
		t.Fatalf("Phase3 读消息 status=%d body=%v", status, body)
	}
	list := body["messages"].([]any)
	if len(list) != 1 {
		t.Fatalf("Phase3 消息数=%d, want 1", len(list))
	}
	first := list[0].(map[string]any)
	if first["id"] != msgID || int64(first["biz_seq"].(float64)) != bizSeq {
		t.Fatalf("Phase3 消息不完整: id=%v biz_seq=%v, want %s/%d", first["id"], first["biz_seq"], msgID, bizSeq)
	}
	// "同一行"证明：created_at 必须与 Phase1 完全一致（若是重新插入的新行，时间戳会变）。
	if got, _ := first["created_at"].(string); got != createdAt {
		t.Fatalf("Phase3 消息不是同一行: created_at=%q, want %q（重开后数据被重建=失败）", got, createdAt)
	}
	// 数据行数仍为 1（不能靠"重新插入"伪装完好）。
	if m, o := c5bCount(t, db, convID); m != 1 || o != 1 {
		t.Fatalf("Phase3 数据行数 messages=%d outbox=%d, want 1/1（不可重建）", m, o)
	}
	// 读位点也应保持。
	status, body = c2Do(t, http.MethodGet, srv3.URL+"/v1/native/conversations/"+convID+"/read", tokenPeer, nil)
	if status != http.StatusOK || int64(body["read_biz_seq"].(float64)) != bizSeq {
		t.Fatalf("Phase3 读位点丢失: status=%d body=%v, want read_biz_seq=%d", status, body, bizSeq)
	}
	t.Logf("C5B_PHASE3_OK: 会话/消息/biz_seq/读位点完好（msg=%s biz_seq=%d）", msgID, bizSeq)
	t.Logf("C5B_ROLLBACK_DRILL PASS: 开启→产生数据→关闭→网关可用/native 可解释拒绝/人类链路正常/数据完好→重开恢复")
}

func TestC5bRolloutThreeStatesE2E(t *testing.T) {
	dsn := b2cGate(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()

	base := time.Now().UnixNano()%100_000_000*10 + 9_500_000
	owner, outsider := base+1, base+2
	tokenOwner, tokenOutsider := c5bToken(t, owner), c5bToken(t, outsider)

	// 状态1：关闭 → NATIVE_DISABLED（404）。
	c5bEnv(t, dsn, false, "")
	k1, srv1, _ := c5bAssemble(t)
	status, body := c2Do(t, http.MethodGet, srv1.URL+"/v1/native/conversations", tokenOwner, nil)
	if status != http.StatusNotFound || c2ErrCode(body) != "NATIVE_DISABLED" {
		t.Fatalf("off 状态: status=%d code=%q, want 404 NATIVE_DISABLED", status, c2ErrCode(body))
	}
	_ = k1.Close(context.Background())
	srv1.Close()

	// 状态2：开启但不在名单 → 421 NATIVE_NOT_ROLLED_OUT（可观测计数）。
	c5bEnv(t, dsn, true, "user:"+int64Str(owner))
	before := routes.NativeRolloutDeniedHits()
	k2, srv2, _ := c5bAssemble(t)
	defer func() { _ = k2.Close(context.Background()) }()
	defer srv2.Close()
	status, body = c2Do(t, http.MethodGet, srv2.URL+"/v1/native/conversations", tokenOutsider, nil)
	if status != http.StatusMisdirectedRequest || c2ErrCode(body) != "NATIVE_NOT_ROLLED_OUT" {
		t.Fatalf("不在名单: status=%d code=%q, want 421 NATIVE_NOT_ROLLED_OUT (body=%v)", status, c2ErrCode(body), body)
	}
	if routes.NativeRolloutDeniedHits() <= before {
		t.Fatal("灰度拒绝计数未增长（必须可观测）")
	}
	// 单调只增：再拒绝一次必须恰好 +1（counter 是原子累加，不允许被名单抖动/重建重置）。
	afterFirst := routes.NativeRolloutDeniedHits()
	status, body = c2Do(t, http.MethodGet, srv2.URL+"/v1/native/conversations", tokenOutsider, nil)
	if status != http.StatusMisdirectedRequest || c2ErrCode(body) != "NATIVE_NOT_ROLLED_OUT" {
		t.Fatalf("第二次不在名单: status=%d code=%q", status, c2ErrCode(body))
	}
	if got := routes.NativeRolloutDeniedHits(); got != afterFirst+1 {
		t.Fatalf("灰度拒绝计数不单调: %d → %d, want %d（只增，不得重置）", afterFirst, got, afterFirst+1)
	}
	// 人类链路不受灰度影响。
	if status, _ := c2Do(t, http.MethodPost, srv2.URL+"/v1/msg/seq", tokenOutsider, map[string]any{"userId": outsider}); status != http.StatusOK {
		t.Fatalf("不在名单时人类链路 status=%d, want 200", status)
	}

	// 状态3：在名单 → 正常 200。
	status, body = c2Do(t, http.MethodGet, srv2.URL+"/v1/native/conversations", tokenOwner, nil)
	if status != http.StatusOK {
		t.Fatalf("在名单: status=%d body=%v, want 200", status, body)
	}
	t.Logf("C5B_ROLLOUT_3STATES PASS: off=404 NATIVE_DISABLED / 不在名单=421 NATIVE_NOT_ROLLED_OUT(计数+1) / 在名单=200；人类链路均 200")
}

// int64Str 避免依赖 fmt（本文件其他用例用 map 字面量拼接）。
func int64Str(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
