package relay

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// peerNameLeadRe 识别"按自然姓名转发"的意图前缀。
// 支持：告诉/转告/转达/发给/通知/跟/向 + （可选 帮我/替我/一下）+ 姓名 + 正文。
// 例：「告诉小红今晚7点晚饭」「帮我跟小明说下午两点碰头」「通知阿明明早开会」。
var peerNameLeadRe = regexp.MustCompile(
	`^(?:帮我|替我|帮忙)?(?:告诉|转告|转达|发给|通知|向|跟)(?:一下)?[\s，,：:]*([\s\S]+)$`)

// bodyLeadVerbRe 去掉姓名与正文之间的连接字（说/问/讲/说一声 等）与标点。
var bodyLeadVerbRe = regexp.MustCompile(`^[\s]*(?:说一声|说一下|说|问|讲|道)[\s，,：:]*`)

// parsePeerNameLead 从前缀剥离意图词，返回剩余的「姓名+正文」片段。
func parsePeerNameLead(text string) (string, bool) {
	m := peerNameLeadRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// extractPeerNameBody 用"已知联系人姓名"从「姓名+正文」里切出姓名与正文。
// 候选姓名按字数从长到短匹配，避免短名误命中；返回 ok=false 表示无已知姓名前缀或正文为空。
func extractPeerNameBody(rest string, names []string) (name, body string, ok bool) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", false
	}
	cands := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); len([]rune(n)) >= 1 {
			cands = append(cands, n)
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		return len([]rune(cands[i])) > len([]rune(cands[j]))
	})
	for _, n := range cands {
		if strings.HasPrefix(rest, n) {
			b := strings.TrimSpace(rest[len(n):])
			if loc := bodyLeadVerbRe.FindStringIndex(b); loc != nil && loc[0] == 0 {
				b = strings.TrimSpace(b[loc[1]:])
			} else {
				b = strings.TrimLeft(b, "，,：: \t")
			}
			if b == "" {
				continue // 只有称呼没有正文，不算转发
			}
			return n, b, true
		}
	}
	return "", "", false
}

// resolvePeerByName 在"已绑定、可投递"的联系人里按自然姓名解析对方 wxid。
// 候选来源：wechat_identity.user_name（自定义昵称）+ 各已绑定 bot 的内置称呼。
// 排除发送者本人；重名时优先选择与发送者有过协商会话的联系人。
func (s *Service) resolvePeerByName(ctx context.Context, fromWxid, name string) (string, bool) {
	if s == nil || s.resolver == nil {
		return "", false
	}
	all, err := s.resolver.AllAccounts()
	if err != nil {
		return "", false
	}
	boundWxids := make(map[string]string) // wxid -> account
	for acc, wx := range all {
		if wx != "" && wx != fromWxid {
			boundWxids[wx] = acc
		}
	}
	if len(boundWxids) == 0 {
		return "", false
	}

	type cand struct {
		wxid string
		neg  bool // 与发送者存在协商会话
	}
	seen := map[string]bool{}
	var matches []cand
	add := func(w string) {
		if _, ok := boundWxids[w]; !ok || seen[w] {
			return
		}
		seen[w] = true
		matches = append(matches, cand{wxid: w, neg: s.hasNegoWith(ctx, fromWxid, w)})
	}

	// 1) 自定义昵称
	if s.db != nil {
		rows, qerr := s.db.QueryContext(ctx,
			`SELECT wxid FROM wechat_identity WHERE trim(coalesce(user_name,''))=$1`, name)
		if qerr == nil {
			for rows.Next() {
				var w string
				if rows.Scan(&w) == nil {
					add(w)
				}
			}
			rows.Close()
		}
	}
	// 2) 内置称呼兜底（覆盖未自定义昵称的已绑定账号）
	for wx := range boundWxids {
		if fallbackName(wx) == name {
			add(wx)
		}
	}

	if len(matches) == 0 {
		return "", false
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].neg != matches[j].neg {
			return matches[i].neg // 有协商关系的优先
		}
		return matches[i].wxid < matches[j].wxid
	})
	return matches[0].wxid, true
}

// hasNegoWith 判断两个 wxid 之间是否存在协商会话（用于重名消歧）。
func (s *Service) hasNegoWith(ctx context.Context, a, b string) bool {
	if s == nil || s.db == nil || a == b {
		return false
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(1) FROM negotiation_sessions
WHERE (from_agent_id=$1 AND to_agent_id=$2) OR (from_agent_id=$2 AND to_agent_id=$1)
LIMIT 1`, a, b).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// lookupPeerByName 串联：意图前缀 → 姓名/正文切分 → 姓名解析为 wxid。
func (s *Service) lookupPeerByName(ctx context.Context, fromWxid, text string) (peerWxid, body string, ok bool) {
	rest, hasLead := parsePeerNameLead(text)
	if !hasLead {
		return "", "", false
	}
	names := s.knownContactNames(ctx, fromWxid)
	name, body, matched := extractPeerNameBody(rest, names)
	if !matched {
		return "", "", false
	}
	w, found := s.resolvePeerByName(ctx, fromWxid, name)
	if !found {
		return "", "", false
	}
	return w, body, true
}

// knownContactNames 收集"可被发送者转发"的联系人姓名（自定义昵称 + 内置称呼）。
func (s *Service) knownContactNames(ctx context.Context, fromWxid string) []string {
	if s == nil || s.resolver == nil {
		return nil
	}
	all, err := s.resolver.AllAccounts()
	if err != nil {
		return nil
	}
	nameset := map[string]bool{}
	for _, wx := range all {
		if wx == "" || wx == fromWxid {
			continue
		}
		if n := s.userName(ctx, wx); n != "" && n != "对方" {
			nameset[n] = true
		}
	}
	if s.db != nil {
		rows, qerr := s.db.QueryContext(ctx,
			`SELECT DISTINCT trim(user_name) FROM wechat_identity WHERE trim(coalesce(user_name,''))<>''`)
		if qerr == nil {
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil && n != "" {
					nameset[n] = true
				}
			}
			rows.Close()
		}
	}
	out := make([]string, 0, len(nameset))
	for n := range nameset {
		out = append(out, n)
	}
	return out
}
