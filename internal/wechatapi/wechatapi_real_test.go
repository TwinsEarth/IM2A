package wechatapi

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"human-agent-im/internal/userkey"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func setup(t *testing.T) (*Service, *sql.DB, string) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("open: %v", err)
	}
	suf := fmt.Sprintf("v205_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM wechat_idem WHERE wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM user_keys WHERE wxid LIKE $1`, suf+"%")
		d.Close()
	})
	uk := userkey.New(d)
	return New(d, uk), d, suf
}

// 构造并签名一个上行 envelope。
func makeReq(t *testing.T, svc *Service, uk *userkey.Service, ctx context.Context, wxid, idem, typ string, payload map[string]any, skew bool) []byte {
	ts := time.Now().Unix()
	if skew {
		ts = 0
	}
	payloadB, _ := json.Marshal(payload)
	e := Envelope{V: 1, Wxid: wxid, Ts: ts, Idem: idem, Type: typ, Payload: payloadB}
	priv, err := uk.Unlock(ctx, wxid)
	if err != nil {
		t.Fatalf("unlock for sign: %v", err)
	}
	sig := ed25519.Sign(priv, e.canonical())
	e.Sig = base64.StdEncoding.EncodeToString(sig)
	raw, _ := json.Marshal(e)
	return raw
}

func TestBindConfirmHappy(t *testing.T) {
	svc, _, suf := setup(t)
	uk := userkey.New(svc.db)
	ctx := context.Background()
	wx := suf + "_wx"
	_, _, err := uk.Bind(ctx, wx, 5001)
	if err != nil {
		t.Fatalf("prebind: %v", err)
	}

	raw := makeReq(t, svc, uk, ctx, wx, suf+"_idem1", "bind.confirm", map[string]any{"user_id": 5001}, false)
	resp, err := svc.Handle(ctx, raw)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if !strings.Contains(string(resp), "bind.result") {
		t.Fatalf("bad resp: %s", resp)
	}
	// 幂等表应有记录
	var n int
	_ = svc.db.QueryRowContext(ctx, `SELECT count(*) FROM wechat_idem WHERE idem=$1`, suf+"_idem1").Scan(&n)
	if n != 1 {
		t.Fatalf("idem row should be 1, got %d", n)
	}
}

func TestIdempotentReplay(t *testing.T) {
	svc, _, suf := setup(t)
	uk := userkey.New(svc.db)
	ctx := context.Background()
	wx := suf + "_wx"
	_, _, _ = uk.Bind(ctx, wx, 5002)
	idem := suf + "_idem2"

	raw := makeReq(t, svc, uk, ctx, wx, idem, "agent.create", map[string]any{"role": "sub"}, false)
	r1, err := svc.Handle(ctx, raw)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// 第二次同 idem 同 envelope，应返回 r1 而非重新 dispatch
	r2, err := svc.Handle(ctx, raw)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var m1, m2 map[string]any
	_ = json.Unmarshal(r1, &m1)
	_ = json.Unmarshal(r2, &m2)
	if fmt.Sprint(m1) != fmt.Sprint(m2) {
		t.Fatalf("replay mismatch:\n%v\n%v", m1, m2)
	}
	var n int
	_ = svc.db.QueryRowContext(ctx, `SELECT count(*) FROM wechat_idem WHERE idem=$1`, idem).Scan(&n)
	if n != 1 {
		t.Fatalf("idem row must stay 1 after replay, got %d", n)
	}
}

func TestBadSignature(t *testing.T) {
	svc, _, suf := setup(t)
	uk := userkey.New(svc.db)
	ctx := context.Background()
	wx := suf + "_wx"
	_, _, _ = uk.Bind(ctx, wx, 5003)
	raw := makeReq(t, svc, uk, ctx, wx, suf+"_idem3", "bind.confirm", map[string]any{"user_id": 5003}, false)
	// 改一个字节
	b := []byte(raw)
	b[len(b)/2] ^= 0xFF
	_, err := svc.Handle(ctx, b)
	if err != ErrWxidUnbound {
		t.Fatalf("bad sig should be ErrWxidUnbound, got %v", err)
	}
}

func TestTimestampSkew(t *testing.T) {
	svc, _, suf := setup(t)
	uk := userkey.New(svc.db)
	ctx := context.Background()
	wx := suf + "_wx"
	_, _, _ = uk.Bind(ctx, wx, 5004)
	raw := makeReq(t, svc, uk, ctx, wx, suf+"_idem4", "bind.confirm", map[string]any{"user_id": 5004}, true)
	_, err := svc.Handle(ctx, raw)
	if err != ErrTSSkew {
		t.Fatalf("skew should be ErrTSSkew, got %v", err)
	}
}

func TestUnboundWxid(t *testing.T) {
	svc, _, suf := setup(t)
	// 不 Bind，直接用任意私钥签名不可行；这里只发无意义 sig，应被验签拒
	raw := []byte(fmt.Sprintf(`{"v":1,"wxid":%q,"ts":%d,"idem":%q,"type":"bind.confirm","payload":{},"sig":"AAAA"}`,
		suf+"_ghost", time.Now().Unix(), suf+"_idem5"))
	_, err := svc.Handle(context.Background(), raw)
	if err != ErrWxidUnbound {
		t.Fatalf("unbound should be ErrWxidUnbound, got %v", err)
	}
}
