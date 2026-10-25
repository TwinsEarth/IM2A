package relay

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"human-agent-im/internal/model"
)

// 意图门（v2.2.7）：自由文本在被"规则转发 / 挂接旧协商"之前，先交给 LLM 理解意图。
//
// 背景：v2.2.4 之前只要存在一个未完成协商，autoReplyOpenNego 就把用户的任意闲聊
// （如「你能干什么」「你叫什么名字」）都当成对协商的回复转发给对方，导致"鸡同鸭讲"。
// 本分类器把"在跟 Bot 说话"还是"在回答协商"还是"明确要转给某人"区分开；
// 安全默认永远是 chat —— 绝不臆造联系人、绝不把闲聊塞进协商。

type intentKind string

const (
	intentChat      intentKind = "chat"       // 跟自己的 Bot 说话（默认）
	intentRelay     intentKind = "relay"      // 明确转达给某联系人
	intentNegoReply intentKind = "nego_reply" // 对进行中协商问题的直接答复
)

// llmIntent 是分类器的结构化输出。
type llmIntent struct {
	Intent     string  `json:"intent"`
	Target     string  `json:"target"`
	Body       string  `json:"body"`
	Confidence float64 `json:"confidence"`
}

// pendingNego 是"最后一轮由对方发出、正等我回复"的进行中协商。
type pendingNego struct {
	sessionID string
	other     string
	state     string
	lastText  string
}

// findPendingNego 定位当前用户"正等他回复"的最近一个进行中协商；没有则返回 nil。
func (s *Service) findPendingNego(ctx context.Context, fromWxid string) *pendingNego {
	if s == nil || s.db == nil {
		return nil
	}
	var p pendingNego
	var last sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT ns.id,
       CASE WHEN ns.from_agent_id=$1 THEN ns.to_agent_id ELSE ns.from_agent_id END,
       ns.state,
       (SELECT COALESCE(nt.payload->>'text', nt.payload->>'body', nt.payload->>'summary', nt.payload->>'message','')
          FROM negotiation_turns nt
         WHERE nt.session_id=ns.id
         ORDER BY nt.created_at DESC LIMIT 1)
FROM negotiation_sessions ns
WHERE (ns.from_agent_id=$1 OR ns.to_agent_id=$1)
  AND ns.state IN ('WORKING','INPUT_REQUIRED','AUTH_REQUIRED')
  AND ns.updated_at > now() - interval '24 hours'
  AND (SELECT nt.from_agent_id FROM negotiation_turns nt
        WHERE nt.session_id=ns.id ORDER BY nt.created_at DESC LIMIT 1) <> $1
ORDER BY ns.updated_at DESC LIMIT 1`, fromWxid).Scan(&p.sessionID, &p.other, &p.state, &last)
	if err != nil {
		return nil
	}
	p.lastText = strings.TrimSpace(last.String)
	return &p
}

// classifyIntent 用 LLM 判定一条自由文本的意图。模型不可用或输出无法解析时，
// 一律安全降级为 chat（宁可正常对话，也不误转发）。
func (s *Service) classifyIntent(ctx context.Context, fromWxid, text string, contactNames []string, p *pendingNego) llmIntent {
	fallback := llmIntent{Intent: string(intentChat), Confidence: 0.3}
	if s == nil || s.mdl == nil {
		return fallback
	}

	var b strings.Builder
	b.WriteString("你是微信消息意图分类器。只输出一个 JSON，不要解释、不要 markdown。\n")
	b.WriteString("判断用户这句话属于哪一类：\n")
	b.WriteString("- chat：在跟 Bot 说话——提问、闲聊、问功能、问名字、让它帮忙查/写/分析、表达感慨。默认选这个。\n")
	b.WriteString("- relay：明确要求把某句话转达/告诉/转发/发给某个【具体联系人】。target 填联系人名，body 填要转达的原话。\n")
	b.WriteString("- nego_reply：这句话是对【正在进行的协商中，对方刚提出的问题】的直接答复，例如接受/拒绝/改时间/补充人数地点。\n")
	b.WriteString("严格规则：\n")
	b.WriteString("1) 只要不是明确的「转发给某人」，或不是在回答协商问题，一律 chat；\n")
	b.WriteString("2) 联系人列表里没有的名字，不许出现在 target；\n")
	b.WriteString("3) 不允许猜测联系人，不允许把普通闲聊当成协商答复。\n")
	if len(contactNames) > 0 {
		b.WriteString("联系人列表：" + strings.Join(contactNames, "、") + "\n")
	} else {
		b.WriteString("联系人列表：空（此时不能输出 relay）\n")
	}
	if p != nil {
		b.WriteString("进行中协商：对方=")
		b.WriteString(s.userName(ctx, p.other))
		b.WriteString("，状态=" + p.state)
		if p.lastText != "" {
			b.WriteString("，对方最后一条=" + p.lastText)
		}
		b.WriteString("。只有用户的话是在回答它，才输出 nego_reply。\n")
	} else {
		b.WriteString("进行中协商：无（禁止输出 nego_reply）。\n")
	}
	b.WriteString(`输出格式：{"intent":"chat","target":"","body":"","confidence":0.0}` + "\n")
	b.WriteString("用户这句话：" + text)

	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	resp, err := s.mdl.Chat(cctx, &model.ChatRequest{
		Messages:    []model.Message{{Role: "system", Content: b.String()}},
		Temperature: 0,
		MaxTokens:   200,
	})
	if err != nil {
		return fallback
	}
	var out llmIntent
	if err := json.Unmarshal([]byte(extractJSONObject(resp.Content)), &out); err != nil {
		return fallback
	}
	out.Intent = strings.TrimSpace(strings.ToLower(out.Intent))
	switch intentKind(out.Intent) {
	case intentRelay, intentNegoReply, intentChat:
	default:
		out.Intent = string(intentChat)
	}
	return out
}
