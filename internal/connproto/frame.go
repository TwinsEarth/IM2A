// Package connproto 实现 conn-gateway 与客户端之间的帧编解码。
//
// 设计依据：CONN-GATEWAY-DESIGN.md §3（v2）。
//   * 控制帧：JSON（低频、字段可变、便于抓包/灰度/审计），≤8KiB；
//   * 数据帧：24 字节定长头 + topic + payload（高频、二进制、零分配解析）。
//
// 与设计文档的一处**实现期修订**（已记录在文档 §3.2 的 errata 里）：
//   设计表 offset 6..8 原写作 `reserved`（必须为 0），但"24B 定长头 + 变长 payload"
//   必须有一个长度字段才能流式解码。B1 把它改成 `payload_len uint16`（网络序），
//   于是单帧 payload 上限为 65535；原设计的 256KiB 需要 header v2（4B 长度），
//   不在 B1 范围。好处是零新增字段、头长不变、解码仍可零分配。
//
// 帧布局（全部大端）：
//
//	 0..2   magic     0x49 0x4D ("IM")
//	 2      ver       0x01
//	 3      flags     bit0 critical / bit1 compressed(保留) / bit2 final / bit3 has_topic
//	 4      type      1=MSG 2=EVT 3=PRESENCE 4=KICK 5=PING 6=PONG 7=ERR
//	 5      topic_len 0..15
//	 6..8   payload_len uint16
//	 8..16  seq       uint64（服务端→客户端投递序号；客户端→服务端填 0）
//	 16..24 ack       uint64（客户端→服务端累积确认；服务端→客户端 msgID 低 64 位）
//	 24..   topic（topic_len 字节，UTF-8）
//	 之后   payload（payload_len 字节）
package connproto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// Magic0/Magic1 帧首两字节。
	Magic0 byte = 0x49 // 'I'
	Magic1 byte = 0x4D // 'M'

	// VersionV1 帧格式版本（只有新增 type / 改布局才升）。
	VersionV1 byte = 0x01

	// HeaderLen 数据帧定长头长度。
	HeaderLen = 24

	// MaxTopicLen topic 最大长度（头部 topic_len 是 uint8，但业务上限制 15）。
	MaxTopicLen = 15

	// MaxPayload 单帧 payload 上限（payload_len uint16）。
	MaxPayload = 65535

	// MaxFrame 单帧最大字节数（头 + topic + payload）。
	MaxFrame = HeaderLen + MaxTopicLen + MaxPayload

	// MaxControlLen 控制帧 JSON 上限（设计 §3.3：超过直接 4001）。
	MaxControlLen = 8 * 1024
)

// Type 数据帧类型。
type Type uint8

const (
	TypeMsg      Type = 1 // 业务消息（payload 不透明）——不可丢
	TypeEvt      Type = 2 // 事件（approval/trust/agent progress）——慢消费者优先丢
	TypePresence Type = 3 // 在线状态变更——可合并
	TypeKick     Type = 4 // 踢下线——不可丢
	TypePing     Type = 5
	TypePong     Type = 6
	TypeErr      Type = 7
)

func (t Type) String() string {
	switch t {
	case TypeMsg:
		return "MSG"
	case TypeEvt:
		return "EVT"
	case TypePresence:
		return "PRESENCE"
	case TypeKick:
		return "KICK"
	case TypePing:
		return "PING"
	case TypePong:
		return "PONG"
	case TypeErr:
		return "ERR"
	default:
		return fmt.Sprintf("TYPE(%d)", uint8(t))
	}
}

// Flags 帧标志位。
type Flags uint8

const (
	FlagCritical   Flags = 1 << 0 // 不可丢弃（MSG/KICK/ERR）
	FlagCompressed Flags = 1 << 1 // 保留
	FlagFinal      Flags = 1 << 2 // 分片末帧
	FlagHasTopic   Flags = 1 << 3 // 头部后带 topic
)

// allowedFlags 是 v1 允许的全部标志位；其余位一律判非法（前向兼容靠升 ver，不靠保留位）。
const allowedFlags = FlagCritical | FlagCompressed | FlagFinal | FlagHasTopic

// 编解码错误（全部可判别，便于网关按错误类型回不同 close code）。
var (
	ErrTruncated       = errors.New("connproto: truncated frame")
	ErrBadMagic        = errors.New("connproto: bad magic")
	ErrBadVersion      = errors.New("connproto: unsupported version")
	ErrBadFlags        = errors.New("connproto: illegal flags")
	ErrTopicTooLong    = errors.New("connproto: topic too long")
	ErrPayloadTooLarge = errors.New("connproto: payload too large")
	ErrBadType         = errors.New("connproto: unknown data frame type")
	ErrControlTooLarge = errors.New("connproto: control frame too large")
	ErrBadControl      = errors.New("connproto: malformed control frame")
	ErrEmptyControl    = errors.New("connproto: empty control type")
	ErrNilFrame        = errors.New("connproto: nil frame")
)

// Frame 一帧数据。
type Frame struct {
	Type    Type
	Flags   Flags
	Seq     uint64
	Ack     uint64
	Topic   string
	Payload []byte
}

// Critical 是否不可丢弃（也可由 Flags 显式指定）。
func (f Frame) Critical() bool { return f.Flags&FlagCritical != 0 }

// Encode 把 f 追加编码到 dst 并返回新切片（复用 dst 可避免分配）。
func Encode(dst []byte, f *Frame) ([]byte, error) {
	if f == nil {
		return dst, ErrNilFrame
	}
	if len(f.Payload) > MaxPayload {
		return dst, ErrPayloadTooLarge
	}
	if len(f.Topic) > MaxTopicLen {
		return dst, ErrTopicTooLong
	}
	flags := f.Flags
	if len(f.Topic) > 0 {
		flags |= FlagHasTopic
	} else {
		flags &^= FlagHasTopic
	}
	if flags&^allowedFlags != 0 {
		return dst, ErrBadFlags
	}

	start := len(dst)
	dst = append(dst, make([]byte, HeaderLen+len(f.Topic))...)
	b := dst[start:]
	b[0], b[1] = Magic0, Magic1
	b[2] = VersionV1
	b[3] = byte(flags)
	b[4] = byte(f.Type)
	b[5] = byte(len(f.Topic))
	binary.BigEndian.PutUint16(b[6:8], uint16(len(f.Payload)))
	binary.BigEndian.PutUint64(b[8:16], f.Seq)
	binary.BigEndian.PutUint64(b[16:24], f.Ack)
	copy(b[HeaderLen:], f.Topic)
	dst = append(dst, f.Payload...)
	return dst, nil
}

// Decode 解出一帧，返回 (frame, 本帧总字节数, error)。
// 支持一次传入多帧（循环调用，按 consumed 前进）。
func Decode(b []byte) (Frame, int, error) {
	var f Frame
	if len(b) < HeaderLen {
		return f, 0, ErrTruncated
	}
	if b[0] != Magic0 || b[1] != Magic1 {
		return f, 0, ErrBadMagic
	}
	if b[2] != VersionV1 {
		return f, 0, ErrBadVersion
	}
	flags := Flags(b[3])
	if flags&^allowedFlags != 0 {
		return f, 0, ErrBadFlags
	}
	tlen := int(b[5])
	if tlen > MaxTopicLen {
		return f, 0, ErrTopicTooLong
	}
	plen := int(binary.BigEndian.Uint16(b[6:8]))
	total := HeaderLen + tlen + plen
	if len(b) < total {
		return f, 0, ErrTruncated
	}
	f.Type = Type(b[4])
	f.Flags = flags
	f.Seq = binary.BigEndian.Uint64(b[8:16])
	f.Ack = binary.BigEndian.Uint64(b[16:24])
	if tlen > 0 {
		f.Topic = string(b[HeaderLen : HeaderLen+tlen])
	}
	if plen > 0 {
		// 复制：解码结果的生命周期独立于读缓冲（读缓冲会被 gorilla 复用）。
		f.Payload = append([]byte(nil), b[HeaderLen+tlen:total]...)
	}
	return f, total, nil
}

// ValidType 判断数据帧类型是否在 v1 已知集合内（未知 type 一律丢帧并计数）。
func ValidType(t Type) bool {
	switch t {
	case TypeMsg, TypeEvt, TypePresence, TypeKick, TypePing, TypePong, TypeErr:
		return true
	default:
		return false
	}
}

// -----------------------------------------------------------------------------
// 控制帧（JSON）
// -----------------------------------------------------------------------------

// 控制帧类型名（与设计 §3.3 一致）。
const (
	CtlHello            = "hello"
	CtlHelloOK          = "hello_ok"
	CtlAuthToken        = "auth_token"
	CtlAuthChallengeReq = "auth_challenge_req"
	CtlAuthChallenge    = "auth_challenge"
	CtlAuthChallengeRes = "auth_challenge_resp"
	CtlAuthOK           = "auth_ok"
	CtlSubscribe        = "subscribe"
	CtlResume           = "resume"
	CtlResumeOK         = "resume_ok"
	CtlAck              = "ack"
	CtlPing             = "ping"
	CtlPong             = "pong"
	CtlErr              = "err"
	CtlKick             = "kick"
	CtlBye              = "bye"
)

// Limits 服务端在 hello_ok 里下发的连接限制。
type Limits struct {
	MaxFrame       int `json:"max_frame"`
	MaxConnsPerUser int `json:"max_conns_per_user"`
	HeartbeatS     int `json:"heartbeat_s"`
	PongWaitS      int `json:"pong_wait_s"`
}

// ControlFrame 控制帧。用一个结构体承载全部控制帧，避免 map[string]any 的类型断言地狱；
// 未用字段 omitempty，未知字段由 json 解码忽略（前向兼容）。
type ControlFrame struct {
	T  string `json:"t"`
	ID uint64 `json:"id,omitempty"`

	// hello / hello_ok
	V           int      `json:"v,omitempty"`
	ProtoMin    string   `json:"proto_min,omitempty"`
	ProtoMax    string   `json:"proto_max,omitempty"`
	Proto       string   `json:"proto,omitempty"`
	Features    []string `json:"features,omitempty"`
	ServerTime  int64    `json:"server_time,omitempty"`
	Limits      *Limits  `json:"limits,omitempty"`
	ResumeToken string   `json:"resume_token,omitempty"`
	LastAckSeq  uint64   `json:"last_ack_seq,omitempty"`
	DeviceID    string   `json:"device_id,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`

	// auth
	Token string `json:"token,omitempty"`
	Nonce string `json:"nonce,omitempty"`
	Sig   string `json:"sig,omitempty"`
	Alg   string `json:"alg,omitempty"`
	TS    int64  `json:"ts,omitempty"`
	Exp   int64  `json:"exp,omitempty"`
	UserID int64 `json:"user_id,omitempty"`
	ConnID uint64 `json:"conn_id,omitempty"`

	// resume 扩展字段（B2-b，纯增量，旧客户端忽略即可）
	//   oldest_available：服务端当前还能补发的最早 seq
	//   dropped_before ：[lastAck+1, oldest_available-1] 区间永久缺失的帧数（显式告知截断）
	//   conn_epoch     ：连接纪元；与内核签发值不匹配时拒绝 resume（要求全量重连）
	OldestAvailable uint64 `json:"oldest_available,omitempty"`
	DroppedBefore   uint64 `json:"dropped_before,omitempty"`
	ConnEpoch       int64  `json:"conn_epoch,omitempty"`

	// subscribe / resume / ack / ping
	Topics     []string `json:"topics,omitempty"`
	From       uint64   `json:"from,omitempty"`
	To         uint64   `json:"to,omitempty"`
	Replayed   uint32   `json:"replayed,omitempty"`
	Truncated  bool     `json:"truncated,omitempty"`
	Seq        uint64   `json:"seq,omitempty"`
	N          uint64   `json:"n,omitempty"`

	// 错误/踢人（复用 errcode 的三段式文案）
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	NextStep string `json:"next_step,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// EncodeControl 编码控制帧并做长度校验。
func EncodeControl(c *ControlFrame) ([]byte, error) {
	if c == nil {
		return nil, ErrBadControl
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadControl, err)
	}
	if len(b) > MaxControlLen {
		return nil, ErrControlTooLarge
	}
	return b, nil
}

// DecodeControl 解析控制帧；未知 t 允许（前向兼容），但 t 为空/JSON 坏/超长一律拒绝。
func DecodeControl(b []byte) (*ControlFrame, error) {
	if len(b) > MaxControlLen {
		return nil, ErrControlTooLarge
	}
	var c ControlFrame
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadControl, err)
	}
	if c.T == "" {
		return nil, ErrEmptyControl
	}
	return &c, nil
}
