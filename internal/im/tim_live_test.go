package im

// 真实腾讯云出站测试：必须 env TIM_SDK_APP_ID / TIM_SECRET_KEY 已配置，否则 skip。
import (
	"os"
	"strconv"
	"testing"
)

func liveClient(t *testing.T) *Client {
	t.Helper()
	appID, err := strconv.ParseInt(os.Getenv("TIM_SDK_APP_ID"), 10, 64)
	if err != nil || appID == 0 {
		t.Skip("TIM creds not set")
	}
	secret := os.Getenv("TIM_SECRET_KEY")
	if secret == "" {
		t.Skip("TIM creds not set")
	}
	return New(Config{SDKAppID: appID, SecretKey: secret, AdminIdentifier: "administrator"})
}

func TestLiveImportAndSendC2C(t *testing.T) {
	c := liveClient(t)
	for _, acc := range []struct{ id, nick string }{
		{"im2a_alice", "IM2A Alice"},
		{"im2a_bob", "IM2A Bob"},
	} {
		if err := c.ImportAccount(acc.id, acc.nick); err != nil {
			t.Fatalf("ImportAccount(%s) failed: %v", acc.id, err)
		}
		t.Logf("imported OK: %s", acc.id)
	}
	rcpt, err := c.SendC2C(SendMessageReq{
		FromUserID: "administrator",
		ToUserID:   "im2a_alice",
		Text:       "IM2A live e2e: admin -> alice via real Tencent Cloud channel",
	})
	if err != nil {
		t.Fatalf("SendC2C admin->alice failed: %v", err)
	}
	if rcpt.MsgKey == "" {
		t.Fatal("expected real MsgKey admin->alice, got empty")
	}
	t.Logf("LIVE OK admin->alice MsgKey=%s at=%d", rcpt.MsgKey, rcpt.At)

	rcpt2, err := c.SendC2C(SendMessageReq{
		FromUserID: "im2a_alice",
		ToUserID:   "im2a_bob",
		Text:       "IM2A live e2e: alice -> bob",
	})
	if err != nil {
		t.Fatalf("SendC2C alice->bob failed: %v", err)
	}
	if rcpt2.MsgKey == "" {
		t.Fatal("expected real MsgKey alice->bob, got empty")
	}
	t.Logf("LIVE OK alice->bob MsgKey=%s", rcpt2.MsgKey)
}

func TestLiveGroupE2E(t *testing.T) {
	c := liveClient(t)
	const gid = "im2a_group_1"

	// 建群（幂等：已存在则忽略已存在错误继续）
	if err := c.CreateGroup(gid, "IM2A 测试群", "administrator", []string{"im2a_alice", "im2a_bob"}); err != nil {
		t.Logf("CreateGroup note (可能已存在): %v", err)
	} else {
		t.Log("CreateGroup OK")
	}

	// 群主在群里真实发一条
	r1, err := c.SendGroup(GroupSendReq{GroupID: gid, From: "administrator", Text: "IM2A live group: admin broadcast"})
	if err != nil {
		t.Fatalf("SendGroup admin failed: %v", err)
	}
	if r1.MsgID == "" {
		t.Fatal("expected group msg_seq (MsgID), got empty")
	}
	t.Logf("LIVE group admin msg_seq=%s", r1.MsgID)

	// 成员 alice 在群里真实回一条
	r2, err := c.SendGroup(GroupSendReq{GroupID: gid, From: "im2a_alice", Text: "IM2A live group: alice reply"})
	if err != nil {
		t.Fatalf("SendGroup alice failed: %v", err)
	}
	if r2.MsgID == "" {
		t.Fatal("expected alice group msg_seq, got empty")
	}
	t.Logf("LIVE group alice msg_seq=%s", r2.MsgID)
}
