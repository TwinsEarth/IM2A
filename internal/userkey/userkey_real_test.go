package userkey

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func db(t *testing.T) (*sql.DB, string) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN not set")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("open: %v", err)
	}
	suf := fmt.Sprintf("v202_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM user_keys WHERE wxid LIKE $1`, suf+"%")
		d.Close()
	})
	return d, suf
}

func TestBindUnlockSignVerify(t *testing.T) {
	d, suf := db(t)
	s := New(d)
	ctx := context.Background()
	wx := suf + "_wx"

	pub, isNew, err := s.Bind(ctx, wx, 1001)
	if err != nil || !isNew {
		t.Fatalf("bind: pub=%s isNew=%v err=%v", pub, isNew, err)
	}
	if len(pub) < 40 {
		t.Fatalf("pub too short: %s", pub)
	}

	// 再次 bind 同一 wxid 到同一 user，应返回同一公钥、isNew=false
	pub2, isNew2, err := s.Bind(ctx, wx, 1001)
	if err != nil || isNew2 || pub2 != pub {
		t.Fatalf("rebind same user: pub=%s isNew=%v err=%v", pub2, isNew2, err)
	}

	// 错 user 绑同一 wxid 应拒绝
	if _, _, err := s.Bind(ctx, wx, 9999); err != ErrAlreadyBound {
		t.Fatalf("bind to another user should be ErrAlreadyBound, got %v", err)
	}

	// 解锁私钥签名
	msg := []byte("open-private-data:" + wx)
	sig, err := s.Sign(ctx, wx, msg)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := s.Verify(ctx, wx, msg, sig); err != nil {
		t.Fatalf("verify own sig: %v", err)
	}

	// 篡改消息验签应失败
	if err := s.Verify(ctx, wx, []byte("tampered"), sig); err == nil {
		t.Fatal("verify tampered msg should fail")
	}
}

func TestUnlockUnknownWxid(t *testing.T) {
	d, suf := db(t)
	s := New(d)
	if _, err := s.Unlock(context.Background(), suf+"_nope"); err != ErrNotFound {
		t.Fatalf("unknown wxid should be ErrNotFound, got %v", err)
	}
}
