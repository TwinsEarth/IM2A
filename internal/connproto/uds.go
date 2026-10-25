// Package connproto —— UDS 帧格式（内核 ⇄ 连接网关）。
//
// 这是 B2-b 唯一的跨进程契约实现：**内核侧（internal/connapi）与网关侧（cmd/conn-gateway）
// 都必须用这里的 `UDSConn` / 各 payload 结构，不要各自再写一套 framing**。
// 设计依据：B2B-DESIGN.md §4（背压四层）与 CONN-GATEWAY-DESIGN.md v2 §1.5。
//
// 外壳（6 字节头，网络序）：
//
//	len   uint32  含 kind+flags 的 body 长度（即 2+len(body)），上限 1MiB
//	kind  uint8   消息类型
//	flags uint8   保留（当前全 0；未知位接收方必须忽略而不是断开）
//	body  N       kind=DeliverReq 时是二进制；其余为 JSON
//
// 选择"信封 + kind"而不是纯长度前缀：单条 UDS 流上要跑十几种消息，kind 放在固定偏移能
// 让两端都做零拷贝分派；body 用 JSON 是刻意的——控制面低频、可读、加字段零成本；
// 唯一高频的 DeliverReq 用二进制（帧本身已有 24B 定长头 + length prefix）。
//
// ── 版本兼容策略（必须遵守）───────────────────────────────────────────────
// 内核与网关是两个独立进程、可独立升级。`UDSProtoVersion=1` 的规则：
//   * **只允许加字段**（JSON omitempty / 二进制尾部追加），不允许改已定义字段的语义、
//     不允许改 kind 编号；未知 kind 必须忽略而不是断开（除 Hello 外）。
//   * 任何**语义**变化都必须 bump `UDSProtoVersion`，并在 Hello/HelloAck 里协商：
//     版本不匹配 → 网关回 `HelloAck{accepted:false, reason:"..."}` 并关闭连接；
//     内核收到 accepted=false 必须进 degraded 并计数，**不许静默继续**。
//   * 旧端读到新字段 = 忽略；新端读到旧消息 = 用默认值（零值语义必须与旧行为一致）。
//
// ── DeliverReq 的批次上限（两个约束取小，勿再各自解释）─────────────────────
//   * 帧数上限 `UDSMaxBatch=4096`；
//   * body 上限 `UDSMaxBody=1MiB`（含 18B 批头与每帧 4B 长度前缀）。
//   有效上限 = min(4096 帧, 1MiB body)。典型值：256B 帧 ≈ 4096 帧（≈1.07MiB，略超则切批）；
//   4KiB 帧 ≈ 250 帧。**超限不报错，用 `SplitDeliverReq` 自动切批**，调用方按 len>1 计
//   一次 `batch_split` 指标（两侧规则一致，见 B2B-DESIGN §4）。
//
// ── C1 增量（2026-10-25，task-21）─────────────────────────────────────────
// 纯增量，**协议版本仍为 v1**（v1 规则：只加字段/只加新 kind；未知 kind 旧端忽略）：
//   * `ConnOpen.Token`（网关透传客户端 hello.auth_token）+ `ConnOpenAck.UserID`（内核验签后权威身份）；
//   * 新 kind 26 `PRESENCE_LEASE`（网关→内核，上行租约）、27 `PRESENCE_STATE`（内核→网关，下行 fanout）。
//   presence 状态机/聚合在内核；网关只上报"socket 活着"与 fanout 投递。
//
// ── AckUp 的语义（最容易两侧理解不一致，写死）─────────────────────────────
//   * AckUp **只由客户端字节级确认驱动**（客户端 → 网关 `ack{seq}` → 网关 → 内核 AckUp）。
//   * `connhub` 的"写成功"ack（OnAck）**不会**产生 AckUp：它只用于网关本地统计/日志。
//     因此"帧已被网关 accepted，但 WS 写失败/连接断"不会出现在 AckUp 里 —— 那批帧仍在
//     位点之后，内核会重发（客户端按 seq 去重）。
//   * 内核收到 AckUp 后必须 `GREATEST(acked_seq)` 落 `im_device_cursor`（device 级主键），
//     并且**先写位点再回执**；epoch 不匹配的 AckUp 直接丢弃（防旧连接污染新位点）。
package connproto

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// UDS 帧常量。
const (
	// UDSProtoVersion 是**协议版本**（Hello 协商），不是帧格式版本。
	// 加字段不升版本；改语义/改 kind 编号必须升版本。
	UDSProtoVersion uint16 = 1

	UDSHeaderLen = 6       // len(4) + kind(1) + flags(1)
	UDSMaxBody   = 1 << 20 // 1MiB：单条 UDS 消息 body 上限
	UDSMaxBatch  = 4096    // DeliverReq 帧数上限；有效上限 = min(UDSMaxBatch, body 能装下的帧数)

	// UDSDefaultMaxFrameBytes 网关接受的单帧字节上限（与 connproto.MaxFrame 不同：
	// MaxFrame 是协议理论上限，这里是**部署口径**，由 Hello 协商下发）。
	UDSDefaultMaxFrameBytes uint32 = 64 << 10 // 64KiB
)

// UDSKind UDS 消息类型。
type UDSKind uint8

const (
	// 1x 握手/健康
	KindHello     UDSKind = 1  // 内核→网关：版本、实例 id
	KindHelloAck  UDSKind = 2  // 网关→内核
	// 10x 投递
	KindDeliverReq UDSKind = 10 // 内核→网关：一批帧（二进制 body）
	KindDeliverAck UDSKind = 11 // 网关→内核：该批回执（JSON）
	KindAckUp      UDSKind = 12 // 网关→内核：客户端累积确认（JSON）
	// 2x 连接/恢复
	KindResumeReq   UDSKind = 20 // 网关→内核：请求补发计划（JSON）
	KindResumeResp  UDSKind = 21 // 内核→网关：补发计划（JSON）
	KindConnOpen    UDSKind = 22 // 网关→内核：连接建立（JSON）
	KindConnOpenAck UDSKind = 25 // 内核→网关：签发 resumeToken/epoch（JSON）
	KindConnClosed  UDSKind = 23 // 网关→内核：连接关闭（JSON）
	KindKick        UDSKind = 24 // 内核→网关：踢人（JSON）
	// 26/27 presence（C1 增量；状态机在内核，网关只上报租约 / 本地 fanout）
	KindPresenceLease UDSKind = 26 // 网关→内核：连接活跃租约（上行，单向）
	KindPresenceState UDSKind = 27 // 内核→网关：聚合后的 presence 状态（下行，网关只 fanout）
	// 3x 指标
	KindStatsReq  UDSKind = 30
	KindStatsResp UDSKind = 31
	// 4x 心跳
	KindPing UDSKind = 40
	KindPong UDSKind = 41
	// 5x 生命周期
	// KindDrain 网关→内核：网关即将退出，请**立刻停止**再向本网关投递新帧；
	// 未 ack 的帧留在位点补发路径上。Drain 之后网关仍会继续处理 AckUp（否则位点会退）。
	KindDrain UDSKind = 50
)

func (k UDSKind) String() string {
	switch k {
	case KindHello:
		return "HELLO"
	case KindHelloAck:
		return "HELLO_ACK"
	case KindDeliverReq:
		return "DELIVER_REQ"
	case KindDeliverAck:
		return "DELIVER_ACK"
	case KindAckUp:
		return "ACK_UP"
	case KindResumeReq:
		return "RESUME_REQ"
	case KindResumeResp:
		return "RESUME_RESP"
	case KindConnOpen:
		return "CONN_OPEN"
	case KindConnOpenAck:
		return "CONN_OPEN_ACK"
	case KindConnClosed:
		return "CONN_CLOSED"
	case KindKick:
		return "KICK"
	case KindPresenceLease:
		return "PRESENCE_LEASE"
	case KindPresenceState:
		return "PRESENCE_STATE"
	case KindStatsReq:
		return "STATS_REQ"
	case KindStatsResp:
		return "STATS_RESP"
	case KindPing:
		return "PING"
	case KindPong:
		return "PONG"
	case KindDrain:
		return "DRAIN"
	default:
		return fmt.Sprintf("KIND(%d)", uint8(k))
	}
}

var (
	ErrUDSBadLen   = errors.New("connproto/uds: bad frame length")
	ErrUDSTooLarge = errors.New("connproto/uds: frame exceeds UDSMaxBody")
	ErrUDSUnknown  = errors.New("connproto/uds: unknown kind")
)

// EncodeUDS 把一个 UDS 帧追加编码到 dst（返回新切片）。
// 调用方负责保证 body <= UDSMaxBody。
func EncodeUDS(dst []byte, kind UDSKind, flags byte, body []byte) ([]byte, error) {
	if len(body) > UDSMaxBody {
		return dst, ErrUDSTooLarge
	}
	n := 2 + len(body)
	var hdr [UDSHeaderLen]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(n))
	hdr[4] = byte(kind)
	hdr[5] = flags
	dst = append(dst, hdr[:]...)
	dst = append(dst, body...)
	return dst, nil
}

// DecodeUDSHeader 解析 6 字节头，返回 (总长, kind, flags)。
func DecodeUDSHeader(h []byte) (int, UDSKind, byte, error) {
	if len(h) < UDSHeaderLen {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint32(h[0:4]))
	if n < 2 {
		return 0, 0, 0, ErrUDSBadLen
	}
	if n > UDSMaxBody+2 {
		return 0, 0, 0, ErrUDSTooLarge
	}
	return n, UDSKind(h[4]), h[5], nil
}

// UDSConn 双向 UDS 消息读写（内核侧与网关侧共用）。
// 读：ReadMsg；写：WriteMsg（内部互斥，可多 goroutine 调）。
type UDSConn struct {
	c  net.Conn
	r  *bufio.Reader
	w  *bufio.Writer
	mu sync.Mutex // 串行化写（帧不可交错）
}

// NewUDSConn 包装一个已建立的连接。
func NewUDSConn(c net.Conn) *UDSConn {
	return &UDSConn{c: c, r: bufio.NewReaderSize(c, 64<<10), w: bufio.NewWriterSize(c, 64<<10)}
}

// ReadMsg 读取一条完整消息。返回 kind、flags、body。
// body 是本次分配的新切片（生命周期独立于读缓冲）。
func (u *UDSConn) ReadMsg() (UDSKind, byte, []byte, error) {
	var hdr [UDSHeaderLen]byte
	if _, err := io.ReadFull(u.r, hdr[:]); err != nil {
		return 0, 0, nil, err
	}
	n, kind, flags, err := DecodeUDSHeader(hdr[:])
	if err != nil {
		return 0, 0, nil, err
	}
	bodyLen := n - 2
	if bodyLen == 0 {
		return kind, flags, nil, nil
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(u.r, body); err != nil {
		return 0, 0, nil, err
	}
	return kind, flags, body, nil
}

// WriteMsg 写一条消息并 flush（批量由上层控制：一次 DeliverReq 已经是一批帧）。
func (u *UDSConn) WriteMsg(kind UDSKind, body []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	frame, err := EncodeUDS(nil, kind, 0, body)
	if err != nil {
		return err
	}
	if _, err := u.w.Write(frame); err != nil {
		return err
	}
	return u.w.Flush()
}

// WriteBatch 一次 write/flush 写多条消息（用于补发：ResumeResp 必须与随后的补发帧在同一
// 次 flush 之前保持顺序，但"必须先写 ResumeResp"由调用方顺序保证，见 B2B-DESIGN §3.3）。
func (u *UDSConn) WriteBatch(msgs []UDSEgress) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	var buf []byte
	for _, m := range msgs {
		var err error
		buf, err = EncodeUDS(buf, m.Kind, m.Flags, m.Body)
		if err != nil {
			return err
		}
	}
	if _, err := u.w.Write(buf); err != nil {
		return err
	}
	return u.w.Flush()
}

// UDSEgress 一条待发消息（WriteBatch 用）。
type UDSEgress struct {
	Kind  UDSKind
	Flags byte
	Body  []byte
}

// SetReadDeadline / SetDeadline 透传（心跳超时用）。
func (u *UDSConn) SetReadDeadline(t time.Time) error  { return u.c.SetReadDeadline(t) }
func (u *UDSConn) SetWriteDeadline(t time.Time) error { return u.c.SetWriteDeadline(t) }

// Close 关闭底层连接。
func (u *UDSConn) Close() error { return u.c.Close() }

// -----------------------------------------------------------------------------
// Body 编解码
// -----------------------------------------------------------------------------

// EncodeJSONBody 控制类消息统一用 JSON（低频、可读、加字段零成本）。
func EncodeJSONBody(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("connproto/uds: marshal body: %w", err)
	}
	if len(b) > UDSMaxBody {
		return nil, ErrUDSTooLarge
	}
	return b, nil
}

// DecodeJSONBody 解析 JSON body；未知字段忽略（前向兼容）。
func DecodeJSONBody(b []byte, v any) error {
	if len(b) == 0 {
		return nil // 允许空 body（例如 Ping）
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("connproto/uds: unmarshal body: %w", err)
	}
	return nil
}

// Hello / HelloAck（协议版本协商） -------------------------------------------

// UDSHello 内核→网关的第一条消息。**必须带协议版本**：两端可独立升级，版本不匹配必须
// 显式拒绝，而不是让旧端把新 body 解析成垃圾。
type UDSHello struct {
	ProtoVersion   uint16 `json:"proto_version"`
	Instance       string `json:"instance,omitempty"`
	MaxFrameBytes  uint32 `json:"max_frame_bytes,omitempty"`  // 内核侧单帧上限
	MaxBatchFrames uint16 `json:"max_batch_frames,omitempty"` // 内核侧单批帧数上限
}

// UDSHelloAck 网关→内核。Accepted=false 时内核必须进 degraded（并计数），不许继续投递。
type UDSHelloAck struct {
	ProtoVersion   uint16 `json:"proto_version"`
	Accepted       bool   `json:"accepted"`
	Reason         string `json:"reason,omitempty"` // 例："kernel proto v2, gateway supports v1"
	Instance       string `json:"instance,omitempty"`
	StartedAt      int64  `json:"started_at,omitempty"`
	MaxFrameBytes  uint32 `json:"max_frame_bytes,omitempty"`
	MaxBatchFrames uint16 `json:"max_batch_frames,omitempty"`
}

// NegotiateUDS 版本协商：只接受完全相等的 ProtoVersion（v1 不做向下兼容读取，
// 因为 v1 还没有历史存量；将来升 v2 时在这里实现 v1↔v2 的字段映射）。
func NegotiateUDS(in UDSHello) UDSHelloAck {
	ack := UDSHelloAck{
		ProtoVersion:   UDSProtoVersion,
		Instance:       in.Instance,
		MaxFrameBytes:  UDSDefaultMaxFrameBytes,
		MaxBatchFrames: UDSMaxBatch,
	}
	if in.ProtoVersion != UDSProtoVersion {
		ack.Accepted = false
		ack.Reason = fmt.Sprintf("kernel proto v%d, gateway supports v%d", in.ProtoVersion, UDSProtoVersion)
		return ack
	}
	ack.Accepted = true
	// 取双方更保守的部署口径（帧上限、批上限），避免大帧把 body 顶爆。
	if in.MaxFrameBytes > 0 && in.MaxFrameBytes < ack.MaxFrameBytes {
		ack.MaxFrameBytes = in.MaxFrameBytes
	}
	if in.MaxBatchFrames > 0 && in.MaxBatchFrames < ack.MaxBatchFrames {
		ack.MaxBatchFrames = in.MaxBatchFrames
	}
	return ack
}

// Drain 网关→内核：网关即将退出（SIGTERM）。内核收到后应立即停止向**该网关**投递新帧，
// 把未 ack 的帧留在位点补发路径上；Drain 之后网关仍会继续处理 AckUp。
type Drain struct {
	DeadlineMS int64 `json:"deadline_ms"`
}

// DeliverReq（二进制 body）---------------------------------------------------

// DeliverReq 一批投递。二进制布局（大端）：
//
//	userID    int64
//	batchID   uint64
//	n         uint16
//	n × { frameLen uint32 | frame bytes }
//
// frame bytes 就是 connproto.Encode 的输出（24B 头 + topic + payload）。
type DeliverReq struct {
	UserID  int64
	BatchID uint64
	Frames  []Frame
}

// EncodeDeliverReq 编码二进制 body。
func EncodeDeliverReq(r *DeliverReq) ([]byte, error) {
	if r == nil {
		return nil, errors.New("connproto/uds: nil DeliverReq")
	}
	if len(r.Frames) > UDSMaxBatch {
		return nil, fmt.Errorf("connproto/uds: too many frames in batch (%d > %d)", len(r.Frames), UDSMaxBatch)
	}
	body := make([]byte, 0, 18+len(r.Frames)*64)
	var hdr [18]byte
	binary.BigEndian.PutUint64(hdr[0:8], uint64(r.UserID))
	binary.BigEndian.PutUint64(hdr[8:16], r.BatchID)
	binary.BigEndian.PutUint16(hdr[16:18], uint16(len(r.Frames)))
	body = append(body, hdr[:]...)
	for i := range r.Frames {
		fb, err := Encode(nil, &r.Frames[i])
		if err != nil {
			return nil, fmt.Errorf("connproto/uds: encode frame %d: %w", i, err)
		}
		var lb [4]byte
		binary.BigEndian.PutUint32(lb[:], uint32(len(fb)))
		body = append(body, lb[:]...)
		body = append(body, fb...)
		if len(body) > UDSMaxBody {
			return nil, ErrUDSTooLarge
		}
	}
	return body, nil
}

// DecodeDeliverReq 解码二进制 body。
func DecodeDeliverReq(b []byte) (*DeliverReq, error) {
	if len(b) < 18 {
		return nil, ErrUDSBadLen
	}
	r := &DeliverReq{
		UserID:  int64(binary.BigEndian.Uint64(b[0:8])),
		BatchID: binary.BigEndian.Uint64(b[8:16]),
	}
	n := int(binary.BigEndian.Uint16(b[16:18]))
	if n > UDSMaxBatch {
		return nil, fmt.Errorf("connproto/uds: batch too large (%d)", n)
	}
	off := 18
	r.Frames = make([]Frame, 0, n)
	for i := 0; i < n; i++ {
		if off+4 > len(b) {
			return nil, ErrUDSBadLen
		}
		fl := int(binary.BigEndian.Uint32(b[off : off+4]))
		off += 4
		if fl <= 0 || off+fl > len(b) {
			return nil, ErrUDSBadLen
		}
		f, consumed, err := Decode(b[off : off+fl])
		if err != nil {
			return nil, fmt.Errorf("connproto/uds: decode frame %d: %w", i, err)
		}
		if consumed != fl {
			return nil, ErrUDSBadLen
		}
		off += fl
		r.Frames = append(r.Frames, f)
	}
	return r, nil
}

// SplitDeliverReq 把一批帧按 **min(UDSMaxBatch, UDSMaxBody)** 切成多批（lead 评审要求：
// 超限不报错，自动切批；调用方按 len(result)>1 计一次 batch_split 指标）。
//
// 注意：返回的每个分片拥有**独立的 BatchID**（= 入参 BatchID + 分片序号），调用方必须用
// 返回值里的 BatchID 去匹配 DeliverAck，不能沿用入参 BatchID。
func SplitDeliverReq(r *DeliverReq) ([]*DeliverReq, error) {
	if r == nil {
		return nil, errors.New("connproto/uds: nil DeliverReq")
	}
	out := make([]*DeliverReq, 0, 1)
	cur := &DeliverReq{UserID: r.UserID, BatchID: r.BatchID}
	size := 18 // userID+userID+batchID+n
	for i := range r.Frames {
		fb, err := Encode(nil, &r.Frames[i])
		if err != nil {
			return nil, fmt.Errorf("connproto/uds: split encode frame %d: %w", i, err)
		}
		need := 4 + len(fb)
		if 18+need > UDSMaxBody {
			return nil, fmt.Errorf("connproto/uds: single frame (%d B) exceeds body budget", len(fb))
		}
		if len(cur.Frames) > 0 && (size+need > UDSMaxBody || len(cur.Frames) >= UDSMaxBatch) {
			out = append(out, cur)
			cur = &DeliverReq{UserID: r.UserID, BatchID: r.BatchID + uint64(len(out))}
			size = 18
		}
		cur.Frames = append(cur.Frames, r.Frames[i])
		size += need
	}
	out = append(out, cur)
	return out, nil
}

// MaxFramesForBody 给定平均帧字节数，返回单批能装下的帧数（供内核侧预估批大小）。
func MaxFramesForBody(avgFrameBytes int) int {
	if avgFrameBytes <= 0 {
		return UDSMaxBatch
	}
	budget := UDSMaxBody - 18
	n := budget / (4 + avgFrameBytes)
	if n > UDSMaxBatch {
		n = UDSMaxBatch
	}
	if n < 1 {
		n = 1
	}
	return n
}

// 其余控制类 body（JSON）------------------------------------------------------

// DeliverAck 网关→内核：一批的处理结果。
// 语义：只有 `Accepted` 的帧才可能被 ack；`DroppedSlow` 的帧**不会**产生 ack，内核应保留
// 在日志里等待重发（B2B-DESIGN §4.1 铁律：任何一层丢弃都不 ack）。
type DeliverAck struct {
	BatchID     uint64 `json:"batch_id"`
	UserID      int64  `json:"user_id"`
	TargetConns int    `json:"target_conns"`
	Accepted    int    `json:"accepted"`
	DroppedSlow int    `json:"dropped_slow"`
	QueuedBytes int    `json:"queued_bytes"`
}

// AckUp 网关→内核：客户端累积确认（**只由客户端 ack 驱动**，不是写成功回调）。
//
// 语义：客户端已确认收到 `AckSeq` 及之前的全部帧。内核据此 `GREATEST` 落 device 级位点，
// **先写位点再回执**；`ConnEpoch` 与内核当前 epoch 不一致时该 AckUp 必须被丢弃。
type AckUp struct {
	ConnID    uint64 `json:"conn_id"`
	UserID    int64  `json:"user_id"`
	DeviceID  string `json:"device_id"`
	ConnEpoch int64  `json:"conn_epoch"`
	AckSeq    uint64 `json:"ack_seq"`
}

// ResumeReq 网关→内核：客户端重连，请求补发计划。
type ResumeReq struct {
	ConnID      uint64 `json:"conn_id"`
	UserID      int64  `json:"user_id"`
	DeviceID    string `json:"device_id"`
	ResumeToken string `json:"resume_token,omitempty"`
	LastAckSeq  uint64 `json:"last_ack_seq"`
	ConnEpoch   int64  `json:"conn_epoch"`
}

// ResumeResp 内核→网关：补发计划（是否可续传 + 补发区间 + 缺口告知）。
type ResumeResp struct {
	ConnID          uint64 `json:"conn_id,omitempty"` // 回显请求里的 connID（网关据此匹配等待者）
	OK              bool   `json:"ok"`
	Reason          string `json:"reason,omitempty"` // epoch_mismatch | token_invalid | no_log | ...
	From            uint64 `json:"from,omitempty"`
	To              uint64 `json:"to,omitempty"`
	Replayed        uint32 `json:"replayed,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
	OldestAvailable uint64 `json:"oldest_available,omitempty"`
	DroppedBefore   uint64 `json:"dropped_before,omitempty"`
	Epoch           int64  `json:"epoch,omitempty"`
}

// ConnOpen 网关→内核：一条原生连接建立。
//
// C1 身份来源变更（纯增量，向后兼容）：
//   - 老路径：网关用 `?user_id=` 填 `UserID`，内核信任（B2 行为）；
//   - 新路径：网关把客户端 hello 里的 `auth_token` 原样放进 `Token`，`UserID` 可为 0；
//     内核验 JWT 后以 `ConnOpenAck.UserID` 返回**权威 userID**，网关据此替换会话身份。
//   - 网关**不做任何身份判断**（不验签、不解析 JWT），只透传。
type ConnOpen struct {
	ConnID   uint64 `json:"conn_id"`
	UserID   int64  `json:"user_id,omitempty"`
	DeviceID string `json:"device_id"`
	Token    string `json:"token,omitempty"`
}

// ConnOpenAck 内核→网关：验签结果 + 签发 resumeToken/epoch（网关不自己签、不自己判身份）。
type ConnOpenAck struct {
	ConnID uint64 `json:"conn_id,omitempty"` // 回显请求里的 connID
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	// UserID 是内核验 JWT 后的**权威 userID**（C1 新增）。OK=true 时网关用它替换会话身份；
	// 为 0 表示内核未启用身份校验（向后兼容：网关沿用 ConnOpen.UserID / query）。
	UserID      int64  `json:"user_id,omitempty"`
	ResumeToken string `json:"resume_token,omitempty"`
	Epoch       int64  `json:"epoch,omitempty"`
}

// ConnClosed 网关→内核：连接关闭（内核据此清理 presence/游标依据）。
type ConnClosed struct {
	ConnID   uint64 `json:"conn_id"`
	UserID   int64  `json:"user_id"`
	DeviceID string `json:"device_id"`
	Code     uint32 `json:"code"`
	Reason   string `json:"reason,omitempty"`
	LastAck  uint64 `json:"last_ack"`
}

// Kick 内核→网关：踢人（单连接或全设备）。
type Kick struct {
	UserID     int64  `json:"user_id"`
	ConnID     uint64 `json:"conn_id,omitempty"`
	AllDevices bool   `json:"all_devices,omitempty"`
	Code       string `json:"code,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// PresenceLease 网关→内核：连接活跃租约（上行）。
//
// 谁是权威：**网关只知道"socket 还活着"**，所以只上报租约（心跳/写成功/降级均可触发续租）；
// presence 状态机、TTL 过期判定、多设备聚合**全在内核**（C1 的规定）。
// `State` 是网关侧的粗粒度推断（online/away），内核可以覆盖或忽略。
type PresenceLease struct {
	ConnID   uint64 `json:"conn_id"`
	UserID   int64  `json:"user_id"`
	DeviceID string `json:"device_id"`
	TTLS     int64  `json:"ttl_s"`
	AtMS     int64  `json:"at_ms"`
	State    string `json:"state,omitempty"` // online | away（网关推断，内核可覆盖）
}

// PresenceState 内核→网关：某 user 聚合后的 presence 状态（下行）。
// 网关**不做状态机**，只做本地 fanout；投递目标按 userID 寻址（B2B §5：网关不认 topic）。
type PresenceState struct {
	UserID    int64  `json:"user_id"`
	State     string `json:"state"` // online | away | offline
	UpdatedAt int64  `json:"updated_at"`
	Devices   int    `json:"devices,omitempty"` // 聚合时的在线设备数（0 = 未知）
}

// StatsResp 内核→网关：内核侧指标（网关 /metrics 透出 kernel_ack_lag_seconds 等）。
type StatsResp struct {
	AckLagSeconds float64 `json:"ack_lag_seconds,omitempty"`
	FramesIn      uint64  `json:"frames_in,omitempty"`
	FramesOut     uint64  `json:"frames_out,omitempty"`
	Dropped       uint64  `json:"dropped,omitempty"`
	SlowKicks     uint64  `json:"slow_kicks,omitempty"`
}
