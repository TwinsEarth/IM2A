package relay

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"human-agent-im/internal/botroute"
	"human-agent-im/internal/model"
	"human-agent-im/internal/store"
	"human-agent-im/internal/tempgroup"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// scriptedModel 按调用次序返回固定的协商 JSON（B 还价→A 还价→B 接受），用于确定性断言。
type scriptedModel struct {
	mu sync.Mutex
	n  int
}

func (m *scriptedModel) Name() string { return "scripted" }

func (m *scriptedModel) Chat(_ context.Context, _ *model.ChatRequest) (*model.ChatResponse, error) {
	m.mu.Lock()
	i := m.n
	m.n++
	m.mu.Unlock()
	var js string
	switch i {
	case 0:
		js = `{"intent":"COUNTER","reply":"明天我没空，后天下午三点可以吗？","payload":{"reason":"明天已满"}}`
	case 1:
		js = `{"intent":"COUNTER","reply":"后天下午三点可以，会议压缩到30分钟行吗？","payload":{"reason":"缩短时长"}}`
	default:
		js = `{"intent":"ACCEPT","reply":"好，那就后天下午三点开30分钟。","payload":{"reason":"达成一致"}}`
	}
	return &model.ChatResponse{Content: js, RoutedTo: "primary"}, nil
}

type recSend struct{ account, target, text string }

type recSender struct {
	mu   sync.Mutex
	sent []recSend
}

func (s *recSender) Send(_ context.Context, accountID, targetWxid, text string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, recSend{account: accountID, target: targetWxid, text: text})
	return "rec_mid_" + fmt.Sprintf("%d", len(s.sent)), nil
}

// v2.0.8：双 Agent 自动协商真实落库。
// 初始 PROPOSE 后，双方 Agent 经状态机自动 3 轮（B 还价→A 还价→B 接受），
// 每轮真实"出站"（recSender 记录账号/目标交替），临时群随 COMPLETED 自动销毁。
func TestAutoNegotiationReal(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	dir := t.TempDir()
	suf := fmt.Sprintf("va_%d", time.Now().UnixNano())
	accountA := suf + "-A-im-bot"
	accountB := suf + "-B-im-bot"
	botWxid := suf + "_botA@im.wechat"
	peerWxid := suf + "_botB@im.wechat"
	fromWxid := suf + "_ownerA@im.wechat"
	for acc, w := range map[string]string{accountA: botWxid, accountB: peerWxid} {
		if err := os.WriteFile(filepath.Join(dir, acc+".json"),
			[]byte(`{"userId":"`+w+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var gids []string
	t.Cleanup(func() {
		for _, g := range gids {
			_, _ = d.Exec(`DELETE FROM temp_messages WHERE group_id=$1`, g)
			_, _ = d.Exec(`DELETE FROM temp_group_members WHERE group_id=$1`, g)
			_, _ = d.Exec(`DELETE FROM temp_groups WHERE id=$1`, g)
		}
		_, _ = d.Exec(`DELETE FROM negotiation_turns WHERE session_id IN
			(SELECT id FROM negotiation_sessions WHERE from_agent_id LIKE $1 OR to_agent_id LIKE $1)`, suf+"%")
		_, _ = d.Exec(`DELETE FROM negotiation_sessions WHERE from_agent_id LIKE $1 OR to_agent_id LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_relay_messages WHERE account_id LIKE $1 OR from_wxid LIKE $2 OR bot_wxid LIKE $2`, suf+"%", suf+"%")
		_, _ = d.Exec(`DELETE FROM bot_route_messages WHERE from_wxid LIKE $1 OR to_wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
	})

	if _, err := d.Exec(`INSERT INTO wechat_identity(wxid,user_id) VALUES($1,950001),($2,950002) ON CONFLICT DO NOTHING`, botWxid, peerWxid); err != nil {
		t.Fatal(err)
	}

	negoStore := store.NewPostgresStore(d, 10).Negotiation
	bridge := NewWechatBridge(negoStore).WithTempGroup(tempgroup.New(d))
	rs := recSender{}
	svc := New(d, NewAccountResolver(dir), &scriptedModel{}, botroute.New(d)).
		WithPeerSender(&rs).
		WithNegotiation(bridge)
	ctx := context.Background()

	// 人类 A 授权发起协商（首轮 PROPOSE）。
	res, err := svc.Relay(ctx, accountA, fromWxid,
		"@"+peerWxid+" 我想约明天下午三点开个30分钟的会。", "va_"+fmt.Sprintf("%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	gid := res.TempGroupID
	gids = append(gids, gid)
	if res.NegoState != "WORKING" {
		t.Fatalf("want WORKING after propose, got %q", res.NegoState)
	}

	// 双方 Agent 自动协商。
	out, err := svc.AutoNegotiate(ctx, res.NegoSessionID, 4, nil)
	if err != nil {
		t.Fatalf("auto negotiate: %v", err)
	}
	if out.FinalState != "COMPLETED" {
		t.Fatalf("want COMPLETED, got %q (turns=%+v)", out.FinalState, out.Turns)
	}
	if len(out.Turns) != 3 {
		t.Fatalf("want 3 auto turns, got %d: %+v", len(out.Turns), out.Turns)
	}
	// 发言方应交替 B→A→B，intent 为 COUNTER/COUNTER/ACCEPT。
	wantSpeakers := []string{peerWxid, botWxid, peerWxid}
	wantIntents := []string{"COUNTER", "COUNTER", "ACCEPT"}
	for i, tr := range out.Turns {
		if tr.Speaker != wantSpeakers[i] {
			t.Fatalf("turn%d speaker=%s want %s", i, tr.Speaker, wantSpeakers[i])
		}
		if tr.Intent != wantIntents[i] {
			t.Fatalf("turn%d intent=%s want %s", i, tr.Intent, wantIntents[i])
		}
		if !tr.Delivered {
			t.Fatalf("turn%d not delivered: %s", i, tr.SendError)
		}
	}

	// 出站投递：初始提案 A→B，随后自动协商 B→A、A→B、B→A，均推到对方微信。
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.sent) != 4 {
		t.Fatalf("want 4 outbound sends (proposal + 3 auto turns), got %d", len(rs.sent))
	}
	wantSends := []recSend{
		{account: accountB, target: peerWxid}, // 初始提案送达 B（用 B 自己的账号）
		{account: accountA, target: botWxid},  // B 还价送达 A（用 A 自己的账号）
		{account: accountB, target: peerWxid}, // A 还价送达 B
		{account: accountA, target: botWxid},  // B 接受送达 A
	}
	for i := range wantSends {
		if rs.sent[i].account != wantSends[i].account || rs.sent[i].target != wantSends[i].target {
			t.Fatalf("send%d=%+v want account=%s target=%s", i, rs.sent[i], wantSends[i].account, wantSends[i].target)
		}
	}

	// 临时群应在 COMPLETED 时自动销毁。
	var st sql.NullString
	var mem, msg int
	if err := d.QueryRow(`SELECT status FROM temp_groups WHERE id=$1`, gid).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st.String != "closed" {
		t.Fatalf("temp group status=%q want closed", st.String)
	}
	d.QueryRow(`SELECT count(*) FROM temp_group_members WHERE group_id=$1`, gid).Scan(&mem)
	d.QueryRow(`SELECT count(*) FROM temp_messages WHERE group_id=$1`, gid).Scan(&msg)
	if mem != 0 || msg != 0 {
		t.Fatalf("after completion members=%d messages=%d want 0", mem, msg)
	}
	var turns int
	d.QueryRow(`SELECT count(*) FROM negotiation_turns WHERE session_id=$1`, res.NegoSessionID).Scan(&turns)
	if turns != 4 {
		t.Fatalf("want 4 total turns (propose+3), got %d", turns)
	}
}
