// cmd/loadgen：连接网关/内核的最小压测器（Wave B1）。
//
// 用法：
//
//	go run ./cmd/loadgen -conns 2000 -rate 50 -dur 30s
//	go run ./cmd/loadgen -conns 1 -dur 3s            # 单连接冒烟（验证握手）
//
// 它做四件事：
//  1. 建 N 条 WS 连接（带 hello 握手，读到 hello_ok/auth_ok 才算成功）；
//  2. 每条连接按 -rate 发送二进制数据帧（payload 头部嵌 8B 纳秒时间戳）；
//  3. 收帧并计算端到端延迟（p50/p95/p99/max）、统计丢弃/重连/RSS；
//  4. 到期后汇总输出（text 可直接贴进报告）。
//
// 只依赖 stdlib + gorilla/websocket + internal/connproto，不引入压测框架。
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"human-agent-im/internal/connproto"
)

type config struct {
	target   string
	conns    int
	rate     float64 // 每连接每秒帧数
	dur      time.Duration
	token       string
	jwt         string // C1：hello.token（JWT），身份来源；不再依赖 ?user_id=
	devInsecure bool   // 仅本地/压测：允许无 jwt 时用 ?user_id= 让 dev 网关放行
	userBase    int64
	warmup      time.Duration
}

type metrics struct {
	dialOK    atomic.Int64
	dialFail  atomic.Int64
	framesOut atomic.Int64
	framesIn  atomic.Int64
	bytesIn   atomic.Int64
	reconnect atomic.Int64
	readErr   atomic.Int64

	lat [64]latShard // 分片直方图：避免全局 mutex 在 10 万帧/s 时成为瓶颈
}

type latShard struct {
	mu sync.Mutex
	xs []time.Duration
}

func (m *metrics) observe(shard int, d time.Duration) {
	m.framesIn.Add(1)
	s := &m.lat[shard&63]
	s.mu.Lock()
	if len(s.xs) < 20_000 { // 每片最多 2 万样本（总 128 万，足够算分位）
		s.xs = append(s.xs, d)
	}
	s.mu.Unlock()
}

func (m *metrics) percentile(p float64) time.Duration {
	var all []time.Duration
	for i := range m.lat {
		s := &m.lat[i]
		s.mu.Lock()
		all = append(all, s.xs...)
		s.mu.Unlock()
	}
	if len(all) == 0 {
		return 0
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return all[int(float64(len(all)-1)*p)]
}

func main() {
	var c config
	flag.StringVar(&c.target, "target", "ws://127.0.0.1:18081/v1/ws", "gateway WS endpoint")
	flag.IntVar(&c.conns, "conns", 100, "concurrent connections")
	flag.Float64Var(&c.rate, "rate", 10, "frames per second per connection")
	flag.DurationVar(&c.dur, "dur", 10*time.Second, "test duration")
	flag.StringVar(&c.token, "token", os.Getenv("CONN_GATEWAY_TOKEN"), "access token (empty for dev gateway)")
	flag.StringVar(&c.jwt, "jwt", os.Getenv("CONN_JWT"), "C1: hello.token (JWT) used as identity; identity now comes from the kernel JWT verification")
	flag.BoolVar(&c.devInsecure, "dev-insecure", false, "DEV ONLY: allow connecting without -jwt using ?user_id= (dev gateway with -dev-insecure)")
	flag.Int64Var(&c.userBase, "user-base", 1000, "first user_id (dev-insecure only; identity from -jwt otherwise)")
	flag.DurationVar(&c.warmup, "warmup", 1*time.Second, "warmup before measuring")
	flag.Parse()
	if c.jwt == "" && !c.devInsecure {
		log.Fatalf("loadgen: 需要 -jwt <token>（C1 起身份来自 JWT）；本地 dev 必须显式 -dev-insecure 才允许用 ?user_id=")
	}
	if c.devInsecure {
		log.Printf("[loadgen] WARN: -dev-insecure 已启用，用 ?user_id= 建连（仅限本地 dev 网关）")
	}

	if c.conns <= 0 || c.dur <= 0 {
		log.Fatalf("loadgen: -conns and -dur must be positive")
	}
	m := &metrics{}
	stop := make(chan struct{})
	runUntil := time.Now().Add(c.warmup + c.dur + 2*time.Second)

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < c.conns; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			runConn(c, idx, m, stop, runUntil)
		}(i)
		// 错峰建连，避免自己制造惊群（惊群另有专门用例）。
		if i%50 == 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}

	// 预热后开始计时（预热期的延迟不计入分位）。
	select {
	case <-time.After(c.warmup):
	case <-time.After(c.dur):
	}
	measureStart := time.Now()
	time.Sleep(c.dur)
	close(stop)
	wg.Wait()

	elapsed := time.Since(measureStart).Seconds()
	rss := rssKB()
	fmt.Printf("=== loadgen ===\n")
	fmt.Printf("target=%s conns=%d rate=%.1f/s dur=%s warmup=%s\n", c.target, c.conns, c.rate, c.dur, c.warmup)
	fmt.Printf("dial_ok=%d dial_fail=%d reconnect=%d read_err=%d\n", m.dialOK.Load(), m.dialFail.Load(), m.reconnect.Load(), m.readErr.Load())
	fmt.Printf("frames_out=%d frames_in=%d bytes_in=%d\n", m.framesOut.Load(), m.framesIn.Load(), m.bytesIn.Load())
	fmt.Printf("throughput_out=%.0f frames/s  throughput_in=%.0f frames/s\n",
		float64(m.framesOut.Load())/elapsed, float64(m.framesIn.Load())/elapsed)
	fmt.Printf("lat p50=%s p95=%s p99=%s\n", m.percentile(0.50), m.percentile(0.95), m.percentile(0.99))
	fmt.Printf("rss_kb=%d goroutines=%d total=%s\n", rss, runtime.NumGoroutine(), time.Since(start).Round(time.Millisecond))
	if m.dialFail.Load() > 0 && m.dialOK.Load() == 0 {
		os.Exit(1)
	}
}

// runConn 一个压测客户端：握手 → 收/发循环 → 断线重连。
func runConn(c config, idx int, m *metrics, stop <-chan struct{}, runUntil time.Time) {
	user := c.userBase + int64(idx)
	device := fmt.Sprintf("lg-%d", idx)
	interval := time.Duration(float64(time.Second) / c.rate)
	if interval <= 0 {
		interval = time.Second
	}

	for {
		select {
		case <-stop:
			return
		default:
		}
		dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, ReadBufferSize: 4096, WriteBufferSize: 4096}
		u := fmt.Sprintf("%s?user_id=%d&device_id=%s", c.target, user, device)
		if c.token != "" {
			u += "&access_token=" + url.QueryEscape(c.token)
		}
		conn, _, err := dialer.Dial(u, nil)
		if err != nil {
			m.dialFail.Add(1)
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		if !handshake(conn, c.jwt) {
			m.dialFail.Add(1)
			_ = conn.Close()
			continue
		}
		m.dialOK.Add(1)

		done := make(chan struct{})
		// 读循环
		go func() {
			defer close(done)
			for {
				mt, data, err := conn.ReadMessage()
				if err != nil {
					m.readErr.Add(1)
					return
				}
				if mt != websocket.BinaryMessage || len(data) < 24 {
					continue
				}
				f, _, err := connproto.Decode(data)
				if err != nil || len(f.Payload) < 8 {
					continue
				}
				m.bytesIn.Add(int64(len(data)))
				if f.Type != connproto.TypeMsg {
					continue
				}
				ts := int64(binary.BigEndian.Uint64(f.Payload[:8]))
				m.observe(idx, time.Duration(time.Now().UnixNano()-ts))
			}
		}()

		// 发循环
		var seq uint64
		filler := make([]byte, 128) // 典型 event payload ~136B
		ticker := time.NewTicker(interval)
		payload := make([]byte, 8+len(filler))
	loop:
		for {
			select {
			case <-stop:
				break loop
			case <-done:
				break loop
			case <-ticker.C:
				seq++
				binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().UnixNano()))
				f := connproto.Frame{Type: connproto.TypeMsg, Flags: connproto.FlagCritical, Seq: seq, Payload: payload}
				b, err := connproto.Encode(nil, &f)
				if err != nil {
					continue
				}
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.BinaryMessage, b); err != nil {
					break loop
				}
				m.framesOut.Add(1)
			}
		}
		ticker.Stop()
		_ = conn.Close()
		// 测试窗口结束后不再重连（否则每个连接都会在收尾时被计一次假重连）。
		select {
		case <-stop:
			return
		default:
		}
		if time.Now().After(runUntil) {
			return
		}
		m.reconnect.Add(1) // 窗口内被动断开 → 重连（模拟真实客户端）
	}
}

// handshake 发送 hello 并等待 auth_ok（B1 协议的最小握手）。
func handshake(conn *websocket.Conn, jwt string) bool {
	hello, err := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlHello, V: 1, ProtoMin: "1.0", ProtoMax: "1.0", Token: jwt,
	})
	if err != nil {
		return false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, hello); err != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < 4; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return false
		}
		c, err := connproto.DecodeControl(data)
		if err != nil {
			continue
		}
		if c.T == connproto.CtlAuthOK {
			// 关键：握手期设的 5s 读写 deadline 必须清零，否则握手 5s 后所有写都会
			// 报 i/o timeout（曾导致"连接每 5 秒集体掉线、reconnect 暴涨"的假象）。
			_ = conn.SetReadDeadline(time.Time{})
			_ = conn.SetWriteDeadline(time.Time{})
			return true
		}
	}
	return false
}

// rssKB 读 /proc/self/statm（Linux）；失败则回退到 Go 堆统计（量级不同，仅作参考）。
func rssKB() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return int64(ms.Sys / 1024)
	}
	var size, rss int64
	fmt.Sscanf(string(b), "%d %d", &size, &rss)
	return rss * int64(os.Getpagesize()) / 1024
}

// 确保 net 包被引用（rss 之外的网络类型在未来扩展里会用；避免 lint 噪声）。
var _ = net.IPv4len
