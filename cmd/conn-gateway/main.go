// Package main 是 Wave B 的连接网关进程。
//
// B1：WS 接入 + connhub（64 shard / ring / 背压）+ /healthz + 优雅退出。
// B2-b（本版新增）：**UDS 服务端**接内核（internal/connapi）推来的 (userID, []Frame)；
// resume/epoch 协议字段落地；网关侧两层背压与指标；SIGTERM 先广播 Drain 再退出。
//
// 边界（CONN-GATEWAY-DESIGN v2 §8.1 / B2B-DESIGN §5）：
//   * 网关只认帧与 userID，**不解析 topic、不认识业务**；
//   * 网关**从不重投**：只把"客户端字节级 ack"上报内核（AckUp），重投由内核按位点负责；
//   * WS 语义与 B1 一致（只新增 resume/epoch 字段，旧客户端忽略即可）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof" // task-24：内存分解用；仅 -debug 时挂到自有 mux
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/connhub"
)

const (
	defaultAddr     = "127.0.0.1:18081"
	defaultUDSPath  = "/run/im2a/conn.sock"
	authDeadline    = 10 * time.Second
	readIdle        = 90 * time.Second
	heartbeatEvery  = 30 * time.Second
	maxInboundFrame = 256 << 10
	drainWait       = 25 * time.Second

	// presence 租约（C1，默认关闭）：刷新 15s / TTL 45s（wire-dev 的 native presence 侧口径）
	presenceLeaseRefresh = 15 * time.Second
	presenceLeaseTTL     = 45 * time.Second
)

// gateway 进程级状态（WS 与 UDS 共享同一个 hub）。
type gateway struct {
	cfg     connhub.Config
	hub     *connhub.Hub
	uds     *udsServer
	metric  *metricsRegistry
	ackLag  *ackLagTracker
	token   string
	echo    bool
	verbose bool
	// devInsecure 显式开启才允许"无 token/内核未校验时回退 query user_id"（默认严格拒绝）。
	devInsecure bool
	// presenceLease 是否发送 presence 租约（CONN_PRESENCE_LEASE=on；默认关，等内核侧就绪）。
	presenceLease bool
	// debug 暴露 /debug/pprof 与 /debug/memstats（task-24 内存分解；默认关）。
	debug bool
	// gcPercent 仅用于展示（Go 没有"只读" API，读取 SetGCPercent(-1) 会禁用 GC）。
	gcPercent atomic.Int64

	startedAt    time.Time
	connsGauge   atomic.Int64
	shardDropped atomic.Int64 // hub.Deliver 返回的 DroppedSlow 累计（用于把 hub 总丢弃归因到 ring）

	mu       sync.RWMutex
	sessions map[uint64]*wsSession

	stopHeartbeat chan struct{}

	upgrader websocket.Upgrader
}

// wsSession 一条原生连接在网关侧的会话状态。
type wsSession struct {
	conn        *connhub.Conn
	sink        *WSSink
	userID      int64  // 注册时使用的身份（兼容：query 或默认值）
	authUserID  int64  // C1：内核 ConnOpenAck 返回的权威 userID（0 = 内核未启用校验）
	deviceID    string
	epoch       atomic.Int64
	resumeToken string
	closed      atomic.Bool

	ctlMu sync.Mutex // 控制帧之间有序（与数据帧共用 sink 的写锁）
}

// effectiveUserID 返回对内核/客户端可见的身份：内核权威值优先，0 表示内核未启用校验。
func (s *wsSession) effectiveUserID() int64 {
	if s.authUserID != 0 {
		return s.authUserID
	}
	return s.userID
}

func (s *wsSession) writeControl(c *connproto.ControlFrame) error {
	b, err := connproto.EncodeControl(c)
	if err != nil {
		return err
	}
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	return s.sink.WriteRawText(append(b, '\n'))
}

// ackLagTracker 网关侧"最早未确认帧"的滞留时长（代理指标，见 B2B-DESIGN §4.2）。
type ackLagTracker struct {
	mu    sync.Mutex
	since map[int64]time.Time
	seen  bool // 是否观测过（至少发生过一次投递）；未观测时指标应为 NaN 而不是 0
}

func newAckLagTracker() *ackLagTracker { return &ackLagTracker{since: map[int64]time.Time{}} }

func (t *ackLagTracker) MarkPending(userID int64) {
	t.mu.Lock()
	t.seen = true
	if _, ok := t.since[userID]; !ok {
		t.since[userID] = time.Now()
	}
	t.mu.Unlock()
}

func (t *ackLagTracker) MarkAcked(userID int64) {
	t.mu.Lock()
	t.seen = true
	delete(t.since, userID)
	t.mu.Unlock()
}

// Seen 是否观测过（用于区分"零延迟"与"还没数据"）。
func (t *ackLagTracker) Seen() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen
}

func (t *ackLagTracker) MaxAge() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	var oldest time.Time
	for _, ts := range t.since {
		if oldest.IsZero() || ts.Before(oldest) {
			oldest = ts
		}
	}
	if oldest.IsZero() {
		return 0
	}
	return time.Since(oldest)
}

func (g *gateway) logf(format string, a ...any) {
	if g.verbose {
		log.Printf(format, a...)
	}
}

func (g *gateway) registerSession(s *wsSession) {
	g.mu.Lock()
	g.sessions[s.conn.ID()] = s
	g.mu.Unlock()
	g.connsGauge.Add(1)
}

func (g *gateway) sessionByConn(connID uint64) *wsSession {
	g.mu.RLock()
	s := g.sessions[connID]
	g.mu.RUnlock()
	return s
}

func (g *gateway) removeSession(connID uint64) {
	g.mu.Lock()
	_, ok := g.sessions[connID]
	delete(g.sessions, connID)
	g.mu.Unlock()
	if ok {
		g.connsGauge.Add(-1)
	}
}

func (g *gateway) onKernelUp() { g.metric.Set("conn_gateway_uds_connected", 1) }

func (g *gateway) onKernelDown(allGone bool) {
	if allGone {
		g.metric.Set("conn_gateway_uds_connected", 0)
	}
}

// onAck 是 hub 的"写成功"回调：只做网关本地统计，**不**产生 AckUp（见 connproto/uds.go 注释）。
func (g *gateway) onAck(connID uint64, userID int64, seq uint64) {
	g.metric.Inc("conn_gateway_frames_out_total", "", 1)
	g.logf("[conn-gateway] write-ack conn=%d user=%d seq=%d", connID, userID, seq)
}

func (g *gateway) onSlowConsumer(connID uint64, userID int64, queued int) {
	g.metric.Inc("conn_gateway_slow_kicks_total", "", 1)
	log.Printf("[conn-gateway] slow consumer kicked conn=%d user=%d queuedBytes=%d", connID, userID, queued)
}

func (g *gateway) onDegrade(connID uint64, userID int64) {
	g.metric.Inc("conn_gateway_degrade_total", "", 1)
	g.logf("[conn-gateway] conn=%d user=%d degraded (>80%% ring)", connID, userID)
}

// onResumeResp 在 peer 读循环里被调用：**先写 resume_ok / err 再返回**，保证先于补发帧。
func (g *gateway) onResumeResp(resp *connproto.ResumeResp) {
	s := g.sessionByConn(resp.ConnID)
	if s == nil {
		g.logf("[conn-gateway] resume resp for unknown conn=%d", resp.ConnID)
		return
	}
	if !resp.OK {
		log.Printf("[conn-gateway] resume_rejected conn=%d user=%d device=%s reason=%s epoch=%d",
			s.conn.ID(), s.userID, s.deviceID, resp.Reason, resp.Epoch)
		_ = s.writeControl(&connproto.ControlFrame{
			T: connproto.CtlErr, Code: "RESUME_REJECTED", Message: resp.Reason,
			NextStep: "请全量重连（不要复用 resume_token）",
		})
		g.hub.CloseConn(s.conn.ID(), 4010, "resume_rejected:"+resp.Reason)
		return
	}
	log.Printf("[conn-gateway] resume_ok conn=%d user=%d device=%s from=%d to=%d replayed=%d truncated=%v oldest_available=%d dropped_before=%d epoch=%d",
		s.conn.ID(), s.userID, s.deviceID, resp.From, resp.To, resp.Replayed,
		resp.Truncated, resp.OldestAvailable, resp.DroppedBefore, resp.Epoch)
	_ = s.writeControl(&connproto.ControlFrame{
		T:               connproto.CtlResumeOK,
		From:            resp.From,
		To:              resp.To,
		Replayed:        resp.Replayed,
		Truncated:       resp.Truncated,
		OldestAvailable: resp.OldestAvailable,
		DroppedBefore:   resp.DroppedBefore,
		ConnEpoch:       resp.Epoch,
	})
}

// onKick 内核要求踢人。
func (g *gateway) onKick(k connproto.Kick) {
	if k.AllDevices {
		g.mu.RLock()
		ids := make([]uint64, 0, 8)
		for id, s := range g.sessions {
			if s.userID == k.UserID {
				ids = append(ids, id)
			}
		}
		g.mu.RUnlock()
		for _, id := range ids {
			g.hub.CloseConn(id, 1002, "kicked_all_devices")
		}
		return
	}
	if k.ConnID != 0 {
		g.hub.CloseConn(k.ConnID, 1002, "kicked")
	}
}

func main() {
	addr := flag.String("addr", envOr("CONN_LISTEN", defaultAddr), "WS listen address (loopback only)")
	udsPath := flag.String("uds", envOr("CONN_UDS_PATH", defaultUDSPath), "kernel UDS path")
	disableUDS := flag.Bool("uds-disable", envBool("CONN_UDS_DISABLE", false), "run WS-only (B1 mode, no kernel)")
	token := flag.String("token", os.Getenv("CONN_GATEWAY_TOKEN"), "bearer token required for upgrade (empty = dev mode)")
	echoMode := flag.Bool("echo", false, "B1 self-test only: echo inbound data frames back to the same connection")
	devInsecure := flag.Bool("dev-insecure", envBool("CONN_DEV_INSECURE", false), "DEV ONLY: allow ?user_id= identity when hello has no token or kernel did not verify (default: reject)")
	presenceLease := flag.Bool("presence-lease", envBool("CONN_PRESENCE_LEASE", false), "C1: send presence lease (kind 26) to kernel every 15s (default off)")
	debugEndpoints := flag.Bool("debug", envBool("CONN_DEBUG", false), "expose /debug/pprof and /debug/memstats (memory profiling; default off)")
	gogc := flag.Int("gogc", 0, "override GOGC at runtime (0 = leave unchanged; task-24 GC tuning experiment)")
	memlimit := flag.String("memlimit", "", "override GOMEMLIMIT, e.g. 384MiB (empty = leave unchanged)")
	verbose := flag.Bool("v", false, "verbose logs")
	ringBytes := flag.Int("ring-bytes", 16384, "per-connection byte budget")
	ringCriticalReserve := flag.Int("ring-critical-reserve", envInt("CONN_RING_CRITICAL_RESERVE", 32),
		"ring slots reserved for critical frames (C5a; 0 disables)")
	shards := flag.Int("shards", 64, "hub shards (power of two)")
	flag.Parse()

	cfg := connhub.DefaultConfig()
	cfg.Shards = *shards
	cfg.RingBytes = *ringBytes
	cfg.RingCriticalReserve = *ringCriticalReserve

	g := &gateway{
		cfg:       cfg,
		metric:    newMetricsRegistry(),
		ackLag:    newAckLagTracker(),
		token:     *token,
		echo:          *echoMode,
		verbose:       *verbose,
		devInsecure:   *devInsecure,
		presenceLease: *presenceLease,
		debug:         *debugEndpoints,
		startedAt: time.Now(),
		sessions:  map[uint64]*wsSession{},
	}
	g.cfg.OnAck = g.onAck
	g.cfg.OnSlowConsumer = g.onSlowConsumer
	g.cfg.OnDegrade = g.onDegrade
	g.cfg.Logf = g.logf
	g.hub = connhub.NewHub(g.cfg)
	g.stopHeartbeat = make(chan struct{})
	go g.heartbeatLoop()
	// task-24 内存优化（每连接省 ~6KB）：
	//   * ReadBufferSize=0 → gorilla 复用 http Hijack 出来的 bufio.Reader（默认 4KB，已存在），
	//     不再为每条连接另分配一个 4KB reader（gorilla 源码：readBufferSize==0 且 hijacked reader >256B 时复用）；
	//   * WriteBufferPool=noop → gorilla 跳过每条连接 2KB 的 writeBuf 预分配（我们的写路径是裸 net.Conn+writev）。
	g.upgrader = websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		ReadBufferSize:   0,
		WriteBufferSize:  2048,
		WriteBufferPool:  noopWriteBufferPool{},
		CheckOrigin:      func(r *http.Request) bool { return true },
	}
	if g.token == "" {
		log.Printf("[conn-gateway] WARN: CONN_GATEWAY_TOKEN 为空，dev 模式（任何 token 都放行）；仅限 loopback")
	}
	if g.devInsecure {
		log.Printf("[conn-gateway] WARN: -dev-insecure 已启用 —— 允许 ?user_id= 作为身份兜底（**仅限本地/压测**，生产必须关闭）")
	}
	if g.presenceLease {
		log.Printf("[conn-gateway] presence lease enabled (kind 26, refresh=%s ttl=%s)", presenceLeaseRefresh, presenceLeaseTTL)
	}
	// 记录当前 GOGC（用于 /debug/memstats 展示；-gogc 可覆盖）
	g.gcPercent.Store(100)
	if env := os.Getenv("GOGC"); env != "" {
		if v, err := strconv.Atoi(env); err == nil {
			g.gcPercent.Store(int64(v))
		}
	}
	if *gogc > 0 {
		old := debug.SetGCPercent(*gogc)
		g.gcPercent.Store(int64(*gogc))
		log.Printf("[conn-gateway] GOGC overridden: %d -> %d", old, *gogc)
	}
	if *memlimit != "" {
		var err error
		var n int64
		// 只接受 Go 的容量字符串形式（如 384MiB / 512MB）
		n, err = parseByteSize(*memlimit)
		if err != nil {
			log.Fatalf("[conn-gateway] bad -memlimit %q: %v", *memlimit, err)
		}
		debug.SetMemoryLimit(n)
		log.Printf("[conn-gateway] GOMEMLIMIT overridden -> %d bytes", n)
	}

	// UDS 服务端（内核侧接入）。失败不致命：降级为 WS-only（B1 行为），但明确告警。
	if !*disableUDS {
		g.uds = newUDSServer(g, *udsPath, instanceID())
		if err := g.uds.Start(); err != nil {
			log.Printf("[conn-gateway] WARN: UDS 监听失败（%s）：降级 WS-only，内核无法接入: %v", *udsPath, err)
			g.uds = nil
		} else {
			log.Printf("[conn-gateway] UDS listening on %s", *udsPath)
		}
	} else {
		log.Printf("[conn-gateway] UDS disabled (-uds-disable)：WS-only 模式")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", g.handleHealth)
	mux.HandleFunc("GET /v1/ws", g.handleWS)
	mux.HandleFunc("GET /metrics", g.handleMetrics)
	if g.debug {
		mux.Handle("/debug/pprof/", http.DefaultServeMux) // pprof 由 net/http/pprof 注册在 DefaultServeMux
		mux.HandleFunc("GET /debug/memstats", g.handleMemStats)
		log.Printf("[conn-gateway] debug endpoints enabled: /debug/pprof/ /debug/memstats")
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[conn-gateway] listen: %v", err)
		}
	}()
	log.Printf("[conn-gateway] listening on %s (shards=%d ringBytes=%d echo=%v)", *addr, g.cfg.Shards, g.cfg.RingBytes, g.echo)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop

	// 退出顺序：先 Drain（内核立刻停推新帧，未 ack 的走位点补发）→ 停新连接 → drain → 关 UDS。
	if g.stopHeartbeat != nil {
		close(g.stopHeartbeat)
	}
	log.Printf("[conn-gateway] SIGTERM received: step 1/4 broadcast Drain (conns=%d)", g.hub.ConnCount())
	if g.uds != nil {
		g.uds.BroadcastDrain(drainWait)
	}
	log.Printf("[conn-gateway] step 2/4 stop accepting new WS, shut down HTTP")
	ctx, cancel := context.WithTimeout(context.Background(), drainWait)
	_ = srv.Shutdown(ctx)
	cancel()
	log.Printf("[conn-gateway] step 3/4 hub closed (conns=%d)", g.hub.ConnCount())
	g.hub.Close()
	if g.uds != nil {
		log.Printf("[conn-gateway] step 4/4 stopping UDS")
		g.uds.Stop()
	}
	log.Printf("[conn-gateway] bye")
}

func instanceID() string {
	host, _ := os.Hostname()
	return host + "-" + strconv.Itoa(os.Getpid())
}

// handleHealth 返回 hub/UDS 状态（运维探针）。
func (g *gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"status": "ok",
		"mode":   "standalone-b2b",
		"time":   time.Now().Unix(),
		"hub":    g.hub.Stats(),
		"uds": map[string]any{
			"enabled": g.uds != nil,
			"path":    g.udsPathSafe(),
		},
	})
}

func (g *gateway) udsPathSafe() string {
	if g.uds == nil {
		return ""
	}
	return g.uds.path
}

// handleMetrics 输出 B2B-DESIGN §4.2 的指标（Prometheus 文本格式）。
func (g *gateway) handleMetrics(w http.ResponseWriter, r *http.Request) {
	st := g.hub.Stats()
	g.metric.Set("conn_gateway_conns", float64(g.connsGauge.Load()))
	if g.uds != nil {
		g.uds.mu.Lock()
		n := 0
		for p := range g.uds.peers {
			if p.ready.Load() {
				n++
			}
		}
		g.uds.mu.Unlock()
		g.metric.Set("conn_gateway_uds_connected", float64(n))
	}
	if g.ackLag.Seen() {
		g.metric.Set("conn_gateway_ack_lag_seconds", g.ackLag.MaxAge().Seconds())
	} else {
		g.metric.SetUnobserved("conn_gateway_ack_lag_seconds")
	}
	g.metric.SetCounter("conn_gateway_frames_out_total", float64(st.FramesOut))
	g.metric.SetCounter("conn_gateway_frames_in_total", float64(st.FramesIn))
	// hub.FramesDrop 是"ring 水位丢弃 + shard 队列满丢弃"之和；减去已知的 shard 部分即为 ring 部分。
	ringDropped := int64(st.FramesDrop) - g.shardDropped.Load()
	if ringDropped < 0 {
		ringDropped = 0
	}
	g.metric.SetCounterLabels("conn_gateway_frames_dropped_total", `reason="ring"`, float64(ringDropped))
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	g.metric.Render(w)
}

// handleWS 升级一条原生连接。
//
// C1（task-21）身份流程（**网关不验签，只透传**）：
//  1. 首帧必须是 hello；token 取 `hello.token`（JSON 字段名就是 `token`，ControlFrame.Token）；
//  2. **先向内核 ConnOpen（带 token）验明身份，再注册连接** —— 绝不再用 `?user_id=` 建索引，
//     避免"按不可信身份注册、之后还要改索引"的竞态；
//  3. 权威 userID 只来自 `ConnOpenAck.UserID`；`OK=false` → `err{CONN_OPEN_REJECTED}` + 关连接(4003)；
//     `ack.UserID==0`（内核未启用校验）时，只有显式 `-dev-insecure` 才允许回退到 query user_id，
//     否则拒绝（**不再静默默认 1**）；
//  4. 无内核（UDS 未启用/未连接）同理：严格模式拒绝，dev-insecure 才允许本地兜底。
func (g *gateway) handleWS(w http.ResponseWriter, r *http.Request) {
	if g.token != "" && !g.tokenOK(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "reason": "bad token"})
		return
	}
	ws, err := g.upgrader.Upgrade(w, r, nil)
	if err != nil {
		g.logf("[conn-gateway] upgrade: %v", err)
		return
	}
	ws.SetReadLimit(maxInboundFrame)
	_ = ws.SetReadDeadline(time.Now().Add(authDeadline))

	sink := NewWSSink(ws) // 内部 SetPingHandler/SetCloseHandler no-op，见 wssink.go
	deviceID := r.URL.Query().Get("device_id")
	if deviceID == "" {
		deviceID = "unknown"
	}
	// 注意：这只是 **dev 兜底值**，不是身份来源。C1 起严格模式完全不看它。
	legacyUserID := parseInt64Default(r.URL.Query().Get("user_id"), 1)

	// 1) 首帧 hello（10s 内）
	mt, data, err := ws.ReadMessage()
	if err != nil {
		_ = ws.Close()
		return
	}
	if mt != websocket.TextMessage {
		g.closeWithReason(ws, "HELLO_REQUIRED", "first frame must be a JSON control frame")
		return
	}
	hello, herr := connproto.DecodeControl(data)
	if herr != nil || hello.T != connproto.CtlHello {
		g.closeWithReason(ws, "BAD_HELLO", "bad hello")
		return
	}
	token := hello.Token
	if token == "" && !g.devInsecure {
		log.Printf("[conn-gateway] reject AUTH_TOKEN_REQUIRED device=%s remote=%s（严格模式：hello 必须带 token）", deviceID, r.RemoteAddr)
		writeCtrlErrTo(sink, "AUTH_TOKEN_REQUIRED", "hello 必须携带 token（JWT）；本地 dev 需显式 -dev-insecure", "请携带有效 auth_token 重新连接")
		_ = ws.Close()
		return
	}

	// 2) 先验身份（connID 预分配，注册放到身份确定之后）
	connID := g.hub.NextConnID()
	var identity, epoch int64
	var resumeToken string
	reject := func(code, msg string) {
		log.Printf("[conn-gateway] reject %s conn=%d device=%s remote=%s: %s", code, connID, deviceID, r.RemoteAddr, msg)
		writeCtrlErrTo(sink, code, msg, "请携带有效 auth_token 重新连接")
		_ = ws.Close()
	}
	if g.uds != nil {
		op := connproto.ConnOpen{ConnID: connID, DeviceID: deviceID, Token: token}
		if token == "" && g.devInsecure {
			op.UserID = legacyUserID // 仅 dev：老内核在未启用校验时可能回显这个值
		}
		ack, cerr := g.uds.RequestConnOpen(op)
		switch {
		case cerr != nil:
			if !g.devInsecure {
				reject("KERNEL_UNAVAILABLE", "连接网关未接入内核，无法校验身份")
				return
			}
			log.Printf("[conn-gateway] WARN: 内核不可用，dev-insecure 回退 query user_id=%d", legacyUserID)
			identity = legacyUserID
		case !ack.OK:
			log.Printf("[conn-gateway] conn=%d ConnOpen rejected reason=%s", connID, ack.Reason)
			reject("CONN_OPEN_REJECTED", ack.Reason)
			return
		default:
			epoch, resumeToken = ack.Epoch, ack.ResumeToken
			if ack.UserID != 0 {
				identity = ack.UserID
			} else if g.devInsecure {
				log.Printf("[conn-gateway] WARN: 内核未启用身份校验，dev-insecure 回退 query user_id=%d", legacyUserID)
				identity = legacyUserID
			} else {
				reject("IDENTITY_UNVERIFIED", "内核未返回权威 userID（未启用身份校验）")
				return
			}
		}
	} else {
		if !g.devInsecure {
			reject("NO_KERNEL", "连接网关未启用 UDS，无法校验身份")
			return
		}
		log.Printf("[conn-gateway] WARN: 无 UDS，dev-insecure 回退 query user_id=%d", legacyUserID)
		identity = legacyUserID
	}

	// 3) 身份确定后才注册（绝不按不可信身份建索引）
	conn := g.hub.RegisterWithID(connID, identity, deviceID, sink)
	s := &wsSession{conn: conn, sink: sink, userID: identity, authUserID: identity, deviceID: deviceID}
	s.epoch.Store(epoch)
	s.resumeToken = resumeToken
	g.registerSession(s)

	defer func() {
		s.closed.Store(true)
		lastAck := conn.LastAck()
		g.hub.CloseConn(conn.ID(), 1000, "peer_closed")
		if g.uds != nil {
			g.uds.SendConnClosed(connproto.ConnClosed{
				ConnID: conn.ID(), UserID: s.effectiveUserID(), DeviceID: deviceID,
				Code: 1000, Reason: "peer_closed", LastAck: lastAck,
			})
		}
		g.removeSession(conn.ID())
	}()

	// 4) hello_ok + auth_ok（身份=权威值）
	helloOK, _ := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlHelloOK, V: 1, Proto: "1.0", ServerTime: time.Now().Unix(),
		Limits: &connproto.Limits{MaxFrame: connproto.MaxPayload, MaxConnsPerUser: 8, HeartbeatS: 30, PongWaitS: 90},
	})
	authOK, _ := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlAuthOK, UserID: s.effectiveUserID(), DeviceID: deviceID, ConnID: conn.ID(),
		ResumeToken: s.resumeToken, ConnEpoch: s.epoch.Load(),
	})
	if err := sink.WriteRawText(append(helloOK, '\n'), append(authOK, '\n')); err != nil {
		return
	}
	_ = ws.SetReadDeadline(time.Now().Add(readIdle))
	log.Printf("[conn-gateway] conn=%d authed user=%d device=%s epoch=%d remote=%s",
		conn.ID(), s.effectiveUserID(), deviceID, s.epoch.Load(), r.RemoteAddr)

	// 5) 心跳：由 gateway 的**全局 ticker**统一驱动（task-24：不再每连接一个 goroutine+ticker，
	//    每条连接省 ~5KB 栈 + timer 堆对象；心跳语义不变=每 heartbeatEvery 一帧 PING）。
	// presence 租约仍按连接（默认关闭）。
	stopHB := make(chan struct{})
	defer close(stopHB)
	if g.presenceLease && g.uds != nil {
		g.sendPresenceLease(s)
		go func() {
			t := time.NewTicker(presenceLeaseRefresh)
			defer t.Stop()
			for {
				select {
				case <-stopHB:
					return
				case <-t.C:
					g.sendPresenceLease(s)
				}
			}
		}()
	}

	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		_ = ws.SetReadDeadline(time.Now().Add(readIdle))
		switch mt {
		case websocket.TextMessage:
			c, cerr := connproto.DecodeControl(data)
			if cerr != nil {
				continue
			}
			switch c.T {
			case connproto.CtlPing:
				if b, e := connproto.EncodeControl(&connproto.ControlFrame{T: connproto.CtlPong, N: c.N}); e == nil {
					_ = sink.WriteRawText(append(b, '\n'))
				}
			case connproto.CtlAck:
				// 客户端字节级确认 → 上报内核（AckUp 的唯一来源）。
				if g.uds != nil {
					g.uds.SendAckUp(connproto.AckUp{
						ConnID: conn.ID(), UserID: s.effectiveUserID(), DeviceID: deviceID,
						ConnEpoch: s.epoch.Load(), AckSeq: c.Seq,
					})
					g.ackLag.MarkAcked(s.effectiveUserID())
					g.logf("[conn-gateway] AckUp conn=%d user=%d device=%s epoch=%d ack_seq=%d",
						conn.ID(), s.effectiveUserID(), deviceID, s.epoch.Load(), c.Seq)
				}
			case connproto.CtlResume:
				g.handleResume(s, c)
			default:
				// 未知控制帧忽略（前向兼容）
			}
		case websocket.BinaryMessage:
			f, _, derr := connproto.Decode(data)
			if derr != nil {
				continue
			}
			if g.echo {
				g.hub.DeliverConn(conn.ID(), []connproto.Frame{f})
			}
		}
	}
}

// heartbeatLoop 全局心跳：一个 goroutine + 一个 ticker 服务所有连接（task-24）。
// 之前是每连接一个 goroutine + ticker（2000 连接 = 2000 goroutine + 2000 timer）。
func (g *gateway) heartbeatLoop() {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-g.stopHeartbeat:
			return
		case <-t.C:
			g.mu.RLock()
			ids := make([]uint64, 0, len(g.sessions))
			for id := range g.sessions {
				ids = append(ids, id)
			}
			g.mu.RUnlock()
			for _, id := range ids {
				g.hub.DeliverConn(id, []connproto.Frame{{Type: connproto.TypePing}})
			}
		}
	}
}

// writeCtrlErrTo 在握手阶段给客户端发一个 JSON 错误控制帧（此时还没进入 hub 写路径）。
func writeCtrlErrTo(sink *WSSink, code, msg, next string) {
	b, err := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlErr, Code: code, Message: msg, NextStep: next,
	})
	if err != nil {
		return
	}
	_ = sink.WriteRawText(append(b, '\n'))
}

// sendPresenceLease 上报一条 presence 租约（C1；上行、非阻塞）。
// 只上报 "online"：`offline` 由内核按 TTL 过期推断，网关不臆造。
func (g *gateway) sendPresenceLease(s *wsSession) {
	if g.uds == nil {
		return
	}
	g.uds.SendPresenceLease(connproto.PresenceLease{
		ConnID:   s.conn.ID(),
		UserID:   s.effectiveUserID(),
		DeviceID: s.deviceID,
		TTLS:     int64(presenceLeaseTTL / time.Second),
		AtMS:     time.Now().UnixMilli(),
		State:    "online",
	})
}
// handleResume 把客户端的 resume 请求转给内核；resume_ok 由 peer 读循环写出（保证顺序）。
func (g *gateway) handleResume(s *wsSession, c *connproto.ControlFrame) {
	if g.uds == nil {
		_ = s.writeControl(&connproto.ControlFrame{
			T: connproto.CtlErr, Code: "NO_KERNEL", Message: "gateway has no kernel attached",
			NextStep: "以全量连接方式使用（不续传）",
		})
		return
	}
	epoch := c.ConnEpoch
	if epoch == 0 {
		epoch = s.epoch.Load()
	}
	resp, err := g.uds.RequestResume(connproto.ResumeReq{
		ConnID: s.conn.ID(), UserID: s.userID, DeviceID: s.deviceID,
		ResumeToken: c.ResumeToken, LastAckSeq: c.LastAckSeq, ConnEpoch: epoch,
	})
	if err != nil {
		_ = s.writeControl(&connproto.ControlFrame{
			T: connproto.CtlErr, Code: "RESUME_TIMEOUT", Message: err.Error(),
			NextStep: "可重试 resume，或全量重连",
		})
		return
	}
	// 成功路径已由 onResumeResp 写过 resume_ok；失败路径也已在那边写 err + 关连接。
	if !resp.OK {
		g.logf("[conn-gateway] conn=%d resume rejected: %s", s.conn.ID(), resp.Reason)
	}
}

func (g *gateway) tokenOK(r *http.Request) bool {
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimPrefix(a, "Bearer ") == g.token
	}
	return r.URL.Query().Get("access_token") == g.token
}

func (g *gateway) closeWithReason(ws *websocket.Conn, code, reason string) {
	b, _ := connproto.EncodeControl(&connproto.ControlFrame{T: connproto.CtlErr, Code: code, Message: reason})
	_ = ws.WriteMessage(websocket.TextMessage, b) // 仅握手失败时使用：此时还没有裸写路径
	_ = ws.Close()
}

// handleMemStats 输出 Go 运行时内存分解（task-24 用；仅 -debug 注册）。
// ?gc=1 会先 runtime.GC() + debug.FreeOSMemory()，用于测"活堆"与"RSS 未归还"的差距。
func (g *gateway) handleMemStats(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("gc") == "1" {
		runtime.GC()
		debug.FreeOSMemory()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	rss := readRSSBytes()
	writeJSON(w, 200, map[string]any{
		"goroutines":     runtime.NumGoroutine(),
		"conns":          g.hub.ConnCount(),
		"rss_bytes":      rss,
		"heap_alloc":     ms.HeapAlloc,
		"heap_inuse":     ms.HeapInuse,
		"heap_idle":      ms.HeapIdle,
		"heap_released":  ms.HeapReleased,
		"heap_objects":   ms.HeapObjects,
		"stack_inuse":    ms.StackInuse,
		"stack_sys":      ms.StackSys,
		"sys":            ms.Sys,
		"mallocs":        ms.Mallocs,
		"frees":          ms.Frees,
		"num_gc":         ms.NumGC,
		"gc_cpu_fraction": ms.GCCPUFraction,
		"pause_total_ns": ms.PauseTotalNs,
		"last_gc_unix":   int64(ms.LastGC / 1e9),
		"gogc":           g.gcPercent.Load(),
		"gomemlimit":     debug.SetMemoryLimit(-1),
	})
}

// readRSSBytes 读 /proc/self/statm 的 resident 页数（Linux；失败返回 0）。
func readRSSBytes() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}

// parseByteSize 解析 "384MiB" / "512MB" / 纯字节数（task-24 的 -memlimit）。
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "GIB"):
		mult = 1 << 30
		s = strings.TrimSuffix(s, "GIB")
	case strings.HasSuffix(s, "GB"):
		mult = 1_000_000_000
		s = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MIB"):
		mult = 1 << 20
		s = strings.TrimSuffix(s, "MIB")
	case strings.HasSuffix(s, "MB"):
		mult = 1_000_000
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KIB"):
		mult = 1 << 10
		s = strings.TrimSuffix(s, "KIB")
	case strings.HasSuffix(s, "KB"):
		mult = 1_000
		s = strings.TrimSuffix(s, "KB")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	switch strings.ToLower(os.Getenv(k)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// envInt 读整型环境变量（解析失败或为空时用默认值）。
func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func parseInt64Default(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}
