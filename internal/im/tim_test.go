package im

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- v1.2.1 票据：真实 HMAC 签发 + 验签 + 过期 ----

func TestUserSig_SignAndVerify(t *testing.T) {
	c := New(Config{SDKAppID: 1400, SecretKey: "real-secret"})
	us, err := c.GenUserSig("alice")
	if err != nil {
		t.Fatal(err)
	}
	if us.Mode != "tim-live" {
		t.Fatalf("mode want tim-live, got %s", us.Mode)
	}
	// 正确票据验签通过
	if err := c.VerifyUserSig("alice", us.ExpiresAt, us.Sig); err != nil {
		t.Fatalf("valid sig should verify: %v", err)
	}
	// 篡改签名拒绝
	if err := c.VerifyUserSig("alice", us.ExpiresAt, strings.Repeat("0", 64)); err != ErrBadSig {
		t.Fatalf("tampered sig should be ErrBadSig, got %v", err)
	}
	// 张冠李戴（bob 用 alice 的 sig）拒绝
	if err := c.VerifyUserSig("bob", us.ExpiresAt, us.Sig); err != ErrBadSig {
		t.Fatalf("wrong user should be ErrBadSig, got %v", err)
	}
	// 过期票据拒绝
	if err := c.VerifyUserSig("alice", time.Now().Add(-time.Second).Unix(), us.Sig); err != ErrSigExpired {
		t.Fatalf("expired sig should be ErrSigExpired, got %v", err)
	}
}

func TestGenUserSig_EmptyUserID(t *testing.T) {
	if _, err := New(Config{}).GenUserSig(""); err == nil {
		t.Fatal("empty userId should error")
	}
}

// ---- v1.2.2 Webhook 验签：真实 HMAC，防伪造/防重放 ----

func TestWebhook_Signature(t *testing.T) {
	secret := "wh-secret"
	body := []byte(`{"CallbackType":"C2C.CallbackAfterMsg","From_Account":"a"}`)
	now := time.Now().Unix()
	sig := SignWebhook(secret, now, body)

	// 正确签名通过
	if err := VerifyWebhook(secret, now, body, sig); err != nil {
		t.Fatalf("valid webhook should pass: %v", err)
	}
	// 错误签名拒绝
	if err := VerifyWebhook(secret, now, body, "deadbeef"); err != ErrBadWebhookSig {
		t.Fatalf("bad sig should be ErrBadWebhookSig, got %v", err)
	}
	// 篡改 body 拒绝
	if err := VerifyWebhook(secret, now, []byte(`{"x":1}`), sig); err != ErrBadWebhookSig {
		t.Fatalf("tampered body should be ErrBadWebhookSig, got %v", err)
	}
	// 过期时间戳（重放）拒绝
	old := time.Now().Add(-10 * time.Minute).Unix()
	if err := VerifyWebhook(secret, old, body, SignWebhook(secret, old, body)); err != ErrWebhookStale {
		t.Fatalf("stale ts should be ErrWebhookStale, got %v", err)
	}
}

// ---- v1.2.3 路由表 ----

func TestRoute_ThreeChannels(t *testing.T) {
	cases := map[string]string{
		"c2c_text":        ChannelHumanIM,
		"group_image":     ChannelHumanIM,
		"agent_propose":   ChannelAgentWS,
		"agent_negotiate": ChannelAgentWS,
		"approval_card":   ChannelApproval,
		"approval_modify": ChannelApproval,
		"unknown":         ChannelHumanIM,
	}
	for in, want := range cases {
		if got := Route(in); got != want {
			t.Errorf("Route(%s)=%s want %s", in, got, want)
		}
	}
}

// ---- v1.2.0 真实发送：httptest 模拟腾讯云 REST，断言真实 HTTP 出站与协议 ----

func newFakeTIM(t *testing.T, wantPath string, fail bool, gotBody *map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path want %s, got %s", wantPath, r.URL.Path)
		}
		// 必须带腾讯云鉴权/应用参数
		q := r.URL.Query()
		if q.Get("sdkappid") != "1400000000" {
			t.Errorf("missing sdkappid query, got %q", q.Get("sdkappid"))
		}
		if q.Get("usersig") == "" || q.Get("identifier") == "" {
			t.Error("usersig/identifier query required")
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		if fail {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ActionStatus": "FAIL", "ErrorCode": 20003, "ErrorInfo": "invalid target"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ActionStatus": "OK", "MsgTime": 1792000000, "MsgKey": "mk_real_001"})
	}))
	t.Cleanup(srv.Close)
	return New(Config{SDKAppID: 1400000000, SecretKey: "sk", Endpoint: srv.URL})
}

func TestSendC2C_RealHTTP(t *testing.T) {
	var got map[string]any
	c := newFakeTIM(t, "/v4/openim/sendmsg", false, &got)
	rcpt, err := c.SendC2C(SendMessageReq{FromUserID: "alice", ToUserID: "bob", Text: "hello"})
	if err != nil {
		t.Fatalf("real send: %v", err)
	}
	if rcpt.MsgKey != "mk_real_001" || rcpt.Mode != "tim-live" {
		t.Fatalf("receipt should carry real MsgKey/mode, got %+v", rcpt)
	}
	// 断言请求体符合腾讯云报文
	if got["From_Account"] != "alice" || got["To_Account"] != "bob" {
		t.Fatalf("body accounts wrong: %v", got)
	}
	body, _ := json.Marshal(got["MsgBody"])
	if !strings.Contains(string(body), "TIMTextElem") || !strings.Contains(string(body), "hello") {
		t.Fatalf("MsgBody not TIMTextElem: %s", body)
	}
}

func TestSendC2C_REALFail(t *testing.T) {
	var got map[string]any
	c := newFakeTIM(t, "/v4/openim/sendmsg", true, &got)
	if _, err := c.SendC2C(SendMessageReq{FromUserID: "a", ToUserID: "b", Text: "x"}); err == nil {
		t.Fatal("TIM ActionStatus=FAIL must return error, not fake success")
	}
}

func TestSendGroup_RealHTTP(t *testing.T) {
	var got map[string]any
	c := newFakeTIM(t, "/v4/group_open_http_svc/send_group_msg", false, &got)
	rcpt, err := c.SendGroup(GroupSendReq{GroupID: "@TGS#fam", From: "alice", Text: "dinner"})
	if err != nil {
		t.Fatalf("real group send: %v", err)
	}
	if rcpt.GroupID != "@TGS#fam" || rcpt.MsgKey != "mk_real_001" {
		t.Fatalf("group receipt wrong: %+v", rcpt)
	}
	if got["GroupId"] != "@TGS#fam" {
		t.Fatalf("body GroupId wrong: %v", got)
	}
}

func TestSendC2C_NotConfigured(t *testing.T) {
	c := New(Config{}) // 无 SDKAppID/SecretKey
	if _, err := c.SendC2C(SendMessageReq{FromUserID: "a", ToUserID: "b", Text: "x"}); err != ErrNotConfigured {
		t.Fatalf("without credentials must be ErrNotConfigured, got %v", err)
	}
}

// ---- v1.2.4 群广播：成员校验 + 真实持久化 + 只推其他成员 ----

type fakeRepo struct {
	members []string
	saved   []GroupMessage
	saveErr error
}

func (f *fakeRepo) Members(string) ([]string, error) { return f.members, nil }
func (f *fakeRepo) SaveGroupMessage(m GroupMessage) (int64, error) {
	if f.saveErr != nil {
		return 0, f.saveErr
	}
	f.saved = append(f.saved, m)
	return int64(len(f.saved)), nil
}

func TestBroadcast_MemberCheck_Persist_Push(t *testing.T) {
	repo := &fakeRepo{members: []string{"alice", "bob", "carol"}}
	pushedTopics := []string{}
	bc := &Broadcaster{Repo: repo, Push: func(topic string, p any) {
		pushedTopics = append(pushedTopics, topic)
	}}
	out, msgID, err := bc.Broadcast(GroupMessage{GroupID: "g1", FromUser: "alice", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	// 真实持久化被调用
	if len(repo.saved) != 1 || repo.saved[0].Text != "hi" {
		t.Fatalf("message should be persisted before push: %v", repo.saved)
	}
	if msgID != 1 {
		t.Fatalf("msgId should come from repo, got %d", msgID)
	}
	// 只推给 bob/carol，topic 为 group:<user>
	if len(out) != 2 || len(pushedTopics) != 2 {
		t.Fatalf("should push to 2 others, got %v / %v", out, pushedTopics)
	}
	for _, tp := range pushedTopics {
		if tp != "group:bob" && tp != "group:carol" {
			t.Fatalf("unexpected topic %s", tp)
		}
	}
}

func TestBroadcast_RejectNonMember(t *testing.T) {
	repo := &fakeRepo{members: []string{"alice", "bob"}}
	bc := &Broadcaster{Repo: repo, Push: func(string, any) { t.Fatal("non-member must not trigger push") }}
	if _, _, err := bc.Broadcast(GroupMessage{GroupID: "g1", FromUser: "stranger", Text: "spam"}); err != ErrNotMember {
		t.Fatalf("non-member sender must be ErrNotMember, got %v", err)
	}
	if len(repo.saved) != 0 {
		t.Fatal("non-member message must not be persisted")
	}
}

func TestNewGroup_Dedup(t *testing.T) {
	g, err := NewGroup("g1", "family", "alice", []string{"bob", "alice", "carol", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Members) != 3 {
		t.Fatalf("members should dedup to 3, got %d: %v", len(g.Members), g.Members)
	}
}

// TestWebhookEventTencentRealFields 固化腾讯云真实回调字段名归一（防回归）：
// CallbackCommand->CallbackType、MsgId->MsgKey、MsgBody 数组、指针接收者回写。
func TestWebhookEventTencentRealFields(t *testing.T) {
	raw := `{"CallbackCommand":"Group.CallbackAfterSendMsg","SdkAppID":1600168829,` +
		`"GroupId":"im2a_group_1","From_Account":"admin","MsgId":"wk-123","MsgTime":1792762000,` +
		`"MsgBody":[{"MsgType":"TIMTextElem","MsgContent":{"Text":"hi"}}]}`
	var ev WebhookEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("unmarshal real Tencent callback: %v", err)
	}
	if err := ev.Valid(); err != nil { // 指针接收者回写
		t.Fatalf("valid: %v", err)
	}
	if ev.CallbackType != "Group.CallbackAfterSendMsg" {
		t.Fatalf("CallbackCommand not normalized: %q", ev.CallbackType)
	}
	if ev.MsgKey != "wk-123" {
		t.Fatalf("MsgId not normalized to MsgKey: %q", ev.MsgKey)
	}
	if len(ev.MsgBody) != 1 {
		t.Fatalf("MsgBody must decode as array, got %v", ev.MsgBody)
	}
	if ev.SdkAppID != 1600168829 {
		t.Fatalf("SdkAppID: %d", ev.SdkAppID)
	}
}
