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
	"human-agent-im/internal/store"
	"human-agent-im/internal/tempgroup"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// 微信 peer 消息 → A2A 协商会话真实落库；对方 ACCEPT 推进到 COMPLETED。
func TestRelayWechatNegoReal(t *testing.T) {
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
	suf := fmt.Sprintf("vnego_%d", time.Now().UnixNano())
	accountID := suf + "-im-bot"
	botWxid := suf + "_bot@im.wechat"
	fromWxid := suf + "_owner@im.wechat"
	peerWxid := suf + "_peer@im.wechat"
	if err := os.WriteFile(filepath.Join(dir, accountID+".json"),
		[]byte(`{"userId":"`+botWxid+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM negotiation_sessions WHERE from_agent_id LIKE $1 OR to_agent_id LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_relay_messages WHERE account_id=$1 OR from_wxid LIKE $2 OR bot_wxid LIKE $2`, accountID, suf+"%")
		_, _ = d.Exec(`DELETE FROM bot_route_messages WHERE from_wxid LIKE $1 OR to_wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
	})

	if _, err := d.Exec(`INSERT INTO wechat_identity(wxid,user_id) VALUES($1,930001),($2,930002) ON CONFLICT DO NOTHING`, botWxid, peerWxid); err != nil {
		t.Fatal(err)
	}

	negoStore := store.NewPostgresStore(d, 10).Negotiation
	svc := New(d, NewAccountResolver(dir), &fakeModel{reply: "x"}, botroute.New(d)).
		WithPeerSender(&fakeSender{}).
		WithNegotiation(NewWechatBridge(negoStore))
	ctx := context.Background()

	// 1) peer 发起协商（PROPOSE）→ WORKING
	res, err := svc.Relay(ctx, accountID, fromWxid,
		"@"+peerWxid+" 我想约你明天下午三点开会，方便吗？", "nego_"+fmt.Sprintf("%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	if res.NegoSessionID == "" || res.NegoState != "WORKING" {
		t.Fatalf("want nego WORKING, got sid=%q state=%q", res.NegoSessionID, res.NegoState)
	}

	var state, fromA, toA string
	var round, turnCount int
	err = d.QueryRow(`SELECT state, round, from_agent_id, to_agent_id FROM negotiation_sessions WHERE id=$1`,
		res.NegoSessionID).Scan(&state, &round, &fromA, &toA)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if state != "WORKING" || round != 1 || fromA != botWxid || toA != peerWxid {
		t.Fatalf("session state=%s round=%d from=%s to=%s", state, round, fromA, toA)
	}
	if err := d.QueryRow(`SELECT count(*) FROM negotiation_turns WHERE session_id=$1`, res.NegoSessionID).Scan(&turnCount); err != nil {
		t.Fatal(err)
	}
	if turnCount != 1 {
		t.Fatalf("want 1 turn after propose, got %d", turnCount)
	}

	// 2) 对方 Agent 回复 ACCEPT → COMPLETED，turns=2
	sess2, err := svc.NegoReply(ctx, res.NegoSessionID, peerWxid, "ACCEPT", "同意，明天三点见")
	if err != nil {
		t.Fatalf("nego reply: %v", err)
	}
	if sess2.State.String() != "COMPLETED" {
		t.Fatalf("want COMPLETED, got %s", sess2.State)
	}
	if err := d.QueryRow(`SELECT count(*) FROM negotiation_turns WHERE session_id=$1`, res.NegoSessionID).Scan(&turnCount); err != nil {
		t.Fatal(err)
	}
	if turnCount != 2 {
		t.Fatalf("want 2 turns after accept, got %d", turnCount)
	}
}

// v2.0.8：多 Agent 协商临时群真实落库。
// 发起协商自动建群并拉双方 Agent 入群、写首轮；每轮写入；到达终态（达成/拒绝）自动销毁。
func TestRelayTempGroupOnNegotiation(t *testing.T) {
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
	suf := fmt.Sprintf("vtg_%d", time.Now().UnixNano())
	accountID := suf + "-im-bot"
	botWxid := suf + "_bot@im.wechat"
	fromWxid := suf + "_owner@im.wechat"
	peerWxid := suf + "_peer@im.wechat"
	if err := os.WriteFile(filepath.Join(dir, accountID+".json"),
		[]byte(`{"userId":"`+botWxid+`"}`), 0o600); err != nil {
		t.Fatal(err)
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
		_, _ = d.Exec(`DELETE FROM wechat_relay_messages WHERE account_id=$1 OR from_wxid LIKE $2 OR bot_wxid LIKE $2`, accountID, suf+"%")
		_, _ = d.Exec(`DELETE FROM bot_route_messages WHERE from_wxid LIKE $1 OR to_wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
	})

	if _, err := d.Exec(`INSERT INTO wechat_identity(wxid,user_id) VALUES($1,940001),($2,940002) ON CONFLICT DO NOTHING`, botWxid, peerWxid); err != nil {
		t.Fatal(err)
	}

	negoStore := store.NewPostgresStore(d, 10).Negotiation
	bridge := NewWechatBridge(negoStore).WithTempGroup(tempgroup.New(d))
	svc := New(d, NewAccountResolver(dir), &fakeModel{reply: "x"}, botroute.New(d)).
		WithPeerSender(&fakeSender{}).
		WithNegotiation(bridge)
	ctx := context.Background()

	// 群断言辅助
	groupStatus := func(gid string) string {
		var st sql.NullString
		_ = d.QueryRow(`SELECT status FROM temp_groups WHERE id=$1`, gid).Scan(&st)
		return st.String
	}
	groupCount := func(table, gid string) int {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM `+table+` WHERE group_id=$1`, gid).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	assertDestroyed := func(gid string) {
		if st := groupStatus(gid); st != "closed" {
			t.Fatalf("group %s want closed, got %q", gid, st)
		}
		if n := groupCount("temp_group_members", gid); n != 0 {
			t.Fatalf("destroyed group %s members=%d want 0", gid, n)
		}
		if n := groupCount("temp_messages", gid); n != 0 {
			t.Fatalf("destroyed group %s messages=%d want 0", gid, n)
		}
	}

	// 1) PROPOSE：自动建群，双方 Agent 入群，写 1 条首轮消息。
	res, err := svc.Relay(ctx, accountID, fromWxid,
		"@"+peerWxid+" 我想约你明天下午三点开一个临时协调会，方便吗？", "vtg_"+fmt.Sprintf("%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("relay propose: %v", err)
	}
	if res.NegoSessionID == "" || res.TempGroupID != "tg_"+res.NegoSessionID {
		t.Fatalf("want tempGroupId derived from nego session, got nego=%q tg=%q", res.NegoSessionID, res.TempGroupID)
	}
	gid := res.TempGroupID
	gids = append(gids, gid)
	if st := groupStatus(gid); st != "active" {
		t.Fatalf("after propose group status=%q want active", st)
	}
	if n := groupCount("temp_group_members", gid); n != 2 {
		t.Fatalf("after propose members=%d want 2", n)
	}
	if n := groupCount("temp_messages", gid); n != 1 {
		t.Fatalf("after propose messages=%d want 1", n)
	}

	// 2) 对方 COUNTER：群仍 active，第 2 条消息，成员不变。
	cs, err := svc.NegoReply(ctx, res.NegoSessionID, peerWxid, "COUNTER", "我后天下午三点可以，明天没空")
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	if cs.State.String() != "WORKING" {
		t.Fatalf("counter state=%s want WORKING", cs.State)
	}
	if st := groupStatus(gid); st != "active" {
		t.Fatalf("after counter group status=%q want active", st)
	}
	if n := groupCount("temp_messages", gid); n != 2 {
		t.Fatalf("after counter messages=%d want 2", n)
	}
	if n := groupCount("temp_group_members", gid); n != 2 {
		t.Fatalf("after counter members=%d want 2", n)
	}

	// 3) 发起方 ACCEPT：协商 COMPLETED，临时群自动销毁。
	as, err := svc.NegoReply(ctx, res.NegoSessionID, botWxid, "ACCEPT", "好，那就定后天下午三点")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if as.State.String() != "COMPLETED" {
		t.Fatalf("accept state=%s want COMPLETED", as.State)
	}
	assertDestroyed(gid)

	// 4) 另一个协商：对方 REJECT 同样触发销毁。
	res2, err := svc.Relay(ctx, accountID, fromWxid,
		"@"+peerWxid+" 周末能帮我加个班吗？", "vtg_"+fmt.Sprintf("%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("relay propose2: %v", err)
	}
	gid2 := res2.TempGroupID
	gids = append(gids, gid2)
	if groupCount("temp_group_members", gid2) != 2 || groupCount("temp_messages", gid2) != 1 {
		t.Fatalf("second propose want 2 members/1 msg, got members=%d msgs=%d",
			groupCount("temp_group_members", gid2), groupCount("temp_messages", gid2))
	}
	rs, err := svc.NegoReply(ctx, res2.NegoSessionID, peerWxid, "REJECT", "周末不行")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rs.State.String() != "REJECTED" {
		t.Fatalf("reject state=%s want REJECTED", rs.State)
	}
	assertDestroyed(gid2)
}
