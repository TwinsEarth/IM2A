// SPDX-License-Identifier: MIT
//
// 联合 E2E（B2B-DESIGN §6 判定性验收）：内核侧 connapi + delivery 持久层 + 真实
// cmd/conn-gateway 进程 + 真实 PostgreSQL + 真实 UDS。
//
// 判定性场景（与 lead 验收口径逐条对应）：
//  1. 起真实 conn-gateway（-uds 临时 socket -addr 随机端口）；
//  2. 内核侧 connapi.Remote 真实连接（不是 mock）；客户端用真实 WebSocket 连接网关；
//  3. 推送 200 条带 seq 的消息；客户端收到前 100 条并 ack（AckUp → im_device_cursor）；
//  4. kill -9 网关 → 客户端重连 → 内核按 last_ack_seq 从 101 补发；
//  5. 断言：两段会话收到的 seq 并集 = 1..200，**无缺口**（允许重复，客户端按 seq 去重）；
//  6. conn_epoch 不匹配的 resume 必须被拒（RESUME_REJECTED）；
//  7. 记录 delivery_log 行数 / cursor 位点 / 指标。
//
// 默认 skip：需要 `IM2A_E2E=1` + `PG_DSN`（Vultr 上的 im2a_e2e 库）。
// 复跑：
//
//	PG_DSN='postgres://im2a:...@127.0.0.1:5432/im2a_e2e?sslmode=disable' IM2A_E2E=1 \
//	  go test ./internal/connapi/ -run TestB2B_ZeroLoss_E2E -v -count=1 -timeout 300s
//
// 注意：本测试会 DROP 掉 e2e 库中的 im_delivery_log/im_user_delivery_seq/im_device_cursor
// 三张表再重建，**只允许在 im2a_e2e 专用库跑**。
package connapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/connapi/delivery"
	"human-agent-im/internal/connproto"
	"human-agent-im/internal/store"
)

const (
	e2eUserID    int64  = 424242
	e2eDeviceID  string = "e2e-ios"
	e2eJWTSecret string = "connapi-e2e-jwt-secret" // C1：内核/客户端共用的 dev JWT secret
	e2eTotal            = 200
	e2eAckAt            = 100
)

// e2eGate 检查 E2E 环境开关。
func e2eGate(t *testing.T) {
	t.Helper()
	if os.Getenv("IM2A_E2E") != "1" {
		t.Skip("IM2A_E2E != 1：联合 E2E 默认跳过（需要真 PG + 真 conn-gateway）")
	}
	if os.Getenv("PG_DSN") == "" {
		t.Skip("PG_DSN 未设置：联合 E2E 需要真 PostgreSQL")
	}
}

// e2eRepoRoot 向上找 go.mod。
func e2eRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// e2eFreePort 分配随机空闲端口。
func e2eFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// e2eGateway 是真实 conn-gateway 子进程。
type e2eGateway struct {
	t    *testing.T
	cmd  *exec.Cmd
	addr string
	uds  string
	log  string
}

// buildE2EGateway 构建 cmd/conn-gateway 二进制。
func buildE2EGateway(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "conn-gateway-e2e")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/conn-gateway")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/conn-gateway: %v\n%s", err, out)
	}
	return bin
}

// startE2EGateway 启动网关并等 /healthz 200 + UDS socket 就绪。
func startE2EGateway(t *testing.T, bin, uds string) *e2eGateway {
	t.Helper()
	_ = os.Remove(uds) // 清理上次 kill -9 留下的 stale socket
	addr := fmt.Sprintf("127.0.0.1:%d", e2eFreePort(t))
	logPath := filepath.Join(t.TempDir(), fmt.Sprintf("gateway-%d.log", time.Now().UnixNano()))
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create gateway log: %v", err)
	}
	cmd := exec.Command(bin, "-uds", uds, "-addr", addr, "-v")
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	g := &e2eGateway{t: t, cmd: cmd, addr: addr, uds: uds, log: logPath}
	t.Cleanup(func() { g.stop() })
	// 等 healthz。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if _, err := os.Stat(uds); err == nil {
					return g
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	body, _ := os.ReadFile(logPath)
	t.Fatalf("gateway not ready (addr=%s uds=%s)\n--- gateway log ---\n%s", addr, uds, body)
	return nil
}

// kill9 模拟网关崩溃（SIGKILL，无优雅退出）。
func (g *e2eGateway) kill9() {
	_ = g.cmd.Process.Kill()
	_, _ = g.cmd.Process.Wait()
}

// stop 幂等清理。
func (g *e2eGateway) stop() {
	if g.cmd != nil && g.cmd.Process != nil {
		_ = g.cmd.Process.Kill()
		_, _ = g.cmd.Process.Wait()
	}
}

// e2eMetrics 抓网关 /metrics 的关键行（用于回报）。
func (g *e2eGateway) e2eMetrics() map[string]string {
	out := map[string]string{}
	resp, err := http.Get("http://" + g.addr + "/metrics")
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		if i := strings.IndexByte(line, ' '); i > 0 {
			name := line[:i]
			if strings.HasPrefix(name, "conn_gateway_") || strings.HasPrefix(name, "kernel_") ||
				strings.HasPrefix(name, "frames_dropped_total") || strings.Contains(name, "ack_lag") {
				out[name] = line[i+1:]
			}
		}
	}
	return out
}

// e2eClient 是真实 WebSocket 客户端（模拟原生客户端）。
type e2eClient struct {
	t    *testing.T
	conn *websocket.Conn

	mu      sync.Mutex
	seen    map[uint64]int
	pings   int
	auth    *connproto.ControlFrame
	resume  *connproto.ControlFrame
	ctlErrs []connproto.ControlFrame
	authCh  chan struct{}
	resCh   chan struct{}
	errCh   chan struct{}

	closeOnce sync.Once
}

func dialE2EClient(t *testing.T, addr string, userID int64, deviceID string) *e2eClient {
	t.Helper()
	url := fmt.Sprintf("ws://%s/v1/ws?user_id=%d&device_id=%s", addr, userID, deviceID)
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := d.Dial(url, nil)
	if err != nil {
		t.Fatalf("ws dial %s: %v", url, err)
	}
	c := &e2eClient{
		t:      t,
		conn:   conn,
		seen:   map[uint64]int{},
		authCh: make(chan struct{}),
		resCh:  make(chan struct{}),
		errCh:  make(chan struct{}, 8),
	}
	go c.readLoop()
	return c
}

func (c *e2eClient) readLoop() {
	defer close(c.errCh)
	for {
		mt, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.TextMessage:
			cf, derr := connproto.DecodeControl(data)
			if derr != nil {
				continue
			}
			switch cf.T {
			case connproto.CtlAuthOK:
				c.mu.Lock()
				c.auth = cf
				c.mu.Unlock()
				select {
				case <-c.authCh:
				default:
					close(c.authCh)
				}
			case connproto.CtlResumeOK:
				c.mu.Lock()
				c.resume = cf
				c.mu.Unlock()
				select {
				case <-c.resCh:
				default:
					close(c.resCh)
				}
			case connproto.CtlPing:
				_ = c.conn.WriteJSON(connproto.ControlFrame{T: connproto.CtlPong, Seq: cf.Seq})
			case connproto.CtlErr:
				c.mu.Lock()
				c.ctlErrs = append(c.ctlErrs, *cf)
				c.mu.Unlock()
			}
		case websocket.BinaryMessage:
			f, _, derr := connproto.Decode(data)
			if derr != nil {
				continue
			}
			switch f.Type {
			case connproto.TypePing, connproto.TypePong:
				c.mu.Lock()
				c.pings++
				c.mu.Unlock()
			default:
				c.mu.Lock()
				c.seen[f.Seq]++
				c.mu.Unlock()
			}
		}
	}
}

// hello 发握手（首帧必须有 resume_token/last_ack_seq/device_id + C1 的 auth token）。
func (c *e2eClient) hello(resumeToken string, lastAck uint64, epoch int64, token string) {
	hello, err := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlHello, V: 1, ProtoMin: "1.0", ProtoMax: "1.0",
		Features:    []string{"resume", "topic"},
		ResumeToken: resumeToken, LastAckSeq: lastAck, ConnEpoch: epoch,
		DeviceID: e2eDeviceID,
		Token:    token, // C1：网关透传 → 内核验签 → ConnOpenAck.UserID
	})
	if err != nil {
		c.t.Fatalf("encode hello: %v", err)
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, hello); err != nil {
		c.t.Fatalf("write hello: %v", err)
	}
}

// waitAuth 等 auth_ok（含内核签发的 resume_token/epoch）。
func (c *e2eClient) waitAuth(timeout time.Duration) *connproto.ControlFrame {
	select {
	case <-c.authCh:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.auth
	case <-time.After(timeout):
		c.t.Fatalf("timeout waiting auth_ok")
		return nil
	}
}

// sendAck 客户端字节级累积确认（网关唯一 AckUp 来源）。
func (c *e2eClient) sendAck(seq uint64) {
	b, err := connproto.EncodeControl(&connproto.ControlFrame{T: connproto.CtlAck, Seq: seq})
	if err != nil {
		c.t.Fatalf("encode ack: %v", err)
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		c.t.Fatalf("write ack: %v", err)
	}
}

// resume 请求补发。
func (c *e2eClient) requestResume(token string, lastAck uint64, epoch int64) {
	b, err := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlResume, ResumeToken: token, LastAckSeq: lastAck, ConnEpoch: epoch,
	})
	if err != nil {
		c.t.Fatalf("encode resume: %v", err)
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		c.t.Fatalf("write resume: %v", err)
	}
}

// waitResumeOK 等 resume_ok / RESUME_REJECTED 二者之一。
func (c *e2eClient) waitResumeOK(timeout time.Duration) (*connproto.ControlFrame, *connproto.ControlFrame) {
	deadline := time.After(timeout)
	for {
		select {
		case <-c.resCh:
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.resume, nil
		case <-c.errCh:
			c.mu.Lock()
			defer c.mu.Unlock()
			if len(c.ctlErrs) > 0 {
				return nil, &c.ctlErrs[len(c.ctlErrs)-1]
			}
			// 连接断开但没有错误帧：等下一次循环/超时。
		case <-deadline:
			c.mu.Lock()
			defer c.mu.Unlock()
			if len(c.ctlErrs) > 0 {
				return nil, &c.ctlErrs[len(c.ctlErrs)-1]
			}
			c.t.Fatalf("timeout waiting resume_ok / err")
			return nil, nil
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// waitSeenAtLeast 等至少 n 条不同 seq 被收到。
func (c *e2eClient) waitSeenAtLeast(n int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.distinct() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.t.Fatalf("timeout waiting %d distinct seqs (got %d)", n, c.distinct())
}

func (c *e2eClient) distinct() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

func (c *e2eClient) snapshot() map[uint64]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[uint64]int, len(c.seen))
	for k, v := range c.seen {
		out[k] = v
	}
	return out
}

func (c *e2eClient) errors() []connproto.ControlFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]connproto.ControlFrame, len(c.ctlErrs))
	copy(out, c.ctlErrs)
	return out
}

// close 关闭 WS。
func (c *e2eClient) close() {
	c.closeOnce.Do(func() { _ = c.conn.Close() })
}

// openE2EDB 打开 PG、重置三张 B2-b 表并跑迁移。
func openE2EDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", os.Getenv("PG_DSN"))
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// 每次 drop 重建（只针对 e2e 专用库；设计要求可重复执行）。
	if _, err := db.ExecContext(ctx,
		`DROP TABLE IF EXISTS im_delivery_log, im_user_delivery_seq, im_device_cursor CASCADE`); err != nil {
		t.Fatalf("drop e2e tables: %v", err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// waitCursor 等 im_device_cursor.acked_seq >= want（AckUp 已持久化）。
func waitCursor(t *testing.T, st delivery.Store, deviceID string, want uint64, timeout time.Duration) delivery.DeviceCursor {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cur, ok, err := st.Cursor(context.Background(), deviceID)
		if err == nil && ok && cur.AckedSeq >= want {
			return cur
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting cursor %s >= %d", deviceID, want)
	return delivery.DeviceCursor{}
}

// countDeliveryLog 返回该用户日志行数。
func countDeliveryLog(t *testing.T, db *sql.DB, userID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM im_delivery_log WHERE user_id = $1`, userID).Scan(&n); err != nil {
		t.Fatalf("count delivery log: %v", err)
	}
	return n
}

// missingSeqs 返回 [1..total] 中未被 union 覆盖的 seq。
func missingSeqs(union map[uint64]int, total int) []uint64 {
	var missing []uint64
	for i := 1; i <= total; i++ {
		if union[uint64(i)] == 0 {
			missing = append(missing, uint64(i))
		}
	}
	return missing
}

// dupSeqs 返回在 union 中出现多次的 seq（仅按"两段会话合计出现次数>1"统计，允许）。
func dupSeqs(union map[uint64]int) []uint64 {
	var dups []uint64
	for seq, n := range union {
		if n > 1 {
			dups = append(dups, seq)
		}
	}
	sort.Slice(dups, func(i, j int) bool { return dups[i] < dups[j] })
	return dups
}

// mergeSeen 合并两段会话的 seq 计数。
func mergeSeen(a, b map[uint64]int) map[uint64]int {
	out := make(map[uint64]int, len(a)+len(b))
	for k, v := range a {
		out[k] += v
	}
	for k, v := range b {
		out[k] += v
	}
	return out
}

// TestB2B_ZeroLoss_E2E 是判定性联合验收：kill -9 网关 → 重连补发 → 零丢。
func TestB2B_ZeroLoss_E2E(t *testing.T) {
	e2eGate(t)
	root := e2eRepoRoot(t)
	bin := buildE2EGateway(t, root)
	work := t.TempDir()
	uds := filepath.Join(work, "im2a-conn.sock")

	db := openE2EDB(t)
	pgStore := delivery.NewPGStore(db, delivery.Options{Now: time.Now})
	quiet := log.New(io.Discard, "", 0)
	bridge := NewBridge(pgStore, quiet)
	// C1：内核启用 JWT 权威身份（网关 C1 起不再信任 query user_id）。
	bridge.SetJWTSecret(e2eJWTSecret)
	jwtToken, err := auth.Issue(e2eJWTSecret, e2eUserID, time.Hour, "connapi-e2e")
	if err != nil {
		t.Fatalf("issue e2e jwt: %v", err)
	}

	// 1) 第一个网关 + 内核真实 UDS 连接。
	gw1 := startE2EGateway(t, bin, uds)
	remote := NewRemote(bridge.Wire(RemoteConfig{
		UDSPath: uds, Instance: "kernel-e2e", Logger: quiet,
		ReconnectMin: 20 * time.Millisecond, ReconnectMax: 100 * time.Millisecond,
	}))
	bridge.SetSink(remote)
	defer func() { _ = remote.Close(context.Background()) }()
	e2eWaitConnected(t, remote, 10*time.Second)

	// 2) 客户端建连，拿到内核签发的 token/epoch。
	c1 := dialE2EClient(t, gw1.addr, e2eUserID, e2eDeviceID)
	defer c1.close()
	c1.hello("", 0, 0, jwtToken)
	auth := c1.waitAuth(5 * time.Second)
	if auth.ResumeToken == "" {
		t.Fatalf("auth_ok 未携带 resume_token（内核 ConnOpenAck 没生效）: %+v", auth)
	}
	t.Logf("session-1 auth: conn=%d epoch=%d token=%q", auth.ConnID, auth.ConnEpoch, auth.ResumeToken)

	// 3) 推 200 条（critical MSG，走 im_delivery_log）。
	ctx := context.Background()
	for i := 1; i <= e2eTotal; i++ {
		if _, err := bridge.Publish(ctx, e2eUserID, []PublishFrame{{
			Payload:  []byte(fmt.Sprintf("msg-%03d", i)),
			Critical: true,
		}}); err != nil {
			t.Fatalf("publish seq=%d: %v", i, err)
		}
	}
	c1.waitSeenAtLeast(e2eAckAt, 10*time.Second)
	c1.sendAck(e2eAckAt)
	cursorBefore := waitCursor(t, pgStore, e2eDeviceID, e2eAckAt, 10*time.Second)
	seenBefore := c1.snapshot()
	rowsBefore := countDeliveryLog(t, db, e2eUserID)
	lastSeq, _ := pgStore.LastSeq(ctx, e2eUserID)
	t.Logf("before kill -9: last_seq=%d delivery_log_rows=%d cursor=%d client_seen=%d",
		lastSeq, rowsBefore, cursorBefore.AckedSeq, len(seenBefore))

	// 4) kill -9 网关；客户端连接必然断开。
	gw1.kill9()
	c1.close()
	time.Sleep(50 * time.Millisecond)

	// 5) 重启网关（同 UDS path，新 WS 端口），客户端重连 + resume。
	gw2 := startE2EGateway(t, bin, uds)
	c2 := dialE2EClient(t, gw2.addr, e2eUserID, e2eDeviceID)
	defer c2.close()
	c2.hello(auth.ResumeToken, cursorBefore.AckedSeq, auth.ConnEpoch, jwtToken)
	auth2 := c2.waitAuth(10 * time.Second)
	t.Logf("session-2 auth: conn=%d epoch=%d token=%q", auth2.ConnID, auth2.ConnEpoch, auth2.ResumeToken)
	c2.requestResume(auth.ResumeToken, cursorBefore.AckedSeq, auth2.ConnEpoch)
	resumeOK, resumeErr := c2.waitResumeOK(10 * time.Second)
	if resumeErr != nil {
		t.Fatalf("resume rejected: code=%s message=%s", resumeErr.Code, resumeErr.Message)
	}
	t.Logf("resume_ok: from=%d to=%d replayed=%d truncated=%v oldest_available=%d dropped_before=%d epoch=%d",
		resumeOK.From, resumeOK.To, resumeOK.Replayed, resumeOK.Truncated,
		resumeOK.OldestAvailable, resumeOK.DroppedBefore, resumeOK.ConnEpoch)
	if resumeOK.From != cursorBefore.AckedSeq+1 {
		t.Fatalf("resume from=%d, want lastAck+1=%d", resumeOK.From, cursorBefore.AckedSeq+1)
	}
	// 等补发帧到齐（理论 100 条：101..200）。
	wantReplay := e2eTotal - int(cursorBefore.AckedSeq)
	c2.waitSeenAtLeast(wantReplay, 15*time.Second)

	// 6) 零丢断言：两段会话的 seq 并集必须恰好覆盖 1..200（无缺口；重复允许）。
	union := mergeSeen(seenBefore, c2.snapshot())
	missing := missingSeqs(union, e2eTotal)
	dups := dupSeqs(union)
	t.Logf("ZERO_LOSS_CHECK: session1_seen=%d session2_seen=%d union=%d missing=%v duplicates=%d",
		len(seenBefore), c2.distinct(), len(union), missing, len(dups))
	if len(missing) != 0 {
		t.Fatalf("ZERO-LOSS FAIL: 缺失 seq=%v（union=%d/%d）", missing, len(union), e2eTotal)
	}
	// 收尾 ack 到 200，位点闭环。
	c2.sendAck(e2eTotal)
	cursorAfter := waitCursor(t, pgStore, e2eDeviceID, e2eTotal, 10*time.Second)

	// 7) conn_epoch 不匹配的 resume 必须被拒：
	//    c3 先正常 auth（session 仍持有旧 epoch），然后内核 bump epoch，再让 c3 用旧
	//    session epoch 发 resume → 网关转发旧 epoch → 内核返回 epoch_mismatch → RESUME_REJECTED。
	c3 := dialE2EClient(t, gw2.addr, e2eUserID, e2eDeviceID)
	defer c3.close()
	c3.hello("", 0, 0, jwtToken)
	auth3 := c3.waitAuth(5 * time.Second)
	newEpoch, err := bridge.BumpEpoch(ctx, e2eUserID, e2eDeviceID)
	if err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
	t.Logf("bump epoch: %d -> %d (c3 session epoch=%d)", cursorAfter.ConnEpoch, newEpoch, auth3.ConnEpoch)
	c3.requestResume(auth3.ResumeToken, cursorAfter.AckedSeq, auth3.ConnEpoch) // 旧 session epoch
	if ok, cerr := c3.waitResumeOK(10 * time.Second); cerr == nil {
		t.Fatalf("stale epoch resume 未被拒绝：resume_ok=%+v", ok)
	} else {
		t.Logf("stale epoch resume rejected: code=%s message=%s", cerr.Code, cerr.Message)
		if cerr.Code != "RESUME_REJECTED" {
			t.Fatalf("reject code=%q, want RESUME_REJECTED", cerr.Code)
		}
	}

	// 8) 记录判定性数值。
	rowsAfter := countDeliveryLog(t, db, e2eUserID)
	km := remote.Metrics()
	gm := gw2.e2eMetrics()
	t.Logf("records: delivery_log_rows=%d cursor_acked=%d last_seq=%d", rowsAfter, cursorAfter.AckedSeq, lastSeq)
	t.Logf("kernel-side metrics: state=%s delivered=%d acks=%d ackups=%d ackups_dropped=%d drops(queueFull=%d critical=%d notReady=%d) reconnects=%d batch_split=%d",
		km.State, km.DeliveredFrames, km.DeliverAcks, km.AckUps, km.AckUpsDropped,
		km.DroppedQueueFull, km.DroppedCritical, km.DroppedNotReady, km.Reconnects, km.BatchSplit)
	var keys []string
	for k := range gm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+strings.TrimSpace(gm[k]))
	}
	t.Logf("gateway metrics: %s", strings.Join(parts, " "))
	raw, _ := json.Marshal(map[string]any{
		"delivery_log_rows": rowsAfter,
		"cursor_acked":      cursorAfter.AckedSeq,
		"last_seq":          lastSeq,
		"missing":           missing,
		"duplicates":        len(dups),
	})
	t.Logf("E2E EVIDENCE JSON: %s", raw)
	t.Logf("KILL9_ZERO_LOSS_PASS: %d/%d seqs covered, 0 missing, %d duplicate deliveries", len(union), e2eTotal, len(dups))
}

// e2eWaitConnected 等 remote 与网关握手完成。
func e2eWaitConnected(t *testing.T, r *Remote, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r.Metrics().Connected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connapi remote 未在 %s 内连上 conn-gateway", timeout)
}
