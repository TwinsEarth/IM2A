package botroute

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestBotRoute(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" { t.Skip() }
	d, _ := sql.Open("pgx", dsn)
	t.Cleanup(func() { d.Close() })
	suf := fmt.Sprintf("v209_%d", time.Now().UnixNano())
	wa := suf + "_a"
	wb := suf + "_b"
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM bot_route_messages WHERE from_wxid LIKE $1 OR to_wxid LIKE $1`, suf+"%")
		_, _ = d.Exec(`DELETE FROM wechat_identity WHERE wxid LIKE $1`, suf+"%")
	})
	ctx := context.Background()
	// 两个 wxid 先落 wechat_identity（模拟已绑定）
	_, _ = d.Exec(`INSERT INTO wechat_identity(wxid, user_id) VALUES ($1, 910001), ($2, 910002)`, wa, wb)
	s := New(d)

	// A → B 发消息
	if rid, err := s.Route(ctx, wa, wb, "chat", map[string]string{"text": "你好"}); err != nil || rid == 0 {
		t.Fatalf("route id=%d err=%v", rid, err)
		t.Fatalf("route: %v", err)
	}
	// B 的 inbox 应拉到 1 条
	items, err := s.Inbox(ctx, wb, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox=%d err=%v", len(items), err)
	}
	if items[0]["from"] != wa {
		t.Errorf("from=%v", items[0]["from"])
	}
	// A 的 inbox 应为空（自己发的不在自己收件箱）
	empty, _ := s.Inbox(ctx, wa, 10)
	if len(empty) != 0 {
		t.Errorf("A inbox=%d", len(empty))
	}
	// 标记投递
	id := items[0]["id"].(int64)
	if err := s.Deliver(ctx, id); err != nil { t.Fatalf("deliver: %v", err) }
	// 再拉应空（已投递）
	again, _ := s.Inbox(ctx, wb, 10)
	if len(again) != 0 {
		t.Errorf("after deliver=%d", len(again))
	}
	// 自己发自己拒绝
	if _, err := s.Route(ctx, wa, wa, "chat", nil); !errors.Is(err, ErrSelf) {
		t.Errorf("self=%v", err)
	}
	// 未绑定 wxid 路由拒绝
	if _, err := s.Route(ctx, wa, "wx_nobody", "chat", nil); !errors.Is(err, ErrUnbound) {
		t.Errorf("unbound=%v", err)
	}
}
