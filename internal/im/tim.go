// Package im 腾讯云 IM 接入层（v1.2.0-v1.2.4 重做）。
//
// 三条通道物理隔离：
//   - 人类消息走腾讯云 IM REST（v4/openim/sendmsg、group_open_http_svc/send_group_msg），真实 HTTPS 出站；
//   - Agent 协商走自研 WebSocket（internal/stream）；
//   - 审批/审计走事件总线（internal/eventbus，落 audit_events）。
//
// 诚实边界：登录票据 UserSig 本实现是网关 HMAC-SHA256 票据（可验签、可过期、可离线复现），
// 腾讯云官方登录鉴权使用 ECDSA 私有 TLV 格式（TLSSig），对接真实腾讯云 IM 应用时需把
// GenUserSig 替换为官方算法或接入官方 SDK；发送 REST 的请求路径、参数、报文均按腾讯云
// 官方协议实现，配置 TIM_SDK_APP_ID + TIM_SECRET_KEY 后即对真实腾讯云出站。
package im

import (
	"bytes"
	"compress/zlib"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 默认端点与管理员账号、票据有效期。
const (
	defaultEndpoint        = "https://console.tim.qq.com"
	defaultAdminIdentifier = "administrator"
	defaultSigTTL          = 7 * 24 * time.Hour
	webhookSkew            = 300 // 回调时间戳允许偏差（秒），防重放
)

var (
	// ErrNotConfigured 未配置真实腾讯云凭证，不允许假装发送成功。
	ErrNotConfigured = errors.New("tim not configured: set TIM_SDK_APP_ID and TIM_SECRET_KEY")
	// ErrBadSig 票据签名不匹配。
	ErrBadSig = errors.New("bad usersig signature")
	// ErrSigExpired 票据已过期。
	ErrSigExpired = errors.New("usersig expired")
	// ErrWebhookStale 回调时间戳超出允许窗口（重放）。
	ErrWebhookStale = errors.New("webhook timestamp stale or replay")
	// ErrBadWebhookSig 回调签名校验失败。
	ErrBadWebhookSig = errors.New("bad webhook signature")
	// ErrNotMember 发送者不是群成员。
	ErrNotMember = errors.New("sender is not a member of the group")
)

// Config 腾讯云 IM 配置。
type Config struct {
	SDKAppID        int64
	SecretKey       string
	Endpoint        string
	AdminIdentifier string
	HTTPClient      *http.Client // 可注入，便于测试与超时治理
}

// Client 腾讯云 IM 客户端。
type Client struct {
	cfg Config
	mu  sync.Mutex
	rnd *rand.Rand
}

// New 构造客户端；Endpoint/Admin/HTTPClient 提供默认值。
func New(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.AdminIdentifier == "" {
		cfg.AdminIdentifier = defaultAdminIdentifier
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{cfg: cfg, rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// Available 是否已配置真实腾讯云凭证。
func (c *Client) Available() bool {
	return c.cfg.SecretKey != "" && c.cfg.SDKAppID != 0
}

// Mode 返回运行模式：tim-live（已配置真实凭证）/ gateway-hmac（仅本地票据）。
func (c *Client) Mode() string {
	if c.Available() {
		return "tim-live"
	}
	return "gateway-hmac"
}

func (c *Client) secret() string {
	if c.cfg.SecretKey != "" {
		return c.cfg.SecretKey
	}
	return "im2a_local_dev_secret" // 开发模式确定性密钥（仅本地票据，不用于腾讯云）
}

func (c *Client) nonce() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rnd.Int63n(4294967295) + 1
}

// ---- v1.2.1: 登录票据（HMAC-SHA256，可验签 / 可过期）----

// UserSig 网关登录票据。
type UserSig struct {
	UserID    string `json:"userId"`
	Sig       string `json:"userSig"`
	ExpiresAt int64  `json:"expiresAt"`
	Mode      string `json:"mode"`
}

func sigPayload(sdkAppID int64, userID string, expires int64) []byte {
	b, _ := json.Marshal(map[string]any{
		"sdkappid":   sdkAppID,
		"identifier": userID,
		"expires":    expires,
	})
	return b
}

func hmacHex(secret string, data []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// GenUserSig 生成登录票据。
func (c *Client) GenUserSig(userID string) (*UserSig, error) {
	if userID == "" {
		return nil, errors.New("empty userId")
	}
	expires := time.Now().Add(defaultSigTTL).Unix()
	sig := hmacHex(c.secret(), sigPayload(c.cfg.SDKAppID, userID, expires))
	return &UserSig{UserID: userID, Sig: sig, ExpiresAt: expires, Mode: c.Mode()}, nil
}

// VerifyUserSig 校验票据：重算签名并检查过期（真实密码学验证，非固定字符串）。
func (c *Client) VerifyUserSig(userID string, expiresAt int64, sig string) error {
	if time.Now().Unix() > expiresAt {
		return ErrSigExpired
	}
	want := hmacHex(c.secret(), sigPayload(c.cfg.SDKAppID, userID, expiresAt))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return ErrBadSig
	}
	return nil
}

// officialUserSig 生成腾讯云官方 TLSSig v2（与官方 tls-sig-api-v2 字节级一致）。
// 调腾讯云 REST API（v4/openim/*、group_open_http_svc/*）必须用它；上面的 GenUserSig
// 只是网关自造登录票据，仅供 /v1/im/usersig 内部签发与验签，腾讯云不认。
//
// 算法（参考官方 TLSSigAPIv2）：
//  1. HMAC 明文（\n 分隔）：TLS.identifier / TLS.sdkappid / TLS.time / TLS.expire(秒数)
//  2. TLS.sig = base64( HmacSHA256(SecretKey, 明文) 原始字节 )
//  3. 组装 JSON（TLS.ver=2.0 + 上述字段 + TLS.sig）
//  4. gzcompress(JSON)
//  5. URL-safe base64：+ -> *，/ -> -，= -> _
func (c *Client) officialUserSig(identifier string) (string, error) {
	if c.cfg.SecretKey == "" || c.cfg.SDKAppID == 0 {
		return "", ErrNotConfigured
	}
	if identifier == "" {
		return "", errors.New("empty identifier")
	}
	now := time.Now().Unix()
	expireSec := int64(defaultSigTTL.Seconds()) // 7 天，相对秒数
	plaintext := fmt.Sprintf("TLS.identifier:%s\nTLS.sdkappid:%d\nTLS.time:%d\nTLS.expire:%d\n",
		identifier, c.cfg.SDKAppID, now, expireSec)
	mac := hmac.New(sha256.New, []byte(c.cfg.SecretKey))
	mac.Write([]byte(plaintext))
	sigB64 := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	sigJSON, _ := json.Marshal(map[string]any{
		"TLS.ver":        "2.0",
		"TLS.identifier": identifier,
		"TLS.sdkappid":   c.cfg.SDKAppID,
		"TLS.expire":     expireSec,
		"TLS.time":       now,
		"TLS.sig":        sigB64,
	})

	var zb bytes.Buffer
	// PHP gzcompress = zlib/deflate (RFC1950)，不是 gzip 格式；必须用 zlib.NewWriter。
	zw := zlib.NewWriter(&zb)
	if _, err := zw.Write(sigJSON); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	std := base64.StdEncoding.EncodeToString(zb.Bytes())
	return strings.NewReplacer("+", "*", "/", "-", "=", "_").Replace(std), nil
}

// ---- v1.2.2: Webhook 回调验签（HMAC，防伪造 / 防重放）----

// SignWebhook 计算回调签名：hex(HMAC(secret, "<timestamp>."+rawBody))。
func SignWebhook(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhook 校验回调签名与时间戳窗口。
func VerifyWebhook(secret string, timestamp int64, body []byte, sig string) error {
	if secret == "" {
		return errors.New("webhook secret not configured")
	}
	if d := time.Now().Unix() - timestamp; d > webhookSkew || d < -webhookSkew {
		return ErrWebhookStale
	}
	want := SignWebhook(secret, timestamp, body)
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return ErrBadWebhookSig
	}
	return nil
}

// WebhookEvent 腾讯云 IM 第三方回调事件。
type WebhookEvent struct {
	CallbackType string `json:"CallbackType"`
	// 腾讯云真实回调字段为 CallbackCommand / MsgId，做归一兼容。
	CallbackCommand string `json:"CallbackCommand"`
	SdkAppID        int64  `json:"SdkAppID"`
	FromUserID      string `json:"From_Account"`
	ToUserID        string `json:"To_Account"`
	GroupID         string `json:"GroupId"`
	MsgKey          string `json:"MsgKey"`
	MsgID           string `json:"MsgId"`
	MsgBody         []any  `json:"MsgBody"` // 腾讯云 MsgBody 是 element 数组，不是对象
	MsgTime         int64  `json:"MsgTime"`
}

// Valid 校验事件完整性，并把腾讯云真实字段名归一到内部字段。指针接收者以便回写归一结果。
func (e *WebhookEvent) Valid() error {
	if e.CallbackType == "" {
		e.CallbackType = e.CallbackCommand
	}
	if e.MsgKey == "" {
		e.MsgKey = e.MsgID
	}
	if e.CallbackType == "" {
		return errors.New("missing CallbackType/CallbackCommand")
	}
	if e.FromUserID == "" && e.GroupID == "" {
		return errors.New("missing From_Account/GroupId")
	}
	return nil
}

// AppIDMatches 配置了 SDKAppID 时，回调必须来自同一应用。
func (c *Client) AppIDMatches(ev WebhookEvent) bool {
	return c.cfg.SDKAppID == 0 || ev.SdkAppID == 0 || ev.SdkAppID == c.cfg.SDKAppID
}

// ---- 通道与路由（v1.2.3）----

// Channel 三条通道。
const (
	ChannelHumanIM  = "human_im"
	ChannelAgentWS  = "agent_ws"
	ChannelApproval = "approval"
)

// Route 消息路由分流。
func Route(msgType string) string {
	switch msgType {
	case "c2c_text", "c2c_image", "c2c_audio", "group_text", "group_image":
		return ChannelHumanIM
	case "agent_propose", "agent_negotiate", "agent_counter":
		return ChannelAgentWS
	case "approval_card", "approval_decision", "approval_modify":
		return ChannelApproval
	default:
		return ChannelHumanIM
	}
}

// ---- 群聊（v1.2.4）----

// Group 群聊。
type Group struct {
	GroupID   string   `json:"groupId"`
	Name      string   `json:"name"`
	OwnerID   string   `json:"ownerId"`
	Members   []string `json:"members"`
	CreatedAt int64    `json:"createdAt"`
}

// NewGroup 建群（成员去重，owner 置首）。
func NewGroup(groupID, name, owner string, members []string) (*Group, error) {
	if groupID == "" || name == "" || owner == "" {
		return nil, errors.New("groupId/name/owner required")
	}
	all := append([]string{owner}, members...)
	seen := map[string]bool{}
	unique := make([]string, 0, len(all))
	for _, m := range all {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		unique = append(unique, m)
	}
	return &Group{GroupID: groupID, Name: name, OwnerID: owner, Members: unique,
		CreatedAt: time.Now().Unix()}, nil
}

// GroupMessage 持久化的群消息。
type GroupMessage struct {
	ID        int64  `json:"id"`
	GroupID   string `json:"groupId"`
	FromUser  string `json:"from"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"createdAt"`
}

// GroupRepo 群聊存储（由真实 PG 实现，测试可注入）。
type GroupRepo interface {
	Members(groupID string) ([]string, error)
	SaveGroupMessage(m GroupMessage) (int64, error)
}

// Broadcaster 群广播器：校验成员 → 持久化 → 推流给除发送者外的成员。
type Broadcaster struct {
	Repo GroupRepo
	Push func(topic string, payload any)
}

// Broadcast 执行一次群广播。
func (b *Broadcaster) Broadcast(m GroupMessage) (pushed []string, msgID int64, err error) {
	if m.GroupID == "" || m.FromUser == "" {
		return nil, 0, errors.New("groupId/from required")
	}
	members, err := b.Repo.Members(m.GroupID)
	if err != nil {
		return nil, 0, err
	}
	isMember := false
	for _, u := range members {
		if u == m.FromUser {
			isMember = true
			break
		}
	}
	if !isMember {
		return nil, 0, ErrNotMember
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().Unix()
	}
	id, err := b.Repo.SaveGroupMessage(m)
	if err != nil {
		return nil, 0, err
	}
	out := make([]string, 0, len(members))
	for _, u := range members {
		if u == m.FromUser {
			continue
		}
		if b.Push != nil {
			b.Push("group:"+u, map[string]any{
				"type": "group_message", "groupId": m.GroupID, "from": m.FromUser,
				"text": m.Text, "msgId": id, "at": m.CreatedAt,
			})
		}
		out = append(out, u)
	}
	return out, id, nil
}

// ---- v1.2.0: 真实消息发送（腾讯云 REST）----

// SendMessageReq 发送单聊请求。
type SendMessageReq struct {
	FromUserID string `json:"from"`
	ToUserID   string `json:"to"`
	Text       string `json:"text"`
}

// GroupSendReq 发送群消息请求。
type GroupSendReq struct {
	GroupID string `json:"groupId"`
	From    string `json:"from"`
	Text    string `json:"text"`
}

// Receipt 消息回执（携带腾讯云真实 MsgKey）。
type Receipt struct {
	MsgID   string `json:"msgId"`
	MsgKey  string `json:"msgKey,omitempty"`
	From    string `json:"from"`
	To      string `json:"to"`
	GroupID string `json:"groupId,omitempty"`
	At      int64  `json:"at"`
	Channel string `json:"channel"`
	Mode    string `json:"mode"`
}

// 腾讯云 REST 响应。
type timAPIResp struct {
	ActionStatus string `json:"ActionStatus"`
	ErrorCode    int    `json:"ErrorCode"`
	ErrorInfo    string `json:"ErrorInfo"`
	MsgTime      int64  `json:"MsgTime"`
	MsgKey       string `json:"MsgKey"` // C2C sendmsg 才有
	MsgSeq       int64  `json:"MsgSeq"` // 群 send_group_msg 返回消息序号
}

func (c *Client) call(path string, body map[string]any) (*timAPIResp, error) {
	sig, err := c.officialUserSig(c.cfg.AdminIdentifier)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("usersig", sig)
	q.Set("identifier", c.cfg.AdminIdentifier)
	q.Set("sdkappid", strconv.FormatInt(c.cfg.SDKAppID, 10))
	q.Set("contenttype", "json")
	q.Set("random", strconv.FormatInt(c.nonce(), 10))

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost,
		c.cfg.Endpoint+path+"?"+q.Encode(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tim http: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out timAPIResp
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("tim decode (http %d): %v; body=%s", resp.StatusCode, err, string(data))
	}
	if resp.StatusCode != http.StatusOK || out.ActionStatus != "OK" {
		return &out, fmt.Errorf("tim api error: code=%d info=%s", out.ErrorCode, out.ErrorInfo)
	}
	return &out, nil
}

// SendC2C 发送单聊：真实 HTTPS 调腾讯云 v4/openim/sendmsg；未配置凭证返回 ErrNotConfigured（不假装成功）。
func (c *Client) SendC2C(req SendMessageReq) (*Receipt, error) {
	if req.FromUserID == "" || req.ToUserID == "" || req.Text == "" {
		return nil, errors.New("from/to/text required")
	}
	if !c.Available() {
		return nil, ErrNotConfigured
	}
	r := c.nonce()
	out, err := c.call("/v4/openim/sendmsg", map[string]any{
		"SyncOtherMachine": 1,
		"From_Account":     req.FromUserID,
		"To_Account":       req.ToUserID,
		"MsgRandom":        r,
		"MsgBody": []map[string]any{
			{"MsgType": "TIMTextElem", "MsgContent": map[string]any{"Text": req.Text}},
		},
	})
	if err != nil {
		return nil, err
	}
	return &Receipt{
		MsgID: out.MsgKey, MsgKey: out.MsgKey, From: req.FromUserID, To: req.ToUserID,
		At: out.MsgTime, Channel: ChannelHumanIM, Mode: c.Mode(),
	}, nil
}

// SendGroup 发送群消息：真实 HTTPS 调腾讯云 v4/group_open_http_svc/send_group_msg。
func (c *Client) SendGroup(req GroupSendReq) (*Receipt, error) {
	if req.GroupID == "" || req.From == "" || req.Text == "" {
		return nil, errors.New("groupId/from/text required")
	}
	if !c.Available() {
		return nil, ErrNotConfigured
	}
	out, err := c.call("/v4/group_open_http_svc/send_group_msg", map[string]any{
		"GroupId":      req.GroupID,
		"Random":       c.nonce(),
		"From_Account": req.From,
		"MsgBody": []map[string]any{
			{"MsgType": "TIMTextElem", "MsgContent": map[string]any{"Text": req.Text}},
		},
	})
	if err != nil {
		return nil, err
	}
	// 群消息返回 MsgSeq（整数），没有 C2C 的 MsgKey；用 MsgSeq 作为回执 ID。
	receiptID := out.MsgKey
	if receiptID == "" && out.MsgSeq > 0 {
		receiptID = strconv.FormatInt(out.MsgSeq, 10)
	}
	return &Receipt{
		MsgID: receiptID, MsgKey: out.MsgKey, From: req.From, GroupID: req.GroupID,
		At: out.MsgTime, Channel: ChannelHumanIM, Mode: c.Mode(),
	}, nil
}

// ImportAccount 把账号导入腾讯云 IM 账号体系（/v4/im_open_login_svc/account_import）。
// 发 C2C/群消息前，From_Account/To_Account 必须是已导入账号，否则腾讯云返回 70107。
// 重复导入同一 Identifier 腾讯云幂等返回 OK。
func (c *Client) ImportAccount(identifier string, nick string) error {
	if identifier == "" {
		return errors.New("empty identifier")
	}
	if !c.Available() {
		return ErrNotConfigured
	}
	_, err := c.call("/v4/im_open_login_svc/account_import", map[string]any{
		"Identifier": identifier,
		"Nick":       nick,
	})
	return err
}

// CreateGroup 真实建群：/v4/group_open_http_svc/create_group。
// Type=Private（私有群，邀请制），Owner 为群主，members 为初始成员（自动加入）。
// 重复创建同 GroupId 腾讯云返回错误，调用方应先幂等判断。
func (c *Client) CreateGroup(groupID, name, owner string, members []string) error {
	if groupID == "" || name == "" || owner == "" {
		return errors.New("groupId/name/owner required")
	}
	if !c.Available() {
		return ErrNotConfigured
	}
	memberList := make([]map[string]any, 0, len(members))
	for _, m := range members {
		if m != "" && m != owner {
			memberList = append(memberList, map[string]any{"Member_Account": m})
		}
	}
	_, err := c.call("/v4/group_open_http_svc/create_group", map[string]any{
		"Type":          "Private",
		"GroupId":       groupID,
		"Name":          name,
		"Owner_Account": owner,
		"MemberList":    memberList,
	})
	return err
}

// AddGroupMembers 向已有群追加成员：/v4/group_open_http_svc/add_group_member。
func (c *Client) AddGroupMembers(groupID string, members []string) error {
	if groupID == "" || len(members) == 0 {
		return errors.New("groupId/members required")
	}
	if !c.Available() {
		return ErrNotConfigured
	}
	list := make([]map[string]any, 0, len(members))
	for _, m := range members {
		if m != "" {
			list = append(list, map[string]any{"Member_Account": m})
		}
	}
	_, err := c.call("/v4/group_open_http_svc/add_group_member", map[string]any{
		"GroupId":    groupID,
		"MemberList": list,
	})
	return err
}

// ---- v1.2.3: 分发器 ----

// Dispatcher 消息路由器：按 msgType 把消息分发到三条通道。
type Dispatcher struct {
	IMClient   *Client
	WSPublish  func(topic string, payload any) // Agent 通道
	BusPublish func(event string, payload any) // 审批/审计通道
}

// Dispatch 分发一条消息，返回走了哪个通道。
func (d *Dispatcher) Dispatch(msgType string, payload any) (string, error) {
	switch Route(msgType) {
	case ChannelHumanIM:
		if req, ok := payload.(SendMessageReq); ok {
			if _, err := d.IMClient.SendC2C(req); err != nil {
				return ChannelHumanIM, err
			}
		}
		return ChannelHumanIM, nil
	case ChannelAgentWS:
		if d.WSPublish != nil {
			d.WSPublish("agent", payload)
		}
		return ChannelAgentWS, nil
	case ChannelApproval:
		if d.BusPublish != nil {
			d.BusPublish(msgType, payload)
		}
		return ChannelApproval, nil
	}
	return ChannelHumanIM, nil
}

// String 调试输出。
func (c *Client) String() string {
	return fmt.Sprintf("IMClient{sdkAppId=%d, mode=%s, endpoint=%s}", c.cfg.SDKAppID, c.Mode(), c.cfg.Endpoint)
}
