// SPDX-License-Identifier: MIT
//
// Bridge 是内核侧投递面的装配点（B2-b）：把 delivery.Store（发号/日志/位点）与
// connapi EventSink（embedded connhub 或 remote UDS）接成一条完整链路。
//
// 链路语义（B2B-DESIGN §0 三条硬约束）：
//
//	Publish:    im_user_delivery_seq 发号 + im_delivery_log 写日志（同一事务）→ commit
//	            → sink.Deliver(userID, frames)   （未提交的 seq 绝不投递）
//	AckUp:      客户端字节级 ack → conn_epoch 校验 → Store.Ack（GREATEST 单调，先写位点）
//	ResumeReq:  epoch 校验 → PlanReplay（2000帧/2MiB/7天三上限）→
//	            返回 ResumeResp + 补发帧（Remote 保证「先 ResumeResp 再帧」）
//	ConnOpen:   EnsureDevice（新设备 epoch=0）→ 签发 resumeToken + epoch
//
// 注意：`DeliverAck.accepted=true` 不代表客户端已收到（ring 满/WS 写失败的那批不会出现在
// AckUp 里），所以 Publish 之后**不能**把日志裁掉——只能靠 AckUp 推进位点后由 Trim 裁。
package connapi

import (
	"context"
	"errors"
	"fmt"
	"log"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/connapi/delivery"
	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

// PublishFrame 是一条待投递帧的输入（尚未发号）。
type PublishFrame struct {
	Topic    string // 可选：内核事件 topic（≤15B），网关不解析
	Payload  []byte // 不透明业务负载（网关不解析）
	Critical bool   // true=不可丢（MSG/KICK/ERR）；false=可丢（EVT/PRESENCE）
	MsgType  uint8  // delivery.MsgType*；0 时按 Critical 推断（critical→MSG，否则 EVT）
}

// PresenceSink 是内核 presence 状态机的最小接口（C1）。实现方在
// internal/kernel/native（presence 只在内核，网关不维护状态机），connapi 只做转发，
// 避免 connapi → native 反向依赖。
type PresenceSink interface {
	// OnLease 消费网关上行租约（kind=26），返回 false 表示拒绝（如非法 state）。
	OnLease(connproto.PresenceLease) bool
	// OnConnClosed 连接关闭：注销该 conn 的租约（offline 由 TTL 推断，不由客户端上报）。
	OnConnClosed(connproto.ConnClosed)
	// OnAck 客户端字节级 ack = 连接活着（ack 即活，续租不需要额外心跳）。
	OnAck(userID int64, deviceID string)
}

// Bridge 内核侧投递面。并发安全依赖底层 Store/EventSink。
type Bridge struct {
	store  delivery.Store
	sink   EventSink
	log    *log.Logger
	now    func() int64
	pageN  int // 单页帧数（Resume 补发分页）
	pageB  int // 单页字节数
	limits delivery.ReplayLimits

	authSecret string       // 非空=C1：对 ConnOpen.Token 做 JWT 验签并以其 sub 为权威 userID
	presence   PresenceSink // 可选：C1 presence 状态机（nil=未启用）
}

// NewBridge 构造 Bridge；sink 可在 Wire/NewRemote 之后用 SetSink 注入（避免构造顺序问题）。
func NewBridge(store delivery.Store, logger *log.Logger) *Bridge {
	if logger == nil {
		logger = log.Default()
	}
	return &Bridge{
		store:  store,
		log:    logger,
		now:    func() int64 { return 0 },
		pageN:  delivery.DefaultPageFrames,
		pageB:  delivery.DefaultPageBytes,
		limits: delivery.DefaultReplayLimits(),
	}
}

// SetSink 注入投递出口（embedded hub 或 remote UDS）。
func (b *Bridge) SetSink(sink EventSink) { b.sink = sink }

// SetJWTSecret 启用 C1 连接身份校验：ConnOpen.Token 必须是有效 JWT，其 sub 为权威 userID。
// 空串 = 关闭（保持 B2 行为：信任网关给的 UserID，仅用于滚动兼容/测试）。
func (b *Bridge) SetJWTSecret(secret string) { b.authSecret = secret }

// SetPresence 注入 presence 状态机（C1）；nil 表示不启用（租约消息被消费但无状态）。
func (b *Bridge) SetPresence(p PresenceSink) { b.presence = p }

// Wire 把本 Bridge 的回调塞进 RemoteConfig 并返回（调用方随后 NewRemote(cfg) 得 sink，
// 再 SetSink）。embedded 模式直接用 NewEmbedded(hub)，无需 Wire。
func (b *Bridge) Wire(cfg RemoteConfig) RemoteConfig {
	cfg.OnAckUp = b.OnAckUp
	cfg.OnResume = b.OnResume
	cfg.OnConnOpen = b.OnConnOpen
	cfg.OnConnClosed = b.OnConnClosed
	cfg.OnPresenceLease = b.OnPresenceLease
	return cfg
}

// Publish 发号+写日志（同一事务）后，把帧投递给 sink；返回投递结果。
// 返回 error 时未发生投递（日志已提交的帧仍可通过 resume 补发）。
func (b *Bridge) Publish(ctx context.Context, userID int64, frames []PublishFrame) (connhub.DeliverResult, error) {
	if userID <= 0 {
		return connhub.DeliverResult{}, delivery.ErrInvalidArgument
	}
	if len(frames) == 0 {
		return connhub.DeliverResult{}, nil
	}
	appends := make([]delivery.AppendFrame, len(frames))
	for i, f := range frames {
		msgType := f.MsgType
		if msgType == 0 {
			if f.Critical {
				msgType = delivery.MsgTypeMSG
			} else {
				msgType = delivery.MsgTypeEVT
			}
		}
		var flags uint8
		if f.Critical {
			flags |= delivery.FlagCritical
		}
		appends[i] = delivery.AppendFrame{MsgType: msgType, Flags: flags, Payload: f.Payload}
	}
	entries, err := b.store.Append(ctx, userID, appends)
	if err != nil {
		return connhub.DeliverResult{}, fmt.Errorf("connapi: append delivery log: %w", err)
	}
	// 事务已提交；现在才允许投递。
	wire := make([]connproto.Frame, len(entries))
	for i, e := range entries {
		f := connproto.Frame{
			Type:    connproto.Type(e.MsgType),
			Flags:   connproto.Flags(e.Flags),
			Seq:     e.Seq,
			Payload: e.Payload,
		}
		if i < len(frames) {
			f.Topic = frames[i].Topic
		}
		wire[i] = f
	}
	if b.sink == nil {
		return connhub.DeliverResult{}, nil // 无出口（embedded 未接线）：日志已持久化，可补发
	}
	return b.sink.Deliver(userID, wire), nil
}

// OnAckUp 处理客户端累积确认：epoch 校验 → 先写位点（GREATEST）→ 才允许回执。
func (b *Bridge) OnAckUp(au connproto.AckUp) bool {
	if au.DeviceID == "" || au.UserID <= 0 {
		return false
	}
	ctx := context.Background()
	cur, ok, err := b.store.Cursor(ctx, au.DeviceID)
	if err != nil {
		b.log.Printf("[connapi] WARN: cursor lookup device=%s: %v (ack dropped)", au.DeviceID, err)
		return false
	}
	if ok && cur.ConnEpoch != au.ConnEpoch {
		b.log.Printf("[connapi] WARN: drop AckUp epoch mismatch device=%s stored=%d got=%d (旧连接污染防护)",
			au.DeviceID, cur.ConnEpoch, au.ConnEpoch)
		return false
	}
	if !ok && au.ConnEpoch != 0 {
		b.log.Printf("[connapi] WARN: drop AckUp for unknown device=%s epoch=%d (无 ConnOpen 记录)", au.DeviceID, au.ConnEpoch)
		return false
	}
	if _, err := b.store.Ack(ctx, au.UserID, au.DeviceID, au.AckSeq); err != nil {
		b.log.Printf("[connapi] WARN: persist cursor device=%s seq=%d: %v (ack NOT acknowledged)", au.DeviceID, au.AckSeq, err)
		return false
	}
	// C1：ack 即活——续租 presence（在内核侧，不由客户端额外心跳）。
	if b.presence != nil {
		b.presence.OnAck(au.UserID, au.DeviceID)
	}
	return true
}

// OnConnOpen 为连接签发 epoch/resumeToken（新设备 epoch=0）。
//
// C1：SetJWTSecret 非空时以 JWT sub 为权威 userID（网关透传 hello.auth_token，
// 不验签、不解析）；ConnOpenAck.UserID 回显权威值，网关用它替换会话身份，
// **忽略 URL query / 报文里的 UserID**。未启用校验时保持 B2 兼容行为（信任 op.UserID）。
func (b *Bridge) OnConnOpen(op connproto.ConnOpen) connproto.ConnOpenAck {
	if op.DeviceID == "" {
		return connproto.ConnOpenAck{ConnID: op.ConnID, OK: false, Reason: "bad_conn_open"}
	}
	userID := op.UserID
	if b.authSecret != "" {
		if op.Token == "" {
			return connproto.ConnOpenAck{ConnID: op.ConnID, OK: false, Reason: "auth_required"}
		}
		claims, err := auth.Parse(b.authSecret, op.Token)
		if err != nil {
			reason := "invalid_token"
			if errors.Is(err, auth.ErrExpired) {
				reason = "token_expired"
			}
			b.log.Printf("[connapi] WARN: ConnOpen JWT rejected device=%s: %v", op.DeviceID, err)
			return connproto.ConnOpenAck{ConnID: op.ConnID, OK: false, Reason: reason}
		}
		if claims.Sub <= 0 {
			return connproto.ConnOpenAck{ConnID: op.ConnID, OK: false, Reason: "invalid_token_sub"}
		}
		userID = claims.Sub // 权威身份：不信任 op.UserID/query
	}
	if userID <= 0 {
		return connproto.ConnOpenAck{ConnID: op.ConnID, OK: false, Reason: "user_required"}
	}
	cur, err := b.store.EnsureDevice(context.Background(), userID, op.DeviceID)
	if err != nil {
		b.log.Printf("[connapi] WARN: ensure device=%s: %v", op.DeviceID, err)
		return connproto.ConnOpenAck{ConnID: op.ConnID, OK: false, Reason: "cursor_unavailable"}
	}
	// 必须回显 ConnID：网关用 openWait[ConnID] 关联 ack 与 WS 连接；不回显会导致
	// 并发建连时 ack 与 conn 错配（A/B 身份互换）。这是 C4 flake 的根因（C1 遗留）。
	return connproto.ConnOpenAck{
		ConnID:      op.ConnID,
		OK:          true,
		UserID:      userID, // C1：权威 userID 回显（B2 老网关忽略此字段即可）
		ResumeToken: resumeToken(userID, op.DeviceID, cur.ConnEpoch),
		Epoch:       cur.ConnEpoch,
	}
}

// OnConnClosed 网关上报连接关闭：交给 presence 注销该 conn 的租约。
// offline 由内核按 TTL 推断，网关/客户端不直接上报 offline（防伪造）。
func (b *Bridge) OnConnClosed(cl connproto.ConnClosed) {
	if b.presence != nil {
		b.presence.OnConnClosed(cl)
	}
}

// OnPresenceLease 消费网关上行租约（kind=26）。未注入 presence 时仅消费不建模。
// 返回 false 也不会让网关重试（租约是易失信息，下一拍会重发）。
func (b *Bridge) OnPresenceLease(l connproto.PresenceLease) bool {
	if b.presence == nil {
		return true
	}
	if l.UserID <= 0 || l.DeviceID == "" {
		return false
	}
	return b.presence.OnLease(l)
}

// DeliverEphemeral 直接投递易失帧（presence 等）：**不发号、不写 im_delivery_log**，
// 客户端离线即丢（presence 重放没有意义）。
func (b *Bridge) DeliverEphemeral(userID int64, frames []connproto.Frame) connhub.DeliverResult {
	if userID <= 0 || len(frames) == 0 || b.sink == nil {
		return connhub.DeliverResult{}
	}
	return b.sink.Deliver(userID, frames)
}

// OnResume 生成补发计划与补发帧（先 ResumeResp 再帧的顺序由 remote.go 保证）。
func (b *Bridge) OnResume(ctx context.Context, req connproto.ResumeReq) (connproto.ResumeResp, []connproto.Frame) {
	plan, err := b.store.PlanReplay(ctx, delivery.ReplayRequest{
		UserID:    req.UserID,
		DeviceID:  req.DeviceID,
		ConnEpoch: req.ConnEpoch,
		FromSeq:   req.LastAckSeq,
		Limits:    b.limits,
	})
	if err != nil {
		reason := "resume_failed"
		if err == delivery.ErrEpochMismatch {
			reason = "epoch_mismatch"
		}
		b.log.Printf("[connapi] WARN: resume rejected user=%d device=%s epoch=%d: %v", req.UserID, req.DeviceID, req.ConnEpoch, err)
		return connproto.ResumeResp{ConnID: req.ConnID, OK: false, Reason: reason, Epoch: plan.Epoch}, nil
	}
	resp := connproto.ResumeResp{
		ConnID:          req.ConnID, // 回显给网关，gateway 靠它匹配等待中的 WS session
		OK:              plan.OK,
		From:            plan.From,
		To:              plan.To,
		Replayed:        plan.Replayed,
		Truncated:       plan.Truncated,
		OldestAvailable: plan.OldestAvailable,
		DroppedBefore:   plan.DroppedBefore,
		Epoch:           plan.Epoch,
	}
	if !plan.OK || plan.Replayed == 0 {
		return resp, nil
	}
	// 分页取回补发帧（帧内容取自日志，seq 即位点；客户端按 seq 去重）。
	frames := make([]connproto.Frame, 0, plan.Replayed)
	from := plan.From - 1
	for len(frames) < int(plan.Replayed) {
		page, err := b.store.Page(ctx, req.UserID, from, plan.To, b.pageN, b.pageB)
		if err != nil {
			b.log.Printf("[connapi] WARN: resume page user=%d from=%d: %v", req.UserID, from, err)
			return connproto.ResumeResp{ConnID: req.ConnID, OK: false, Reason: "resume_page_failed", Epoch: plan.Epoch}, nil
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			frames = append(frames, connproto.Frame{
				Type:    connproto.Type(e.MsgType),
				Flags:   connproto.Flags(e.Flags),
				Seq:     e.Seq,
				Payload: e.Payload,
			})
		}
		from = page[len(page)-1].Seq
	}
	resp.Replayed = uint32(len(frames))
	if len(frames) > 0 {
		resp.To = frames[len(frames)-1].Seq
	}
	return resp, frames
}

// BumpEpoch 踢人/解绑：让旧 resumeToken 立即失效。
func (b *Bridge) BumpEpoch(ctx context.Context, userID int64, deviceID string) (int64, error) {
	return b.store.BumpEpoch(ctx, userID, deviceID)
}

// Store 暴露底层存储（metrics/管理面用）。
func (b *Bridge) Store() delivery.Store { return b.store }

// resumeToken 生成内核侧不透明 token（B2-b 只做 epoch 绑定校验，格式不作为契约）。
func resumeToken(userID int64, deviceID string, epoch int64) string {
	return fmt.Sprintf("rt-%d-%s-%d", userID, deviceID, epoch)
}
