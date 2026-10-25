package main

// uds.go：内核 ⇄ 网关的 UDS 服务端（B2-b 网关侧）。
//
// 角色划分（B2B-DESIGN §4/§5）：
//   * 内核（internal/connapi）是**客户端**：连上来、发 Hello、推 DeliverReq、回 ResumeResp/ConnOpenAck；
//   * 网关是**服务端**：只认 (userID, []Frame)，**不解析 topic**；把帧投进共享的 connhub.Hub。
//
// 两条硬规则（与 connproto/uds.go 头注释一致）：
//   1. 任何一层丢弃都**不 ack**：DeliverAck 里的 accepted 只是"已进 ring"，最终 ack 由客户端
//      字节级确认驱动（AckUp），所以丢掉的帧永远落在位点之后，由内核重发。
//   2. 同一次 resume：ResumeResp 到达时**先在 peer 读循环里写 resume_ok**，之后才处理后续
//      DeliverReq —— 保证客户端先收到 resume_ok 再收到补发帧（顺序由单流 + sink 互斥保证）。
//
// 背压两层里网关负责的部分：
//   * L2 UDS socket：AckUp/回执走有界队列（ackQ），满则丢 + `frames_dropped_total{reason="ackq"}`
//     （丢 ack 安全：内核会重发 → 重复但可去重）；
//   * L3 shard 队列：hub.Deliver 返回 DroppedSlow，原样写进 DeliverAck 并计
//     `frames_dropped_total{reason="shard"}`，**不进 accepted**。

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

const (
	udsHelloWait      = 3 * time.Second
	connOpenAckWait   = 3 * time.Second
	resumeRespWait    = 10 * time.Second
	peerReadDeadline  = 90 * time.Second
	statsPollInterval = 5 * time.Second
	ackQSize          = 4096
	leaseQSize        = 256
)

// udsServer 监听 UDS，接受内核连接。
type udsServer struct {
	gw        *gateway
	path      string
	instance  string
	startedAt time.Time

	ln net.Listener

	mu    sync.Mutex
	peers map[*kernelPeer]struct{}

	pendMu     sync.Mutex
	openWait   map[uint64]chan *connproto.ConnOpenAck
	resumeWait map[uint64]chan *connproto.ResumeResp

	stopOnce sync.Once
	stopCh   chan struct{}
}

func newUDSServer(gw *gateway, path, instance string) *udsServer {
	return &udsServer{
		gw: gw, path: path, instance: instance, startedAt: time.Now(),
		peers:      map[*kernelPeer]struct{}{},
		openWait:   map[uint64]chan *connproto.ConnOpenAck{},
		resumeWait: map[uint64]chan *connproto.ResumeResp{},
		stopCh:     make(chan struct{}),
	}
}

// Start 建立 UDS 监听。**必须能自愈 kill -9 残留的 socket 文件**：
// 上一个网关被 SIGKILL 时不会 unlink /tmp/im2a-conn.sock，直接 bind 会 EADDRINUSE。
// 判定规则（顺序很重要，避免误删活进程的 socket）：
//  1) 先直接 bind；
//  2) EADDRINUSE 时先 **dial 探活**：能连上 = 有活网关 → 拒绝启动（绝不 unlink，否则会出现双网关）；
//  3) 连不上且路径是 socket 文件 → 判定为残留 → unlink 后重建（日志明确打出）；
//  4) 路径存在但不是 socket（普通文件）→ 拒绝启动，不删任意文件。
func (s *udsServer) Start() error {
	ln, err := s.listenWithStaleRecovery()
	if err != nil {
		return err
	}
	_ = os.Chmod(s.path, 0o660)
	s.ln = ln
	go s.acceptLoop()
	go s.statsLoop()
	return nil
}

func (s *udsServer) listenWithStaleRecovery() (net.Listener, error) {
	ln, err := net.Listen("unix", s.path)
	if err == nil {
		return ln, nil
	}
	if !isAddrInUse(err) {
		return nil, err
	}
	// 有活网关占用？dial 能连上就说明是活进程。
	if c, derr := net.DialTimeout("unix", s.path, 300*time.Millisecond); derr == nil {
		_ = c.Close()
		return nil, fmt.Errorf("uds path %s 已被活进程占用（dial 成功），拒绝启动以避免双网关", s.path)
	}
	fi, serr := os.Lstat(s.path)
	if serr != nil {
		return nil, err // 路径已不存在：把原始 bind 错误返回，由上层记录
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("uds path %s 存在但不是 socket（%s），拒绝删除", s.path, fi.Mode())
	}
	if rerr := os.Remove(s.path); rerr != nil {
		return nil, rerr
	}
	log.Printf("[conn-gateway] removed stale UDS socket %s (previous process was SIGKILLed)", s.path)
	return net.Listen("unix", s.path)
}

// Stop 关闭监听与全部 peer；返回前广播 Drain（见 BroadcastDrain）。
func (s *udsServer) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		if s.ln != nil {
			_ = s.ln.Close()
		}
		s.mu.Lock()
		peers := make([]*kernelPeer, 0, len(s.peers))
		for p := range s.peers {
			peers = append(peers, p)
		}
		s.mu.Unlock()
		for _, p := range peers {
			p.close()
		}
		_ = os.Remove(s.path)
		log.Printf("[conn-gateway] uds stopped (listener closed, socket removed)")
	})
}

func (s *udsServer) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		p := &kernelPeer{
			srv:  s,
			conn: connproto.NewUDSConn(c),
			ackQ:   make(chan connproto.AckUp, ackQSize),
			leaseQ: make(chan connproto.PresenceLease, leaseQSize),
			done:   make(chan struct{}),
		}
		s.mu.Lock()
		s.peers[p] = struct{}{}
		s.mu.Unlock()
		s.gw.logf("[conn-gateway] UDS peer accepted (remote=%s)", c.RemoteAddr())
		go p.ackWriter()
		go p.readLoop()
		s.gw.onKernelUp()
	}
}

func (s *udsServer) removePeer(p *kernelPeer) {
	s.mu.Lock()
	_, ok := s.peers[p]
	delete(s.peers, p)
	n := len(s.peers)
	s.mu.Unlock()
	if ok {
		s.gw.onKernelDown(n == 0)
	}
}

// peerReady 返回第一个已完成 Hello 协商的 peer（通常只有一个内核）。
func (s *udsServer) peerReady() *kernelPeer {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := range s.peers {
		if p.ready.Load() {
			return p
		}
	}
	return nil
}

// BroadcastDrain 网关退出前通知内核停止投递新帧（B2B-DESIGN + lead 评审要求）。
func (s *udsServer) BroadcastDrain(deadline time.Duration) {
	body, err := connproto.EncodeJSONBody(connproto.Drain{DeadlineMS: deadline.Milliseconds()})
	if err != nil {
		return
	}
	s.mu.Lock()
	peers := make([]*kernelPeer, 0, len(s.peers))
	for p := range s.peers {
		if p.ready.Load() {
			peers = append(peers, p)
		}
	}
	s.mu.Unlock()
	for _, p := range peers {
		_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_ = p.conn.WriteMsg(connproto.KindDrain, body)
	}
	log.Printf("[conn-gateway] Drain broadcast to %d kernel peer(s), deadline=%s", len(peers), deadline)
}

// SendAckUp 把客户端确认转给内核（有界队列；满则丢并计数，安全）。
func (s *udsServer) SendAckUp(a connproto.AckUp) {
	s.mu.Lock()
	peers := make([]*kernelPeer, 0, len(s.peers))
	for p := range s.peers {
		if p.ready.Load() {
			peers = append(peers, p)
		}
	}
	s.mu.Unlock()
	if len(peers) == 0 {
		s.gw.metric.Inc("conn_gateway_frames_dropped_total", `reason="ackq"`, 1)
		return
	}
	for _, p := range peers {
		select {
		case p.ackQ <- a:
		default:
			// ack 丢了不会丢数据：内核会重发未确认帧。
			s.gw.metric.Inc("conn_gateway_frames_dropped_total", `reason="ackq"`, 1)
		}
	}
}

// SendPresenceLease 上报 presence 租约（C1，kind 26，上行单向）。
// 非阻塞：peer 的 leaseQ 满则丢弃并计数（presence 是软状态，丢了下一轮刷新会补上）。
func (s *udsServer) SendPresenceLease(l connproto.PresenceLease) {
	s.mu.Lock()
	peers := make([]*kernelPeer, 0, len(s.peers))
	for p := range s.peers {
		if p.ready.Load() {
			peers = append(peers, p)
		}
	}
	s.mu.Unlock()
	if len(peers) == 0 {
		s.gw.metric.Inc("conn_gateway_frames_dropped_total", `reason="lease"`, 1)
		return
	}
	for _, p := range peers {
		select {
		case p.leaseQ <- l:
		default:
			s.gw.metric.Inc("conn_gateway_frames_dropped_total", `reason="lease"`, 1)
		}
	}
}

// SendConnClosed 通知内核连接已关闭（best-effort）。
func (s *udsServer) SendConnClosed(cc connproto.ConnClosed) {
	p := s.peerReady()
	if p == nil {
		return
	}
	body, err := connproto.EncodeJSONBody(cc)
	if err != nil {
		return
	}
	_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = p.conn.WriteMsg(connproto.KindConnClosed, body)
}

// RequestConnOpen 请求内核签发 resumeToken/epoch（3s 超时；无内核则返回 ErrNoKernel）。
func (s *udsServer) RequestConnOpen(req connproto.ConnOpen) (*connproto.ConnOpenAck, error) {
	p := s.peerReady()
	if p == nil {
		return nil, errNoKernel
	}
	ch := make(chan *connproto.ConnOpenAck, 1)
	s.pendMu.Lock()
	s.openWait[req.ConnID] = ch
	s.pendMu.Unlock()
	defer func() {
		s.pendMu.Lock()
		delete(s.openWait, req.ConnID)
		s.pendMu.Unlock()
	}()

	body, err := connproto.EncodeJSONBody(req)
	if err != nil {
		return nil, err
	}
	_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := p.conn.WriteMsg(connproto.KindConnOpen, body); err != nil {
		return nil, err
	}
	select {
	case ack := <-ch:
		return ack, nil
	case <-time.After(connOpenAckWait):
		return nil, errConnOpenTimeout
	}
}

// RequestResume 请求内核给出补发计划。
// 注意：resume_ok 由 **peer 读循环**在收到 ResumeResp 时立刻写（保证先于补发帧），
// 本函数只等"peer 已处理完 ResumeResp"的信号。
func (s *udsServer) RequestResume(req connproto.ResumeReq) (*connproto.ResumeResp, error) {
	p := s.peerReady()
	if p == nil {
		return nil, errNoKernel
	}
	ch := make(chan *connproto.ResumeResp, 1)
	s.pendMu.Lock()
	s.resumeWait[req.ConnID] = ch
	s.pendMu.Unlock()
	defer func() {
		s.pendMu.Lock()
		delete(s.resumeWait, req.ConnID)
		s.pendMu.Unlock()
	}()

	body, err := connproto.EncodeJSONBody(req)
	if err != nil {
		return nil, err
	}
	_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := p.conn.WriteMsg(connproto.KindResumeReq, body); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(resumeRespWait):
		return nil, errResumeTimeout
	}
}

func (s *udsServer) statsLoop() {
	t := time.NewTicker(statsPollInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			p := s.peerReady()
			if p == nil {
				continue
			}
			_ = p.sendStatsReq()
			// ack lag 由 /metrics 的 handleMetrics 统一计算，这里不再覆盖（避免把 NaN 观测态改回 0）。
		}
	}
}

// ---- 单个内核连接 ----

type kernelPeer struct {
	srv     *udsServer
	conn    *connproto.UDSConn
	ackQ    chan connproto.AckUp
	leaseQ  chan connproto.PresenceLease
	done    chan struct{}
	ready   atomic.Bool
	closed  atomic.Bool
	closeMu sync.Once
}

func (p *kernelPeer) close() {
	p.closeMu.Do(func() {
		p.closed.Store(true)
		close(p.done)
		_ = p.conn.Close()
		p.srv.removePeer(p)
	})
}

// ackWriter 独立 goroutine 写"上行控制"消息（AckUp / PresenceLease）：
// 绝不阻塞 WS 读循环 / shard 循环。
func (p *kernelPeer) ackWriter() {
	for {
		select {
		case <-p.done:
			return
		case a := <-p.ackQ:
			body, err := connproto.EncodeJSONBody(a)
			if err != nil {
				continue
			}
			_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := p.conn.WriteMsg(connproto.KindAckUp, body); err != nil {
				p.srv.gw.logf("[conn-gateway] UDS peer write error (AckUp): %v", err)
				p.srv.gw.metric.Inc("conn_gateway_frames_dropped_total", `reason="uds"`, 1)
				p.close()
				return
			}
		case l := <-p.leaseQ:
			body, err := connproto.EncodeJSONBody(l)
			if err != nil {
				continue
			}
			_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := p.conn.WriteMsg(connproto.KindPresenceLease, body); err != nil {
				p.srv.gw.logf("[conn-gateway] UDS peer write error (PresenceLease): %v", err)
				p.srv.gw.metric.Inc("conn_gateway_frames_dropped_total", `reason="uds"`, 1)
				p.close()
				return
			}
		}
	}
}

func (p *kernelPeer) readLoop() {
	defer p.close()
	for {
		_ = p.conn.SetReadDeadline(time.Now().Add(peerReadDeadline))
		kind, _, body, err := p.conn.ReadMsg()
		if err != nil {
			p.srv.gw.logf("[conn-gateway] UDS peer read error (本轮 UDS 连接结束): %v", err)
			return
		}
		p.srv.gw.metric.Inc("conn_gateway_uds_messages_total", `kind="`+kind.String()+`"`, 1)
		switch kind {
		case connproto.KindHello:
			var hello connproto.UDSHello
			if err := connproto.DecodeJSONBody(body, &hello); err != nil {
				return
			}
			ack := connproto.NegotiateUDS(hello)
			ack.Instance = p.srv.instance
			ack.StartedAt = p.srv.startedAt.Unix()
			ab, _ := connproto.EncodeJSONBody(ack)
			_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := p.conn.WriteMsg(connproto.KindHelloAck, ab); err != nil {
				return
			}
			if !ack.Accepted {
				p.srv.gw.logf("[conn-gateway] UDS 版本不匹配，拒绝内核连接: %s", ack.Reason)
				return
			}
			p.ready.Store(true)
			p.srv.gw.logf("[conn-gateway] UDS 内核已连接 (proto v%d, instance=%s)", ack.ProtoVersion, hello.Instance)
			// 立刻拉一次 Stats：否则 kernel_ack_lag_seconds 在头 5s 内是"未观测"（NaN），
			// 短生命周期的 E2E/健康检查会误以为指标坏了。
			_ = p.sendStatsReq()

		case connproto.KindDeliverReq:
			req, derr := connproto.DecodeDeliverReq(body)
			if derr != nil {
				p.srv.gw.logf("[conn-gateway] 坏 DeliverReq: %v", derr)
				return // 字节流已错位，宁可断开让内核重连
			}
			res := p.srv.gw.hub.Deliver(req.UserID, req.Frames)
			p.srv.gw.metric.Inc("conn_gateway_frames_in_total", "", float64(res.Enqueued))
			if res.DroppedSlow > 0 {
				p.srv.gw.metric.Inc("conn_gateway_frames_dropped_total", `reason="shard"`, float64(res.DroppedSlow))
				p.srv.gw.shardDropped.Add(int64(res.DroppedSlow))
			}
			// 有任何 accepted 就记 pending（网关侧 ack lag 的代理指标）
			p.srv.gw.ackLag.MarkPending(req.UserID)
			ack := connproto.DeliverAck{
				BatchID:     req.BatchID,
				UserID:      req.UserID,
				TargetConns: res.TargetConns,
				Accepted:    res.Enqueued,
				DroppedSlow: res.DroppedSlow,
				QueuedBytes: 0, // B2-b 暂不统计（近似值，内核只作参考）
			}
			// 只在 -v 下打（lead 定：默认静默）。100k fps 时逐批一行仍可能放大日志量，
			// 常规观测请用指标 conn_gateway_uds_messages_total{kind="DELIVER_REQ"}。
			p.srv.gw.logf("[conn-gateway] deliver_req user=%d batch=%d frames=%d accepted=%d dropped=%d",
				req.UserID, req.BatchID, len(req.Frames), res.Enqueued, res.DroppedSlow)
			ab, _ := connproto.EncodeJSONBody(ack)
			_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := p.conn.WriteMsg(connproto.KindDeliverAck, ab); err != nil {
				return
			}

		case connproto.KindResumeResp:
			var resp connproto.ResumeResp
			if err := connproto.DecodeJSONBody(body, &resp); err != nil {
				return
			}
			// 关键顺序：先在 peer 读循环里把 resume_ok 写给客户端，再唤醒等待者，
			// 之后才可能处理补发帧（保证 resume_ok 先于帧）。
			p.srv.gw.onResumeResp(&resp)
			p.srv.deliverResumeWaiter(&resp)

		case connproto.KindConnOpenAck:
			var ack connproto.ConnOpenAck
			if err := connproto.DecodeJSONBody(body, &ack); err != nil {
				return
			}
			p.srv.deliverOpenWaiter(&ack)

		case connproto.KindKick:
			var k connproto.Kick
			if err := connproto.DecodeJSONBody(body, &k); err != nil {
				return
			}
			p.srv.gw.onKick(k)

		case connproto.KindStatsResp:
			var sr connproto.StatsResp
			if err := connproto.DecodeJSONBody(body, &sr); err != nil {
				return
			}
			// 收到任何 StatsResp 都算"已观测"：0 是有效值（健康），不再是未知。
			p.srv.gw.metric.Set("kernel_ack_lag_seconds", sr.AckLagSeconds)

		case connproto.KindPresenceState:
			// C4 前置：presence 聚合状态下行 fanout（瞬时状态，不写 delivery log、不产生 ack/位点）。
			//
			// 红线（lead 定，任何人不要越线）：
			//   1) 网关**不做状态机**：状态是什么就转发什么，不合并、不排序、不过期；
			//   2) 网关**不推断 offline**：offline 只由内核按 TTL 得出，网关绝不自己造；
			//   3) 网关**不缓存**：没有本地 presence 表，收到即 fanout，丢掉即丢（无重试/无补发）。
			//
			// 寻址：与 DeliverReq 同一 hub 路径，按 userID 投递；payload 就是 PresenceState 的 JSON，
			// 客户端按 TypePresence 数据帧 + JSON 解析（与内核 DeliverEphemeral 的帧表示一致）。
			var st connproto.PresenceState
			if err := connproto.DecodeJSONBody(body, &st); err != nil {
				p.srv.gw.logf("[conn-gateway] 坏 PresenceState: %v", err)
				return // 字节流仍完整（信封已按长度切分），但字段坏了不值得继续；断开让内核重连
			}
			payload, _ := connproto.EncodeJSONBody(st)
			frames := []connproto.Frame{{Type: connproto.TypePresence, Payload: payload}}
			res := p.srv.gw.hub.Deliver(st.UserID, frames)
			if res.Enqueued > 0 {
				p.srv.gw.metric.Inc("conn_gateway_presence_state_fanout_total", "", float64(res.Enqueued))
			}
			if res.TargetConns == 0 || res.Enqueued == 0 {
				// 该 user 没有本机连接（或 ring 拒绝）：丢弃并计数，**不报错、不重试**。
				p.srv.gw.metric.Inc("conn_gateway_presence_state_dropped_total", "", 1)
				p.srv.gw.logf("[conn-gateway] presence_state user=%d state=%s 无本机连接，丢弃", st.UserID, st.State)
			}

		case connproto.KindPing:
			_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_ = p.conn.WriteMsg(connproto.KindPong, nil)

		case connproto.KindDrain:
			// 内核不会给我们发 Drain（方向是网关→内核）；忽略即可。

		default:
			// 未知 kind 忽略（前向兼容，见 connproto/uds.go 版本策略）
		}
	}
}

// sendStatsReq 向内核要一次 StatsResp（peer ready 时立刻拉一次，之后每 statsPollInterval 一次）。
func (p *kernelPeer) sendStatsReq() error {
	_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return p.conn.WriteMsg(connproto.KindStatsReq, nil)
}

func (p *kernelPeer) writeControl(kind connproto.UDSKind, body []byte) error {
	_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return p.conn.WriteMsg(kind, body)
}

// ---- waiter 分发 ----

// deliverOpenWaiter 把 ConnOpenAck 交给对应的 RequestConnOpen 等待者。
//
// **配对必须精确**（C4 flake 的根因）：内核早期版本不回显 `ConnOpenAck.ConnID`，
// 网关曾在"connID=0"时兜底唤醒"第一个 waiter"——并发建连时会**把 ack 配给错误连接**，
// 导致会话身份互换（A 拿到 B 的 userID）。现在的规则：
//   - ack.ConnID != 0：只精确匹配；未知 connID（已超时/非本网关）直接丢弃，绝不错配；
//   - ack.ConnID == 0（旧内核兼容）：**仅当只有 1 个 waiter** 时才配对；多个 waiter 时丢弃
//     （宁可让该连接超时拒绝，也不接受一个可能错误的身份）。
func (s *udsServer) deliverOpenWaiter(ack *connproto.ConnOpenAck) {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	if ack.ConnID != 0 {
		ch, ok := s.openWait[ack.ConnID]
		if !ok {
			s.gw.logf("[conn-gateway] ConnOpenAck conn_id=%d 无对应等待者（已超时或非本网关），丢弃", ack.ConnID)
			return
		}
		delete(s.openWait, ack.ConnID)
		select {
		case ch <- ack:
		default:
		}
		return
	}
	if len(s.openWait) == 1 {
		for id, ch := range s.openWait {
			delete(s.openWait, id)
			select {
			case ch <- ack:
			default:
			}
		}
		return
	}
	s.gw.logf("[conn-gateway] ConnOpenAck 未回显 conn_id 且有 %d 个并发等待者：丢弃（避免身份错配）", len(s.openWait))
}

// deliverResumeWaiter 把 ResumeResp 交给 RequestResume 的等待者。
// 优先按 ResumeResp.ConnID 精确匹配；内核若未回显（v1 允许省略）则单飞唤醒一个。
// deliverResumeWaiter 与 deliverOpenWaiter 同样的精确配对规则（resume 也不允许错配）。
func (s *udsServer) deliverResumeWaiter(resp *connproto.ResumeResp) {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	if resp.ConnID != 0 {
		ch, ok := s.resumeWait[resp.ConnID]
		if !ok {
			s.gw.logf("[conn-gateway] ResumeResp conn_id=%d 无对应等待者，丢弃", resp.ConnID)
			return
		}
		delete(s.resumeWait, resp.ConnID)
		select {
		case ch <- resp:
		default:
		}
		return
	}
	if len(s.resumeWait) == 1 {
		for connID, ch := range s.resumeWait {
			delete(s.resumeWait, connID)
			select {
			case ch <- resp:
			default:
			}
		}
		return
	}
	s.gw.logf("[conn-gateway] ResumeResp 未回显 conn_id 且有 %d 个并发等待者：丢弃（避免错配）", len(s.resumeWait))
}

var (
	errNoKernel        = errors.New("conn-gateway: no kernel attached")
	errConnOpenTimeout = errors.New("conn-gateway: conn open ack timeout")
	errResumeTimeout   = errors.New("conn-gateway: resume response timeout")
)

func isAddrInUse(err error) bool {
	var oe *net.OpError
	if errors.As(err, &oe) {
		return oe.Err != nil && (os.IsExist(oe.Err) || containsStr(oe.Err.Error(), "address already in use"))
	}
	return containsStr(err.Error(), "address already in use")
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 占位：确保 connhub 包被引用（hub 类型在 gateway 里）。
var _ = connhub.DefaultConfig
