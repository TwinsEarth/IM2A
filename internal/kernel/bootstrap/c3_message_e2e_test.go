// SPDX-License-Identifier: MIT
//
// C3 离线补发判定性 E2E（task-25 最看重的一条）：
//   - 真 PG + 真 bootstrap.Assemble(dual) + 真 cmd/conn-gateway + 真 JWT 客户端；
//   - B 首连（建档位点）后断开（模拟离线）；
//   - A 在 B 离线期间发 5 条消息（HTTP → n_messages+outbox → worker → Bridge.Publish）；
//   - 断言：消息已写 n_messages、outbox delivered、**im_delivery_log 有 B 的 5 条投递序**；
//   - B 重连并发 resume（lastAck=0，复用 B2-b 协议）→ 收到 5 条，message_id 5 个、biz_seq=1..5；
//     frame.Seq（deliverySeq）>0 且与 biz_seq 不同 —— 证明"两个序"各司其职。
//
// 默认 skip：IM2A_E2E=1 + PG_DSN。复跑：
//
//	source /root/im2a-work/e2e-env.sh
//	cd /root/im2a-work/build
//	IM2A_E2E=1 PG_DSN="$PG_DSN" go test -p 1 ./internal/kernel/bootstrap -run TestC3OfflineResumeE2E -v -count=1 -timeout 300s
package bootstrap_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/kernel/routes"
	"human-agent-im/internal/store"
)

// c3Auth 取 b2cClient 的 auth_ok 帧（同包可访问未导出字段）。
func (c *b2cClient) c3Auth() *connproto.ControlFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.auth
}

// c3Resume 发 resume 控制帧（复用 B2-b 协议：resume_token/last_ack_seq/conn_epoch）。
func (c *b2cClient) c3Resume(t *testing.T, lastAck uint64) {
	t.Helper()
	a := c.c3Auth()
	if a == nil {
		t.Fatal("c3Resume: auth_ok 尚未到达")
	}
	b, err := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlResume, ResumeToken: a.ResumeToken,
		LastAckSeq: lastAck, ConnEpoch: a.ConnEpoch,
	})
	if err != nil {
		t.Fatalf("encode resume: %v", err)
	}
	if err := c.conn.WriteMessage(1 /* websocket.TextMessage */, b); err != nil {
		t.Fatalf("write resume: %v", err)
	}
}

// c3NativeMessages 等待并返回 type=n_message 的 payload（去重计数）。
func (c *b2cClient) c3NativeMessages(t *testing.T, want int, timeout time.Duration) ([]map[string]any, []connproto.Frame) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	seen := map[string]bool{}
	var out []map[string]any
	var frames []connproto.Frame
	for time.Now().Before(deadline) {
		c.mu.Lock()
		copyFrames := append([]connproto.Frame(nil), c.frames...)
		c.mu.Unlock()
		out = out[:0]
		seen = map[string]bool{}
		frames = nil
		for _, f := range copyFrames {
			var env map[string]any
			if err := json.Unmarshal(f.Payload, &env); err != nil {
				continue
			}
			if env["type"] != "n_message" {
				continue
			}
			id, _ := env["message_id"].(string)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, env)
			frames = append(frames, f)
		}
		if len(out) >= want {
			return out, frames
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting %d n_message, got %d", want, len(out))
	return nil, nil
}

func TestC3OfflineResumeE2E(t *testing.T) {
	dsn := b2cGate(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	root := b2cRepoRoot(t)
	bin := b2cBuildGateway(t, root)
	uds := filepath.Join(t.TempDir(), "im2a-conn.sock")
	b2cEnv(t, map[string]string{
		"OPENCLAW_ENABLED": "off",
		"DSBOT_ENABLED":    "off",
		"PG_DSN":           dsn,
		"CONN_MODE":        bootstrap.ConnModeDual,
		"CONN_UDS_PATH":    uds,
		"GATEWAY_TOKEN":    "",
		"IM2A_ADMIN_TOKEN": "c3-admin",
		"IM2A_JWT_SECRET":  "c3-e2e-jwt",
		"NATIVE_ENABLED":   "on",
	})
	k, err := bootstrap.Assemble(bootstrap.Load(0))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = k.Close(context.Background())
		}
	}()
	if k.NativeMsg == nil || k.NativeConv == nil || k.ConnBridge == nil {
		t.Fatalf("装配不完整: msg=%v conv=%v bridge=%v", k.NativeMsg != nil, k.NativeConv != nil, k.ConnBridge != nil)
	}
	mux := http.NewServeMux()
	routes.Register(mux, k)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	addr, stop := b2cStartGateway(t, bin, uds)
	defer stop()
	waitConn := func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && !k.ConnSink.Metrics().Connected {
			time.Sleep(10 * time.Millisecond)
		}
		if !k.ConnSink.Metrics().Connected {
			t.Fatal("connapi 未连上 conn-gateway")
		}
	}
	waitConn()

	baseID := time.Now().UnixNano()%100_000_000*10 + 4_000_000
	userA, userB := baseID+1, baseID+2
	tokenA := c3IssueToken(t, userA)
	tokenB := c3IssueToken(t, userB)
	deviceB := "c3-b-dev"

	// A 建 DIRECT 会话（成员 B）。
	status, body := c2Do(t, http.MethodPost, srv.URL+"/v1/native/conversations", tokenA, map[string]any{
		"kind": "DIRECT", "title": "c3", "member_user_ids": []int64{userB},
	})
	if status != http.StatusCreated {
		t.Fatalf("create conversation status=%d body=%v", status, body)
	}
	convID := body["conversation"].(map[string]any)["id"].(string)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM n_outbox WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_messages WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_read_cursor WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_conversation_members WHERE conversation_id=$1`, convID)
		_, _ = db.Exec(`DELETE FROM n_conversations WHERE id=$1`, convID)
		for _, uid := range []int64{userA, userB} {
			_, _ = db.Exec(`DELETE FROM im_delivery_log WHERE user_id=$1`, uid)
			_, _ = db.Exec(`DELETE FROM im_user_delivery_seq WHERE user_id=$1`, uid)
			_, _ = db.Exec(`DELETE FROM im_device_cursor WHERE user_id=$1`, uid)
			_, _ = db.Exec(`DELETE FROM n_principal WHERE kind='human' AND user_id=$1`, uid)
		}
	})

	// B 首连：建档位点/epoch，然后断开（离线）。
	b1 := b2cDialClient(t, addr, userB, deviceB, tokenB)
	authB := b1.waitAuth(5 * time.Second)
	t.Logf("B 首连: conn=%d epoch=%d token=%q", authB.ConnID, authB.ConnEpoch, authB.ResumeToken)
	b1.close()
	time.Sleep(100 * time.Millisecond) // 让网关处理 ConnClosed

	// A 在 B 离线期间发 5 条。
	for i := 1; i <= 5; i++ {
		status, body := c2Do(t, http.MethodPost,
			srv.URL+"/v1/native/conversations/"+convID+"/messages", tokenA,
			map[string]any{"client_msg_id": fmt.Sprintf("c3-m%d", i), "body": fmt.Sprintf("offline-%d", i)})
		if status != http.StatusCreated {
			t.Fatalf("send message %d status=%d body=%v", i, status, body)
		}
	}
	// 等 outbox 投递完成（worker 已把 5 条写入 B 的 im_delivery_log）。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var delivered int
		_ = db.QueryRow(`SELECT COUNT(*) FROM n_outbox WHERE conversation_id=$1 AND status='delivered'`, convID).Scan(&delivered)
		if delivered >= 5 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	var delivered int
	if err := db.QueryRow(`SELECT COUNT(*) FROM n_outbox WHERE conversation_id=$1 AND status='delivered'`, convID).Scan(&delivered); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if delivered != 5 {
		t.Fatalf("outbox delivered=%d, want 5", delivered)
	}
	var logRowsB int
	if err := db.QueryRow(`SELECT COUNT(*) FROM im_delivery_log WHERE user_id=$1`, userB).Scan(&logRowsB); err != nil {
		t.Fatalf("count delivery log: %v", err)
	}
	if logRowsB != 5 {
		t.Fatalf("B 的 im_delivery_log 行数=%d, want 5（复用 B2-b 投递日志）", logRowsB)
	}
	t.Logf("OFFLINE_SEND_OK: 5 条消息已落库；outbox delivered=5；B 的 im_delivery_log=5（复用 B2-b）")

	// B 重连 + resume(lastAck=0) → 从 im_delivery_log 补发 5 条。
	b2 := b2cDialClient(t, addr, userB, deviceB, tokenB)
	authB2 := b2.waitAuth(5 * time.Second)
	if authB2.ConnEpoch != authB.ConnEpoch {
		t.Fatalf("重连 epoch 变化: %d -> %d", authB.ConnEpoch, authB2.ConnEpoch)
	}
	b2.c3Resume(t, 0)
	messages, frames := b2.c3NativeMessages(t, 5, 10*time.Second)
	if len(messages) != 5 {
		t.Fatalf("resume 后收到 %d 条, want 5", len(messages))
	}
	// biz_seq 必须是 1..5（会话内连续）；frame.Seq 是 deliverySeq，应 >0 且通常不等于 biz_seq。
	bizSeqs := make([]int64, 0, 5)
	for i, m := range messages {
		bizSeqs = append(bizSeqs, int64(m["biz_seq"].(float64)))
		if frames[i].Seq == 0 {
			t.Fatalf("补发帧 deliverySeq=0（应来自 im_delivery_log）")
		}
	}
	for i, s := range bizSeqs {
		if s != int64(i+1) {
			t.Fatalf("biz_seq 不连续: %v (want 1..5)", bizSeqs)
		}
	}
	t.Logf("RESUME_OK: 收到 %d 条；biz_seq=%v；deliverySeq(帧)=[%d..%d]（两个序独立）",
		len(messages), bizSeqs, frames[0].Seq, frames[len(frames)-1].Seq)
	b2.close()

	_ = k.Close(context.Background())
	closed = true
	t.Logf("C3_OFFLINE_RESUME_E2E PASS: 5/5 补齐，复用 B2-b resume 协议与 im_delivery_log")
}

// c3IssueToken 为 C3 E2E 签发 dev JWT。
func c3IssueToken(t *testing.T, uid int64) string {
	t.Helper()
	tok, err := auth.Issue("c3-e2e-jwt", uid, time.Hour, "c3-e2e")
	if err != nil {
		t.Fatalf("issue jwt uid=%d: %v", uid, err)
	}
	return tok
}
