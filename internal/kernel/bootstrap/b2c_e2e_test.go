// SPDX-License-Identifier: MIT
//
// B2-c 判定性 E2E（task-20 §2/§3）：真实事件 → stream.Mirror → connapi.Bridge
// → 真实 UDS → 真实 cmd/conn-gateway → 真实 WS 客户端。
//
// 覆盖：
//   - dual：旧 stream.Hub 订阅者收到 + connapi 侧 delivered 增长 + 网关客户端收到事件帧；
//   - standalone：/v1/ws 421 + 同一事件经 conn-gateway 送达客户端；
//   - 网关不在时：网关仍可用、投递降级计数增长、Broadcast 主路径非阻塞；
//   - 关闭顺序：Close 后 stream.Mirror 已撤。
//
// 注意：本文件用 **bootstrap_test 外部测试包**，因为需要 import internal/kernel/routes，
// 而 routes 反向 import bootstrap——同包（package bootstrap）会形成 import cycle。
//
// 默认 skip：需要 IM2A_E2E=1 + PG_DSN（真 PG；Vultr im2a_e2e 库）。
// 复跑：
//
//	source /root/im2a-work/e2e-env.sh
//	cd /root/im2a-work/build
//	IM2A_E2E=1 PG_DSN="$PG_DSN" go test ./internal/kernel/bootstrap -run TestB2C_EventMirrorE2E -v -count=1 -timeout 300s -p 1
package bootstrap_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/kernel/routes"
	"human-agent-im/internal/store"
)

// b2cEnv 保存并在测试结束时恢复一组环境变量。
func b2cEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	type prev struct {
		val string
		ok  bool
	}
	saved := make(map[string]prev, len(kv))
	for k := range kv {
		v, ok := os.LookupEnv(k)
		saved[k] = prev{v, ok}
	}
	for k, v := range kv {
		_ = os.Setenv(k, v)
	}
	t.Cleanup(func() {
		for k, p := range saved {
			if p.ok {
				_ = os.Setenv(k, p.val)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	})
}

func b2cGate(t *testing.T) string {
	t.Helper()
	if os.Getenv("IM2A_E2E") != "1" {
		t.Skip("IM2A_E2E != 1：B2-c E2E 默认跳过（需要真 PG + 真 conn-gateway）")
	}
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN 未设置：B2-c E2E 需要真 PostgreSQL")
	}
	return dsn
}

func b2cRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/kernel/bootstrap → repo root
	root := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s: %v", root, err)
	}
	return root
}

func b2cBuildGateway(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "conn-gateway-b2c")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/conn-gateway")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build conn-gateway: %v\n%s", err, out)
	}
	return bin
}

func b2cFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// b2cStartGateway 起真实 conn-gateway 子进程（extraArgs 追加给网关，如 -presence-lease）。
func b2cStartGateway(t *testing.T, bin, uds string, extraArgs ...string) (addr string, stop func()) {
	t.Helper()
	_ = os.Remove(uds)
	port := b2cFreePort(t)
	addr = fmt.Sprintf("127.0.0.1:%d", port)
	logPath := filepath.Join(t.TempDir(), "gateway.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create gateway log: %v", err)
	}
	args := append([]string{"-uds", uds, "-addr", addr, "-v"}, extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	stop = func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		_ = f.Close()
	}
	t.Cleanup(stop)
	// 等 healthz + socket。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
			_ = resp.Body.Close()
			if _, err := os.Stat(uds); err == nil {
				return addr, stop
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, _ := os.ReadFile(logPath)
	t.Fatalf("gateway 未就绪 addr=%s uds=%s\n%s", addr, uds, raw)
	return "", func() {}
}

// b2cClient 是真实 WS 客户端（conn-gateway 协议：hello → auth_ok → 收二进制帧）。
type b2cClient struct {
	t    *testing.T
	conn *websocket.Conn

	mu     sync.Mutex
	auth   *connproto.ControlFrame
	seqs   []uint64
	frames []connproto.Frame

	authCh chan struct{}
	done   chan struct{}
}

func b2cDialClient(t *testing.T, addr string, userID int64, deviceID string, token string) *b2cClient {
	t.Helper()
	url := fmt.Sprintf("ws://%s/v1/ws?user_id=%d&device_id=%s", addr, userID, deviceID)
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("ws dial %s: %v", url, err)
	}
	cli := &b2cClient{t: t, conn: c, authCh: make(chan struct{}), done: make(chan struct{})}
	go cli.readLoop()
	hello, _ := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlHello, V: 1, ProtoMin: "1.0", ProtoMax: "1.0", DeviceID: deviceID,
		Token: token, // C1：内核验签后 ConnOpenAck.UserID 为权威身份
	})
	if err := c.WriteMessage(websocket.TextMessage, hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	return cli
}

func (c *b2cClient) readLoop() {
	defer close(c.done)
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
			if cf.T == connproto.CtlAuthOK {
				c.mu.Lock()
				c.auth = cf
				c.mu.Unlock()
				select {
				case <-c.authCh:
				default:
					close(c.authCh)
				}
			}
		case websocket.BinaryMessage:
			f, _, derr := connproto.Decode(data)
			if derr != nil {
				continue
			}
			c.mu.Lock()
			c.frames = append(c.frames, f)
			if f.Seq > 0 {
				c.seqs = append(c.seqs, f.Seq)
			}
			c.mu.Unlock()
		}
	}
}

func (c *b2cClient) waitAuth(timeout time.Duration) *connproto.ControlFrame {
	select {
	case <-c.authCh:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.auth
	case <-time.After(timeout):
		c.t.Fatalf("timeout auth_ok")
		return nil
	}
}

// waitEvent 等待一条 payload 含 marker 的数据帧（EVT/MSG）。
func (c *b2cClient) waitEvent(marker string, timeout time.Duration) connproto.Frame {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, f := range c.frames {
			if strings.Contains(string(f.Payload), marker) {
				c.mu.Unlock()
				return f
			}
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	c.t.Fatalf("timeout waiting event containing %q", marker)
	return connproto.Frame{}
}

func (c *b2cClient) close() { _ = c.conn.Close() }

// readTextOne 读一条文本消息（dual 旧订阅者用）。
func readTextOne(t *testing.T, c *websocket.Conn, timeout time.Duration) string {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("legacy subscriber read: %v", err)
	}
	return string(data)
}

// TestB2C_EventMirrorE2E 判定性验收：真实事件经 mirror 投递到 conn-gateway 客户端。
func TestB2C_EventMirrorE2E(t *testing.T) {
	dsn := b2cGate(t)
	root := b2cRepoRoot(t)
	bin := b2cBuildGateway(t, root)

	// 确保 B2-b 表存在（Assemble 会跑迁移，这里提前跑一次更稳）。
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, mode := range []string{bootstrap.ConnModeDual, bootstrap.ConnModeStandalone} {
		t.Run(mode, func(t *testing.T) {
			uds := filepath.Join(t.TempDir(), "im2a-conn.sock")
			b2cEnv(t, map[string]string{
				"OPENCLAW_ENABLED": "off",
				"DSBOT_ENABLED":    "off",
				"PG_DSN":           dsn,
				"CONN_MODE":        mode,
				"CONN_UDS_PATH":    uds,
				"GATEWAY_TOKEN":    "",
				"IM2A_ADMIN_TOKEN": "b2c-e2e-admin",
				"IM2A_JWT_SECRET":  "b2c-e2e-jwt",
				"WEBHOOK_ASYNC":    "",
			})
			cfg := bootstrap.Load(0)
			k, err := bootstrap.Assemble(cfg)
			if err != nil {
				t.Fatalf("Assemble(%s): %v", mode, err)
			}
			closed := false
			defer func() {
				if !closed {
					_ = k.Close(context.Background())
				}
			}()
			if k.ConnBridge == nil || k.ConnSink == nil || k.ConnTap == nil {
				t.Fatalf("%s: 事件镜像未装配（bridge/sink/tap 有 nil）", mode)
			}
			if k.Deps.Stream == nil || !k.Deps.Stream.HasMirror() {
				t.Fatalf("%s: stream.Mirror 未设置", mode)
			}

			userID := time.Now().UnixNano()%1_000_000_000 + 100000
			topic := fmt.Sprintf("trust:%d", userID)
			payload := map[string]any{"type": "trust_granted", "level": 3, "user_id": userID}

			// A) 网关尚未启动：Broadcast 主路径非阻塞，投递降级计数增长。
			const n = 20
			start := time.Now()
			for i := 0; i < n; i++ {
				k.Deps.Stream.Broadcast(topic, payload)
			}
			if elapsed := time.Since(start); elapsed > time.Duration(n)*5*time.Millisecond {
				t.Fatalf("%s: 网关不在时 Broadcast 主路径被拖慢: %d 次 %v", mode, n, elapsed)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) && k.ConnSink.Metrics().DroppedNotReady == 0 {
				time.Sleep(10 * time.Millisecond)
			}
			if k.ConnSink.Metrics().DroppedNotReady == 0 {
				t.Fatalf("%s: 网关不在时 DroppedNotReady=0（降级计数未增长）", mode)
			}
			// 网关不在时内核仍可用（HTTP 200；/healthz 需要 admin token，与 golden 口径一致）。
			mux := http.NewServeMux()
			routes.Register(mux, k)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			healthReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/healthz", nil)
			healthReq.Header.Set("Authorization", "Bearer b2c-e2e-admin")
			{
				resp, err := http.DefaultClient.Do(healthReq)
				if err != nil {
					t.Fatalf("%s: 网关不在时 /healthz 请求失败: %v", mode, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("%s: 网关不在时 /healthz status=%d, want 200", mode, resp.StatusCode)
				}
			}
			if mode == bootstrap.ConnModeStandalone {
				resp, err := http.Get(srv.URL + "/v1/ws")
				if err != nil {
					t.Fatalf("standalone /v1/ws: %v", err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusMisdirectedRequest {
					t.Fatalf("standalone /v1/ws status=%d, want 421", resp.StatusCode)
				}
			}

			// B) 启动真实 conn-gateway，等待内核 UDS 连上。
			addr, stop := b2cStartGateway(t, bin, uds)
			defer stop()
			deadline = time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) && !k.ConnSink.Metrics().Connected {
				time.Sleep(10 * time.Millisecond)
			}
			if !k.ConnSink.Metrics().Connected {
				t.Fatalf("%s: connapi 未连上 conn-gateway", mode)
			}

			// dual：旧 stream.Hub 订阅者也要收到；standalone：内核 /v1/ws 421。
			//
			// 注意：这里直接对 stream.Hub 起 httptest WS（等价旧订阅者），而不是走 routes 的
			// /v1/ws——后者当前被 security.Audit 的 statusWriter 包装后不实现 http.Hijacker，
			// gorilla Upgrade 会报 "response does not implement http.Hijacker"（**Wave A 之前就存在**，
			// 只在 deps.DB != nil 时触发；已单列缺陷上报 lead，不属本任务修复范围）。
			var legacy *websocket.Conn
			if mode == bootstrap.ConnModeDual {
				legacySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					k.Deps.Stream.HandleWS(r.URL.Query().Get("topic"), w, r)
				}))
				defer legacySrv.Close()
				wsURL := "ws" + strings.TrimPrefix(legacySrv.URL, "http") + "/?topic=" + topic
				legacy, _, err = websocket.DefaultDialer.Dial(wsURL, nil)
				if err != nil {
					t.Fatalf("dual legacy subscriber dial: %v", err)
				}
				defer legacy.Close()
				_ = readTextOne(t, legacy, 2*time.Second) // connected
			}

			// 客户端连真实网关并握手（C1：hello 必须带 JWT，网关透传内核验签）。
			jwtToken, jerr := auth.Issue("b2c-e2e-jwt", userID, time.Hour, "b2c-e2e")
			if jerr != nil {
				t.Fatalf("issue b2c jwt: %v", jerr)
			}
			cli := b2cDialClient(t, addr, userID, "b2c-e2e-device", jwtToken)
			defer cli.close()
			auth := cli.waitAuth(5 * time.Second)
			t.Logf("[%s] gateway client auth: conn=%d user=%d epoch=%d", mode, auth.ConnID, userID, auth.ConnEpoch)

			// C) 触发真实业务事件（与 /v1/trust/grant 相同的 topic/payload 形状）。
			k.Deps.Stream.Broadcast(topic, payload)
			if mode == bootstrap.ConnModeDual {
				got := readTextOne(t, legacy, 3*time.Second)
				if !strings.Contains(got, "trust_granted") {
					t.Fatalf("dual 旧订阅者未收到事件: %s", got)
				}
			}
			evt := cli.waitEvent("trust_granted", 10*time.Second)
			t.Logf("[%s] conn-gateway 客户端收到事件帧: type=%v seq=%d payload=%s", mode, evt.Type, evt.Seq, evt.Payload)

			// D) 断言：tap published 增长；事件先落 im_delivery_log（同事务发号），seq>0。
			pub, dropped, unmapped, _ := k.ConnTap.Stats()
			if pub == 0 {
				t.Fatalf("%s: EventTap published=0 (dropped=%d unmapped=%d)", mode, dropped, unmapped)
			}
			rows := 0
			if err := db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM im_delivery_log WHERE user_id = $1`, userID).Scan(&rows); err != nil {
				t.Fatalf("count im_delivery_log: %v", err)
			}
			if rows == 0 {
				t.Fatalf("%s: 事件未写入 im_delivery_log", mode)
			}
			if evt.Seq == 0 {
				t.Fatalf("%s: 投递帧 seq=0，发号链路未生效", mode)
			}
			t.Logf("[%s] PASS: tap(published=%d) delivery_log_rows=%d frame_seq=%d", mode, pub, rows, evt.Seq)

			// E) 关闭顺序：Close 后 mirror 必须已撤（避免关机继续投递）。
			_ = k.Close(context.Background())
			closed = true
			if k.Deps.Stream.HasMirror() {
				t.Fatalf("%s: Close 后 stream.Mirror 仍被设置", mode)
			}
		})
	}
}
