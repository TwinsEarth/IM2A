package relay

import (
	"context"
	"fmt"
)

// RouteNegoReply 自由微信消息自动挂接到未完成的 A2A 协商。
const RouteNegoReply = "nego_reply"

// autoReplyOpenNego 把"自由文本"作为 REQUEST_INFO 推进到该 wxid 正在等待回复的协商，
// 并把协商回执主动 push 给对方手机。命中返回 (result,true,nil)；不命中返回 (nil,false,nil)。
//
// v2.2.7 关键修正（智能体理解）：
//   - 先由 findPendingNego 定位"最后一轮是对方发来、正等我回复"的协商；
//   - 再由 LLM 意图门 classifyIntent 判断这句话是不是"在回答协商里的问题"；
//   - 只有 nego_reply 才推进并转发，普通闲聊/提问（如「你能干什么」）一律回落 self_chat，
//     不再被旧协商吸走造成"鸡同鸭讲"。
//
// 投递仍由【对方自己的 bot 账号】给对方自己的 ilink id 发回执（与 peer 投递同一姿势）。
func (s *Service) autoReplyOpenNego(ctx context.Context, accountID, botWxid, fromWxid, text, idemKey string) (*Result, bool, error) {
	if s == nil || s.nego == nil || s.db == nil {
		return nil, false, nil
	}

	pending := s.findPendingNego(ctx, fromWxid)
	if pending == nil {
		return nil, false, nil
	}

	in := s.classifyIntent(ctx, fromWxid, text, s.knownContactNames(ctx, fromWxid), pending)
	if intentKind(in.Intent) != intentNegoReply {
		// 不是在回答协商问题 → 交给普通对话，绝不把闲聊塞给对方。
		return nil, false, nil
	}

	sess, err := s.nego.Reply(ctx, pending.sessionID, fromWxid, "REQUEST_INFO", text)
	if err != nil {
		return nil, true, err
	}

	delivered, pmid := false, ""
	if s.sender != nil && pending.other != "" {
		if acc := s.accountForWxid(ctx, pending.other); acc != "" {
			summary := fmt.Sprintf("%s：%s", s.userName(ctx, fromWxid), text)
			if mid, serr := s.sender.Send(ctx, acc, pending.other, summary); serr == nil {
				delivered, pmid = true, mid
			}
		}
	}

	reply := fmt.Sprintf("好，跟%s说了。", s.userName(ctx, pending.other))
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO wechat_relay_messages(account_id,bot_wxid,from_wxid,text,route,reply,routed_to,idem_key)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		accountID, botWxid, fromWxid, text, RouteNegoReply, reply, string(sess.State), idemKey); err != nil {
		// 审计落库失败不影响主链路
		_ = err
	}
	return &Result{
		Reply: reply, Route: RouteNegoReply, BotWxid: botWxid,
		PeerWxid: pending.other, PeerDelivered: delivered, PeerMessageID: pmid,
		NegoSessionID: pending.sessionID, NegoState: string(sess.State),
	}, true, nil
}
