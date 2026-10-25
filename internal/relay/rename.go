package relay

import (
	"context"
	"regexp"
	"strings"
)

// RouteRename 微信对话里用户给自己/Agent 命名、改名。
const RouteRename = "rename"

var (
	// 给 Agent 起名：你叫阿福 / 以后你叫阿福 / 你的名字叫阿福 / 给你起个名字叫阿福 / 把你的名字改成阿福
	// 给 Agent 起名：你叫阿福 / 以后你叫阿福 / 你的名字叫阿福 / 给你起个名字叫阿福 / 把你的名字改成阿福
	// 以及：我的 Agent 叫小佑佑 / 我的助理叫小佑佑 / 你的 agent 叫小佑佑（含空格、大小写）。
	renameAgentRe = regexp.MustCompile(`^(?:你叫|以后你叫|你以后叫|你的名字叫|给你起个名字(?:叫|为)?|把你的名字改成|我的\s*[Aa]gent\s*叫|我的\s*助理\s*叫|我的\s*小助理\s*叫|你的\s*[Aa]gent\s*叫|给我的\s*[Aa]gent\s*起?名?字?叫?)\s*(.+?)[。.！!？?\s]*$`)
	// 给自己起名：我叫阿明 / 以后叫我阿明 / 我的名字叫阿明 / 把我的名字改成阿明
	// 不匹配"我是…""叫我…"，避免"我是学生""叫我吃饭"这类正常话被误判。
	renameUserRe = regexp.MustCompile(`^(?:我叫|以后叫我|我的名字(?:叫|是)|把我的名字改成|把我名字改成)\s*(.+?)[。.！!？?\s]*$`)
)

// 句首寒暄/标点，命名词前常带"你好，我叫小房"，剥离后再匹配，避免漏识别。
var renameGreetingRe = regexp.MustCompile(`^(?:你好|您好|嗨|哈喽|[Hh]ello|[Hh]i|喂)[，,。.、\s！!~～]*`)

// stripRenameGreeting 反复剥离句首寒暄与前导标点，返回真正的指令体。
func stripRenameGreeting(t string) string {
	for {
		t2 := strings.TrimLeft(t, " ，,。.、！!？?~～")
		t2 = renameGreetingRe.ReplaceAllString(t2, "")
		if t2 == t {
			return t2
		}
		t = t2
	}
}

// 问句/常见干扰词，命中则视为正常聊天，不按改名处理。
var renameNoise = []string{"什么", "谁", "吗", "呢", "干嘛", "怎么", "为什么", "多少", "哪里"}

// parseRename 判断一句话是否是命名/改名指令。
func parseRename(text string) (kind, name string, ok bool) {
	t := stripRenameGreeting(strings.TrimSpace(text))
	if t == "" {
		return "", "", false
	}
	if m := renameAgentRe.FindStringSubmatch(t); m != nil {
		n := strings.TrimSpace(m[1])
		if n != "" && !hasRenameNoise(n) {
			return "agent", n, true
		}
	}
	if m := renameUserRe.FindStringSubmatch(t); m != nil {
		n := strings.TrimSpace(m[1])
		if n != "" && !hasRenameNoise(n) {
			return "user", n, true
		}
	}
	return "", "", false
}

func hasRenameNoise(n string) bool {
	for _, w := range renameNoise {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

func cleanName(n string) (string, bool) {
	n = strings.TrimSpace(n)
	n = strings.Trim(n, "。.！!？?，,、 ")
	r := []rune(n)
	if len(r) < 1 || len(r) > 16 {
		return "", false
	}
	return string(r), true
}

// handleRename 处理命名/改名；handled=false 表示这句话不是命名指令。
func (s *Service) handleRename(ctx context.Context, accountID, botWxid, fromWxid, text, idemKey string) (res *Result, handled bool, err error) {
	kind, raw, ok := parseRename(text)
	if !ok {
		return nil, false, nil
	}
	name, good := cleanName(raw)
	var reply string
	if !good {
		reply = "名字得是 1 到 16 个字，再给我起一个？"
	} else {
		col := "user_name"
		if kind == "agent" {
			col = "agent_name"
		}
		if werr := s.setName(ctx, fromWxid, col, name); werr != nil {
			return nil, true, werr
		}
		if kind == "agent" {
			reply = "好，那我以后就叫" + name + "了。"
		} else {
			reply = "好，以后就叫你" + name + "。"
		}
	}
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO wechat_relay_messages(account_id,bot_wxid,from_wxid,text,route,reply,routed_to,idem_key)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		accountID, botWxid, fromWxid, text, RouteRename, reply, "", idemKey)
	return &Result{Reply: reply, Route: RouteRename, BotWxid: botWxid}, true, nil
}
