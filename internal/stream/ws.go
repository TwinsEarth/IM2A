// Package stream 提供 WebSocket 双向流（v1.1.0-v1.1.4）。
//
// 并发模型（v1.1 审查重做修正）：
//   - gorilla/websocket 要求每个连接同一时刻只能有一个 writer。旧版 Broadcast、
//     读循环回显、cleanup 探活三处会并发写同一连接，生产环境会随机
//     panic("concurrent write to websocket connection")。现每个连接用 writeMu 串行化所有写。
//   - 每个连接有独立的 ping ticker（周期 < pongWait），服务端主动发 Ping；
//     对端回 Pong 后由 PongHandler 续读超时。对端不回 Pong（拔网线/卡死）时
//     读超时触发读循环退出，defer 注销连接——这是死连接的权威清理路径。
//   - cleanupLoop 每 cleanupInterval 做一次显式探活：ping 写失败（连接已断）立即
//     close，使读循环尽快退出并注销；写成功但不回 pong 的交给读超时兜底。
package stream

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 5 * time.Second,
	CheckOrigin:      func(r *http.Request) bool { return true },
}

// MessageHandler 处理客户端发来的消息，返回要广播的响应。
type MessageHandler func(negotiationID string, incoming map[string]any) (any, error)

// client 是一个 WebSocket 连接的并发安全封装。
type client struct {
	hub   *Hub
	topic string
	conn  *websocket.Conn
	wmu   sync.Mutex // 串行化该连接的所有写（gorilla 禁止并发写）
	once  sync.Once
	done  chan struct{}
}

func (c *client) write(messageType int, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.conn.WriteMessage(messageType, data)
}

func (c *client) writeJSON(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.conn.WriteJSON(v)
}

func (c *client) ping() error {
	return c.write(websocket.PingMessage, nil)
}

// close 幂等关闭连接并停止 ping ticker；读循环会随之退出并在 defer 中注销。
func (c *client) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// Hub 管理所有活跃的 WebSocket 连接。
type Hub struct {
	mu       sync.RWMutex
	conns    map[string][]*client
	handlers map[string]MessageHandler

	// B2-c：可选广播镜像（standalone/dual 把内核事件镜像到 connapi 投递链路）。
	// 默认 nil = 不镜像（embedded 生产路径行为逐字节不变）。
	// 镜像回调在 Broadcast 调用方 goroutine 同步执行，实现方必须快速返回
	// （bootstrap 侧用有界队列 + worker，绝不在此做 DB/网络 IO）。
	mirrorMu sync.RWMutex
	mirror   func(topic string, msg any)

	pongWait        time.Duration // 读超时：超过该时长未收到任何消息/Pong 则判定死连接
	pingInterval    time.Duration // 服务端主动发 Ping 的周期（必须 < pongWait）
	cleanupInterval time.Duration // 主动探活/清理扫描周期
}

// NewHub 使用生产默认心跳（60s 读超时 / 54s ping / 30s 清理）。
func NewHub() *Hub {
	return NewHubWithIntervals(60*time.Second, 54*time.Second, 30*time.Second)
}

// NewHubWithIntervals 允许自定义心跳/清理间隔（测试用于缩短时间）。
func NewHubWithIntervals(pongWait, pingInterval, cleanupInterval time.Duration) *Hub {
	if pongWait <= 0 {
		pongWait = 60 * time.Second
	}
	if pingInterval <= 0 || pingInterval >= pongWait {
		pingInterval = pongWait * 9 / 10
	}
	if cleanupInterval <= 0 {
		cleanupInterval = 30 * time.Second
	}
	h := &Hub{
		conns:           map[string][]*client{},
		handlers:        map[string]MessageHandler{},
		pongWait:        pongWait,
		pingInterval:    pingInterval,
		cleanupInterval: cleanupInterval,
	}
	go h.cleanupLoop()
	return h
}

// RegisterHandler 为指定 topic 注册消息处理器。
func (h *Hub) RegisterHandler(nid string, fn MessageHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers[nid] = fn
}

// SetMirror 设置广播镜像回调（B2-c）；nil 关闭。默认未设置。
//
// 语义：每次 Broadcast(topic, msg) 在完成本地 fanout 后，若 mirror != nil 则同 goroutine
// 调用 mirror(topic, msg)。embedded 生产路径不设置（零行为变化）；standalone/dual 由
// bootstrap 设为事件 tap（仅做非阻塞入队，见 internal/kernel/bootstrap/connmirror.go）。
func (h *Hub) SetMirror(fn func(topic string, msg any)) {
	h.mirrorMu.Lock()
	h.mirror = fn
	h.mirrorMu.Unlock()
}

// HasMirror 报告当前是否设置了广播镜像（自检/测试用）。
func (h *Hub) HasMirror() bool {
	h.mirrorMu.RLock()
	defer h.mirrorMu.RUnlock()
	return h.mirror != nil
}

// Broadcast 向指定 topic 的所有连接推送消息（每个连接的写均经 writeMu 串行化）。
func (h *Hub) Broadcast(nid string, msg any) {
	data, _ := json.Marshal(msg)
	h.mu.RLock()
	clients := make([]*client, len(h.conns[nid]))
	copy(clients, h.conns[nid])
	h.mu.RUnlock()
	for _, c := range clients {
		// 写失败不立即移除（可能只是瞬时缓冲满），由读超时/探活兜底清理。
		_ = c.write(websocket.TextMessage, data)
	}
	// B2-c：镜像给跨进程投递链路（nil 时不做任何事，行为与 Wave A 完全一致）。
	// 放在本地 fanout 之后：本地订阅者语义不受镜像影响；镜像实现必须非阻塞。
	h.mirrorMu.RLock()
	mirror := h.mirror
	h.mirrorMu.RUnlock()
	if mirror != nil {
		mirror(nid, msg)
	}
}

// Stats 返回当前连接统计（v1.1.4）。
func (h *Hub) Stats() map[string]int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	total := 0
	topics := map[string]int{}
	for id, cs := range h.conns {
		topics[id] = len(cs)
		total += len(cs)
	}
	topics["_total"] = total
	return topics
}

// cleanupLoop 周期性显式探活：ping 写失败的连接立即关闭（读循环随后注销）。
func (h *Hub) cleanupLoop() {
	ticker := time.NewTicker(h.cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.RLock()
		var all []*client
		for _, cs := range h.conns {
			all = append(all, cs...)
		}
		h.mu.RUnlock()

		dead := 0
		for _, c := range all {
			select {
			case <-c.done:
				dead++
				continue
			default:
			}
			if err := c.ping(); err != nil {
				dead++
				log.Printf("[ws] topic=%s: probe failed, closing: %v", c.topic, err)
				c.close() // 读循环退出 → defer 注销
			}
		}
		if dead > 0 {
			log.Printf("[ws] cleanup scan closed %d dead conns", dead)
		}
	}
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c.topic] = append(h.conns[c.topic], c)
}

func (h *Hub) unregister(c *client) {
	c.close()
	h.mu.Lock()
	defer h.mu.Unlock()
	cs := h.conns[c.topic]
	// 仅在持锁内做就地过滤，register/unregister 均在同一把锁下，无并发改写。
	out := cs[:0]
	for _, x := range cs {
		if x != c {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		delete(h.conns, c.topic)
	} else {
		h.conns[c.topic] = out
	}
}

// HandleWS 处理 WebSocket 升级请求。
func (h *Hub) HandleWS(nid string, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	c := &client{hub: h, topic: nid, conn: conn, done: make(chan struct{})}

	// v1.1.4：读限制 + 读超时（pongWait）+ Pong 续命。
	conn.SetReadLimit(64 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(h.pongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(h.pongWait))
		return nil
	})

	h.register(c)
	defer h.unregister(c)

	// v1.1.4：每连接独立 Ping ticker（周期 < pongWait），写经 writeMu 串行化。
	go func() {
		ticker := time.NewTicker(h.pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-ticker.C:
				if err := c.ping(); err != nil {
					c.close()
					return
				}
			}
		}
	}()

	if err := c.writeJSON(map[string]any{
		"type": "connected", "negotiationId": nid, "ts": time.Now().Unix(),
	}); err != nil {
		return
	}

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(h.pongWait))

		var in map[string]any
		if json.Unmarshal(msg, &in) != nil {
			continue
		}

		h.mu.RLock()
		fn := h.handlers[nid]
		h.mu.RUnlock()

		if fn != nil {
			result, err := fn(nid, in)
			if err != nil {
				_ = c.writeJSON(map[string]any{"type": "error", "error": err.Error()})
				continue
			}
			h.Broadcast(nid, result)
		} else {
			in["ts"] = time.Now().Unix()
			_ = c.writeJSON(in)
		}
	}
}
