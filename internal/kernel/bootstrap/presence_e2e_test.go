// SPDX-License-Identifier: MIT
//
// C1 presence 判定性 E2E（task-21 step 2）：
//   - 真 conn-gateway（-presence-lease）+ 立内核 presence 状态机（NATIVE_ENABLED=on）
//   - 客户端真实连接（JWT）→ 网关立刻上发 lease → 内核聚合 ONLINE
//   - 本人 /v1/native/presence/self：聚合态 + 逐设备明细
//   - 多端：第二台设备接入 → devices=2
//   - 可见性：L4 无关系 → 404（fail-closed，不泄露存在性）；L3 → online 布尔；L2 → 聚合态
//   - TTL：网关 kill 后无 ConnClosed → lease 过期 → OFFLINE（只由内核推断）
//   - 快照：SnapshotOnce → n_presence 有行（观测用，非真相源）
//
// 默认 skip：IM2A_E2E=1 + PG_DSN。复跑：
//
//	source /root/im2a-work/e2e-env.sh
//	cd /root/im2a-work/build
//	IM2A_E2E=1 PG_DSN="$PG_DSN" go test -p 1 ./internal/kernel/bootstrap -run TestC1PresenceE2E -v -count=1 -timeout 300s
package bootstrap_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"human-agent-im/internal/auth"
	"human-agent-im/internal/kernel/bootstrap"
	"human-agent-im/internal/kernel/routes"
	"human-agent-im/internal/store"
)

// c1GetJSON 带 JWT 请求内核 HTTP 端点，返回 status + JSON map。
func c1GetJSON(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func TestC1PresenceE2E(t *testing.T) {
	dsn := b2cGate(t)
	root := b2cRepoRoot(t)
	bin := b2cBuildGateway(t, root)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	uds := filepath.Join(t.TempDir(), "im2a-conn.sock")
	b2cEnv(t, map[string]string{
		"OPENCLAW_ENABLED":     "off",
		"DSBOT_ENABLED":        "off",
		"PG_DSN":               dsn,
		"CONN_MODE":            bootstrap.ConnModeDual,
		"CONN_UDS_PATH":        uds,
		"GATEWAY_TOKEN":        "",
		"IM2A_ADMIN_TOKEN":     "c1-presence-admin",
		"IM2A_JWT_SECRET":      "c1-presence-jwt",
		"NATIVE_ENABLED":       "on",
		"NATIVE_PRESENCE_TTL":  "2s",
		"NATIVE_PRESENCE_TICK": "100ms",
		"WEBHOOK_ASYNC":        "",
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
	if k.NativePresence == nil || k.ConnBridge == nil {
		t.Fatalf("NATIVE_ENABLED=on 但 presence/connbridge 未装配: presence=%v bridge=%v", k.NativePresence != nil, k.ConnBridge != nil)
	}

	mux := http.NewServeMux()
	routes.Register(mux, k)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	addr, stop := b2cStartGateway(t, bin, uds, "-presence-lease")
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !k.ConnSink.Metrics().Connected {
		time.Sleep(10 * time.Millisecond)
	}
	if !k.ConnSink.Metrics().Connected {
		t.Fatal("connapi 未连上 conn-gateway")
	}

	userID := time.Now().UnixNano()%1_000_000_000 + 300000
	otherID := userID + 7
	device1 := "presence-d1-" + fmt.Sprint(userID%1000)
	device2 := "presence-d2-" + fmt.Sprint(userID%1000)
	token, err := auth.Issue("c1-presence-jwt", userID, time.Hour, "c1-presence")
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}

	// 网关在连接建立后立刻上发 lease → 内核聚合 ONLINE。
	cli1 := b2cDialClient(t, addr, userID, device1, token)
	defer cli1.close()
	cli1.waitAuth(5 * time.Second)
	waitAgg := func(want string, minDevices int, timeout time.Duration) {
		t.Helper()
		dl := time.Now().Add(timeout)
		for time.Now().Before(dl) {
			state, devs := k.NativePresence.Aggregate(userID)
			if state == want && devs >= minDevices {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		state, devs := k.NativePresence.Aggregate(userID)
		t.Fatalf("presence aggregate = (%s,%d), want (%s,>=%d)", state, devs, want, minDevices)
	}
	waitAgg("online", 1, 5*time.Second)
	t.Logf("aggregate after conn1: online devices=1 (lease 来自网关 kind 26)")

	// 本人端点：聚合态 + 逐设备明细。
	status, body := c1GetJSON(t, srv.URL+"/v1/native/presence/self", token)
	if status != http.StatusOK {
		t.Fatalf("presence/self status=%d body=%v", status, body)
	}
	if body["state"] != "online" || body["user_id"].(float64) != float64(userID) {
		t.Fatalf("presence/self = %v", body)
	}
	t.Logf("SELF_PRESENCE: %v", body)

	// 第二台设备 → devices=2。
	cli2 := b2cDialClient(t, addr, userID, device2, token)
	defer cli2.close()
	cli2.waitAuth(5 * time.Second)
	waitAgg("online", 2, 5*time.Second)
	status, body = c1GetJSON(t, srv.URL+"/v1/native/presence/self", token)
	if status != http.StatusOK {
		t.Fatalf("presence/self2 status=%d body=%v", status, body)
	}
	devs, _ := body["devices"].(float64)
	if devs != 2 {
		t.Fatalf("presence/self devices = %v, want 2 (%v)", body["devices"], body)
	}
	t.Logf("MULTI_DEVICE: devices=%v device_list=%v", body["devices"], body["device_list"])

	// 快照（观测用）：SnapshotOnce → n_presence 行。
	k.NativePresence.SnapshotOnce(ctx)
	var snapState string
	var snapDevs int
	if err := db.QueryRowContext(ctx,
		`SELECT state, device_count FROM n_presence WHERE user_id = $1`, userID).Scan(&snapState, &snapDevs); err != nil {
		t.Fatalf("n_presence snapshot 行不存在: %v", err)
	}
	t.Logf("N_PRESENCE_SNAPSHOT: state=%s device_count=%d", snapState, snapDevs)

	// L4：无信任关系 → 404（不泄露存在性）。
	status, body = c1GetJSON(t, fmt.Sprintf("%s/v1/native/presence/%d", srv.URL, otherID), token)
	if status != http.StatusNotFound {
		t.Fatalf("L4 presence status=%d body=%v, want 404", status, body)
	}
	t.Logf("VISIBILITY_L4: status=404 (fail-closed, no existence leak)")

	// L3 known：只给 online 布尔。
	owner := fmt.Sprint(userID)
	contact := fmt.Sprint(otherID)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO trust_relations (owner_id, contact_id, trust_level) VALUES ($1,$2,$3)
		 ON CONFLICT (owner_id, contact_id) DO UPDATE SET trust_level = EXCLUDED.trust_level`,
		owner, contact, 3); err != nil {
		t.Fatalf("insert trust_relations L3: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM trust_relations WHERE owner_id=$1 AND contact_id=$2`, owner, contact)
	})
	status, body = c1GetJSON(t, fmt.Sprintf("%s/v1/native/presence/%d", srv.URL, otherID), token)
	if status != http.StatusOK {
		t.Fatalf("L3 presence status=%d body=%v", status, body)
	}
	if _, hasOnline := body["online"]; !hasOnline {
		t.Fatalf("L3 只应有 online 布尔: %v", body)
	}
	t.Logf("VISIBILITY_L3: %v (仅 online 布尔，无设备/精确时间)", body)

	// L2 trusted：聚合态。
	if _, err := db.ExecContext(ctx,
		`UPDATE trust_relations SET trust_level=2 WHERE owner_id=$1 AND contact_id=$2`, owner, contact); err != nil {
		t.Fatalf("update trust_relations L2: %v", err)
	}
	status, body = c1GetJSON(t, fmt.Sprintf("%s/v1/native/presence/%d", srv.URL, otherID), token)
	if status != http.StatusOK || body["state"] == nil {
		t.Fatalf("L2 presence status=%d body=%v", status, body)
	}
	t.Logf("VISIBILITY_L2: %v (仅聚合态)", body)

	// TTL：kill 网关（不产生 ConnClosed）→ lease 过期 → 内核推断 OFFLINE。
	stop()
	dl := time.Now().Add(6 * time.Second)
	offline := false
	for time.Now().Before(dl) {
		if state, _ := k.NativePresence.Aggregate(userID); state == "offline" {
			offline = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !offline {
		state, devs := k.NativePresence.Aggregate(userID)
		t.Fatalf("TTL 过期后仍未 offline: (%s,%d)", state, devs)
	}
	st := k.NativePresence.Stats()
	t.Logf("TTL_OFFLINE: aggregate=offline expired=%d rejectedOffline=%d", st.Expired, st.RejectedOffline)

	_ = k.Close(context.Background())
	closed = true
	if k.Deps.Stream.HasMirror() {
		t.Fatal("Close 后 stream.Mirror 仍被设置")
	}
	t.Logf("C1_PRESENCE_E2E PASS: online→multi-device(2)→L4 404→L3 bool→L2 aggregate→TTL offline")
}
