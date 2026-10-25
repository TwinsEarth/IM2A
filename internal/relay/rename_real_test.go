package relay

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestParseRename 纯解析：命名指令命中、问句/正常话不误判。
func TestParseRename(t *testing.T) {
	cases := []struct {
		text     string
		wantKind string
		wantName string
		wantOK   bool
	}{
		{"我叫阿明", "user", "阿明", true},
		{"我叫阿明。", "user", "阿明", true},
		{"以后叫我阿明", "user", "阿明", true},
		{"我的名字是阿明", "user", "阿明", true},
		{"把我的名字改成阿明", "user", "阿明", true},
		{"你叫阿福", "agent", "阿福", true},
		{"以后你叫阿福", "agent", "阿福", true},
		{"给你起个名字叫阿福", "agent", "阿福", true},
		{"把你的名字改成小助", "agent", "小助", true},
		// 问句/正常话不得误判
		{"你叫什么名字", "", "", false},
		{"我是学生", "", "", false},
		{"叫我吃饭", "", "", false},
		{"明天一起吃饭吗", "", "", false},
		{"我叫什么", "", "", false},
		{"你好", "", "", false},
	}
	for _, c := range cases {
		kind, name, ok := parseRename(c.text)
		if ok != c.wantOK {
			t.Errorf("parseRename(%q) ok=%v want %v", c.text, ok, c.wantOK)
			continue
		}
		if ok && (kind != c.wantKind || name != c.wantName) {
			t.Errorf("parseRename(%q) = (%s,%s), want (%s,%s)", c.text, kind, name, c.wantKind, c.wantName)
		}
	}
}

// TestRenameReal 真实落库：微信对话里给自己/Agent 改名 → 写入 wechat_identity →
// userName/agentName 读取到新值，且改名立即生效（缓存失效）。零残留。
func TestRenameReal(t *testing.T) {
	svc, d, accountID, botWxid, fromWxid := setupRelayTest(t)
	ctx := context.Background()
	suf := fmt.Sprintf("rn_%d", time.Now().UnixNano())
	uid := 920000000 + time.Now().UnixNano()%90000000

	if _, err := d.Exec(`INSERT INTO wechat_identity(wxid,user_id) VALUES($1,$2)`, fromWxid, uid); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid=$1 OR wxid LIKE $2`, fromWxid, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_relay_messages WHERE idem_key LIKE $1`, suf+"%")
	})

	// 1) 给自己起名
	res, handled, err := svc.handleRename(ctx, accountID, botWxid, fromWxid, "我叫阿明", suf+"-u")
	if err != nil || !handled || res == nil {
		t.Fatalf("rename user: handled=%v res=%v err=%v", handled, res, err)
	}
	var userName string
	if err := d.QueryRow(`SELECT user_name FROM wechat_identity WHERE wxid=$1`, fromWxid).Scan(&userName); err != nil {
		t.Fatalf("read user_name: %v", err)
	}
	if userName != "阿明" {
		t.Fatalf("user_name=%q want 阿明", userName)
	}
	if got := svc.userName(ctx, fromWxid); got != "阿明" {
		t.Fatalf("userName()=%q want 阿明（改名后缓存应失效）", got)
	}

	// 2) 给 Agent 起名
	res2, h2, err2 := svc.handleRename(ctx, accountID, botWxid, fromWxid, "你叫阿福", suf+"-a")
	if err2 != nil || !h2 || res2 == nil {
		t.Fatalf("rename agent: handled=%v res=%v err=%v", h2, res2, err2)
	}
	if got := svc.agentName(ctx, fromWxid); got != "阿福" {
		t.Fatalf("agentName()=%q want 阿福", got)
	}

	// 3) 改名覆盖
	if _, h3, err3 := svc.handleRename(ctx, accountID, botWxid, fromWxid, "把我的名字改成阿明哥", suf+"-u2"); err3 != nil || !h3 {
		t.Fatalf("rename again: handled=%v err=%v", h3, err3)
	}
	if got := svc.userName(ctx, fromWxid); got != "阿明哥" {
		t.Fatalf("userName after change=%q want 阿明哥", got)
	}

	// 4) 非命名指令不得命中
	if _, h4, err4 := svc.handleRename(ctx, accountID, botWxid, fromWxid, "你叫什么名字", suf+"-q"); err4 != nil || h4 {
		t.Fatalf("question must not be rename: handled=%v err=%v", h4, err4)
	}

	// 5) peer 出站文本用新昵称
	if got := svc.peerOutText(ctx, fromWxid, "在吗"); got != "阿明哥：在吗" {
		t.Fatalf("peerOutText=%q want 阿明哥：在吗", got)
	}
}
