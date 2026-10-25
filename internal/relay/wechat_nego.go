package relay

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
	"time"

	"human-agent-im/internal/negotiation"
)

// NegoStore 协商持久化接口（store.NegotiationStore 天然满足）。
type NegoStore interface {
	SaveSession(ctx context.Context, s *negotiation.Session) error
	LoadSession(ctx context.Context, id string) (*negotiation.Session, error)
}

// TempGroupLifecycle 是多 Agent 协商临时群的生命周期能力（v2.0.8）。
// internal/tempgroup.Service 天然满足该接口；nil 时协商仍可进行，但不建临时群。
type TempGroupLifecycle interface {
	// Create 建群并把创建者加入成员。
	Create(ctx context.Context, id, topic, createdBy string) error
	// Join 让 Agent 入群（幂等，重复加入不报错）。
	Join(ctx context.Context, groupID, agentID string) error
	// Post 仅群成员可在群内写入一条消息。
	Post(ctx context.Context, groupID, fromAgent string, payload any) error
	// Destroy 结束协商：群置 closed、清空成员与消息（群头留审计）。
	Destroy(ctx context.Context, groupID string) error
}

// WechatBridge 把微信 peer 消息桥接为 A2A 协商会话（8 态状态机），
// 让"用户↔用户"的 Agent 之间不是裸文本转发，而是可审计、可熔断、可上抛审批的协商。
type WechatBridge struct {
	store NegoStore
	tg    TempGroupLifecycle
}

// NewWechatBridge 构造微信协商桥接；store 为 nil 时协商能力关闭（退化为纯转发）。
func NewWechatBridge(store NegoStore) *WechatBridge {
	if store == nil {
		return nil
	}
	return &WechatBridge{store: store}
}

// WithTempGroup 注入 v2.0.8 协商临时群生命周期：发起协商自动建群并拉双方 Agent 入群，
// 每一轮写入群消息，协商到达终态（COMPLETED/REJECTED/FAILED/CANCELED）后自动销毁；
// 挂起态（AUTH_REQUIRED/INPUT_REQUIRED）保留群，待审批/补充后继续。
func (b *WechatBridge) WithTempGroup(tg TempGroupLifecycle) *WechatBridge {
	if b != nil {
		b.tg = tg
	}
	return b
}

// tempGroupID 由协商会话 ID 确定性派生临时群 ID（无需额外映射表）。
func tempGroupID(sessionID string) string { return "tg_" + sessionID }

// isNegoTerminal 协商是否已到达不可继续的终态（与 negotiation.isEnded 对齐）。
func isNegoTerminal(st negotiation.State) bool {
	switch st {
	case negotiation.StateCompleted,
		negotiation.StateFailed,
		negotiation.StateCanceled,
		negotiation.StateRejected:
		return true
	}
	return false
}

// truncTopic 取正文前缀作为临时群主题。
func truncTopic(body string, n int) string {
	r := []rune(strings.TrimSpace(body))
	if len(r) == 0 {
		return "微信协商"
	}
	if len(r) > n {
		return "微信协商：" + string(r[:n]) + "…"
	}
	return "微信协商：" + string(r)
}

// LoadSession 读取一个协商会话（供自动协商循环复用）。
func (b *WechatBridge) LoadSession(ctx context.Context, sessionID string) (*negotiation.Session, error) {
	if b == nil || b.store == nil {
		return nil, errors.New("relay: negotiation bridge not configured")
	}
	return b.store.LoadSession(ctx, sessionID)
}

// wechatPayload 是挂在协商 Turn.Payload 上的微信上下文。
func wechatPayload(accountID, fromWxid, text string) map[string]any {
	return map[string]any{
		"channel":  "wechat",
		"account":  accountID,
		"fromWxid": fromWxid,
		"text":     text,
		"tsUnix":   time.Now().Unix(),
	}
}

// snChecksum 对 SN 主体生成 3 位数字纠错码（CRC32 取模），供用户口头报号时人眼核对/防抄错。
func snChecksum(seed string) string {
	return fmt.Sprintf("%03d", crc32.ChecksumIEEE([]byte(seed))%1000)
}

// newSessionID 生成可读协商单 SN：用户名_Agent名_yyMMddHHmmss_3位纠错码。
// 例：nego_95f26c5cfca6_o9cq80y8gA_261024121530_734
func newSessionID(accountID, fromBotWxid string) string {
	userCode := strings.TrimSuffix(strings.TrimSuffix(accountID, "-im-bot"), "-im-bot2")
	if len(userCode) > 12 {
		userCode = userCode[:12]
	}
	agentCode := fromBotWxid
	if i := strings.Index(agentCode, "@"); i > 0 {
		agentCode = agentCode[:i]
	}
	if len(agentCode) > 10 {
		agentCode = agentCode[:10]
	}
	ts := time.Now().Format("060102150405")
	// 同一秒内可能连续发起多张协商单，加随机段避免 SN 撞主键（SN 仅后台可见）。
	seed := userCode + "_" + agentCode + "_" + ts + "_" + snRandom()
	return "nego_" + seed + "_" + snChecksum(seed)
}

// snRandom 生成 3 字节随机十六进制（6 位）；失败时退回时间戳尾段。
func snRandom() string {
	b := make([]byte, 3)
	if _, err := crand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()&0xffffff, 16)
	}
	return hex.EncodeToString(b)
}

// OpenProposal 由 fromBotWxid 向 peerWxid 发起一个微信协商（首轮 PROPOSE），
// 状态机推进到 WORKING 并持久化 session + turn。
func (b *WechatBridge) OpenProposal(ctx context.Context, accountID, fromBotWxid, peerWxid, body string) (*negotiation.Session, error) {
	if b == nil || b.store == nil {
		return nil, errors.New("relay: negotiation bridge not configured")
	}
	id := newSessionID(accountID, fromBotWxid)
	sess := negotiation.NewSession(id, negotiation.DefaultMaxRounds, 0)
	t := negotiation.Turn{
		SessionID:   id,
		FromAgentID: fromBotWxid,
		ToAgentID:   peerWxid,
		Intent:      negotiation.IntentPropose,
		Payload:     wechatPayload(accountID, fromBotWxid, body),
	}
	if _, err := sess.Apply(t); err != nil {
		return nil, err
	}
	if err := b.store.SaveSession(ctx, sess); err != nil {
		return nil, err
	}
	b.openTempGroup(ctx, id, fromBotWxid, peerWxid, accountID, body, t.Payload)
	return sess, nil
}

// openTempGroup 在发起协商时创建临时对话群：创建者（发起方 Agent）入群，
// 对方 Agent 入群，并把首轮 PROPOSE 写入群消息。幂等重放同一 session 时不重复建。
func (b *WechatBridge) openTempGroup(ctx context.Context, sessionID, fromBotWxid, peerWxid, accountID, body string, proposalPayload any) {
	if b == nil || b.tg == nil {
		return
	}
	gid := tempGroupID(sessionID)
	// 已存在（重放）则忽略唯一冲突，继续保证成员/消息齐全。
	_ = b.tg.Create(ctx, gid, truncTopic(body, 30), fromBotWxid)
	_ = b.tg.Join(ctx, gid, peerWxid)
	if proposalPayload == nil {
		proposalPayload = wechatPayload(accountID, fromBotWxid, body)
	}
	_ = b.tg.Post(ctx, gid, fromBotWxid, proposalPayload)
}

// Reply 推进一个已存在的微信协商会话。
// responderWxid 为当前回复方（from 或 to），intent 为 ACCEPT/COUNTER/REJECT/REQUEST_INFO/REQUEST_AUTH。
func (b *WechatBridge) Reply(ctx context.Context, sessionID, responderWxid, intentStr, body string) (*negotiation.Session, error) {
	if b == nil || b.store == nil {
		return nil, errors.New("relay: negotiation bridge not configured")
	}
	sess, err := b.store.LoadSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	intent := negotiation.Intent(strings.ToUpper(strings.TrimSpace(intentStr)))
	switch intent {
	case negotiation.IntentAccept, negotiation.IntentCounter, negotiation.IntentReject,
		negotiation.IntentRequestInfo, negotiation.IntentRequestAuth:
	default:
		return nil, fmt.Errorf("relay: invalid nego intent %q", intentStr)
	}
	// 回复方是另一方（对最新一轮的发起者）。
	other := sess.Turns[len(sess.Turns)-1].FromAgentID
	if other == responderWxid {
		if len(sess.Turns) >= 2 {
			other = sess.Turns[len(sess.Turns)-2].FromAgentID
		}
	}
	t := negotiation.Turn{
		SessionID:   sessionID,
		FromAgentID: responderWxid,
		ToAgentID:   other,
		Intent:      intent,
		Payload:     wechatPayload("", responderWxid, body),
	}
	applied, err := sess.Apply(t)
	if err != nil && !errors.Is(err, negotiation.ErrRoundLimit) {
		return nil, err
	}
	if err := b.store.SaveSession(ctx, sess); err != nil {
		return nil, err
	}
	b.advanceTempGroup(ctx, sessionID, responderWxid, t.Payload, applied)
	return sess, nil
}

// advanceTempGroup 把每一轮协商写入临时群：回复方入群（幂等）+ 群消息；
// 一旦协商到达终态（达成/拒绝/失败/取消）即销毁临时群（群头留审计，成员与消息清空）。
// 熔断/审批挂起（INPUT_REQUIRED/AUTH_REQUIRED）不销毁，保留群等待继续。
func (b *WechatBridge) advanceTempGroup(ctx context.Context, sessionID, responderWxid string, turnPayload any, applied negotiation.State) {
	if b == nil || b.tg == nil {
		return
	}
	gid := tempGroupID(sessionID)
	_ = b.tg.Join(ctx, gid, responderWxid)
	_ = b.tg.Post(ctx, gid, responderWxid, turnPayload)
	if isNegoTerminal(applied) {
		_ = b.tg.Destroy(ctx, gid)
	}
}
