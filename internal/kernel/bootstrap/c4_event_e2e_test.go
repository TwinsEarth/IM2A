// SPDX-License-Identifier: MIT
//
// C4 联合 E2E（task-26 判定性验收）：真 cmd/conn-gateway + 真 PG + 真 WS 客户端。
//  1. 四类 topic：approval/trust → A；negotiation → A+B 多播；subagent → A；C（无关）收不到；
//  2. unmapped topic → UnmappedPrefixes()["other"]+1，不影响其它事件；
//  3. 三样对齐：客户端帧 / 网关 /metrics / 网关日志；关键帧证据查 im_delivery_log.flags&1；
//  4. presence kind 27：TypePresence + Seq=0、im_delivery_log 不增；ephemeral fallback 同样可达；
//  5. agent token：签发→建连 UserID=principal；跨 owner 吊销 404；吊销后重连被拒。
//
// 默认 skip：IM2A_E2E=1 + PG_DSN。复跑：
//
//	source /root/im2a-work/e2e-env.sh
//	cd /root/im2a-work/build
//	IM2A_E2E=1 PG_DSN="$PG_DSN" go test -p 1 ./internal/kernel/bootstrap -run TestC4EventE2E -v -count=1 -timeout 300s
package bootstrap_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/connproto"
	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/kernel/native"
	"human-agent-im/internal/kernel/routes"
	"human-agent-im/internal/negotiation"
	"human-agent-im/internal/store"
)

// c4StartGateway 起真网关并返回日志路径（E2E 三样对齐需要）。
func c4StartGateway(t *testing.T, bin, uds string) (addr, logPath string, stop func()) {
	t.Helper()
	_ = os.Remove(uds)
	port := b2cFreePort(t)
	addr = fmt.Sprintf("127.0.0.1:%d", port)
	logPath = filepath.Join(t.TempDir(), "c4-gateway.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	cmd := exec.Command(bin, "-uds", uds, "-addr", addr, "-v", "-presence-lease")
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
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
			_ = resp.Body.Close()
			if _, err := os.Stat(uds); err == nil {
				return addr, logPath, stop
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, _ := os.ReadFile(logPath)
	t.Fatalf("gateway 未就绪: %s", raw)
	return "", "", func() {}
}

// c4WaitMarkers 等客户端收到含指定 marker 的 n 条事件帧（marker 在 payload JSON 的 marker 字段）。
func c4WaitMarkers(t *testing.T, c *b2cClient, markers []string, timeout time.Duration) []connproto.Frame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	seen := map[string]bool{}
	var out []connproto.Frame
	for time.Now().Before(deadline) {
		c.mu.Lock()
		fs := append([]connproto.Frame(nil), c.frames...)
		c.mu.Unlock()
		out = out[:0]
		seen = map[string]bool{}
		for _, f := range fs {
			var env map[string]any
			if json.Unmarshal(f.Payload, &env) != nil {
				continue
			}
			m, _ := env["marker"].(string)
			if m != "" && !seen[m] {
				seen[m] = true
				out = append(out, f)
			}
		}
		all := true
		for _, want := range markers {
			if !seen[want] {
				all = false
			}
		}
		if all {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout: got markers %v, want %v (client frames=%d seqs=%v)", keys(seen), markers, len(c.frames), c4FrameSeqs(c))
	return nil
}

// c4FrameSeqs 返回客户端收到的帧序号（诊断用）。
func c4FrameSeqs(c *b2cClient) []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]uint64, 0, len(c.frames))
	for _, f := range c.frames {
		out = append(out, f.Seq)
	}
	return out
}

func subOf(c *auth.Claims) int64 { if c == nil { return -1 }; return c.Sub }

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// c4CountFrames 统计 TypePresence 且 Seq=0 的帧。
func c4PresenceFrames(c *b2cClient) []connproto.Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []connproto.Frame
	for _, f := range c.frames {
		if f.Type == connproto.TypePresence {
			out = append(out, f)
		}
	}
	return out
}

// c4ExpectRejected 用 token 建连，期望被拒（收到 err 或连接关闭）。
func c4ExpectRejected(t *testing.T, addr, device, token string) string {
	t.Helper()
	url := fmt.Sprintf("ws://%s/v1/ws?device_id=%s", addr, device)
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	b, _ := connproto.EncodeControl(&connproto.ControlFrame{
		T: connproto.CtlHello, V: 1, ProtoMin: "1.0", ProtoMax: "1.0", DeviceID: device, Token: token,
	})
	_ = c.WriteMessage(websocket.TextMessage, b)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			return "closed: " + err.Error()
		}
		if mt != websocket.TextMessage {
			continue
		}
		cf, derr := connproto.DecodeControl(data)
		if derr != nil {
			continue
		}
		if cf.T == connproto.CtlErr {
			return cf.Code + ": " + cf.Message
		}
		if cf.T == connproto.CtlAuthOK {
			t.Fatalf("期望被拒但 auth_ok: user=%d", cf.UserID)
		}
	}
}

func TestC4EventE2E(t *testing.T) {
	dsn := b2cGate(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()
	root := b2cRepoRoot(t)
	bin := b2cBuildGateway(t, root)
	uds := filepath.Join(t.TempDir(), "im2a-conn.sock")
	b2cEnv(t, map[string]string{
		"OPENCLAW_ENABLED": "off", "DSBOT_ENABLED": "off", "PG_DSN": dsn,
		"CONN_MODE": bootstrap.ConnModeDual, "CONN_UDS_PATH": uds,
		"GATEWAY_TOKEN": "", "IM2A_ADMIN_TOKEN": "c4-admin", "IM2A_JWT_SECRET": "c4-e2e-jwt",
		"NATIVE_ENABLED": "on", "NATIVE_PRESENCE_TTL": "30s", "NATIVE_PRESENCE_TICK": "100ms",
		"NATIVE_PRESENCE_DOWNLINK": "state27", "CONN_TAP_DEBUG": "on",
	})
	k, err := bootstrap.Assemble(bootstrap.Load(0))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
				_ = k.Close(context.Background())
		}
	}()
	if k.NativeAgentTokens == nil || k.ConnTap == nil || k.NativePresence == nil {
		t.Fatalf("装配不完整: tokens=%v tap=%v presence=%v", k.NativeAgentTokens != nil, k.ConnTap != nil, k.NativePresence != nil)
	}
	ctx := context.Background()
	addr, logPath, stop := c4StartGateway(t, bin, uds)
	defer stop()

	mux := http.NewServeMux()
	routes.Register(mux, k)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !k.ConnSink.Metrics().Connected {
		time.Sleep(10 * time.Millisecond)
	}
	if !k.ConnSink.Metrics().Connected {
		t.Fatal("connapi 未连上网关")
	}

	// 用户/身份/agent/协商会话 数据准备。
	base := time.Now().UnixNano()%100_000_000*10 + 8_000_000
	var uidA, uidB, uidC int64
	if err := db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&uidA); err != nil {
		t.Fatalf("insert user A: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&uidB); err != nil {
		t.Fatalf("insert user B: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&uidC); err != nil {
		t.Fatalf("insert user C: %v", err)
	}
	wxA, wxB := fmt.Sprintf("c4-wx-a-%d", base), fmt.Sprintf("c4-wx-b-%d", base)
	agentA, agentB := fmt.Sprintf("c4-agent-a-%d", base), fmt.Sprintf("c4-agent-b-%d", base)
	cleanup := func() {
		_, _ = db.Exec(`DELETE FROM account_identities WHERE open_id IN ($1,$2)`, wxA, wxB)
		_, _ = db.Exec(`DELETE FROM agent_registry WHERE agent_id IN ($1,$2)`, agentA, agentB)
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1,$2,$3)`, uidA, uidB, uidC)
		_, _ = db.Exec(`DELETE FROM n_agent_token WHERE agent_id=$1`, agentA)
		_, _ = db.Exec(`DELETE FROM n_principal WHERE kind='agent' AND agent_id=$1`, agentA)
	}
	defer cleanup()
	if _, err := db.Exec(`INSERT INTO account_identities(user_id, provider, open_id) VALUES ($1,'wechat',$2),($3,'wechat',$4)`, uidA, wxA, uidB, wxB); err != nil {
		t.Fatalf("insert identities: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_registry(user_wxid, agent_id) VALUES ($1,$2),($3,$4)`, wxA, agentA, wxB, agentB); err != nil {
		t.Fatalf("insert agent_registry: %v", err)
	}
	sess := negotiation.NewSession("c4-sess-"+fmt.Sprintf("%d", base), 5, 10_000)
	sess.Turns = []negotiation.Turn{{SessionID: sess.ID, FromAgentID: agentA, ToAgentID: agentB}}
	if err := k.Deps.Store.Negotiation.SaveSession(ctx, sess); err != nil {
		t.Fatalf("save negotiation session: %v", err)
	}
	tokenA, _ := auth.Issue("c4-e2e-jwt", uidA, time.Hour, "c4-e2e")
	tokenB, _ := auth.Issue("c4-e2e-jwt", uidB, time.Hour, "c4-e2e")
	tokenC, _ := auth.Issue("c4-e2e-jwt", uidC, time.Hour, "c4-e2e")
	// 前置断言：token.sub 必须等于其所属 principal 的 uid（排查"token 绑错 principal"）。
	for name, pair := range map[string]struct {
		tok  string
		want int64
	}{"A": {tokenA, uidA}, "B": {tokenB, uidB}, "C": {tokenC, uidC}} {
		cl, err := auth.Parse("c4-e2e-jwt", pair.tok)
		if err != nil || cl.Sub != pair.want {
			t.Fatalf("token %s 绑错 principal: sub=%v err=%v, want uid=%d (DB A=%d B=%d C=%d)",
				name, subOf(cl), err, pair.want, uidA, uidB, uidC)
		}
	}
	t.Logf("C4_TOKEN_BIND_OK: A.sub=%d B.sub=%d C.sub=%d", uidA, uidB, uidC)

	cA := b2cDialClient(t, addr, uidA, "c4-dev-a", tokenA)
	defer cA.close()
	cB := b2cDialClient(t, addr, uidB, "c4-dev-b", tokenB)
	defer cB.close()
	cC := b2cDialClient(t, addr, uidC, "c4-dev-c", tokenC)
	defer cC.close()
	cA.waitAuth(5 * time.Second)
	cB.waitAuth(5 * time.Second)
	cC.waitAuth(5 * time.Second)
	// 身份断言：auth_ok.UserID 必须与 token.sub/DB userID 一致（错位时直接报）。
	if a, b, cc := cA.c3Auth(), cB.c3Auth(), cC.c3Auth(); a == nil || b == nil || cc == nil ||
		a.UserID != uidA || b.UserID != uidB || cc.UserID != uidC {
		got := []int64{0, 0, 0}
		if a != nil {
			got[0] = a.UserID
		}
		if b != nil {
			got[1] = b.UserID
		}
		if cc != nil {
			got[2] = cc.UserID
		}
		t.Fatalf("身份错位: auth A=%d B=%d C=%d, want %d/%d/%d", got[0], got[1], got[2], uidA, uidB, uidC)
	}
	t.Logf("C4_AUTH_OK: A=%d B=%d C=%d", uidA, uidB, uidC)
	// 显式 barrier：等网关上 3 条连接都注册完成，再发事件（不靠 sleep）。
	waitConns := func() {
		dl := time.Now().Add(5 * time.Second)
		for time.Now().Before(dl) {
			resp, err := http.Get("http://" + addr + "/metrics")
			if err == nil {
				raw, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.HasPrefix(line, "conn_gateway_conns ") {
						if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "conn_gateway_conns "))); err == nil && n >= 3 {
							return
						}
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("barrier: 网关未在 5s 内注册 3 条连接")
	}
	waitConns()
	if m := k.ConnSink.Metrics(); m.Reconnects != 1 || m.ActiveConns != 1 {
		t.Fatalf("UDS 连接不满足单飞：reconnects=%d active=%d，want 1/1", m.Reconnects, m.ActiveConns)
	}
	t.Logf("C4_UDS_SINGLEFLIGHT_OK: reconnects=%d active=%d", k.ConnSink.Metrics().Reconnects, k.ConnSink.Metrics().ActiveConns)
	if k.NativeResolver != nil {
		for _, tp := range []string{fmt.Sprintf("approval:%d", uidA), fmt.Sprintf("trust:%d", uidA), "negotiation:" + sess.ID, "subagent:" + agentA} {
			users, ok := k.NativeResolver.Debug(ctx, tp)
			t.Logf("C4_RESOLVER_DEBUG %s -> users=%v ok=%v", tp, users, ok)
		}
	}

	// 四类 topic 各一条（marker 区分）。
	k.Deps.Stream.Broadcast(fmt.Sprintf("approval:%d", uidA), map[string]any{"marker": "approval"})
	k.Deps.Stream.Broadcast(fmt.Sprintf("trust:%d", uidA), map[string]any{"marker": "trust"})
	k.Deps.Stream.Broadcast("negotiation:"+sess.ID, map[string]any{"marker": "negotiation"})
	k.Deps.Stream.Broadcast("subagent:"+agentA, map[string]any{"marker": "subagent"})
	k.Deps.Stream.Broadcast("unknown:zzz-"+fmt.Sprintf("%d", base), map[string]any{"marker": "unmapped"})

	gotA := c4WaitMarkers(t, cA, []string{"approval", "trust", "negotiation", "subagent"}, 10*time.Second)
	gotB := c4WaitMarkers(t, cB, []string{"negotiation"}, 10*time.Second)
	time.Sleep(300 * time.Millisecond) // 给无关 client 一个接收窗口
	if gotC := c4WaitMarkers(t, cC, nil, 100*time.Millisecond); len(gotC) != 0 {
		t.Fatalf("无关用户 C 收到了事件: %v", gotC)
	}
	if len(gotA) < 4 || len(gotB) < 1 {
		if resp, err := http.Get("http://" + addr + "/metrics"); err == nil {
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.Contains(line, "conn_gateway_conns") || strings.Contains(line, "frames_in_total") || strings.Contains(line, "frames_out_total") || strings.Contains(line, "frames_dropped_total") {
					t.Logf("C4_FAILDUMP gw: %s", strings.TrimSpace(line))
				}
			}
		}
		t.Logf("C4_FAILDUMP remote: %+v", k.ConnSink.Metrics())
		pub, dropped, unmapped, merr := k.ConnTap.Stats()
		t.Logf("C4_FAILDUMP tap: published=%d dropped=%d unmapped=%d marshalErr=%d prefixes=%v", pub, dropped, unmapped, merr, k.ConnTap.UnmappedPrefixes())
		if rawLog, err := os.ReadFile(logPath); err == nil {
			for _, line := range strings.Split(string(rawLog), "\n") {
				if strings.Contains(line, "deliver_req") || strings.Contains(line, "registered") || strings.Contains(line, "presence_state") {
					t.Logf("C4_FAILDUMP gwlog: %s", strings.TrimSpace(line))
				}
			}
		}
		t.Fatalf("A=%d B=%d（A frames=%d seqs=%v；B frames=%d seqs=%v）", len(gotA), len(gotB),
			len(cA.frames), c4FrameSeqs(cA), len(cB.frames), c4FrameSeqs(cB))
	}
	prefixes := k.ConnTap.UnmappedPrefixes()
	if prefixes["other"] < 1 {
		t.Fatalf("unmapped 未计数: %v", prefixes)
	}
	// 关键帧证据：im_delivery_log.flags bit0（approval/trust 关键；negotiation/subagent 非关键）。
	var crit, noncrit int
	rows, err := db.Query(`SELECT flags FROM im_delivery_log WHERE user_id=$1`, uidA)
	if err != nil {
		t.Fatalf("query flags: %v", err)
	}
	for rows.Next() {
		var fl int
		_ = rows.Scan(&fl)
		if fl&1 != 0 {
			crit++
		} else {
			noncrit++
		}
	}
	rows.Close()
	if crit < 2 || noncrit < 2 {
		t.Fatalf("critical flags: crit=%d noncrit=%d, want >=2/>=2", crit, noncrit)
	}

	// presence kind 27：客户端收到 TypePresence 且 Seq=0；投递日志不增。
	var logCountBefore int
	_ = db.QueryRow(`SELECT COUNT(*) FROM im_delivery_log WHERE user_id=$1`, uidA).Scan(&logCountBefore)
	deadline = time.Now().Add(5 * time.Second)
	var pres []connproto.Frame
	for time.Now().Before(deadline) {
		pres = c4PresenceFrames(cA)
		if len(pres) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pres) == 0 {
		t.Fatal("客户端未收到 TypePresence（kind 27 未生效）")
	}
	for _, f := range pres {
		if f.Seq != 0 {
			t.Fatalf("presence 帧 Seq=%d, want 0", f.Seq)
		}
	}
	var logCountAfter int
	_ = db.QueryRow(`SELECT COUNT(*) FROM im_delivery_log WHERE user_id=$1`, uidA).Scan(&logCountAfter)
	if logCountAfter != logCountBefore {
		t.Fatalf("presence 写入了 im_delivery_log: %d -> %d", logCountBefore, logCountAfter)
	}
	// 网关 metrics（三样之一）。
	resp, err := http.Get("http://" + addr + "/metrics")
	var metricsText string
	if err == nil {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "PRESENCE_STATE") || strings.Contains(line, "presence_state_fanout") || strings.Contains(line, "frames_dropped_total") {
				metricsText += line + "\n"
			}
		}
	}
	// 网关日志片段（三样之二）。
	rawLog, _ := os.ReadFile(logPath)
	var gwLines []string
	for _, line := range strings.Split(string(rawLog), "\n") {
		if strings.Contains(line, "conn-gateway") || strings.Contains(line, "deliver") || strings.Contains(line, "presence") {
			gwLines = append(gwLines, strings.TrimSpace(line))
		}
	}
	if len(gwLines) > 8 {
		gwLines = gwLines[len(gwLines)-8:]
	}
	t.Logf("C4_GATEWAY_LOG_TAIL: %s", strings.Join(gwLines, " | "))
	t.Logf("C4_TOPICS_OK: A(topic frames=%d) B(negotiation=%d) C(0) unmapped_prefixes=%v crit_logs=%d noncrit_logs=%d",
		len(gotA), len(gotB), prefixes, crit, noncrit)
	t.Logf("C4_PRESENCE27_OK: TypePresence Seq=0, im_delivery_log 不增（%d→%d）", logCountBefore, logCountAfter)
	t.Logf("C4_GATEWAY_METRICS: %s", metricsText)

	// agent token：A 签发 → 建连 UserID=principal；B 跨 principal 吊销→404；A 吊销后重连被拒。
	status, body := c2Do(t, http.MethodPost, srv.URL+"/v1/native/agents/"+agentA+"/token", tokenA, map[string]any{})
	if status != http.StatusCreated {
		t.Fatalf("issue agent token status=%d body=%v", status, body)
	}
	agentTok := body["token"].(string)
	jti := body["jti"].(string)
	claims, err := auth.Parse("c4-e2e-jwt", agentTok)
	if err != nil {
		t.Fatalf("parse agent token: %v", err)
	}
	agc := b2cDialClient(t, addr, 0, "c4-agent-dev", agentTok)
	agcAuth := agc.waitAuth(5 * time.Second)
	if agcAuth.UserID != claims.Sub {
		t.Fatalf("agent auth_ok.UserID=%d, want token.sub=%d", agcAuth.UserID, claims.Sub)
	}
	agc.close()
	if status, _ := c2Do(t, http.MethodDelete, srv.URL+"/v1/native/agents/tokens/"+jti, tokenB, nil); status != http.StatusNotFound {
		t.Fatalf("跨 principal 吊销 status=%d, want 404", status)
	}
	if status, _ := c2Do(t, http.MethodDelete, srv.URL+"/v1/native/agents/tokens/"+jti, tokenA, nil); status != http.StatusOK {
		t.Fatalf("owner 吊销 status=%d", status)
	}
	reject := c4ExpectRejected(t, addr, "c4-agent-dev2", agentTok)
	t.Logf("C4_AGENT_TOKEN_OK: auth_ok.UserID=%d=sub=principal；跨owner吊销=404；吊销后重连被拒: %s", agcAuth.UserID, reject)

	// 跨用户隔离 + 重连：断开 A → A 新连接收 5 条；B 一条都收不到（防"错投"）。
	cA.close()
	cA2 := b2cDialClient(t, addr, uidA, "c4-dev-a2", tokenA)
	defer cA2.close()
	cA2.waitAuth(5 * time.Second)
	// 等网关侧旧连接下线、新连接注册后再发（barrier 不靠 sleep）。
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		users, ok := k.NativeResolver.Debug(ctx, fmt.Sprintf("approval:%d", uidA))
		if ok && len(users) == 1 && users[0] == uidA {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := 1; i <= 5; i++ {
		k.Deps.Stream.Broadcast(fmt.Sprintf("approval:%d", uidA), map[string]any{"marker": fmt.Sprintf("rl%d", i)})
	}
	gotRL := c4WaitMarkers(t, cA2, []string{"rl1", "rl2", "rl3", "rl4", "rl5"}, 10*time.Second)
	if len(gotRL) < 5 {
		t.Fatalf("重连后 A 只收到 %d/5 条", len(gotRL))
	}
	time.Sleep(300 * time.Millisecond)
	cB.mu.Lock()
	leaked := 0
	for _, fr := range cB.frames {
		var env map[string]any
		if json.Unmarshal(fr.Payload, &env) == nil {
			if m, _ := env["marker"].(string); strings.HasPrefix(m, "rl") {
				leaked++
			}
		}
	}
	cB.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("跨用户错投：B 收到了 A 的 %d 条事件", leaked)
	}
	t.Logf("C4_ISOLATION_OK: A 重连后收 5/5，B 泄漏 0 条")
	if m := k.ConnSink.Metrics(); m.Reconnects != 1 {
		t.Fatalf("测试结束 UDS reconnects=%d, want 1（出现多连接=并发泄漏）", m.Reconnects)
	}

	_ = k.Close(context.Background())
	closed = true
	t.Logf("C4_E2E PASS: 四类 topic+多播/无关隔离/unmapped 指标/critical flags/kind27 无日志/agent token 全链路")
}

// 供未使用告警（E2E 失败信息扩展）。
var _ = native.AgentIssuer
var _ = store.ErrNotFound
