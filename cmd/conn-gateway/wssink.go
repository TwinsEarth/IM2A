package main

// wssink.go：连接网关的**写路径**。
//
// 设计约束（CONN-GATEWAY-DESIGN.md v2 §4.4，最容易翻车的一处）：
//   * 读路径用 gorilla（它负责握手/unmask/分片/close 语义）；
//   * **写路径完全不用 gorilla**：gorilla 不暴露 writev，一帧一次 syscall 在 1 万帧/s 时
//     是纯浪费；而且 gorilla 的自动 pong / close 回复会与我们的写并发操作同一 socket。
//   * 既然绕开 gorilla 写，就必须自己拼 RFC6455 帧头（服务端→客户端**不 mask**），
//     再用 net.Buffers 一次 writev 把一批帧发出去。
//
// 三条必须同时成立的规则（任何一条破了都会帧交错/连接损坏）：
//   1. `SetPingHandler(func(string) error { return nil })` —— 关掉 gorilla 自动回 pong；
//   2. `SetCloseHandler(func(int, string) error { return nil })` —— 关掉 gorilla 自动回 close；
//   3. 关闭时直接 `raw.Close()`，**不调用** gorilla 的 Close()（它会写 close 帧）。
//
// 静态检查：cmd/conn-gateway/static_guard_test.go 会断言本文件不含任何 gorilla 写调用，
// 且上面两条 handler 置空确实存在。改这个文件前先跑它。

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"human-agent-im/internal/connproto"
)

var errSinkClosed = errors.New("wssink: closed")

const (
	wsOpBinary = 0x2
	wsOpText   = 0x1
)

// WSSink 把 connhub.Sink 实现为"裸 net.Conn + 自拼 WS 帧"。
type WSSink struct {
	ws  *websocket.Conn
	raw net.Conn

	mu       sync.Mutex // 串行化所有写（writev 批次之间不能交错）
	closed   atomic.Bool
	deadline time.Duration

	writes atomic.Uint64
	bytes  atomic.Uint64
}

// noopWriteBufferPool 让 gorilla **不为每条连接预分配 2KB 写缓冲**（task-24）。
//
// 依据：gorilla `newConn` 里 `if writeBuf == nil && writeBufferPool == nil { writeBuf = make([]byte, writeBufferSize+maxFrameHeaderSize) }`
// —— 只要 `WriteBufferPool != nil` 就不预分配；而网关的稳态写路径是**裸 net.Conn + writev**，
// 只用 gorilla 读，`Conn.WriteMessage` 只在握手失败分支用一次。因此这里的池永远不应被 Get；
// 真被 Get 到说明有人绕过裸写路径，返回 nil 会让 gorilla 自行退化为直写（仍正确，只是没缓冲）。
type noopWriteBufferPool struct{}

// gorilla 的 BufferPool 接口是 Get()/Put(interface{})（返回值为 []byte 的切片）。
func (noopWriteBufferPool) Get() interface{} { return nil }
func (noopWriteBufferPool) Put(interface{})  {}

// NewWSSink 接管一条已升级的连接。必须在任何读循环启动前调用（置空 handler）。
func NewWSSink(ws *websocket.Conn) *WSSink {
	// 规则 1 & 2：禁止 gorilla 在读循环里顺手写 socket。
	ws.SetPingHandler(func(string) error { return nil })
	ws.SetCloseHandler(func(int, string) error { return nil })
	raw := ws.UnderlyingConn()
	// 读写缓冲调小：设计 §9.3 的 25KB/连接预算里给 gorilla 只留 2KB。
	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(2048)
		_ = tc.SetWriteBuffer(2048)
	}
	return &WSSink{ws: ws, raw: raw, deadline: 30 * time.Second}
}

// WriteFrames 是 connhub 唯一允许的写入口：一批帧 → 一批 WS binary message → 一次 writev。
func (s *WSSink) WriteFrames(frames []connproto.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return errSinkClosed
	}
	if len(frames) == 0 {
		return nil
	}
	bufs := make(net.Buffers, 0, len(frames)*2)
	for i := range frames {
		payload, err := connproto.Encode(nil, &frames[i])
		if err != nil {
			// 编码失败属于调用方 bug（超长 payload 等）；返回错误让 hub 断开，绝不静默吞。
			return err
		}
		bufs = append(bufs, wsHeader(wsOpBinary, len(payload)), payload)
	}
	if err := s.flush(bufs); err != nil {
		return err
	}
	s.writes.Add(uint64(len(frames)))
	return nil
}

// WriteRawText 用于握手阶段的 JSON 控制帧（hello_ok/auth_ok/pong）。
// 每个 part 是一条独立的 WS text message。
func (s *WSSink) WriteRawText(parts ...[]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return errSinkClosed
	}
	bufs := make(net.Buffers, 0, len(parts)*2)
	for _, p := range parts {
		bufs = append(bufs, wsHeader(wsOpText, len(p)), p)
	}
	return s.flush(bufs)
}

// flush 一次 writev（net.Buffers.WriteTo 在多数平台会退化为 writev）。
func (s *WSSink) flush(bufs net.Buffers) error {
	_ = s.raw.SetWriteDeadline(time.Now().Add(s.deadline))
	n, err := bufs.WriteTo(s.raw)
	s.bytes.Add(uint64(n))
	return err
}

// Close 直接关 TCP：不走 gorilla 的 close 帧路径（规则 3）。
func (s *WSSink) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		return s.raw.Close()
	}
	return nil
}

// Stats 写路径统计（/healthz 之外的自检用）。
func (s *WSSink) Stats() (writes, bytes uint64) {
	return s.writes.Load(), s.bytes.Load()
}

// wsHeader 拼一个服务端→客户端的 WS 帧头（FIN=1，不 mask）。
// 长度编码：<126 单字节；≤65535 用 126+uint16；更大用 127+uint64。
func wsHeader(opcode byte, n int) []byte {
	switch {
	case n < 126:
		return []byte{0x80 | opcode, byte(n)}
	case n <= 65535:
		return []byte{0x80 | opcode, 126, byte(n >> 8), byte(n)}
	default:
		return []byte{0x80 | opcode, 127,
			0, 0, 0, 0,
			byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}
