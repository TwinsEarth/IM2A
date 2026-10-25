// SPDX-License-Identifier: MIT
//
// C1 身份绑定判定性 E2E（task-21 验收 #1/#2/#3）：
//   - 伪造 `?user_id=999` + 无 token        → 握手失败（网关 4003 / CONN_OPEN_REJECTED）
//   - 伪造 token（错误签名）                 → 拒绝
//   - 合法 JWT（sub=1001）+ 伪造 query=999  → auth_ok.UserID == 1001（打印三者对比）
//   - im_device_cursor 落在 JWT.sub 上；后续 Publish(1001) 客户端真实收到帧
//
// 需要 openclaw-arch 的网关侧 C1 改动（删 query 身份 / hello token→ConnOpen.Token /
// 以 ConnOpenAck.UserID 为准）落地后才可通过；默认 skip。
//
// 复跑：
//
//	source /root/im2a-work/e2e-env.sh
//	cd /root/im2a-work/build
//	IM2A_E2E=1 PG_DSN="$PG_DSN" go test ./internal/connapi -run TestC1IdentityBinding -v -count=1 -timeout 300s -p 1
package connapi

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/connapi/delivery"
	"human-agent-im/internal/connproto"
)

const c1E2EJWTSecret = "c1-e2e-jwt-secret"

// c1Dial 连网关（query 带伪造 user_id，用于验证网关不再信任它）。
func c1Dial(t *testing.T, addr string, deviceID string, forgedUserID int64) *websocket.Conn {
	t.Helper()
	url := fmt.Sprintf("ws://%s/v1/ws?user_id=%d&device_id=%s", addr, forgedUserID, deviceID)
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("ws dial %s: %v", url, err)
	}
	return c
}

// c1Hello 发 hello 控制帧（auth_token 走 ControlFrame.Token，字段名与网关侧对齐后可能需要调整）。
func c1Hello(t *testing.T, c *websocket.Conn, token string) {
	t.Helper()
	b, err := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlHello, V: 1, ProtoMin: "1.0", ProtoMax: "1.0", Token: token,
	})
	if err != nil {
		t.Fatalf("encode hello: %v", err)
	}
	if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write hello: %v", err)
	}
}

// c1AwaitAuth 读到 auth_ok 或失败（err 控制帧/连接关闭）。
func c1AwaitAuth(t *testing.T, c *websocket.Conn, timeout time.Duration) (auth *connproto.ControlFrame, failure string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(time.Now().Add(timeout))
		mt, data, err := c.ReadMessage()
		if err != nil {
			return nil, "connection closed: " + err.Error()
		}
		if mt != websocket.TextMessage {
			continue // 握手阶段只关心控制帧
		}
		cf, derr := connproto.DecodeControl(data)
		if derr != nil {
			continue
		}
		switch cf.T {
		case connproto.CtlAuthOK:
			return cf, ""
		case connproto.CtlErr:
			return nil, fmt.Sprintf("err code=%s message=%s", cf.Code, cf.Message)
		}
	}
	return nil, "timeout waiting auth_ok/err"
}

func TestC1IdentityBinding(t *testing.T) {
	e2eGate(t)
	root := e2eRepoRoot(t)
	bin := buildE2EGateway(t, root)
	uds := filepath.Join(t.TempDir(), "im2a-conn.sock")
	gw := startE2EGateway(t, bin, uds)
	defer gw.stop()

	db := openE2EDB(t)
	pgStore := delivery.NewPGStore(db, delivery.Options{Now: time.Now})
	quiet := log.New(io.Discard, "", 0)
	bridge := NewBridge(pgStore, quiet)
	bridge.SetJWTSecret(c1E2EJWTSecret) // C1：内核启用 JWT 权威身份校验
	remote := NewRemote(bridge.Wire(RemoteConfig{
		UDSPath: uds, Instance: "c1-e2e", Logger: quiet,
		ReconnectMin: 20 * time.Millisecond, ReconnectMax: 100 * time.Millisecond,
	}))
	bridge.SetSink(remote)
	defer func() { _ = remote.Close(context.Background()) }()
	e2eWaitConnected(t, remote, 10*time.Second)

	const userID int64 = 1001
	const forgedQuery int64 = 999
	deviceID := "c1-e2e-dev"

	// A) 伪造 query + 无 token：必须拒绝（网关以内核 ack 为准）。
	cA := c1Dial(t, gw.addr, deviceID+"-forged", forgedQuery)
	defer cA.Close()
	c1Hello(t, cA, "")
	if got, fail := c1AwaitAuth(t, cA, 5*time.Second); got != nil {
		t.Fatalf("无 token 但握手成功（伪造 query 被信任）: auth_ok=%+v", got)
	} else {
		t.Logf("NEGATIVE no-token: rejected: %s", fail)
	}

	// B) 伪造 token（错误 secret）：必须拒绝。
	badToken, err := auth.Issue("wrong-secret", userID, time.Hour, "im2a-e2e")
	if err != nil {
		t.Fatalf("issue bad jwt: %v", err)
	}
	cB := c1Dial(t, gw.addr, deviceID+"-bad", forgedQuery)
	defer cB.Close()
	c1Hello(t, cB, badToken)
	if got, fail := c1AwaitAuth(t, cB, 5*time.Second); got != nil {
		t.Fatalf("伪造 token 但握手成功: auth_ok=%+v", got)
	} else {
		t.Logf("NEGATIVE bad-token: rejected: %s", fail)
	}

	// C) 合法 JWT（sub=1001）+ 伪造 query=999：auth_ok.UserID 必须是 1001。
	token, err := auth.Issue(c1E2EJWTSecret, userID, time.Hour, "im2a-e2e")
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}
	cC := c1Dial(t, gw.addr, deviceID, forgedQuery)
	defer cC.Close()
	c1Hello(t, cC, token)
	authOK, fail := c1AwaitAuth(t, cC, 5*time.Second)
	if authOK == nil {
		t.Fatalf("合法 JWT 握手失败: %s", fail)
	}
	t.Logf("IDENTITY_COMPARE: jwt.sub=%d forged_query=%d auth_ok.UserID=%d", userID, forgedQuery, authOK.UserID)
	if authOK.UserID != userID {
		t.Fatalf("auth_ok.UserID=%d, want JWT.sub=%d（不得使用 query=%d）", authOK.UserID, userID, forgedQuery)
	}

	// D) 位点必须落在权威主体上。
	cur, ok, err := pgStore.Cursor(context.Background(), deviceID)
	if err != nil || !ok {
		t.Fatalf("cursor: ok=%v err=%v", ok, err)
	}
	if cur.UserID != userID {
		t.Fatalf("im_device_cursor.user_id=%d, want JWT.sub=%d", cur.UserID, userID)
	}

	// E) 投递键 = 权威 userID：Publish(1001) 客户端应收到帧。
	if _, err := bridge.Publish(context.Background(), userID, []PublishFrame{{
		Payload: []byte(`{"type":"c1_identity_ok"}`), Critical: true,
	}}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	gotFrame := false
	for time.Now().Before(deadline) {
		_ = cC.SetReadDeadline(time.Now().Add(2 * time.Second))
		mt, data, err := cC.ReadMessage()
		if err != nil {
			t.Fatalf("读投递帧: %v", err)
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		f, _, derr := connproto.Decode(data)
		if derr != nil {
			continue
		}
		if len(f.Payload) > 0 && string(f.Payload) == `{"type":"c1_identity_ok"}` {
			gotFrame = true
			t.Logf("DELIVERY_OK: seq=%d payload=%s", f.Seq, f.Payload)
			break
		}
	}
	if !gotFrame {
		t.Fatal("Publish(权威 userID) 后客户端未收到帧")
	}
	m := remote.Metrics()
	t.Logf("C1_IDENTITY_E2E PASS: connOpens=%d state=%s | jwt.sub=%d forged_query=%d ack.UserID=%d cursor.user_id=%d delivery=ok",
		m.ConnOpens, m.State, userID, forgedQuery, authOK.UserID, cur.UserID)
}
