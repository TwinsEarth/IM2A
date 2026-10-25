// SPDX-License-Identifier: MIT
package connapi

import (
	"context"
	"io"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

// testLogWriter 把 Remote 的内部日志转给 t.Logf（排障用）。
type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// outMsg 是 fake gateway 的出站消息（经独立 writer goroutine 串行写出，模拟真实网关的写路径）。
type outMsg struct {
	kind connproto.UDSKind
	body []byte
}

// fakeGateway 是内核视角的"网关侧 UDS 服务端"：net.Pipe 内存管道，不依赖真实 socket。
type fakeGateway struct {
	t   *testing.T
	raw net.Conn
	uc  *connproto.UDSConn

	helloAck   connproto.UDSHelloAck
	autoAck    bool
	onConnOpen func(connproto.ConnOpen) connproto.ConnOpenAck

	mu     sync.Mutex
	events []connproto.UDSKind
	bodies map[connproto.UDSKind][][]byte
	frames chan connproto.DeliverReq
	ready  chan struct{}
	out    chan outMsg // 出站消息（writer goroutine 消费）
}

func (g *fakeGateway) start() {
	go g.writeLoop()
	go g.loop()
}

// writeLoop 是 fake gateway 的唯一写者：与真实网关一致，读循环不会被写阻塞。
func (g *fakeGateway) writeLoop() {
	for m := range g.out {
		if err := g.uc.WriteMsg(m.kind, m.body); err != nil {
			return
		}
	}
}

func (g *fakeGateway) enqueue(kind connproto.UDSKind, body []byte) {
	select {
	case g.out <- outMsg{kind: kind, body: body}:
	default:
		g.t.Errorf("fake gateway outbound queue full (kind=%v)", kind)
	}
}

func (g *fakeGateway) loop() {
	// 握手：读 Hello → 回 HelloAck。
	kind, _, body, err := g.uc.ReadMsg()
	if err != nil {
		return
	}
	if kind != connproto.KindHello {
		g.t.Errorf("fake gateway: first msg kind=%v, want HELLO", kind)
		return
	}
	ack := g.helloAck
	if ack.ProtoVersion == 0 {
		var hello connproto.UDSHello
		_ = connproto.DecodeJSONBody(body, &hello)
		ack = connproto.NegotiateUDS(hello)
	}
	ab, _ := connproto.EncodeJSONBody(ack)
	g.enqueue(connproto.KindHelloAck, ab)
	close(g.ready)

	for {
		kind, _, body, err := g.uc.ReadMsg()
		if err != nil {
			return
		}
		g.mu.Lock()
		g.events = append(g.events, kind)
		g.bodies[kind] = append(g.bodies[kind], append([]byte(nil), body...))
		g.mu.Unlock()

		switch kind {
		case connproto.KindDeliverReq:
			req, err := connproto.DecodeDeliverReq(body)
			if err != nil {
				g.t.Errorf("fake gateway: decode DeliverReq: %v", err)
				continue
			}
			select {
			case g.frames <- *req:
			default:
			}
			if g.autoAck {
				ackBody, _ := connproto.EncodeJSONBody(connproto.DeliverAck{
					BatchID: req.BatchID, UserID: req.UserID, TargetConns: 1, Accepted: len(req.Frames),
				})
				g.enqueue(connproto.KindDeliverAck, ackBody)
			}
		case connproto.KindConnOpen:
			var op connproto.ConnOpen
			_ = connproto.DecodeJSONBody(body, &op)
			ack := connproto.ConnOpenAck{OK: true}
			if g.onConnOpen != nil {
				ack = g.onConnOpen(op)
			}
			ab, _ := connproto.EncodeJSONBody(ack)
			g.enqueue(connproto.KindConnOpenAck, ab)
		case connproto.KindStatsReq:
			ab, _ := connproto.EncodeJSONBody(connproto.StatsResp{FramesIn: 7, FramesOut: 3, Dropped: 1})
			g.enqueue(connproto.KindStatsResp, ab)
		case connproto.KindPing:
			g.enqueue(connproto.KindPong, nil)
		}
	}
}

func (g *fakeGateway) send(kind connproto.UDSKind, v any) {
	var body []byte
	if v != nil {
		body, _ = connproto.EncodeJSONBody(v)
	}
	g.enqueue(kind, body)
}

func (g *fakeGateway) kinds() []connproto.UDSKind {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]connproto.UDSKind, len(g.events))
	copy(out, g.events)
	return out
}

func (g *fakeGateway) lastBody(kind connproto.UDSKind) []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	bs := g.bodies[kind]
	if len(bs) == 0 {
		return nil
	}
	return bs[len(bs)-1]
}

// newPipeRemote 构造 Remote，Dial 返回 net.Pipe 客户端端；setups 在 fakeGateway 启动前执行
// （用于预设 HelloAck 等行为）。测试结束后清理。
func newPipeRemote(t *testing.T, cfg RemoteConfig, setups ...func(*fakeGateway)) (*Remote, *fakeGateway) {
	t.Helper()
	client, server := net.Pipe()
	gw := &fakeGateway{
		t:      t,
		raw:    server,
		uc:     connproto.NewUDSConn(server),
		bodies: map[connproto.UDSKind][][]byte{},
		frames: make(chan connproto.DeliverReq, 64),
		ready:  make(chan struct{}),
		out:    make(chan outMsg, 128),
	}
	for _, setup := range setups {
		setup(gw)
	}
	gw.start()
	cfg.UDSPath = "pipe-test"
	cfg.Dial = func(ctx context.Context, path string) (net.Conn, error) { return client, nil }
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	if cfg.ReconnectMin == 0 {
		cfg.ReconnectMin = 10 * time.Millisecond
	}
	if cfg.ReconnectMax == 0 {
		cfg.ReconnectMax = 30 * time.Millisecond
	}
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = time.Millisecond
	}
	r := NewRemote(cfg)
	t.Cleanup(func() {
		_ = r.Close(context.Background())
		_ = gw.raw.Close()
	})
	return r, gw
}

// waitFor 轮询条件（测试超时保障）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func testFrame(seq uint64, payloadLen int) connproto.Frame {
	return connproto.Frame{Type: connproto.TypeEvt, Seq: seq, Payload: make([]byte, payloadLen)}
}

func TestRemoteHandshakeRejected(t *testing.T) {
	r, _ := newPipeRemote(t, RemoteConfig{}, func(gw *fakeGateway) {
		gw.helloAck = connproto.UDSHelloAck{ProtoVersion: 1, Accepted: false, Reason: "kernel proto v2, gateway supports v1"}
	})
	// 等待拒绝计数（remote 会进 degraded）。
	waitFor(t, "negotiation rejected counter", func() bool { return r.Metrics().NegotiationRejected >= 1 })
	m := r.Metrics()
	if m.State != "degraded" && m.State != "connecting" {
		t.Fatalf("state = %q, want degraded/connecting", m.State)
	}
	// 拒绝后 Deliver 必须立刻返回（不阻塞），帧计入 dropped。
	start := time.Now()
	res := r.Deliver(1, []connproto.Frame{testFrame(1, 16)})
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Deliver blocked %v after rejection", elapsed)
	}
	if res.Enqueued != 0 || res.DroppedSlow != 1 {
		t.Fatalf("Deliver result = %+v, want enqueued=0 dropped=1", res)
	}
	if r.Metrics().DroppedNotReady == 0 {
		t.Fatalf("DroppedNotReady = 0, want >0")
	}
}

func TestRemoteDeliverAndAck(t *testing.T) {
	r, gw := newPipeRemote(t, RemoteConfig{})
	gw.autoAck = true
	<-gw.ready
	waitFor(t, "connected", func() bool { return r.Metrics().Connected })

	frames := []connproto.Frame{testFrame(1, 16), testFrame(2, 16), testFrame(3, 16)}
	res := r.Deliver(42, frames)
	if res.Enqueued != 3 {
		t.Fatalf("Deliver enqueued = %d, want 3", res.Enqueued)
	}
	waitFor(t, "gateway got DeliverReq", func() bool {
		select {
		case req := <-gw.frames:
			if req.UserID != 42 || len(req.Frames) != 3 {
				t.Fatalf("DeliverReq = user=%d frames=%d, want user=42 frames=3", req.UserID, len(req.Frames))
			}
			return true
		default:
			return false
		}
	})
	waitFor(t, "deliver ack", func() bool { return r.Metrics().DeliveredFrames == 3 && r.Metrics().DeliverAcks >= 1 })
}

func TestRemoteBatchSplit(t *testing.T) {
	cfg := RemoteConfig{MaxBatchFrames: 32}
	r, gw := newPipeRemote(t, cfg)
	gw.autoAck = true
	<-gw.ready
	waitFor(t, "connected", func() bool { return r.Metrics().Connected })

	// 20 帧 × 65000B ≈ 1.30MiB > UDSMaxBody(1MiB) → SplitDeliverReq 必须切批且 BatchID 独立。
	frames := make([]connproto.Frame, 0, 20)
	for i := 0; i < 20; i++ {
		frames = append(frames, testFrame(uint64(i+1), 65000))
	}
	if res := r.Deliver(7, frames); res.Enqueued != 20 {
		t.Fatalf("Deliver enqueued = %d, want 20", res.Enqueued)
	}
	seen := map[uint64]bool{}
	total := 0
	deadline := time.Now().Add(2 * time.Second)
	for total < 20 && time.Now().Before(deadline) {
		select {
		case req := <-gw.frames:
			if seen[req.BatchID] {
				t.Fatalf("duplicate BatchID %d (SplitDeliverReq 必须生成独立 BatchID)", req.BatchID)
			}
			seen[req.BatchID] = true
			total += len(req.Frames)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if total != 20 {
		t.Fatalf("received frames = %d, want 20", total)
	}
	if len(seen) < 2 {
		t.Fatalf("batches = %d, want >=2 (body 超 1MiB 必须切批)", len(seen))
	}
	waitFor(t, "batch_split counter", func() bool { return r.Metrics().BatchSplit >= 1 })
}

func TestRemoteDrainStopsDeliveryKeepsAckUp(t *testing.T) {
	ackUps := make(chan connproto.AckUp, 1)
	epochOK := true
	r, gw := newPipeRemote(t, RemoteConfig{
		OnAckUp: func(au connproto.AckUp) bool {
			if epochOK {
				ackUps <- au
			}
			return epochOK
		},
	})
	gw.autoAck = true
	<-gw.ready
	waitFor(t, "connected", func() bool { return r.Metrics().Connected })

	gw.send(connproto.KindDrain, connproto.Drain{DeadlineMS: 5000})
	waitFor(t, "drain counter", func() bool { return r.Metrics().DrainCount >= 1 })
	if !r.Metrics().Draining {
		t.Fatalf("Draining = false after Drain")
	}
	before := len(gw.kinds())
	res := r.Deliver(9, []connproto.Frame{testFrame(1, 16)})
	if res.Enqueued != 0 {
		t.Fatalf("Deliver after Drain enqueued = %d, want 0", res.Enqueued)
	}
	time.Sleep(30 * time.Millisecond)
	for _, k := range gw.kinds()[before:] {
		if k == connproto.KindDeliverReq {
			t.Fatalf("DeliverReq sent after Drain")
		}
	}
	// Drain 之后 AckUp 仍必须处理（否则位点会退）。
	gw.send(connproto.KindAckUp, connproto.AckUp{UserID: 9, DeviceID: "ios", ConnEpoch: 0, AckSeq: 3})
	select {
	case au := <-ackUps:
		if au.AckSeq != 3 {
			t.Fatalf("AckUp seq=%d, want 3", au.AckSeq)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("AckUp not processed after Drain")
	}
	if r.Metrics().AckUps != 1 {
		t.Fatalf("AckUps = %d, want 1", r.Metrics().AckUps)
	}
}

func TestRemoteAckUpEpochMismatchDropped(t *testing.T) {
	r, gw := newPipeRemote(t, RemoteConfig{
		OnAckUp: func(connproto.AckUp) bool { return false }, // 模拟 epoch 不匹配
	})
	gw.autoAck = true
	<-gw.ready
	waitFor(t, "connected", func() bool { return r.Metrics().Connected })
	gw.send(connproto.KindAckUp, connproto.AckUp{UserID: 1, DeviceID: "ios", ConnEpoch: 99, AckSeq: 5})
	waitFor(t, "ackup dropped", func() bool { return r.Metrics().AckUpsDropped == 1 })
	if r.Metrics().AckUps != 0 {
		t.Fatalf("AckUps = %d, want 0 (epoch mismatch must be dropped)", r.Metrics().AckUps)
	}
}

func TestRemoteResumeOrderAndConnOpenAndStats(t *testing.T) {
	r, gw := newPipeRemote(t, RemoteConfig{
		Logger: log.New(testLogWriter{t: t}, "", 0), // 排障：Remote 内部日志进测试输出
		OnResume: func(_ context.Context, req connproto.ResumeReq) (connproto.ResumeResp, []connproto.Frame) {
			return connproto.ResumeResp{OK: true, From: 1, To: 2, Replayed: 2, Epoch: 3},
				[]connproto.Frame{testFrame(1, 8), testFrame(2, 8)}
		},
		OnConnOpen: func(op connproto.ConnOpen) connproto.ConnOpenAck {
			return connproto.ConnOpenAck{OK: true, Epoch: 3, ResumeToken: "tok-1"}
		},
	})
	gw.autoAck = true
	<-gw.ready
	waitFor(t, "connected", func() bool { return r.Metrics().Connected })

	// Resume：网关发 ResumeReq → 内核必须先 ResumeResp 再补发 DeliverReq。
	gw.send(connproto.KindResumeReq, connproto.ResumeReq{UserID: 5, DeviceID: "ios", ConnEpoch: 3})
	waitFor(t, "resume frames delivered", func() bool { return r.Metrics().Resumes >= 1 })
	waitFor(t, "replay DeliverReq", func() bool {
		kinds := gw.kinds()
		for _, k := range kinds {
			if k == connproto.KindDeliverReq {
				return true
			}
		}
		return false
	})
	kinds := gw.kinds()
	firstResp, firstDeliver := -1, -1
	for i, k := range kinds {
		if k == connproto.KindResumeResp && firstResp < 0 {
			firstResp = i
		}
		if k == connproto.KindDeliverReq && firstDeliver < 0 {
			firstDeliver = i
		}
	}
	if firstResp < 0 || firstDeliver < 0 || firstResp > firstDeliver {
		t.Fatalf("order = %v, want ResumeResp before DeliverReq", kinds)
	}
	var resp connproto.ResumeResp
	if err := connproto.DecodeJSONBody(gw.lastBody(connproto.KindResumeResp), &resp); err != nil {
		t.Fatalf("decode ResumeResp: %v", err)
	}
	if !resp.OK || resp.Epoch != 3 {
		t.Fatalf("ResumeResp = %+v", resp)
	}

	// ConnOpen → ConnOpenAck。
	gw.send(connproto.KindConnOpen, connproto.ConnOpen{ConnID: 1, UserID: 5, DeviceID: "ios"})
	waitFor(t, "ConnOpen received by kernel", func() bool { return r.Metrics().ConnOpens >= 1 })
	waitFor(t, "ConnOpenAck", func() bool { return len(gw.lastBody(connproto.KindConnOpenAck)) > 0 })
	var openAck connproto.ConnOpenAck
	if err := connproto.DecodeJSONBody(gw.lastBody(connproto.KindConnOpenAck), &openAck); err != nil {
		t.Fatalf("decode ConnOpenAck: %v", err)
	}
	if !openAck.OK || openAck.Epoch != 3 || openAck.ResumeToken != "tok-1" {
		t.Fatalf("ConnOpenAck = %+v", openAck)
	}

	// StatsReq → StatsResp（内核侧指标：本次 resume 补发的 2 帧已被 gateway accepted）。
	gw.send(connproto.KindStatsReq, nil)
	waitFor(t, "StatsResp", func() bool { return len(gw.lastBody(connproto.KindStatsResp)) > 0 })
	var st connproto.StatsResp
	if err := connproto.DecodeJSONBody(gw.lastBody(connproto.KindStatsResp), &st); err != nil {
		t.Fatalf("decode StatsResp: %v", err)
	}
	if st.FramesOut != 2 {
		t.Fatalf("StatsResp = %+v, want FramesOut=2 (replay 帧已被 accepted)", st)
	}
}

func TestRemoteNoGatewayNonBlocking(t *testing.T) {
	r := NewRemote(RemoteConfig{
		UDSPath: "/nonexistent/im2a-conn.sock",
		Dial: func(context.Context, string) (net.Conn, error) {
			return nil, context.DeadlineExceeded
		},
		Logger:        log.New(io.Discard, "", 0),
		ReconnectMin:  5 * time.Millisecond,
		ReconnectMax:  10 * time.Millisecond,
		FlushInterval: time.Millisecond,
	})
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	start := time.Now()
	res := r.Deliver(1, []connproto.Frame{testFrame(1, 1024)})
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("Deliver blocked %v with gateway down", elapsed)
	}
	if res.Enqueued != 0 || res.DroppedSlow != 1 {
		t.Fatalf("Deliver result = %+v, want dropped (gateway down)", res)
	}
	if r.Metrics().DroppedNotReady == 0 {
		t.Fatalf("DroppedNotReady = 0")
	}
}

func TestEmbeddedEventSink(t *testing.T) {
	hub := connhub.NewHub(connhub.DefaultConfig())
	defer hub.Close()
	sink := NewEmbedded(hub)
	res := sink.Deliver(1, []connproto.Frame{testFrame(1, 8)})
	if res.TargetConns != 0 {
		t.Fatalf("Deliver to empty hub TargetConns=%d, want 0", res.TargetConns)
	}
	if st := sink.Stats(); st.Conns != 0 {
		t.Fatalf("Stats.Conns = %d, want 0", st.Conns)
	}
	var nilSink *Embedded
	if nilSink.Deliver(1, nil) != (connhub.DeliverResult{}) {
		t.Fatal("nil Embedded must return zero result")
	}
}

// TestRemoteNoGatewayLatency 对应 task-17 验收 #2：conn-gateway 不在时内核可用、
// 投递降级计数增长、主路径（EventSink.Deliver）p99 退化可忽略。
func TestRemoteNoGatewayLatency(t *testing.T) {
	r := NewRemote(RemoteConfig{
		UDSPath: "/nonexistent/im2a-conn.sock",
		Dial: func(context.Context, string) (net.Conn, error) {
			return nil, context.DeadlineExceeded
		},
		Logger:        log.New(io.Discard, "", 0),
		ReconnectMin:  5 * time.Millisecond,
		ReconnectMax:  10 * time.Millisecond,
		FlushInterval: time.Millisecond,
	})
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	frame := testFrame(1, 256)
	const n = 2000
	lat := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		r.Deliver(1, []connproto.Frame{frame})
		lat = append(lat, time.Since(start))
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p50, p99 := lat[n/2], lat[n*99/100]
	if p99 > 5*time.Millisecond {
		t.Fatalf("gateway down: Deliver p99=%v (>5ms)，主路径被拖慢", p99)
	}
	if r.Metrics().DroppedNotReady == 0 {
		t.Fatalf("DroppedNotReady = 0，降级计数未增长")
	}
	// 参照基线：进程内 embedded sink 的同一 Deliver 路径。
	hub := connhub.NewHub(connhub.DefaultConfig())
	defer hub.Close()
	emb := NewEmbedded(hub)
	base := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		emb.Deliver(1, []connproto.Frame{frame})
		base = append(base, time.Since(start))
	}
	sort.Slice(base, func(i, j int) bool { return base[i] < base[j] })
	bp50, bp99 := base[n/2], base[n*99/100]
	t.Logf("gateway-down deliver latency: p50=%v p99=%v | embedded baseline: p50=%v p99=%v",
		p50, p99, bp50, bp99)
}

// TestRemoteQueueFullDropsNonBlocking 对应 task-17 验收 #4：网关慢（不读 UDS）时
// 内核侧有界队列满即丢并计数，Deliver 绝不阻塞。
func TestRemoteQueueFullDropsNonBlocking(t *testing.T) {
	client, server := net.Pipe()
	// fake gateway：完成 Hello 握手后停止读取（模拟网关卡住/慢消费）。
	go func() {
		uc := connproto.NewUDSConn(server)
		kind, _, body, err := uc.ReadMsg()
		if err != nil || kind != connproto.KindHello {
			return
		}
		var hello connproto.UDSHello
		if err := connproto.DecodeJSONBody(body, &hello); err != nil {
			return
		}
		ack := connproto.NegotiateUDS(hello)
		ab, _ := connproto.EncodeJSONBody(ack)
		_ = uc.WriteMsg(connproto.KindHelloAck, ab)
		<-time.After(2 * time.Second) // 不再读取：内核对 UDS 的写会阻塞
		_ = server.Close()
	}()
	r := NewRemote(RemoteConfig{
		UDSPath:       "pipe-slow",
		Dial:          func(context.Context, string) (net.Conn, error) { return client, nil },
		QueueFrames:   2,
		QueueBytes:    1 << 20,
		Logger:        log.New(io.Discard, "", 0),
		ReconnectMin:  5 * time.Millisecond,
		ReconnectMax:  10 * time.Millisecond,
		FlushInterval: time.Millisecond,
	})
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	waitFor(t, "connected", func() bool { return r.Metrics().Connected })

	dropped := 0
	for i := 0; i < 20; i++ {
		start := time.Now()
		res := r.Deliver(1, []connproto.Frame{testFrame(uint64(i+1), 1024)})
		if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
			t.Fatalf("Deliver blocked %v when queue full", elapsed)
		}
		dropped += res.DroppedSlow
	}
	if dropped == 0 {
		t.Fatalf("slow gateway: 没有发生丢帧（queueFull 未触发）")
	}
	if got := r.Metrics().DroppedQueueFull + r.Metrics().DroppedCritical; got == 0 {
		t.Fatalf("DroppedQueueFull+DroppedCritical = 0, want >0")
	}
	t.Logf("slow gateway: Deliver 非阻塞，drops=%d (queueFull=%d)", dropped, r.Metrics().DroppedQueueFull)
}

// TestRemotePresenceLeaseAndState 覆盖 C1 的两条 presence UDS 方向：
//   - 上行 kind=26：网关发租约 → Remote 解码 → OnPresenceLease 回调 + 计数；
//   - 下行 kind=27：Remote.DeliverPresenceState → 网关收到 PresenceState（易失，不进投递队列）。
func TestRemotePresenceLeaseAndState(t *testing.T) {
	leases := make(chan connproto.PresenceLease, 1)
	r, gw := newPipeRemote(t, RemoteConfig{
		OnPresenceLease: func(l connproto.PresenceLease) bool {
			leases <- l
			return true
		},
	})
	gw.autoAck = true
	<-gw.ready
	waitFor(t, "connected", func() bool { return r.Metrics().Connected })

	// 上行租约。
	gw.send(connproto.KindPresenceLease, connproto.PresenceLease{
		ConnID: 1, UserID: 7, DeviceID: "d1", TTLS: 45, AtMS: 123, State: "online",
	})
	select {
	case l := <-leases:
		if l.UserID != 7 || l.DeviceID != "d1" || l.TTLS != 45 || l.State != "online" {
			t.Fatalf("lease = %+v", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnPresenceLease 未被调用")
	}
	waitFor(t, "presence lease metric", func() bool { return r.Metrics().PresenceLeases == 1 })

	// 下行聚合状态。
	if err := r.DeliverPresenceState(connproto.PresenceState{
		UserID: 7, State: "online", UpdatedAt: 456, Devices: 2,
	}); err != nil {
		t.Fatalf("DeliverPresenceState: %v", err)
	}
	waitFor(t, "gateway got PresenceState", func() bool {
		return len(gw.lastBody(connproto.KindPresenceState)) > 0
	})
	var st connproto.PresenceState
	if err := connproto.DecodeJSONBody(gw.lastBody(connproto.KindPresenceState), &st); err != nil {
		t.Fatalf("decode PresenceState: %v", err)
	}
	if st.UserID != 7 || st.State != "online" || st.Devices != 2 {
		t.Fatalf("PresenceState = %+v", st)
	}
	if r.Metrics().PresenceStates != 1 {
		t.Fatalf("PresenceStates = %d, want 1", r.Metrics().PresenceStates)
	}
}

// 网关不在时 presence 下发必须立即失败（易失，不阻塞/不排队）。
func TestRemotePresenceStateNoGateway(t *testing.T) {
	r := NewRemote(RemoteConfig{
		UDSPath: "/nonexistent/c1.sock",
		Dial: func(context.Context, string) (net.Conn, error) {
			return nil, context.DeadlineExceeded
		},
		Logger:       log.New(io.Discard, "", 0),
		ReconnectMin: 5 * time.Millisecond,
		ReconnectMax: 10 * time.Millisecond,
	})
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	start := time.Now()
	err := r.DeliverPresenceState(connproto.PresenceState{UserID: 1, State: "online"})
	if err == nil {
		t.Fatal("网关不在时 DeliverPresenceState 应返回错误")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("DeliverPresenceState 阻塞 %v", elapsed)
	}
}

// TestRemoteIdleNoReconnect 回归（C4 flake 根因）：握手期临时读超时未清零会导致
// 空闲 DialTimeout(3s) 后 ReadMsg 超时→反复重连。修复后空闲 4s 必须仍只有 1 次连接。
func TestRemoteIdleNoReconnect(t *testing.T) {
	r, gw := newPipeRemote(t, RemoteConfig{})
	gw.autoAck = true
	<-gw.ready
	waitFor(t, "connected", func() bool { return r.Metrics().Connected })
	time.Sleep(4 * time.Second)
	m := r.Metrics()
	if m.Reconnects != 1 || !m.Connected {
		t.Fatalf("空闲 4s 后 reconnects=%d connected=%v, want 1/true（读超时未清零会反复重连）", m.Reconnects, m.Connected)
	}
}