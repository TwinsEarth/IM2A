// SPDX-License-Identifier: MIT
//
// C1 内核侧身份绑定判定性单测（不依赖真实网关，pipe/内存即可）：
//   - 合法 JWT → ConnOpenAck.UserID = JWT.sub（**忽略**报文/query 里的 UserID）
//   - 无 token / 伪造 / 过期 / sub<=0 → OK=false + 明确 Reason
//   - SetJWTSecret 未启用时保持 B2 兼容（信任网关给的 UserID）
//   - OnConnClosed / OnPresenceLease / DeliverEphemeral / OnAck→presence 续租 的接线
package connapi

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/connapi/delivery"
	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

const testJWTSecret = "c1-test-secret"

func newAuthBridge(t *testing.T, secret string) (*Bridge, *delivery.MemStore, *captureSink) {
	t.Helper()
	store := delivery.NewMemStore(delivery.Options{Now: time.Now})
	b := NewBridge(store, log.New(io.Discard, "", 0))
	sink := &captureSink{}
	b.SetSink(sink)
	b.SetJWTSecret(secret)
	return b, store, sink
}

func TestC1IdentityJWTAuthoritative(t *testing.T) {
	b, store, _ := newAuthBridge(t, testJWTSecret)
	ctx := context.Background()
	token, err := auth.Issue(testJWTSecret, 777, time.Hour, "im2a-test")
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}
	// 报文里伪造 UserID=999；内核必须以 JWT.sub=777 为准。
	ack := b.OnConnOpen(connproto.ConnOpen{
		ConnID: 1, UserID: 999, DeviceID: "dev-c1", Token: token,
	})
	if !ack.OK {
		t.Fatalf("ConnOpenAck = %+v, want OK", ack)
	}
	if ack.UserID != 777 {
		t.Fatalf("ConnOpenAck.UserID = %d, want JWT.sub=777（权威身份，不是报文里的 999）", ack.UserID)
	}
	cur, ok, err := store.Cursor(ctx, "dev-c1")
	if err != nil || !ok {
		t.Fatalf("cursor: ok=%v err=%v", ok, err)
	}
	if cur.UserID != 777 {
		t.Fatalf("im_device_cursor.user_id = %d, want 777（位点必须落在 JWT 主体上）", cur.UserID)
	}
	if ack.ResumeToken == "" {
		t.Fatalf("ResumeToken 为空")
	}
}

func TestC1IdentityRejects(t *testing.T) {
	b, _, _ := newAuthBridge(t, testJWTSecret)
	expired, err := auth.Issue(testJWTSecret, 1, -time.Minute, "im2a-test")
	if err != nil {
		t.Fatalf("issue expired: %v", err)
	}
	zero, err := auth.Issue(testJWTSecret, 0, time.Hour, "im2a-test")
	if err != nil {
		t.Fatalf("issue zero: %v", err)
	}
	cases := []struct {
		name     string
		token    string
		wantOK   bool
		wantReas string
	}{
		{"no-token", "", false, "auth_required"},
		{"garbage", "not-a-jwt", false, "invalid_token"},
		{"wrong-secret", func() string { s, _ := auth.Issue("other-secret", 1, time.Hour, "x"); return s }(), false, "invalid_token"},
		{"expired", expired, false, "token_expired"},
		{"sub-zero", zero, false, "invalid_token_sub"},
	}
	for _, c := range cases {
		ack := b.OnConnOpen(connproto.ConnOpen{ConnID: 1, UserID: 999, DeviceID: "dev-" + c.name, Token: c.token})
		if ack.OK != c.wantOK {
			t.Fatalf("%s: OK=%v, want %v (ack=%+v)", c.name, ack.OK, c.wantOK, ack)
		}
		if c.wantReas != "" && ack.Reason != c.wantReas {
			t.Fatalf("%s: Reason=%q, want %q", c.name, ack.Reason, c.wantReas)
		}
	}
}

func TestC1IdentityBackwardCompatWithoutSecret(t *testing.T) {
	// 未启用校验（B2 兼容）：信任网关给的 UserID，token 不被解析。
	b, _, _ := newAuthBridge(t, "")
	ack := b.OnConnOpen(connproto.ConnOpen{ConnID: 1, UserID: 42, DeviceID: "legacy-dev", Token: "ignored"})
	if !ack.OK || ack.UserID != 42 {
		t.Fatalf("legacy ConnOpenAck = %+v, want OK UserID=42", ack)
	}
}

// fakePresence 记录 C1 回调。
type fakePresence struct {
	leases  []connproto.PresenceLease
	closed  []connproto.ConnClosed
	acks    int
	lastAck int64
	lastDev string
}

func (f *fakePresence) OnLease(l connproto.PresenceLease) bool {
	f.leases = append(f.leases, l)
	return true
}
func (f *fakePresence) OnConnClosed(cl connproto.ConnClosed) {
	f.closed = append(f.closed, cl)
}
func (f *fakePresence) OnAck(userID int64, deviceID string) {
	f.acks++
	f.lastAck, f.lastDev = userID, deviceID
}

func TestC1PresenceHooks(t *testing.T) {
	b, _, _ := newAuthBridge(t, "")
	pres := &fakePresence{}
	b.SetPresence(pres)

	// Wire 必须把 C1 回调全部接上（含此前漏接的 OnConnClosed）。
	cfg := b.Wire(RemoteConfig{})
	if cfg.OnConnClosed == nil || cfg.OnPresenceLease == nil || cfg.OnConnOpen == nil {
		t.Fatalf("Wire 未接全 C1 回调: closed=%v lease=%v open=%v",
			cfg.OnConnClosed != nil, cfg.OnPresenceLease != nil, cfg.OnConnOpen != nil)
	}

	// 租约：合法→true+记录；非法→false+不建模。
	if !b.OnPresenceLease(connproto.PresenceLease{ConnID: 1, UserID: 7, DeviceID: "d1", TTLS: 45, AtMS: 1, State: "online"}) {
		t.Fatal("合法租约应被接收")
	}
	if b.OnPresenceLease(connproto.PresenceLease{ConnID: 1, UserID: 0, DeviceID: "d1"}) {
		t.Fatal("UserID=0 的租约应被丢弃")
	}
	if len(pres.leases) != 1 {
		t.Fatalf("presence leases = %d, want 1", len(pres.leases))
	}

	// Controller closed 回调。
	cfg.OnConnClosed(connproto.ConnClosed{ConnID: 1, UserID: 7, DeviceID: "d1"})
	if len(pres.closed) != 1 {
		t.Fatalf("presence closed = %d, want 1", len(pres.closed))
	}

	// ack 即活：成功落位点后 OnAck 被调用；epoch 不匹配的 ack 不触发。
	if !b.OnAckUp(connproto.AckUp{UserID: 7, DeviceID: "d1", ConnEpoch: 0, AckSeq: 3}) {
		t.Fatal("AckUp 应成功")
	}
	if pres.acks != 1 || pres.lastAck != 7 || pres.lastDev != "d1" {
		t.Fatalf("presence OnAck = %d (user=%d dev=%q), want 1 (7,d1)", pres.acks, pres.lastAck, pres.lastDev)
	}
	if _, err := b.BumpEpoch(context.Background(), 7, "d1"); err != nil {
		t.Fatalf("BumpEpoch: %v", err)
	}
	if b.OnAckUp(connproto.AckUp{UserID: 7, DeviceID: "d1", ConnEpoch: 0, AckSeq: 4}) {
		t.Fatal("旧 epoch 的 AckUp 应被拒绝")
	}
	if pres.acks != 1 {
		t.Fatalf("被拒绝的 AckUp 不应触发 presence 续租（acks=%d）", pres.acks)
	}
}

func TestC1DeliverEphemeralNoLog(t *testing.T) {
	b, store, sink := newAuthBridge(t, "")
	frames := []connproto.Frame{{Type: connproto.TypePresence, Payload: []byte(`{"state":"online"}`)}}
	res := b.DeliverEphemeral(31, frames)
	if sink.userID != 31 || len(sink.frames) != 1 {
		t.Fatalf("ephemeral sink = user=%d frames=%d, want 31/1", sink.userID, len(sink.frames))
	}
	if res.Enqueued != 1 {
		t.Fatalf("ephemeral DeliverResult = %+v", res)
	}
	// 关键：presence 不进离线箱（不写 im_delivery_log / 不发号）。
	last, err := store.LastSeq(context.Background(), 31)
	if err != nil || last != 0 {
		t.Fatalf("ephemeral 写入了 delivery log（LastSeq=%d err=%v），应为 0", last, err)
	}
	// 非法参数安全返回零值。
	if got := b.DeliverEphemeral(0, frames); got != (connhub.DeliverResult{}) {
		t.Fatalf("DeliverEphemeral(0) = %+v, want zero", got)
	}
}

// TestBridgeConnOpenEchoConnID 回归（C4 flake 根因）：网关用 openWait[ConnID] 关联
// ConnOpenAck 与 WS 连接；内核不回显 ConnID 时并发建连会把 ack 绑到错误的 conn，
// 表现为 A/B 身份互换。此测试在修复前必然失败。
func TestBridgeConnOpenEchoConnID(t *testing.T) {
	b, _, _ := newAuthBridge(t, testJWTSecret)
	tok, err := auth.Issue(testJWTSecret, 1234, time.Hour, "im2a-test")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	for _, connID := range []uint64{1, 2, 3} {
		ack := b.OnConnOpen(connproto.ConnOpen{ConnID: connID, DeviceID: "dev-x", Token: tok})
		if !ack.OK {
			t.Fatalf("connID=%d ack not ok: %+v", connID, ack)
		}
		if ack.ConnID != connID {
			t.Fatalf("ConnOpenAck.ConnID=%d, want %d（不回显会导致 ack 与连接错配）", ack.ConnID, connID)
		}
	}
	// 拒绝路径也必须回显 ConnID（否则网关无法关闭正确连接）。
	bad := b.OnConnOpen(connproto.ConnOpen{ConnID: 9, DeviceID: "dev-x", Token: "bad"})
	if bad.ConnID != 9 || bad.OK {
		t.Fatalf("拒绝 ack=%+v, want ConnID=9 OK=false", bad)
	}
	t.Logf("C4_CONNID_ECHO_OK: 成功/拒绝路径均回显 ConnID")
}