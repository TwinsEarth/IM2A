package relay

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"human-agent-im/internal/botroute"
	"human-agent-im/internal/model"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// fakeModel 确定性模型，不依赖真实云端（落库/路由断言用）。
type fakeModel struct{ reply string }

func (f *fakeModel) Name() string { return "fake" }
func (f *fakeModel) Chat(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, error) {
	return &model.ChatResponse{Content: f.reply, RoutedTo: "primary"}, nil
}

func setupRelayTest(t *testing.T) (*Service, *sql.DB, string, string, string) {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	// 假 openclaw 账号目录
	dir := t.TempDir()
	suf := fmt.Sprintf("v22_%d", time.Now().UnixNano())
	accountID := suf + "-im-bot"
	botWxid := suf + "_bot@im.wechat"
	fromWxid := suf + "_from@im.wechat"
	if err := os.WriteFile(filepath.Join(dir, accountID+".json"),
		[]byte(`{"userId":"`+botWxid+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := NewAccountResolver(dir)
	svc := New(d, resolver, &fakeModel{reply: "秘书回复OK"}, botroute.New(d))

	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM wechat_relay_messages WHERE account_id=$1 OR from_wxid LIKE $2 OR bot_wxid LIKE $2`, accountID, suf+"%")
		_, _ = d.Exec(`DELETE FROM bot_route_messages WHERE from_wxid LIKE $1 OR to_wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
	})
	return svc, d, accountID, botWxid, fromWxid
}

// self_chat：消息落库 + 模型回复 + 幂等。
func TestRelaySelfChatReal(t *testing.T) {
	svc, d, accountID, botWxid, fromWxid := setupRelayTest(t)
	ctx := context.Background()

	res, err := svc.Relay(ctx, accountID, fromWxid, "你好，帮我安排明天会议", "idem_"+time.Now().String())
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	if res.Route != RouteSelfChat || res.Reply != "秘书回复OK" || res.BotWxid != botWxid {
		t.Fatalf("unexpected result: %+v", res)
	}

	// 真实落库断言
	var n int
	var savedRoute, savedReply string
	err = d.QueryRow(`SELECT count(*), route, reply FROM wechat_relay_messages
		WHERE account_id=$1 AND from_wxid=$2 GROUP BY route, reply`, accountID, fromWxid).
		Scan(&n, &savedRoute, &savedReply)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 || savedRoute != "self_chat" || savedReply != "秘书回复OK" {
		t.Fatalf("db assert failed n=%d route=%s reply=%s", n, savedRoute, savedReply)
	}
}

// 幂等：同 idem_key 重复调用，不产生第二条，返回同一回复。
func TestRelayIdempotent(t *testing.T) {
	svc, d, accountID, _, fromWxid := setupRelayTest(t)
	ctx := context.Background()
	idem := "idem_key_" + fmt.Sprintf("%d", time.Now().UnixNano())

	r1, err := svc.Relay(ctx, accountID, fromWxid, "第一条", idem)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := svc.Relay(ctx, accountID, fromWxid, "第一条", idem)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Reply != r2.Reply {
		t.Fatalf("idempotent mismatch: %q vs %q", r1.Reply, r2.Reply)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM wechat_relay_messages WHERE idem_key=$1`, idem).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want exactly 1 row, got %d", n)
	}
}

// 未绑定账号 → 明确错误。
func TestRelayUnknownAccount(t *testing.T) {
	svc, _, _, _, fromWxid := setupRelayTest(t)
	_, err := svc.Relay(context.Background(), "nonexistent-im-bot", fromWxid, "hi", "")
	if err == nil {
		t.Fatal("want error for unknown account")
	}
}

// peer_route：@目标 转发给另一个已绑定用户。
func TestRelayPeerRoute(t *testing.T) {
	svc, d, accountID, botWxid, fromWxid := setupRelayTest(t)
	ctx := context.Background()
	suf := botWxid[:len(botWxid)-len("_bot@im.wechat")]
	peerWxid := suf + "_peer@im.wechat"
	// 目标必须已绑定 wechat_identity 且 bot 主人也在
	if _, err := d.Exec(`INSERT INTO wechat_identity(wxid,user_id) VALUES($1,920001),($2,920002) ON CONFLICT DO NOTHING`, botWxid, peerWxid); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Relay(ctx, accountID, fromWxid,
		"@"+peerWxid+" 我们明天三点开会", "peer_"+fmt.Sprintf("%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("relay peer: %v", err)
	}
	if res.Route != RoutePeerRoute || res.PeerWxid != peerWxid {
		t.Fatalf("want peer_route to %s, got %+v", peerWxid, res)
	}
	// bot_route_messages 应落一条
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM bot_route_messages WHERE from_wxid=$1 AND to_wxid=$2`,
		botWxid, peerWxid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 routed msg, got %d", n)
	}
}

// fakeSender 记录出站调用，不真实发微信（relay 逻辑/落库断言用）。
type fakeSender struct {
	calls                  int
	lastAccount, lastTarget, lastText, msgID string
}

func (f *fakeSender) Send(ctx context.Context, accountID, targetWxid, text string) (string, error) {
	f.calls++
	f.lastAccount, f.lastTarget, f.lastText = accountID, targetWxid, text
	f.msgID = "fake_mid_" + fmt.Sprintf("%d", time.Now().UnixNano())
	return f.msgID, nil
}

// peer_route + 出站投递：@目标 → 落库 → 反查发送账号 → 主动推送 → 标记 delivered。
func TestRelayPeerPushReal(t *testing.T) {
	svc, d, accountID, botWxid, fromWxid := setupRelayTest(t)
	ctx := context.Background()
	suf := botWxid[:len(botWxid)-len("_bot@im.wechat")]
	peerWxid := suf + "_peer@im.wechat"
	if _, err := d.Exec(`INSERT INTO wechat_identity(wxid,user_id) VALUES($1,920011),($2,920012) ON CONFLICT DO NOTHING`, botWxid, peerWxid); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSender{}
	svc.WithPeerSender(fs)
	// 收件人 B 有自己的 openclaw 账号（真机投递必须用 B 自己的账号发 B 自己）。
	peerAccount := suf + "-peer-im-bot"
	if err := os.WriteFile(filepath.Join(svc.resolver.dir, peerAccount+".json"),
		[]byte(`{"userId":"`+peerWxid+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	body := "请你转达：明天九点开会"
	res, err := svc.Relay(ctx, accountID, fromWxid, "@"+peerWxid+" "+body, "peerpush_"+fmt.Sprintf("%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("relay peer push: %v", err)
	}
	// 出站确实被调用，且发送账号=from bot、目标=peer
	if fs.calls != 1 || fs.lastAccount != peerAccount || fs.lastTarget != peerWxid {
		t.Fatalf("sender calls=%d account=%s target=%s text=%q", fs.calls, fs.lastAccount, fs.lastTarget, fs.lastText)
	}
	if !res.PeerDelivered || res.PeerMessageID == "" {
		t.Fatalf("want delivered with msgId, got %+v", res)
	}
	// wechat_relay_messages 真实落库 peer_delivered/peer_message_id
	var del bool
	var mid string
	err = d.QueryRow(`SELECT peer_delivered, peer_message_id FROM wechat_relay_messages
		WHERE account_id=$1 AND route=$2 AND peer_wxid=$3`, accountID, RoutePeerRoute, peerWxid).
		Scan(&del, &mid)
	if err != nil {
		t.Fatalf("query relay: %v", err)
	}
	if !del || mid == "" {
		t.Fatalf("want peer_delivered=true mid, got del=%v mid=%q", del, mid)
	}
	// bot_route_messages 已标记 delivered
	var bd bool
	err = d.QueryRow(`SELECT delivered FROM bot_route_messages WHERE from_wxid=$1 AND to_wxid=$2`, botWxid, peerWxid).Scan(&bd)
	if err != nil {
		t.Fatalf("query route: %v", err)
	}
	if !bd {
		t.Fatal("bot_route_messages.delivered want true")
	}
}
