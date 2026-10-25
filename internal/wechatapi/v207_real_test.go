package wechatapi

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"human-agent-im/internal/a2a"
	"human-agent-im/internal/agentdir"
	"human-agent-im/internal/agentquota"
	"human-agent-im/internal/userkey"
	"human-agent-im/internal/wechatid"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestAgentCreateFullChain(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" { t.Skip() }
	d, _ := sql.Open("pgx", dsn)
	t.Cleanup(func() { d.Close() })
	suf := fmt.Sprintf("v207_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM wechat_idem WHERE wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM user_keys WHERE wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM agent_quota_usage WHERE user_id > 900000`)
	})
	ctx := context.Background()
	uk := userkey.New(d)
	q := agentquota.New(d)
	dir := agentdir.New(d)
	a2 := a2a.New(d)
	wid := wechatid.New(d)

	wx := suf + "_wx"
	// 先 Identify 拿真实 uid，再 Bind（两表一致）
	uid, _, err := wid.Identify(ctx, wx)
	if err != nil { t.Fatalf("identify: %v", err) }
	_, _, err = uk.Bind(ctx, wx, uid)
	if err != nil { t.Fatalf("bind: %v", err) }

	svc := New(d, uk).WithOptions(Options{Quota: q, Dir: dir, A2A: a2, WechatID: wid})

	priv, _ := uk.Unlock(ctx, wx)
	raw := signedEnvelope(wx, "agent.create", `{"role":"sub","prompt":"采购助理"}`, priv)
	resp, err := svc.Handle(ctx, raw)
	if err != nil { t.Fatalf("handle: %v", err) }
	var m map[string]any
	_ = json.Unmarshal(resp, &m)
	if m["type"] != "agent.create.result" {
		t.Fatalf("resp: %s", resp)
	}
	agentID, _ := m["agent_id"].(string)
	if agentID == "" {
		t.Fatalf("no agent_id: %s", resp)
	}
	a, err := dir.Get(ctx, agentID)
	if err != nil || a.OwnerUser != fmt.Sprintf("%d", uid) {
		t.Fatalf("agent not in dir: err=%v a=%+v", err, a)
	}
	items, err := a2.Inbox(ctx, fmt.Sprintf("agent-%d-main", uid), 10)
	if err != nil { t.Fatalf("inbox: %v", err) }
	found := false
	for _, it := range items {
		if it.Type == "agent.online" && it.FromAgent == m["agent_id"] {
			found = true
		}
	}
	if !found {
		t.Fatal("agent.online message not in main agent inbox")
	}
}

func TestAgentCreateQuotaExceeded(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" { t.Skip() }
	d, _ := sql.Open("pgx", dsn)
	t.Cleanup(func() { d.Close() })
	suf := fmt.Sprintf("v207q_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM wechat_idem WHERE wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM user_keys WHERE wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
	})
	ctx := context.Background()
	uk := userkey.New(d)
	q := agentquota.New(d)
	dir := agentdir.New(d)
	a2 := a2a.New(d)
	wid := wechatid.New(d)
	wx := suf + "_wx"
	uid2, _, _ := wid.Identify(ctx, wx)
	_, _, _ = uk.Bind(ctx, wx, uid2)
	svc := New(d, uk).WithOptions(Options{Quota: q, Dir: dir, A2A: a2, WechatID: wid})
	priv, _ := uk.Unlock(ctx, wx)
	_, _ = svc.Handle(ctx, signedEnvelope(wx, "agent.create", `{"role":"main"}`, priv))
	_, err := svc.Handle(ctx, signedEnvelope(wx, "agent.create", `{"role":"main"}`, priv))
	if err == nil {
		t.Fatal("second main should fail quota")
	}
}

func signedEnvelope(wxid, typ, payload string, priv []byte) []byte {
	e := Envelope{V:1, Wxid:wxid, Ts:time.Now().Unix(), Idem:fmt.Sprintf("idem-%d-%s", time.Now().UnixNano(), typ), Type:typ, Payload:json.RawMessage(payload)}
	sig := ed25519.Sign(ed25519.PrivateKey(priv), e.canonical())
	e.Sig = base64.StdEncoding.EncodeToString(sig)
	b, _ := json.Marshal(e)
	return b
}
