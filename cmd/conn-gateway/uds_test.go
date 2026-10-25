package main

// uds_test.go：B2-b 网关侧端到端测试。
//
// 测试形态：真 UDS（unix socket）+ 真 WS（httptest）+ 一个 **fake kernel**（用
// connproto.NewUDSConn 模拟 wire-dev 的 internal/connapi 客户端）。这样网关侧的行为
// 不依赖内核实现，但用的是与内核完全相同的线格式。
//
// 覆盖：
//  1. 内核经 UDS 推一帧 → 已连接 WS 客户端收到（端到端）
//  2. ConnOpen → 内核签发 epoch/resumeToken → 客户端 auth_ok 里拿到
//  3. resume：epoch 不匹配被拒（err RESUME_REJECTED + 关连接）
//  4. resume：成功时把 oldest_available/dropped_before/truncated 透传给客户端
//  5. 客户端 ack → 网关发 AckUp（带 device_id/conn_epoch）——**只有客户端 ack 才产生 AckUp**
//  6. Hello 版本不匹配 → HelloAck{accepted:false, reason} 且连接关闭
//  7. 网关侧背压：ring 灌满后 DeliverAck.dropped_slow>0（丢弃不进 accepted → 不会被 ack）

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

// ---- 测试用网关装配（与 main 的装配顺序一致，但地址/路径可注入） ----

type testGateway struct {
	gw      *gateway
	http    *httptest.Server
	udsPath string
}

func newTestGateway(t *testing.T, ringBytes int) *testGateway {
	t.Helper()
	cfg := connhub.DefaultConfig()
	cfg.RingBytes = ringBytes
	g := &gateway{
		cfg:      cfg,
		metric:   newMetricsRegistry(),
		ackLag:   newAckLagTracker(),
		startedAt: time.Now(),
		sessions: map[uint64]*wsSession{},
		verbose:  false,
		// 测试网关默认开 dev-insecure：绝大多数用例不带 JWT（C1 严格模式会拒绝无 token 连接）。
		// 严格模式的行为由 TestC1_Strict* 用例单独把 devInsecure 置 false 验证。
		devInsecure: true,
	}
	g.cfg.OnAck = g.onAck
	g.cfg.OnSlowConsumer = g.onSlowConsumer
	g.cfg.OnDegrade = g.onDegrade
	g.cfg.Logf = g.logf
	g.hub = connhub.NewHub(g.cfg)
	g.upgrader = websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(r *http.Request) bool { return true },
	}

	udsPath := filepath.Join(t.TempDir(), "conn.sock")
	g.uds = newUDSServer(g, udsPath, "test-gw")
	if err := g.uds.Start(); err != nil {
		t.Fatalf("UDS start: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", g.handleHealth)
	mux.HandleFunc("GET /v1/ws", g.handleWS)
	mux.HandleFunc("GET /metrics", g.handleMetrics)
	hs := httptest.NewServer(mux)

	tg := &testGateway{gw: g, http: hs, udsPath: udsPath}
	t.Cleanup(func() {
		hs.Close()
		g.hub.Close()
		g.uds.Stop()
	})
	return tg
}

// ---- fake kernel（与 internal/connapi 用同一条线格式） ----

type fakeKernel struct {
	t    *testing.T
	conn *connproto.UDSConn

	opens     chan connproto.ConnOpen
	resumes   chan connproto.ResumeReq
	acks      chan connproto.DeliverAck
	ackUps    chan connproto.AckUp
	closedNot chan connproto.ConnClosed
	leases    chan connproto.PresenceLease

	mu           sync.Mutex
	openReply    *connproto.ConnOpenAck
	resumeReply  func(req connproto.ResumeReq) connproto.ResumeResp
	statsLagSec  float64
	droppedAckMs int
	helloAck     connproto.UDSHelloAck
	omitOpenConnID bool // true = 模拟旧内核：ConnOpenAck 不回显 conn_id
}

func dialFakeKernel(t *testing.T, path string) *fakeKernel {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial UDS: %v", err)
	}
	fk := &fakeKernel{
		t: t, conn: connproto.NewUDSConn(c),
		opens:     make(chan connproto.ConnOpen, 16),
		resumes:   make(chan connproto.ResumeReq, 16),
		acks:      make(chan connproto.DeliverAck, 256),
		ackUps:    make(chan connproto.AckUp, 256),
		closedNot: make(chan connproto.ConnClosed, 16),
		leases:    make(chan connproto.PresenceLease, 16),
	}
	fk.openReply = &connproto.ConnOpenAck{OK: true, ResumeToken: "tok-1", Epoch: 7}

	// 握手必须**同步**做完再起读循环：同一 UDSConn 只能有一个读者。
	body, _ := connproto.EncodeJSONBody(connproto.UDSHello{
		ProtoVersion: connproto.UDSProtoVersion, Instance: "fake-kernel",
		MaxFrameBytes: 64 << 10, MaxBatchFrames: 1024,
	})
	if err := fk.conn.WriteMsg(connproto.KindHello, body); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	kind, _, rb, err := fk.conn.ReadMsg()
	if err != nil || kind != connproto.KindHelloAck {
		t.Fatalf("read hello ack: kind=%v err=%v", kind, err)
	}
	if err := connproto.DecodeJSONBody(rb, &fk.helloAck); err != nil {
		t.Fatalf("decode hello ack: %v", err)
	}
	if !fk.helloAck.Accepted {
		t.Fatalf("hello 被拒: %+v", fk.helloAck)
	}

	go fk.readLoop()
	t.Cleanup(func() { _ = fk.conn.Close() })
	return fk
}

func (fk *fakeKernel) readLoop() {
	for {
		kind, _, body, err := fk.conn.ReadMsg()
		if err != nil {
			return
		}
		switch kind {
		case connproto.KindConnOpen:
			var req connproto.ConnOpen
			_ = connproto.DecodeJSONBody(body, &req)
			fk.mu.Lock()
			reply := *fk.openReply
			fk.mu.Unlock()
			if !fk.omitOpenConnID {
				reply.ConnID = req.ConnID
			} else {
				reply.ConnID = 0
			}
			if b, e := connproto.EncodeJSONBody(reply); e == nil {
				_ = fk.conn.WriteMsg(connproto.KindConnOpenAck, b)
			}
			select {
			case fk.opens <- req:
			default:
			}

		case connproto.KindResumeReq:
			var req connproto.ResumeReq
			_ = connproto.DecodeJSONBody(body, &req)
			fk.mu.Lock()
			replyFn := fk.resumeReply
			fk.mu.Unlock()
			resp := connproto.ResumeResp{ConnID: req.ConnID, OK: true}
			if replyFn != nil {
				resp = replyFn(req)
				resp.ConnID = req.ConnID
			}
			if b, e := connproto.EncodeJSONBody(resp); e == nil {
				_ = fk.conn.WriteMsg(connproto.KindResumeResp, b)
			}
			select {
			case fk.resumes <- req:
			default:
			}

		case connproto.KindDeliverAck:
			var ack connproto.DeliverAck
			_ = connproto.DecodeJSONBody(body, &ack)
			fk.acks <- ack

		case connproto.KindAckUp:
			var a connproto.AckUp
			_ = connproto.DecodeJSONBody(body, &a)
			fk.ackUps <- a

		case connproto.KindConnClosed:
			var c connproto.ConnClosed
			_ = connproto.DecodeJSONBody(body, &c)
			select {
			case fk.closedNot <- c:
			default:
			}

		case connproto.KindPresenceLease:
			var l connproto.PresenceLease
			_ = connproto.DecodeJSONBody(body, &l)
			select {
			case fk.leases <- l:
			default:
			}

		case connproto.KindStatsReq:
			fk.mu.Lock()
			lag := fk.statsLagSec
			fk.mu.Unlock()
			if b, e := connproto.EncodeJSONBody(connproto.StatsResp{AckLagSeconds: lag}); e == nil {
				_ = fk.conn.WriteMsg(connproto.KindStatsResp, b)
			}

		case connproto.KindPing:
			_ = fk.conn.WriteMsg(connproto.KindPong, nil)
		}
	}
}

func (fk *fakeKernel) sendPresenceState(st connproto.PresenceState) {
	fk.t.Helper()
	body, err := connproto.EncodeJSONBody(st)
	if err != nil {
		fk.t.Fatalf("encode PresenceState: %v", err)
	}
	if err := fk.conn.WriteMsg(connproto.KindPresenceState, body); err != nil {
		fk.t.Fatalf("write PresenceState: %v", err)
	}
}

func (fk *fakeKernel) deliver(req connproto.DeliverReq) {
	fk.t.Helper()
	chunks, err := connproto.SplitDeliverReq(&req)
	if err != nil {
		fk.t.Fatalf("split: %v", err)
	}
	for _, c := range chunks {
		body, err := connproto.EncodeDeliverReq(c)
		if err != nil {
			fk.t.Fatalf("encode deliver: %v", err)
		}
		if err := fk.conn.WriteMsg(connproto.KindDeliverReq, body); err != nil {
			fk.t.Fatalf("write deliver: %v", err)
		}
	}
}

// ---- WS 客户端（原生客户端替身） ----

type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
	auth *connproto.ControlFrame
}

func dialWSClient(t *testing.T, srvURL, userID, deviceID string) *wsClient {
	t.Helper()
	return dialWSClientToken(t, srvURL, userID, deviceID, "")
}

// dialWSClientToken 发送带 token 的 hello（C1 严格模式必须用这个）。
func dialWSClientToken(t *testing.T, srvURL, userID, deviceID, jwt string) *wsClient {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srvURL, "http") + "/v1/ws?user_id=" + userID + "&device_id=" + deviceID
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	wc := &wsClient{t: t, conn: c}
	t.Cleanup(func() { _ = c.Close() })

	hello, _ := connproto.EncodeControl(&connproto.ControlFrame{T: connproto.CtlHello, V: 1, Token: jwt})
	if err := c.WriteMessage(websocket.TextMessage, hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	// hello_ok
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read hello_ok: %v", err)
	}
	if ctl, err := connproto.DecodeControl(data); err != nil || ctl.T != connproto.CtlHelloOK {
		t.Fatalf("hello_ok mismatch: %v %v", ctl, err)
	}
	// auth_ok
	_, data, err = c.ReadMessage()
	if err != nil {
		t.Fatalf("read auth_ok: %v", err)
	}
	auth, err := connproto.DecodeControl(data)
	if err != nil || auth.T != connproto.CtlAuthOK {
		t.Fatalf("auth_ok mismatch: %v %v", auth, err)
	}
	wc.auth = auth
	_ = c.SetReadDeadline(time.Time{})
	return wc
}

func (w *wsClient) sendControl(c *connproto.ControlFrame) {
	w.t.Helper()
	b, err := connproto.EncodeControl(c)
	if err != nil {
		w.t.Fatalf("encode control: %v", err)
	}
	if err := w.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		w.t.Fatalf("write control: %v", err)
	}
}

func (w *wsClient) readControl(t *testing.T) (*connproto.ControlFrame, error) {
	t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := w.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	return connproto.DecodeControl(data)
}

func (w *wsClient) readFrame(t *testing.T) (connproto.Frame, error) {
	t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	mt, data, err := w.conn.ReadMessage()
	if err != nil {
		return connproto.Frame{}, err
	}
	if mt != websocket.BinaryMessage {
		return connproto.Frame{}, errNotBinary
	}
	f, _, err := connproto.Decode(data)
	return f, err
}

var errNotBinary = &testErr{"expected binary frame"}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }

// ---- 测试 ----

func TestUDSEndToEnd_DeliverReachesClient(t *testing.T) {
	tg := newTestGateway(t, 16384)
	fk := dialFakeKernel(t, tg.udsPath)
	wc := dialWSClient(t, tg.http.URL, "42", "dev-A")

	// 内核收到 ConnOpen，且客户端 auth_ok 里带回 epoch/token
	select {
	case op := <-fk.opens:
		if op.UserID != 42 || op.DeviceID != "dev-A" {
			t.Fatalf("ConnOpen 字段不对: %+v", op)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内未收到 ConnOpen")
	}
	if wc.auth.ConnEpoch != 7 || wc.auth.ResumeToken != "tok-1" {
		t.Fatalf("auth_ok 未带回 epoch/token: %+v", wc.auth)
	}

	// 内核推一帧 → 客户端收到
	fk.deliver(connproto.DeliverReq{UserID: 42, BatchID: 100, Frames: []connproto.Frame{
		{Type: connproto.TypeMsg, Flags: connproto.FlagCritical, Seq: 1, Payload: []byte("hello-native")},
	}})
	f, err := wc.readFrame(t)
	if err != nil {
		t.Fatalf("client read frame: %v", err)
	}
	if f.Seq != 1 || string(f.Payload) != "hello-native" {
		t.Fatalf("frame mismatch: %+v", f)
	}
	// 内核收到该批的 DeliverAck
	select {
	case a := <-fk.acks:
		if a.BatchID != 100 || a.Accepted != 1 || a.DroppedSlow != 0 {
			t.Fatalf("DeliverAck 不对: %+v", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内未收到 DeliverAck")
	}
}

func TestUDSResume_EpochMismatchRejected(t *testing.T) {
	tg := newTestGateway(t, 16384)
	fk := dialFakeKernel(t, tg.udsPath)
	fk.mu.Lock()
	fk.resumeReply = func(req connproto.ResumeReq) connproto.ResumeResp {
		return connproto.ResumeResp{OK: false, Reason: "epoch_mismatch"}
	}
	fk.mu.Unlock()
	wc := dialWSClient(t, tg.http.URL, "42", "dev-A")
	<-fk.opens

	wc.sendControl(&connproto.ControlFrame{T: connproto.CtlResume, ResumeToken: "tok-1", LastAckSeq: 0, ConnEpoch: 7})

	ctl, err := wc.readControl(t)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if ctl.T != connproto.CtlErr || ctl.Code != "RESUME_REJECTED" {
		t.Fatalf("期望 RESUME_REJECTED err，got %+v", ctl)
	}
	// 随后连接应被关闭（内核 epoch 不匹配 → 要求全量重连）
	if _, err := wc.readFrame(t); err == nil {
		t.Fatal("epoch 不匹配后连接应关闭，但仍能读到帧")
	}
}

func TestUDSResume_TruncationFieldsPassThrough(t *testing.T) {
	tg := newTestGateway(t, 16384)
	fk := dialFakeKernel(t, tg.udsPath)
	fk.mu.Lock()
	fk.resumeReply = func(req connproto.ResumeReq) connproto.ResumeResp {
		return connproto.ResumeResp{
			OK: true, From: 5, To: 7, Replayed: 3,
			Truncated: true, OldestAvailable: 6, DroppedBefore: 2, Epoch: 7,
		}
	}
	fk.mu.Unlock()
	wc := dialWSClient(t, tg.http.URL, "42", "dev-A")
	<-fk.opens

	wc.sendControl(&connproto.ControlFrame{T: connproto.CtlResume, ResumeToken: "tok-1", LastAckSeq: 4, ConnEpoch: 7})
	ctl, err := wc.readControl(t)
	if err != nil {
		t.Fatalf("read resume_ok: %v", err)
	}
	if ctl.T != connproto.CtlResumeOK {
		t.Fatalf("期望 resume_ok, got %+v", ctl)
	}
	if !ctl.Truncated || ctl.OldestAvailable != 6 || ctl.DroppedBefore != 2 || ctl.From != 5 || ctl.To != 7 || ctl.Replayed != 3 {
		t.Fatalf("截断字段透传错误: %+v", ctl)
	}
}

func TestUDSAckUpOnlyFromClientAck(t *testing.T) {
	tg := newTestGateway(t, 16384)
	fk := dialFakeKernel(t, tg.udsPath)
	wc := dialWSClient(t, tg.http.URL, "42", "dev-A")
	<-fk.opens

	// 内核推一帧（会被写出，但**不**产生 AckUp）
	fk.deliver(connproto.DeliverReq{UserID: 42, BatchID: 1, Frames: []connproto.Frame{
		{Type: connproto.TypeMsg, Flags: connproto.FlagCritical, Seq: 9, Payload: []byte("x")},
	}})
	if f, err := wc.readFrame(t); err != nil || f.Seq != 9 {
		t.Fatalf("read frame: %+v err=%v", f, err)
	}
	select {
	case a := <-fk.ackUps:
		t.Fatalf("写成功不应产生 AckUp（只有客户端 ack 才产生）: %+v", a)
	case <-time.After(200 * time.Millisecond):
	}

	// 客户端 ack → 网关发 AckUp，带 device_id 与 epoch
	wc.sendControl(&connproto.ControlFrame{T: connproto.CtlAck, Seq: 9})
	select {
	case a := <-fk.ackUps:
		if a.AckSeq != 9 || a.DeviceID != "dev-A" || a.ConnEpoch != 7 || a.UserID != 42 || a.ConnID == 0 {
			t.Fatalf("AckUp 字段不对: %+v", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内未收到 AckUp")
	}
}

func TestUDSHelloVersionMismatchRejected(t *testing.T) {
	tg := newTestGateway(t, 16384)
	c, err := net.Dial("unix", tg.udsPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	uc := connproto.NewUDSConn(c)
	body, _ := connproto.EncodeJSONBody(connproto.UDSHello{ProtoVersion: connproto.UDSProtoVersion + 1})
	if err := uc.WriteMsg(connproto.KindHello, body); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	kind, _, rb, err := uc.ReadMsg()
	if err != nil || kind != connproto.KindHelloAck {
		t.Fatalf("read hello ack: kind=%v err=%v", kind, err)
	}
	var ack connproto.UDSHelloAck
	_ = json.Unmarshal(rb, &ack)
	if ack.Accepted || ack.Reason == "" {
		t.Fatalf("版本不匹配必须 accepted=false 且带 reason: %+v", ack)
	}
	// 网关随后应关闭连接
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, _, err := uc.ReadMsg(); err == nil {
		t.Fatal("版本不匹配后连接应被关闭")
	}
}

// TestGatewayBackpressure_RingDropNotAcked ring 很小时灌满：
//   * 网关指标出现 reason="ring" 丢弃（或慢消费者被踢）；
//   * 被丢弃的帧**不会**产生 AckUp（客户端从未 ack）——内核据位点重发即可。
//
// 注意：批量级的 DeliverAck.accepted 只表示"进入网关传输层（shard 队列）"，不是交付保证；
// ring 级丢弃发生在 shard goroutine 里（晚于该批 ack），因此只能通过指标观测。
// 真正的交付确认只有客户端 ack → AckUp。
func TestGatewayBackpressure_RingDropNotAcked(t *testing.T) {
	tg := newTestGateway(t, 256) // 256B 字节预算：写端一阻塞就灌满
	fk := dialFakeKernel(t, tg.udsPath)
	wc := dialWSClient(t, tg.http.URL, "42", "dev-A")
	// 让客户端"不读且接收窗口极小"，使服务端写很快阻塞，从而把 ring 灌满。
	if tc, ok := wc.conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(1024)
	}
	<-fk.opens

	// 3 批 × 2000 帧 × ~900B ≈ 5.4MB，远超客户端接收窗口；SplitDeliverReq 会自动切到 ≤1MiB/批。
	payload := make([]byte, 900)
	for batch := 0; batch < 3; batch++ {
		frames := make([]connproto.Frame, 0, 2000)
		base := batch * 2000
		for i := 0; i < 2000; i++ {
			frames = append(frames, connproto.Frame{
				Type: connproto.TypeMsg, Flags: connproto.FlagCritical,
				Seq: uint64(base + i + 1), Payload: payload,
			})
		}
		fk.deliver(connproto.DeliverReq{UserID: 42, BatchID: uint64(100 + batch), Frames: frames})
	}

	// 等 ring 丢弃或慢消费者出现（两者任一都证明背压生效）。
	deadline := time.Now().Add(10 * time.Second)
	var lastMetrics string
	for time.Now().Before(deadline) {
		lastMetrics = scrapeMetrics(t, tg.http.URL)
		if strings.Contains(lastMetrics, `conn_gateway_frames_dropped_total{reason="ring"}`) &&
			!strings.Contains(lastMetrics, `conn_gateway_frames_dropped_total{reason="ring"} 0`) {
			break
		}
		if strings.Contains(lastMetrics, "conn_gateway_slow_kicks_total 1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(lastMetrics, `reason="ring"`) && !strings.Contains(lastMetrics, "conn_gateway_slow_kicks_total 1") {
		t.Fatalf("ring 灌满后既没有 ring 丢弃也没有 slow kick；metrics:\n%s", lastMetrics)
	}

	// 丢弃的帧绝不能产生 AckUp（只有客户端 ack 才产生；本测试客户端从不 ack）。
	select {
	case a := <-fk.ackUps:
		t.Fatalf("客户端未 ack，却收到 AckUp（丢弃/写成功不应产生 ack）: %+v", a)
	case <-time.After(300 * time.Millisecond):
	}
}
// TestMetricsKernelAckLagObservedAfterFirstStats：内核握手完成后网关应**立刻**拉一次 Stats，
// 让 kernel_ack_lag_seconds 从"未观测（NaN）"变成观测值（0 是有效健康值）。
// 如果不立即拉，短生命周期 E2E（1~2s）里该指标会一直是 NaN，被误判成指标坏了。
func TestMetricsKernelAckLagObservedAfterFirstStats(t *testing.T) {
	tg := newTestGateway(t, 16384)
	_ = dialFakeKernel(t, tg.udsPath) // 构造即完成 Hello 握手，随后 readLoop 会回 StatsResp

	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = scrapeMetrics(t, tg.http.URL)
		if strings.Contains(last, "kernel_ack_lag_seconds 0\n") {
			// 观测后不应再出现 NaN
			if strings.Contains(last, "kernel_ack_lag_seconds NaN") {
				t.Fatalf("观测后仍出现 NaN：\n%s", last)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("3s 内 kernel_ack_lag_seconds 未从 NaN 变为观测值：\n%s", last)
}

func scrapeMetrics(t *testing.T, base string) string {
	t.Helper()
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64<<10)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}


// ---- C1（task-21）身份：token 透传 / 内核拒绝 / 权威 userID 回填 ----

func (fk *fakeKernel) setOpenReply(ack connproto.ConnOpenAck) {
	fk.mu.Lock()
	fk.openReply = &ack
	fk.mu.Unlock()
}

// TestC1_TokenPassthroughAndReject：网关把 hello.auth_token 原样放进 ConnOpen.Token（不验签）；
// 内核回 OK=false 时网关给客户端 err{CONN_OPEN_REJECTED} 并关连接（不猜身份、不降级放行）。
func TestC1_TokenPassthroughAndReject(t *testing.T) {
	tg := newTestGateway(t, 16384)
	fk := dialFakeKernel(t, tg.udsPath)
	fk.setOpenReply(connproto.ConnOpenAck{OK: false, Reason: "auth_required"})

	c, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(tg.http.URL, "http")+"/v1/ws?user_id=999&device_id=dev-X", nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer c.Close()

	hello, _ := connproto.EncodeControl(&connproto.ControlFrame{T: connproto.CtlHello, V: 1, Token: "bad-token"})
	if err := c.WriteMessage(websocket.TextMessage, hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read err frame: %v", err)
	}
	ctl, err := connproto.DecodeControl(data)
	if err != nil || ctl.T != connproto.CtlErr || ctl.Code != "CONN_OPEN_REJECTED" {
		t.Fatalf("期望 err{CONN_OPEN_REJECTED}, got %+v err=%v", ctl, err)
	}
	// 随后连接关闭
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("CONN_OPEN_REJECTED 后连接应关闭")
	}
	// 内核收到的 ConnOpen：token 原样透传；**有 token 时网关不再把 query user_id 当身份传下去**
	// （C1：query 只有在 -dev-insecure 且无 token 时才作为兜底）。
	select {
	case op := <-fk.opens:
		if op.Token != "bad-token" || op.UserID != 0 || op.DeviceID != "dev-X" {
			t.Fatalf("ConnOpen 字段不对: %+v", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("内核未收到 ConnOpen")
	}
}

// TestC1_AuthoritativeUserIDInAuthOK：内核回 UserID 时，客户端 auth_ok 以它为准。
func TestC1_AuthoritativeUserIDInAuthOK(t *testing.T) {
	tg := newTestGateway(t, 16384)
	fk := dialFakeKernel(t, tg.udsPath)
	fk.setOpenReply(connproto.ConnOpenAck{OK: true, UserID: 42, ResumeToken: "rt-c1", Epoch: 5})

	wc := dialWSClient(t, tg.http.URL, "42", "dev-A")
	if wc.auth.UserID != 42 {
		t.Fatalf("auth_ok.UserID = %d, want 42（权威身份）", wc.auth.UserID)
	}
	if wc.auth.ResumeToken != "rt-c1" || wc.auth.ConnEpoch != 5 {
		t.Fatalf("auth_ok 未带回 token/epoch: %+v", wc.auth)
	}
	select {
	case op := <-fk.opens:
		if op.UserID != 42 {
			t.Fatalf("ConnOpen.UserID = %d", op.UserID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("内核未收到 ConnOpen")
	}
}

// ---- C1 严格模式（dev-insecure 关闭）与 presence 租约 ----

// TestC1_Strict_NoTokenRejected 严格模式下 hello 不带 token：直接拒绝，且**不向内核发 ConnOpen**。
func TestC1_Strict_NoTokenRejected(t *testing.T) {
	tg := newTestGateway(t, 16384)
	tg.gw.devInsecure = false // 严格模式
	fk := dialFakeKernel(t, tg.udsPath)

	c, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(tg.http.URL, "http")+"/v1/ws?user_id=999&device_id=dev-X", nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer c.Close()
	hello, _ := connproto.EncodeControl(&connproto.ControlFrame{T: connproto.CtlHello, V: 1})
	if err := c.WriteMessage(websocket.TextMessage, hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read err frame: %v", err)
	}
	ctl, err := connproto.DecodeControl(data)
	if err != nil || ctl.T != connproto.CtlErr || ctl.Code != "AUTH_TOKEN_REQUIRED" {
		t.Fatalf("期望 err{AUTH_TOKEN_REQUIRED}, got %+v err=%v", ctl, err)
	}
	select {
	case op := <-fk.opens:
		t.Fatalf("严格模式无 token 不应向内核发 ConnOpen: %+v", op)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestC1_Strict_IdentityUnverifiedRejected 严格模式下内核回 UserID=0（未启用校验）：
// 网关必须拒绝，不能回退 query user_id（默认 1 之类）。
func TestC1_Strict_IdentityUnverifiedRejected(t *testing.T) {
	tg := newTestGateway(t, 16384)
	tg.gw.devInsecure = false
	fk := dialFakeKernel(t, tg.udsPath)
	fk.setOpenReply(connproto.ConnOpenAck{OK: true, UserID: 0})

	c, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(tg.http.URL, "http")+"/v1/ws?user_id=999&device_id=dev-X", nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer c.Close()
	hello, _ := connproto.EncodeControl(&connproto.ControlFrame{T: connproto.CtlHello, V: 1, Token: "opaque-token"})
	if err := c.WriteMessage(websocket.TextMessage, hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read err frame: %v", err)
	}
	ctl, err := connproto.DecodeControl(data)
	if err != nil || ctl.T != connproto.CtlErr || ctl.Code != "IDENTITY_UNVERIFIED" {
		t.Fatalf("期望 err{IDENTITY_UNVERIFIED}, got %+v err=%v", ctl, err)
	}
}

// TestC1_Strict_TokenAuthoritative：严格模式 + 合法 token → 身份取 ack.UserID，
// 注册到该 userID 下（Deliver 能路由到），auth_ok 带权威身份与 epoch/token。
func TestC1_Strict_TokenAuthoritative(t *testing.T) {
	tg := newTestGateway(t, 16384)
	tg.gw.devInsecure = false
	fk := dialFakeKernel(t, tg.udsPath)
	fk.setOpenReply(connproto.ConnOpenAck{OK: true, UserID: 1001, ResumeToken: "rt-c1", Epoch: 9})

	wc := dialWSClientToken(t, tg.http.URL, "999", "dev-A", "jwt-for-1001")
	if wc.auth.UserID != 1001 {
		t.Fatalf("auth_ok.UserID=%d, want 1001（JWT.sub 权威）", wc.auth.UserID)
	}
	if wc.auth.ResumeToken != "rt-c1" || wc.auth.ConnEpoch != 9 {
		t.Fatalf("auth_ok token/epoch 不对: %+v", wc.auth)
	}
	// 注册在 1001 下 → Deliver 能路由到
	fk.deliver(connproto.DeliverReq{UserID: 1001, BatchID: 1, Frames: []connproto.Frame{
		{Type: connproto.TypeMsg, Seq: 1, Payload: []byte("for-1001")},
	}})
	if f, err := wc.readFrame(t); err != nil || f.Seq != 1 {
		t.Fatalf("客户端未收到 1001 的帧: %+v err=%v", f, err)
	}
	// 伪造的 999 不应命中
	if res := tg.gw.hub.Deliver(999, []connproto.Frame{{Type: connproto.TypeMsg, Seq: 2}}); res.TargetConns != 0 {
		t.Fatalf("伪造 user 999 不应命中任何连接: %+v", res)
	}
}

// TestC1_PresenceLeaseSent：开启 presence-lease 后，连接建立即上报一条 lease（kind 26）。
func TestC1_PresenceLeaseSent(t *testing.T) {
	tg := newTestGateway(t, 16384)
	tg.gw.presenceLease = true
	fk := dialFakeKernel(t, tg.udsPath)
	fk.setOpenReply(connproto.ConnOpenAck{OK: true, UserID: 7, ResumeToken: "rt", Epoch: 1})
	wc := dialWSClientToken(t, tg.http.URL, "7", "dev-L", "jwt")

	select {
	case l := <-fk.leases:
		if l.UserID != 7 || l.DeviceID != "dev-L" || l.State != "online" || l.TTLS != 45 || l.ConnID == 0 {
			t.Fatalf("lease 字段不对: %+v", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内未收到 presence lease")
	}
	_ = wc
}

// ---- kind 27 PRESENCE_STATE 下行 fanout（C4 前置） ----

// TestPresenceStateFanout 内核推 kind 27 → 已连接的该 userID 客户端收到 TypePresence 帧；
// 且**不产生 AckUp**（瞬时状态，不推进位点）。
func TestPresenceStateFanout(t *testing.T) {
	tg := newTestGateway(t, 16384)
	fk := dialFakeKernel(t, tg.udsPath)
	wc := dialWSClientToken(t, tg.http.URL, "7", "dev-p", "")
	<-fk.opens

	fk.sendPresenceState(connproto.PresenceState{
		UserID: 7, State: "online", UpdatedAt: 1700000000123, Devices: 2,
	})

	f, err := wc.readFrame(t)
	if err != nil {
		t.Fatalf("客户端未收到 presence 帧: %v", err)
	}
	if f.Type != connproto.TypePresence {
		t.Fatalf("帧类型=%v, want TypePresence", f.Type)
	}
	var got connproto.PresenceState
	if err := connproto.DecodeJSONBody(f.Payload, &got); err != nil {
		t.Fatalf("payload 不是 PresenceState JSON: %v", err)
	}
	if got.UserID != 7 || got.State != "online" || got.Devices != 2 || got.UpdatedAt != 1700000000123 {
		t.Fatalf("presence 字段被网关改动（红线：网关不做状态机）: %+v", got)
	}

	// 瞬时语义：不产生 AckUp（AckUp 只来自客户端 ack）
	select {
	case a := <-fk.ackUps:
		t.Fatalf("PRESENCE_STATE 不应产生 AckUp: %+v", a)
	case <-time.After(250 * time.Millisecond):
	}

	// 指标：kind 计数 + fanout 计数
	m := scrapeMetrics(t, tg.http.URL)
	if !strings.Contains(m, `conn_gateway_uds_messages_total{kind="PRESENCE_STATE"}`) {
		t.Fatalf("缺少 uds_messages_total{kind=PRESENCE_STATE}:\n%s", m)
	}
	if !strings.Contains(m, "conn_gateway_presence_state_fanout_total 1") {
		t.Fatalf("fanout 计数未增加:\n%s", m)
	}
}

// TestPresenceStateDroppedWhenNoConn 该 userID 无本机连接 → 丢弃并计数，不报错、不重试；
// 之后的正常连接/投递不受影响。
func TestPresenceStateDroppedWhenNoConn(t *testing.T) {
	tg := newTestGateway(t, 16384)
	fk := dialFakeKernel(t, tg.udsPath)

	// 没有任何客户端连接，直接推
	fk.sendPresenceState(connproto.PresenceState{UserID: 12345, State: "online", UpdatedAt: 1, Devices: 1})

	deadline := time.Now().Add(3 * time.Second)
	var m string
	for time.Now().Before(deadline) {
		m = scrapeMetrics(t, tg.http.URL)
		if strings.Contains(m, "conn_gateway_presence_state_dropped_total 1") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(m, "conn_gateway_presence_state_dropped_total 1") {
		t.Fatalf("未计丢弃:\n%s", m)
	}
	// 不重试/不报错：UDS 仍健康，随后建连并投递正常
	wc := dialWSClientToken(t, tg.http.URL, "12345", "dev-late", "")
	<-fk.opens
	fk.sendPresenceState(connproto.PresenceState{UserID: 12345, State: "online", UpdatedAt: 2, Devices: 1})
	if f, err := wc.readFrame(t); err != nil || f.Type != connproto.TypePresence {
		t.Fatalf("丢弃后新连接未收到 presence: %+v err=%v", f, err)
	}
}

// ---- ConnOpenAck 配对正确性（C4 flake 根因回归）----

// TestConnOpenAckLegacyNoConnIDSingleWaiter：旧内核不回显 conn_id，但只有 1 个等待者 → 兼容放行。
func TestConnOpenAckLegacyNoConnIDSingleWaiter(t *testing.T) {
	tg := newTestGateway(t, 16384)
	tg.gw.devInsecure = false
	fk := dialFakeKernel(t, tg.udsPath)
	fk.mu.Lock()
	fk.omitOpenConnID = true
	fk.mu.Unlock()
	fk.setOpenReply(connproto.ConnOpenAck{OK: true, UserID: 55, ResumeToken: "rt-legacy", Epoch: 2})

	wc := dialWSClientToken(t, tg.http.URL, "999", "dev-legacy", "jwt")
	if wc.auth.UserID != 55 {
		t.Fatalf("旧内核单等待者应兼容放行：auth.UserID=%d want 55", wc.auth.UserID)
	}
}

// TestConnOpenAckUnknownConnIDIgnored：非零但未知的 conn_id → 丢弃，绝不能配给任何等待者。
func TestConnOpenAckUnknownConnIDIgnored(t *testing.T) {
	tg := newTestGateway(t, 16384)
	s := tg.gw.uds
	// 手工放一个 waiter，模拟"正在等待"的连接
	waitID := uint64(4242)
	ch := make(chan *connproto.ConnOpenAck, 1)
	s.pendMu.Lock()
	s.openWait[waitID] = ch
	s.pendMu.Unlock()

	// 一个 conn_id=999 的 ack（不属于任何等待者）
	s.deliverOpenWaiter(&connproto.ConnOpenAck{ConnID: 999, OK: true, UserID: 7})

	select {
	case ack := <-ch:
		t.Fatalf("未知 conn_id 的 ack 不应配给等待者: %+v", ack)
	case <-time.After(150 * time.Millisecond):
	}
	s.pendMu.Lock()
	stillThere := s.openWait[waitID] != nil
	delete(s.openWait, waitID)
	s.pendMu.Unlock()
	if !stillThere {
		t.Fatal("未知 conn_id 的 ack 不应消费等待者")
	}
}

// TestConnOpenAckAmbiguousLegacyDropped：旧内核不回显 conn_id 且存在多个并发等待者 →
// 必须丢弃（宁可超时拒绝，也不接受可能错误的身份）。
func TestConnOpenAckAmbiguousLegacyDropped(t *testing.T) {
	tg := newTestGateway(t, 16384)
	s := tg.gw.uds
	ch1 := make(chan *connproto.ConnOpenAck, 1)
	ch2 := make(chan *connproto.ConnOpenAck, 1)
	s.pendMu.Lock()
	s.openWait[1] = ch1
	s.openWait[2] = ch2
	s.pendMu.Unlock()

	s.deliverOpenWaiter(&connproto.ConnOpenAck{OK: true, UserID: 7}) // ConnID=0

	for i, ch := range []chan *connproto.ConnOpenAck{ch1, ch2} {
		select {
		case ack := <-ch:
			t.Fatalf("歧义 ack 不应配给 waiter%d: %+v", i+1, ack)
		case <-time.After(100 * time.Millisecond):
		}
	}
	s.pendMu.Lock()
	delete(s.openWait, 1)
	delete(s.openWait, 2)
	s.pendMu.Unlock()
}