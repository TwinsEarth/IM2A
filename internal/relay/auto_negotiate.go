package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"human-agent-im/internal/model"
	"human-agent-im/internal/negotiation"
)

// AutoNegoTurn 记录 Agent↔Agent 自动协商中的一轮（某个 Agent 的一次立场）。
type AutoNegoTurn struct {
	Round     int    `json:"round"`
	Speaker   string `json:"speaker"` // 该轮发言的 Agent（bot wxid）
	Intent    string `json:"intent"`  // ACCEPT / COUNTER / REJECT
	Reply     string `json:"reply"`   // 发给对方看的中文微信消息
	State     string `json:"state"`   // 该轮后的状态机状态
	Delivered bool   `json:"delivered"`
	MessageID string `json:"messageId,omitempty"`
	SendError string `json:"sendError,omitempty"`
}

// AutoNegoResult 是双 Agent 自动协商的结果。
type AutoNegoResult struct {
	SessionID   string         `json:"sessionId"`
	TempGroupID string         `json:"tempGroupId"`
	FinalState  string         `json:"finalState"`
	Turns       []AutoNegoTurn `json:"turns"`
}

// AutoNegotiate 让两个微信用户各自的 Agent（LLM）围绕已打开的协商会话自动对谈，
// 直到达成/拒绝（终态）或达到 maxRounds。
//
// 每一轮：
//  1. 由"该发言的一方"Agent 调用主/备双通道大模型（DeepSeek 主、Kimi 备），
//     基于其主人的立场生成下一轮动作（intent + 一句微信回复）；
//  2. 经 WechatBridge.Reply 推进 8 态状态机（同步更新临时群，终态自动销毁）；
//  3. 把该 Agent 的立场经 openclaw 出站真实推送给"另一方"的微信，
//     实现两台手机上都能看到两个 Agent 来回协商。
//
// personas 为 botWxid → 该 Agent 所代表用户的立场/约束；缺省则给中性的协商助手设定。
func (s *Service) AutoNegotiate(ctx context.Context, sessionID string, maxRounds int, personas map[string]string) (*AutoNegoResult, error) {
	if s.nego == nil {
		return nil, errors.New("relay: negotiation bridge not configured")
	}
	if s.mdl == nil {
		return nil, errors.New("relay: model not configured")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("relay: sessionId required")
	}
	if maxRounds <= 0 {
		maxRounds = 4
	}
	out := &AutoNegoResult{SessionID: sessionID, TempGroupID: tempGroupID(sessionID)}

	// botWxid → openclaw accountId 反查（出站投递时用）。
	acctByWxid := map[string]string{}
	if s.resolver != nil {
		if all, err := s.resolver.AllAccounts(); err == nil {
			for acc, w := range all {
				acctByWxid[w] = acc
			}
		}
	}

	for i := 0; i < maxRounds; i++ {
		sess, err := s.nego.LoadSession(ctx, sessionID)
		if err != nil {
			return out, err
		}
		if isNegoTerminal(sess.State) {
			out.FinalState = sess.State.String()
			break
		}
		speaker := nextSpeaker(sess)
		other := peerOf(sess, speaker)
		if speaker == "" || other == "" {
			return out, errors.New("relay: cannot resolve negotiation parties")
		}

		// 1) 该方 Agent 调大模型决策。
		intent, reply, _, err := s.agentTurn(ctx, sess, speaker, other, personas[speaker])
		if err != nil {
			return out, fmt.Errorf("agent %s turn: %w", speaker, err)
		}

		// 2) 推进状态机 + 临时群（终态自动销毁）。
		updated, err := s.nego.Reply(ctx, sessionID, speaker, intent, reply)
		if err != nil {
			return out, err
		}

		// 3) 把该 Agent 的立场真实推送给另一方微信。
		tr := AutoNegoTurn{Round: updated.Round, Speaker: speaker, Intent: intent, Reply: reply,
			State: updated.State.String()}
		if s.sender != nil {
			// 真机投递：必须用【收件人 other 自己的账号】发 other 自己，对方手机才收得到。
			if acc, ok := acctByWxid[other]; ok {
				text := s.userName(ctx, speaker) + "：" + strings.TrimSpace(reply)
				if mid, serr := s.sender.Send(ctx, acc, other, text); serr == nil {
					tr.Delivered, tr.MessageID = true, mid
				} else {
					tr.SendError = serr.Error()
				}
			} else {
				tr.SendError = "no openclaw account for recipient " + other
			}
		}
		out.Turns = append(out.Turns, tr)

		if isNegoTerminal(updated.State) {
			out.FinalState = updated.State.String()
			break
		}

		// 让出节奏，避免双通道瞬时打满（真机更稳）。
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(1200 * time.Millisecond):
		}
	}

	if out.FinalState == "" {
		if sess, err := s.nego.LoadSession(ctx, sessionID); err == nil {
			out.FinalState = sess.State.String()
		}
	}
	return out, nil
}

// nextSpeaker 下一轮该发言的 Agent：首轮 PROPOSE 是 A→B，下一轮即 B（=最后一轮的 To）。
func nextSpeaker(sess *negotiation.Session) string {
	if len(sess.Turns) == 0 {
		return ""
	}
	return sess.Turns[len(sess.Turns)-1].ToAgentID
}

// peerOf 返回与 speaker 相对的另一方（首轮的双方即全体协商方）。
func peerOf(sess *negotiation.Session, speaker string) string {
	if len(sess.Turns) == 0 {
		return ""
	}
	first := sess.Turns[0]
	if speaker == first.FromAgentID {
		return first.ToAgentID
	}
	return first.FromAgentID
}

// agentTurn 调某一方 Agent 的大模型生成下一轮动作（严格 JSON）。
func (s *Service) agentTurn(ctx context.Context, sess *negotiation.Session, speaker, other, persona string) (string, string, map[string]any, error) {
	var b strings.Builder
	b.WriteString("你是用户在 IM2A 平台上的私人 AI 助手（Agent），正在与另一个用户的 Agent 进行 A2A 协商。\n")
	b.WriteString(fmt.Sprintf("你（本 Agent）的微信标识：%s；对方 Agent：%s。\n", speaker, other))
	if strings.TrimSpace(persona) != "" {
		b.WriteString("你所代表用户的立场/约束（你必须为主人争取，不能无条件让步）：\n" + strings.TrimSpace(persona) + "\n")
	} else {
		b.WriteString("你没有收到额外的个性化授权，请以合理、礼貌、高效的方式为主人安排。\n")
	}
	b.WriteString(fmt.Sprintf("当前协商状态：%s，轮次 %d/%d。\n", sess.State, sess.Round, sess.MaxRounds))
	b.WriteString("协商历史（最早在前）：\n")
	for i, t := range sess.Turns {
		txt := ""
		if t.Payload != nil {
			if v, ok := t.Payload["text"].(string); ok {
				txt = v
			}
		}
		b.WriteString(fmt.Sprintf("  轮%d %s→%s %s：%s\n", i+1, t.FromAgentID, t.ToAgentID, t.Intent, txt))
	}
	b.WriteString("\n请基于你主人的立场给出下一轮动作。只输出一个 JSON 对象，不要输出任何额外文字，格式：\n")
	b.WriteString(`{"intent":"ACCEPT"|"COUNTER"|"REJECT","reply":"给对方看的一句中文微信消息，口语、简洁、像真人在微信里沟通","payload":{"reason":"简短理由"}}` + "\n")
	b.WriteString("对方立场已满足你主人要求时用 ACCEPT；仍需协调时间/条件时用 COUNTER；完全无法接受才 REJECT。请尽量在 2-4 轮内达成一致。")

	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resp, err := s.mdl.Chat(cctx, &model.ChatRequest{
		Messages: []model.Message{
			{Role: "system", Content: b.String()},
			{Role: "user", Content: "请输出你的下一轮 JSON。"},
		},
		Temperature: 0.4,
		MaxTokens:   400,
		AgentID:     "wechat-nego-agent",
	})
	if err != nil {
		return "", "", nil, err
	}
	raw := extractJSONObject(resp.Content)
	if raw == "" {
		return "", "", nil, fmt.Errorf("agent %s: 无法解析模型输出: %q", speaker, resp.Content)
	}
	var parsed struct {
		Intent  string         `json:"intent"`
		Reply   string         `json:"reply"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", "", nil, fmt.Errorf("agent %s: 解析 JSON 失败: %w", speaker, err)
	}
	intent := strings.ToUpper(strings.TrimSpace(parsed.Intent))
	switch negotiation.Intent(intent) {
	case negotiation.IntentAccept, negotiation.IntentCounter, negotiation.IntentReject:
	default:
		intent = string(negotiation.IntentCounter) // 非法/缺失兜底为继续协商
	}
	reply := strings.TrimSpace(parsed.Reply)
	if reply == "" {
		reply = "（我的 Agent 正在处理，请稍候）"
	}
	return intent, reply, parsed.Payload, nil
}

// extractJSONObject 截取模型输出里第一个完整的 {...} JSON 对象。
func extractJSONObject(s string) string {
	i := strings.Index(s, "{")
	if i < 0 {
		return ""
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[i : j+1]
			}
		}
	}
	return ""
}

